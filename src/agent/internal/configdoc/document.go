// Package configdoc applies narrowly scoped edits to TOML source while
// preserving unrelated values and comments. Whitespace adjoining a deleted
// key or table may be removed with that source region.
package configdoc

import (
	"bytes"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"unicode"

	toml "github.com/pelletier/go-toml/v2"
	"github.com/pelletier/go-toml/v2/unstable"
)

type Operation struct {
	Path        []string
	Value       any
	Delete      bool
	DeleteTable bool
}

type keyValue struct {
	path       []string
	tablePath  []string
	valueStart int
	valueEnd   int
	lineStart  int
	lineEnd    int
}

type table struct {
	path      []string
	lineStart int
	lineEnd   int
}

type inlineField struct {
	path       []string
	itemStart  int
	valueStart int
	valueEnd   int
	tableStart int
	tableEnd   int
}

type inlineTable struct {
	path       []string
	valueStart int
	valueEnd   int
}

type index struct {
	keys         []keyValue
	tables       []table
	inlineFields []inlineField
	inlineTables []inlineTable
}

// Apply returns a new document containing only the requested semantic edits.
// Operations are applied in path order so output is deterministic.
func Apply(source []byte, operations []Operation) ([]byte, []string, error) {
	if _, err := parse(source); err != nil {
		return nil, nil, err
	}
	if len(operations) == 0 {
		return append([]byte(nil), source...), nil, nil
	}

	ops := append([]Operation(nil), operations...)
	sort.SliceStable(ops, func(i, j int) bool {
		return strings.Join(ops[i].Path, "\x00") < strings.Join(ops[j].Path, "\x00")
	})
	result := append([]byte(nil), source...)
	changed := make([]string, 0, len(ops))
	for _, op := range ops {
		if len(op.Path) == 0 {
			return nil, nil, fmt.Errorf("config path must not be empty")
		}
		if op.Delete && op.DeleteTable {
			return nil, nil, fmt.Errorf("%s: key and table deletion are mutually exclusive", pathString(op.Path))
		}

		doc, err := parse(result)
		if err != nil {
			return nil, nil, err
		}
		var next []byte
		var didChange bool
		switch {
		case op.DeleteTable:
			next, didChange = deleteTable(result, doc, op.Path)
		case op.Delete:
			next, didChange = deleteKey(result, doc, op.Path)
		default:
			encoded, err := encodeValue(op.Value)
			if err != nil {
				return nil, nil, fmt.Errorf("%s: %w", pathString(op.Path), err)
			}
			next, didChange, err = setValue(result, doc, op.Path, encoded)
			if err != nil {
				return nil, nil, fmt.Errorf("%s: %w", pathString(op.Path), err)
			}
		}
		if !didChange {
			continue
		}
		if _, err := parse(next); err != nil {
			return nil, nil, fmt.Errorf("%s produced invalid TOML: %w", pathString(op.Path), err)
		}
		result = next
		changed = append(changed, pathString(op.Path))
	}
	return result, changed, nil
}

func parse(source []byte) (index, error) {
	var parser unstable.Parser
	parser.KeepComments = true
	parser.Reset(source)
	var result index
	var currentTable []string

	for parser.NextExpression() {
		node := parser.Expression()
		switch node.Kind {
		case unstable.Table, unstable.ArrayTable:
			currentTable = nodeKey(node)
			first := node.Child()
			if first == nil {
				return index{}, fmt.Errorf("TOML table has no key")
			}
			start := lineStart(source, int(first.Raw.Offset))
			result.tables = append(result.tables, table{
				path:      append([]string(nil), currentTable...),
				lineStart: start,
				lineEnd:   lineEnd(source, start),
			})
		case unstable.KeyValue:
			value := node.Value()
			keys := nodeKey(node)
			path := append(append([]string(nil), currentTable...), keys...)
			key := value.Next()
			if key == nil {
				return index{}, fmt.Errorf("TOML key/value has no key")
			}
			start := lineStart(source, int(key.Raw.Offset))
			lastKey := key
			for lastKey.Next() != nil {
				lastKey = lastKey.Next()
			}
			valueStart, err := valueStartAfterKey(source, int(lastKey.Raw.Offset+lastKey.Raw.Length))
			if err != nil {
				return index{}, err
			}
			valueEnd, err := valueEnd(source, valueStart, value)
			if err != nil {
				return index{}, err
			}
			result.keys = append(result.keys, keyValue{
				path:       path,
				tablePath:  append([]string(nil), currentTable...),
				valueStart: valueStart,
				valueEnd:   valueEnd,
				lineStart:  start,
				lineEnd:    lineEnd(source, valueEnd),
			})
			if value.Kind == unstable.InlineTable {
				if err := indexInlineTable(source, &result, path, value, valueStart, valueEnd); err != nil {
					return index{}, err
				}
			}
		}
	}
	if err := parser.Error(); err != nil {
		return index{}, fmt.Errorf("parse TOML: %w", err)
	}
	return result, nil
}

func indexInlineTable(source []byte, result *index, path []string, node *unstable.Node, start, end int) error {
	result.inlineTables = append(result.inlineTables, inlineTable{
		path:       append([]string(nil), path...),
		valueStart: start,
		valueEnd:   end,
	})
	for child := node.Child(); child != nil; child = child.Next() {
		if child.Kind != unstable.KeyValue {
			return fmt.Errorf("inline TOML table contains unexpected %s node", child.Kind)
		}
		value := child.Value()
		childPath := append(append([]string(nil), path...), nodeKey(child)...)
		key := value.Next()
		if key == nil {
			return fmt.Errorf("inline TOML key/value has no key")
		}
		lastKey := key
		for lastKey.Next() != nil {
			lastKey = lastKey.Next()
		}
		valueStart, err := valueStartAfterKey(source, int(lastKey.Raw.Offset+lastKey.Raw.Length))
		if err != nil {
			return err
		}
		valueEnd, err := valueEnd(source, valueStart, value)
		if err != nil {
			return err
		}
		result.inlineFields = append(result.inlineFields, inlineField{
			path:       childPath,
			itemStart:  int(key.Raw.Offset),
			valueStart: valueStart,
			valueEnd:   valueEnd,
			tableStart: start,
			tableEnd:   end,
		})
		if value.Kind == unstable.InlineTable {
			if err := indexInlineTable(source, result, childPath, value, valueStart, valueEnd); err != nil {
				return err
			}
		}
	}
	return nil
}

func valueStartAfterKey(source []byte, offset int) (int, error) {
	for offset < len(source) && (source[offset] == ' ' || source[offset] == '\t') {
		offset++
	}
	if offset >= len(source) || source[offset] != '=' {
		return 0, fmt.Errorf("TOML key has no assignment")
	}
	offset++
	for offset < len(source) && (source[offset] == ' ' || source[offset] == '\t') {
		offset++
	}
	return offset, nil
}

func valueEnd(source []byte, start int, value *unstable.Node) (int, error) {
	if value.Kind == unstable.Array || value.Kind == unstable.InlineTable {
		end, err := compositeValueEnd(source, start)
		if err != nil {
			return 0, err
		}
		return end, nil
	}
	if value.Raw.Length != 0 {
		return int(value.Raw.Offset + value.Raw.Length), nil
	}
	return start + len(value.Data), nil
}

func compositeValueEnd(source []byte, start int) (int, error) {
	if start >= len(source) || (source[start] != '[' && source[start] != '{') {
		return 0, fmt.Errorf("invalid composite TOML value offset")
	}
	var square, curly int
	for i := start; i < len(source); {
		switch source[i] {
		case '#':
			if newline := bytes.IndexByte(source[i:], '\n'); newline >= 0 {
				i += newline + 1
				continue
			}
			return 0, fmt.Errorf("unterminated composite TOML value")
		case '"', '\'':
			next, err := quotedValueEnd(source, i)
			if err != nil {
				return 0, err
			}
			i = next
			continue
		case '[':
			square++
		case ']':
			square--
		case '{':
			curly++
		case '}':
			curly--
		}
		i++
		if square == 0 && curly == 0 {
			return i, nil
		}
	}
	return 0, fmt.Errorf("unterminated composite TOML value")
}

func quotedValueEnd(source []byte, start int) (int, error) {
	quote := source[start]
	triple := start+2 < len(source) && source[start+1] == quote && source[start+2] == quote
	i := start + 1
	if triple {
		i = start + 3
	}
	for i < len(source) {
		if quote == '"' && source[i] == '\\' {
			i += 2
			continue
		}
		if triple {
			if i+2 < len(source) && source[i] == quote && source[i+1] == quote && source[i+2] == quote {
				return i + 3, nil
			}
		} else if source[i] == quote {
			return i + 1, nil
		}
		i++
	}
	return 0, fmt.Errorf("unterminated TOML string")
}

func nodeKey(node *unstable.Node) []string {
	var result []string
	it := node.Key()
	for it.Next() {
		result = append(result, string(it.Node().Data))
	}
	return result
}

func setValue(source []byte, doc index, path []string, encoded []byte) ([]byte, bool, error) {
	if key := findKey(doc, path); key != nil {
		if bytes.Equal(source[key.valueStart:key.valueEnd], encoded) {
			return source, false, nil
		}
		return splice(source, key.valueStart, key.valueEnd, encoded), true, nil
	}
	if field := findInlineField(doc, path); field != nil {
		if bytes.Equal(source[field.valueStart:field.valueEnd], encoded) {
			return source, false, nil
		}
		return splice(source, field.valueStart, field.valueEnd, encoded), true, nil
	}

	tablePath := path[:len(path)-1]
	keyText, err := encodeKey(path[len(path)-1])
	if err != nil {
		return nil, false, err
	}
	line := append(append([]byte(nil), keyText...), []byte(" = ")...)
	line = append(line, encoded...)
	line = append(line, '\n')
	if target := findInlineTable(doc, tablePath); target != nil {
		return insertInlineTableValue(source, *target, keyText, encoded)
	}
	if target := findTable(doc, tablePath); target != nil {
		position := target.lineEnd
		for _, key := range doc.keys {
			if equalPath(key.tablePath, tablePath) && key.lineEnd > position {
				position = key.lineEnd
			}
		}
		return splice(source, position, position, line), true, nil
	}
	if context, position, ok := findDottedTableContext(doc, tablePath); ok {
		dottedKey, err := encodeKeyPath(path[len(context):])
		if err != nil {
			return nil, false, err
		}
		dottedLine := append(append([]byte(nil), dottedKey...), []byte(" = ")...)
		dottedLine = append(dottedLine, encoded...)
		dottedLine = append(dottedLine, '\n')
		return splice(source, position, position, dottedLine), true, nil
	}
	if len(tablePath) == 0 && len(doc.tables) > 0 {
		return splice(source, doc.tables[0].lineStart, doc.tables[0].lineStart, line), true, nil
	}

	addition := make([]byte, 0, len(line)+32)
	if len(source) > 0 {
		if source[len(source)-1] != '\n' {
			addition = append(addition, '\n')
		}
		if !bytes.HasSuffix(source, []byte("\n\n")) {
			addition = append(addition, '\n')
		}
	}
	if len(tablePath) > 0 {
		addition = append(addition, '[')
		for i, segment := range tablePath {
			if i > 0 {
				addition = append(addition, '.')
			}
			encodedKey, err := encodeKey(segment)
			if err != nil {
				return nil, false, err
			}
			addition = append(addition, encodedKey...)
		}
		addition = append(addition, ']', '\n')
	}
	addition = append(addition, line...)
	return append(append([]byte(nil), source...), addition...), true, nil
}

func insertInlineTableValue(source []byte, table inlineTable, key, value []byte) ([]byte, bool, error) {
	if table.valueStart >= table.valueEnd || table.valueEnd > len(source) ||
		source[table.valueStart] != '{' || source[table.valueEnd-1] != '}' {
		return nil, false, fmt.Errorf("invalid inline TOML table range")
	}
	innerStart := table.valueStart + 1
	innerEnd := table.valueEnd - 1
	contentEnd := innerEnd
	for contentEnd > innerStart && (source[contentEnd-1] == ' ' || source[contentEnd-1] == '\t') {
		contentEnd--
	}
	assignment := append(append(append([]byte(nil), key...), []byte(" = ")...), value...)
	if len(bytes.TrimSpace(source[innerStart:innerEnd])) == 0 {
		replacement := append([]byte{' '}, assignment...)
		replacement = append(replacement, ' ')
		return splice(source, innerStart, innerEnd, replacement), true, nil
	}
	insertion := append([]byte(", "), assignment...)
	return splice(source, contentEnd, contentEnd, insertion), true, nil
}

func deleteKey(source []byte, doc index, path []string) ([]byte, bool) {
	key := findKey(doc, path)
	if key != nil {
		return splice(source, key.lineStart, key.lineEnd, nil), true
	}
	field := findInlineField(doc, path)
	if field == nil {
		return source, false
	}
	return deleteInlineTableValue(source, *field), true
}

func deleteInlineTableValue(source []byte, field inlineField) []byte {
	innerStart := field.tableStart + 1
	innerEnd := field.tableEnd - 1
	start := field.itemStart
	for start > innerStart && (source[start-1] == ' ' || source[start-1] == '\t') {
		start--
	}
	if start > innerStart && source[start-1] == ',' {
		start--
		return splice(source, start, field.valueEnd, nil)
	}

	end := field.valueEnd
	for end < innerEnd && (source[end] == ' ' || source[end] == '\t') {
		end++
	}
	if end < innerEnd && source[end] == ',' {
		end++
		for end < innerEnd && (source[end] == ' ' || source[end] == '\t') {
			end++
		}
		return splice(source, field.itemStart, end, nil)
	}
	return splice(source, innerStart, innerEnd, []byte(" "))
}

func deleteTable(source []byte, doc index, path []string) ([]byte, bool) {
	if findInlineTable(doc, path) != nil {
		if field := findInlineField(doc, path); field != nil {
			return deleteInlineTableValue(source, *field), true
		}
	}
	// A provider may have nested tables. They are not necessarily contiguous
	// with the parent table: an unrelated top-level table can be interleaved in
	// the source and must remain intact. Build one deletion interval per matching
	// table and dotted key instead of deleting the whole span from the parent to
	// its last child.
	type interval struct{ start, end int }
	intervals := make([]interval, 0)
	for _, table := range doc.tables {
		if !hasPathPrefix(table.path, path) {
			continue
		}
		end := table.lineEnd
		for _, key := range doc.keys {
			if equalPath(key.tablePath, table.path) && key.lineEnd > end {
				end = key.lineEnd
			}
		}
		for end < len(source) {
			next := lineEnd(source, end)
			if len(bytes.TrimSpace(source[end:next])) != 0 {
				break
			}
			end = next
		}
		intervals = append(intervals, interval{start: table.lineStart, end: end})
	}
	for _, key := range doc.keys {
		if hasPathPrefix(key.path, path) {
			intervals = append(intervals, interval{start: key.lineStart, end: key.lineEnd})
		}
	}
	if len(intervals) == 0 {
		return source, false
	}
	sort.Slice(intervals, func(i, j int) bool {
		if intervals[i].start == intervals[j].start {
			return intervals[i].end < intervals[j].end
		}
		return intervals[i].start < intervals[j].start
	})
	merged := intervals[:0]
	for _, current := range intervals {
		if len(merged) == 0 || current.start > merged[len(merged)-1].end {
			merged = append(merged, current)
			continue
		}
		if current.end > merged[len(merged)-1].end {
			merged[len(merged)-1].end = current.end
		}
	}
	result := append([]byte(nil), source...)
	for i := len(merged) - 1; i >= 0; i-- {
		result = splice(result, merged[i].start, merged[i].end, nil)
	}
	return result, true
}

// findDottedTableContext locates an implicit table represented by dotted keys.
// It returns the explicit parent table path that owns those keys and the source
// position immediately after the last member, so new fields can preserve the
// existing dotted-key style.
func findDottedTableContext(doc index, path []string) ([]string, int, bool) {
	var context []string
	position := 0
	found := false
	for _, key := range doc.keys {
		if !hasPathPrefix(key.path, path) || len(key.path) <= len(path) ||
			len(key.tablePath) >= len(path) || !hasPathPrefix(path, key.tablePath) {
			continue
		}
		if !found {
			context = append([]string(nil), key.tablePath...)
			found = true
		}
		if equalPath(key.tablePath, context) && key.lineEnd > position {
			position = key.lineEnd
		}
	}
	return context, position, found
}

func findKey(doc index, path []string) *keyValue {
	for i := range doc.keys {
		if equalPath(doc.keys[i].path, path) {
			return &doc.keys[i]
		}
	}
	return nil
}

func findTable(doc index, path []string) *table {
	for i := range doc.tables {
		if equalPath(doc.tables[i].path, path) {
			return &doc.tables[i]
		}
	}
	return nil
}

func findInlineField(doc index, path []string) *inlineField {
	for i := range doc.inlineFields {
		if equalPath(doc.inlineFields[i].path, path) {
			return &doc.inlineFields[i]
		}
	}
	return nil
}

func findInlineTable(doc index, path []string) *inlineTable {
	for i := range doc.inlineTables {
		if equalPath(doc.inlineTables[i].path, path) {
			return &doc.inlineTables[i]
		}
	}
	return nil
}

func encodeValue(value any) ([]byte, error) {
	if text, ok := value.(string); ok {
		return []byte(strconv.Quote(text)), nil
	}
	encoded, err := toml.Marshal(map[string]any{"value": value})
	if err != nil {
		return nil, fmt.Errorf("encode TOML value: %w", err)
	}
	equals := bytes.IndexByte(encoded, '=')
	if equals < 0 {
		return nil, fmt.Errorf("TOML encoder returned no value")
	}
	return bytes.TrimSpace(encoded[equals+1:]), nil
}

func encodeKey(key string) ([]byte, error) {
	if key != "" {
		bare := true
		for _, r := range key {
			if !(unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_' || r == '-') {
				bare = false
				break
			}
		}
		if bare {
			return []byte(key), nil
		}
	}
	encoded, err := toml.Marshal(map[string]any{key: true})
	if err != nil {
		return nil, fmt.Errorf("encode TOML key: %w", err)
	}
	equals := bytes.IndexByte(encoded, '=')
	if equals < 0 {
		return nil, fmt.Errorf("TOML encoder returned no key")
	}
	return bytes.TrimSpace(encoded[:equals]), nil
}

func encodeKeyPath(path []string) ([]byte, error) {
	var result []byte
	for i, segment := range path {
		encoded, err := encodeKey(segment)
		if err != nil {
			return nil, err
		}
		if i > 0 {
			result = append(result, '.')
		}
		result = append(result, encoded...)
	}
	return result, nil
}

func splice(source []byte, start, end int, replacement []byte) []byte {
	result := make([]byte, 0, len(source)-(end-start)+len(replacement))
	result = append(result, source[:start]...)
	result = append(result, replacement...)
	result = append(result, source[end:]...)
	return result
}

func lineStart(source []byte, offset int) int {
	if offset > len(source) {
		offset = len(source)
	}
	if previous := bytes.LastIndexByte(source[:offset], '\n'); previous >= 0 {
		return previous + 1
	}
	return 0
}

func lineEnd(source []byte, offset int) int {
	if offset > len(source) {
		offset = len(source)
	}
	if next := bytes.IndexByte(source[offset:], '\n'); next >= 0 {
		return offset + next + 1
	}
	return len(source)
}

func equalPath(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func hasPathPrefix(path, prefix []string) bool {
	return len(path) >= len(prefix) && equalPath(path[:len(prefix)], prefix)
}

func pathString(path []string) string {
	return strings.Join(path, ".")
}
