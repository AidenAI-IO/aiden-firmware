package agent

import (
	"fmt"
	"io"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf16"
	"unicode/utf8"
)

type jsonFieldStreamState int

const (
	jsonFieldStreamExpectObject jsonFieldStreamState = iota
	jsonFieldStreamExpectKey
	jsonFieldStreamInKey
	jsonFieldStreamAfterKey
	jsonFieldStreamBeforeValue
	jsonFieldStreamInTargetString
	jsonFieldStreamInOtherString
	jsonFieldStreamSkipValue
	jsonFieldStreamDone
)

// JSONFieldStreamWriter streams the decoded string value of a top-level JSON
// object field into target as bytes arrive.
type JSONFieldStreamWriter struct {
	target io.Writer
	field  string
	state  jsonFieldStreamState

	key     strings.Builder
	pending []byte

	escape               bool
	unicodeEscape        bool
	unicodeDigits        string
	pendingHighSurrogate rune
	lastKey              string

	depth         int
	skipValueRoot int
	skipString    bool
	emitted       bool
}

// NewJSONFieldStreamWriter creates a writer that extracts field from a
// streaming JSON object. Nested fields with the same name are ignored.
func NewJSONFieldStreamWriter(target io.Writer, field string) *JSONFieldStreamWriter {
	return &JSONFieldStreamWriter{
		target: target,
		field:  strings.TrimSpace(field),
		state:  jsonFieldStreamExpectObject,
	}
}

func (w *JSONFieldStreamWriter) ResetStreamState() {
	if w == nil {
		return
	}
	w.state = jsonFieldStreamExpectObject
	w.key.Reset()
	w.pending = nil
	w.resetJSONStringDecoder()
	w.lastKey = ""
	w.depth = 0
	w.skipValueRoot = 0
	w.skipString = false
	w.emitted = false
}

func (w *JSONFieldStreamWriter) StreamEmitted() bool {
	return w != nil && w.emitted
}

func (w *JSONFieldStreamWriter) Write(p []byte) (int, error) {
	if len(p) == 0 || w == nil || w.target == nil || w.field == "" {
		return len(p), nil
	}
	written := len(p)
	if len(w.pending) > 0 {
		combined := make([]byte, 0, len(w.pending)+len(p))
		combined = append(combined, w.pending...)
		combined = append(combined, p...)
		p = combined
		w.pending = nil
	}
	for len(p) > 0 {
		r, size := utf8.DecodeRune(p)
		if r == utf8.RuneError && size == 1 {
			if !utf8.FullRune(p) {
				w.pending = append(w.pending[:0], p...)
				return written, nil
			}
			return 0, fmt.Errorf("invalid utf8 in JSON field stream")
		}
		if err := w.consumeRune(r); err != nil {
			return 0, err
		}
		p = p[size:]
	}
	return written, nil
}

func (w *JSONFieldStreamWriter) consumeRune(r rune) error {
	switch w.state {
	case jsonFieldStreamExpectObject:
		if unicode.IsSpace(r) {
			return nil
		}
		if r == '{' {
			w.depth = 1
			w.state = jsonFieldStreamExpectKey
		}
	case jsonFieldStreamExpectKey:
		if w.depth != 1 {
			return nil
		}
		if unicode.IsSpace(r) || r == ',' {
			return nil
		}
		if r == '}' {
			w.depth = 0
			w.state = jsonFieldStreamDone
			return nil
		}
		if r == '"' {
			w.key.Reset()
			w.resetJSONStringDecoder()
			w.state = jsonFieldStreamInKey
		}
	case jsonFieldStreamInKey:
		if complete, decoded, err := w.consumeJSONStringRune(r); err != nil {
			return err
		} else if complete {
			w.lastKey = w.key.String()
			w.key.Reset()
			w.state = jsonFieldStreamAfterKey
		} else if decoded != "" {
			w.key.WriteString(decoded)
		}
	case jsonFieldStreamAfterKey:
		if unicode.IsSpace(r) {
			return nil
		}
		if r == ':' {
			w.state = jsonFieldStreamBeforeValue
			return nil
		}
		w.state = jsonFieldStreamExpectKey
	case jsonFieldStreamBeforeValue:
		if unicode.IsSpace(r) {
			return nil
		}
		if r == '"' {
			w.resetJSONStringDecoder()
			if w.lastKey == w.field {
				w.state = jsonFieldStreamInTargetString
			} else {
				w.state = jsonFieldStreamInOtherString
			}
			return nil
		}
		w.startSkippingValue(r)
	case jsonFieldStreamInTargetString:
		if complete, decoded, err := w.consumeJSONStringRune(r); err != nil {
			return err
		} else if complete {
			w.state = jsonFieldStreamDone
		} else if decoded != "" {
			n, err := io.WriteString(w.target, decoded)
			if n > 0 {
				w.emitted = true
			}
			if err != nil {
				return err
			}
		}
	case jsonFieldStreamInOtherString:
		if complete, _, err := w.consumeJSONStringRune(r); err != nil {
			return err
		} else if complete {
			w.state = jsonFieldStreamExpectKey
		}
	case jsonFieldStreamSkipValue:
		return w.skipValueRune(r)
	case jsonFieldStreamDone:
	}
	return nil
}

func (w *JSONFieldStreamWriter) startSkippingValue(r rune) {
	switch r {
	case '{', '[':
		w.skipValueRoot = w.depth
		w.depth++
		w.state = jsonFieldStreamSkipValue
		w.skipString = false
	case '"':
		w.skipValueRoot = w.depth
		w.resetJSONStringDecoder()
		w.skipString = true
		w.state = jsonFieldStreamSkipValue
	case '}':
		if w.depth > 0 {
			w.depth--
		}
		if w.depth == 0 {
			w.state = jsonFieldStreamDone
		} else {
			w.state = jsonFieldStreamExpectKey
		}
	case ',':
		w.state = jsonFieldStreamExpectKey
	default:
		w.skipValueRoot = w.depth
		w.state = jsonFieldStreamSkipValue
	}
}

func (w *JSONFieldStreamWriter) skipValueRune(r rune) error {
	if w.skipString {
		complete, _, err := w.consumeJSONStringRune(r)
		if err != nil {
			return err
		}
		if complete {
			w.skipString = false
		}
		return nil
	}
	switch r {
	case '"':
		w.resetJSONStringDecoder()
		w.skipString = true
	case '{', '[':
		w.depth++
	case '}', ']':
		if w.depth > 0 {
			w.depth--
		}
		if w.depth == 0 {
			w.state = jsonFieldStreamDone
		} else if w.depth == w.skipValueRoot {
			w.state = jsonFieldStreamExpectKey
		}
	case ',':
		if w.depth == w.skipValueRoot {
			w.state = jsonFieldStreamExpectKey
		}
	}
	return nil
}

func (w *JSONFieldStreamWriter) resetJSONStringDecoder() {
	w.escape = false
	w.unicodeEscape = false
	w.unicodeDigits = ""
	w.pendingHighSurrogate = 0
}

func (w *JSONFieldStreamWriter) consumeJSONStringRune(r rune) (bool, string, error) {
	if w.unicodeEscape {
		w.unicodeDigits += string(r)
		if len(w.unicodeDigits) < 4 {
			return false, "", nil
		}
		value, err := strconv.ParseInt(w.unicodeDigits, 16, 32)
		w.unicodeDigits = ""
		w.unicodeEscape = false
		w.escape = false
		if err != nil {
			return false, "", fmt.Errorf("invalid unicode escape in JSON field stream: %w", err)
		}
		return w.consumeJSONUnicodeEscape(rune(value))
	}

	if w.escape {
		w.escape = false
		switch r {
		case '"', '\\', '/':
			return w.consumeJSONStringScalar(string(r))
		case 'b':
			return w.consumeJSONStringScalar("\b")
		case 'f':
			return w.consumeJSONStringScalar("\f")
		case 'n':
			return w.consumeJSONStringScalar("\n")
		case 'r':
			return w.consumeJSONStringScalar("\r")
		case 't':
			return w.consumeJSONStringScalar("\t")
		case 'u':
			w.unicodeEscape = true
			w.unicodeDigits = ""
			return false, "", nil
		default:
			return false, "", fmt.Errorf("invalid escape in JSON field stream: \\%c", r)
		}
	}

	switch r {
	case '\\':
		w.escape = true
		return false, "", nil
	case '"':
		if err := w.ensureNoPendingHighSurrogate(); err != nil {
			return false, "", err
		}
		return true, "", nil
	default:
		return w.consumeJSONStringScalar(string(r))
	}
}

func (w *JSONFieldStreamWriter) consumeJSONStringScalar(decoded string) (bool, string, error) {
	if err := w.ensureNoPendingHighSurrogate(); err != nil {
		return false, "", err
	}
	return false, decoded, nil
}

func (w *JSONFieldStreamWriter) consumeJSONUnicodeEscape(value rune) (bool, string, error) {
	switch {
	case isHighSurrogate(value):
		if err := w.ensureNoPendingHighSurrogate(); err != nil {
			return false, "", err
		}
		w.pendingHighSurrogate = value
		return false, "", nil
	case isLowSurrogate(value):
		if w.pendingHighSurrogate == 0 {
			return false, "", fmt.Errorf("low surrogate without high surrogate in JSON field stream")
		}
		decoded := utf16.DecodeRune(w.pendingHighSurrogate, value)
		w.pendingHighSurrogate = 0
		return false, string(decoded), nil
	default:
		if err := w.ensureNoPendingHighSurrogate(); err != nil {
			return false, "", err
		}
		return false, string(value), nil
	}
}

func (w *JSONFieldStreamWriter) ensureNoPendingHighSurrogate() error {
	if w.pendingHighSurrogate == 0 {
		return nil
	}
	return fmt.Errorf("unpaired high surrogate in JSON field stream")
}

func isHighSurrogate(r rune) bool {
	return r >= 0xD800 && r <= 0xDBFF
}

func isLowSurrogate(r rune) bool {
	return r >= 0xDC00 && r <= 0xDFFF
}

// JSONFieldOrPlainStreamWriter extracts a field when the stream starts with a
// JSON object and otherwise forwards the stream unchanged.
type JSONFieldOrPlainStreamWriter struct {
	target     io.Writer
	structured *JSONFieldStreamWriter
	field      string
	prefix     []byte
	mode       int
	detect     int
	emitted    bool
}

const (
	jsonStreamDetectMode = iota
	jsonStreamStructuredMode
	jsonStreamPlainMode
)

const (
	jsonStreamDetectLeading = iota
	jsonStreamDetectAfterObjectStart
)

// NewJSONFieldOrPlainStreamWriter creates a structured-field-or-plain passthrough writer.
func NewJSONFieldOrPlainStreamWriter(target io.Writer, field string) io.Writer {
	return &JSONFieldOrPlainStreamWriter{
		target: target,
		field:  strings.TrimSpace(field),
	}
}

func (w *JSONFieldOrPlainStreamWriter) ResetStreamState() {
	if w == nil {
		return
	}
	if w.structured != nil {
		w.structured.ResetStreamState()
	}
	w.structured = nil
	w.prefix = nil
	w.mode = jsonStreamDetectMode
	w.detect = jsonStreamDetectLeading
	w.emitted = false
}

func (w *JSONFieldOrPlainStreamWriter) StreamEmitted() bool {
	if w == nil {
		return false
	}
	if w.emitted {
		return true
	}
	return w.structured != nil && w.structured.StreamEmitted()
}

func (w *JSONFieldOrPlainStreamWriter) Write(p []byte) (int, error) {
	if w == nil || w.target == nil || len(p) == 0 {
		return len(p), nil
	}
	switch w.mode {
	case jsonStreamStructuredMode:
		_, err := w.structured.Write(p)
		if w.structured.StreamEmitted() {
			w.emitted = true
		}
		return len(p), err
	case jsonStreamPlainMode:
		n, err := w.target.Write(p)
		if n > 0 {
			w.emitted = true
		}
		return len(p), err
	}

	consumed := 0
	for consumed < len(p) {
		b := p[consumed]
		switch w.detect {
		case jsonStreamDetectLeading:
			if isJSONWhitespaceByte(b) {
				w.prefix = append(w.prefix, b)
				consumed++
				continue
			}
			if b == '{' && w.field != "" {
				w.prefix = append(w.prefix, b)
				w.detect = jsonStreamDetectAfterObjectStart
				consumed++
				continue
			}
			return len(p), w.startPlainMode(p[consumed:])
		case jsonStreamDetectAfterObjectStart:
			if isJSONWhitespaceByte(b) {
				w.prefix = append(w.prefix, b)
				consumed++
				continue
			}
			if b == '"' {
				w.mode = jsonStreamStructuredMode
				w.structured = NewJSONFieldStreamWriter(w.target, w.field)
				chunk := w.bufferedChunk(p[consumed:])
				w.prefix = nil
				_, err := w.structured.Write(chunk)
				if w.structured.StreamEmitted() {
					w.emitted = true
				}
				return len(p), err
			}
			return len(p), w.startPlainMode(p[consumed:])
		}
	}
	return len(p), nil
}

func (w *JSONFieldOrPlainStreamWriter) startPlainMode(p []byte) error {
	w.mode = jsonStreamPlainMode
	chunk := w.bufferedChunk(p)
	w.prefix = nil
	n, err := w.target.Write(chunk)
	if n > 0 {
		w.emitted = true
	}
	return err
}

func (w *JSONFieldOrPlainStreamWriter) bufferedChunk(p []byte) []byte {
	chunk := make([]byte, 0, len(w.prefix)+len(p))
	chunk = append(chunk, w.prefix...)
	chunk = append(chunk, p...)
	return chunk
}

func isJSONWhitespaceByte(b byte) bool {
	switch b {
	case ' ', '\n', '\r', '\t':
		return true
	default:
		return false
	}
}
