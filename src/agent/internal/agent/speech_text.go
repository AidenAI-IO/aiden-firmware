package agent

import (
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

// BuildSpeechText derives a short, spoken-friendly text from the full assistant
// output. The full output remains the source of truth for UI, history, and memory.
func BuildSpeechText(output string, cfg Config) string {
	output = strings.TrimSpace(output)
	if output == "" {
		return ""
	}
	if !cfg.VoiceSpeechSummaryEnabledOrDefault() {
		return output
	}

	text := stripSpeechUnfriendlyMarkdown(output)
	text = strings.Join(strings.Fields(text), " ")
	if text == "" {
		text = strings.Join(strings.Fields(output), " ")
	}
	if sentence := firstSpeechSentence(text); sentence != "" {
		text = sentence
	}
	return truncateSpeechRunes(text, cfg.VoiceSpeechMaxRunesOrDefault())
}

func stripSpeechUnfriendlyMarkdown(output string) string {
	lines := strings.Split(output, "\n")
	var kept []string
	inCodeBlock := false
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "```") || strings.HasPrefix(trimmed, "~~~") {
			inCodeBlock = !inCodeBlock
			continue
		}
		if inCodeBlock || trimmed == "" {
			continue
		}
		if isMarkdownListLine(trimmed) || isMarkdownTableLine(trimmed) {
			continue
		}
		kept = append(kept, strings.TrimSpace(stripMarkdownDecorators(trimmed)))
	}
	return strings.Join(kept, " ")
}

func isMarkdownListLine(line string) bool {
	if strings.HasPrefix(line, "- ") || strings.HasPrefix(line, "* ") || strings.HasPrefix(line, "+ ") {
		return true
	}
	i := 0
	for ; i < len(line); i++ {
		r, size := utf8.DecodeRuneInString(line[i:])
		if !unicode.IsDigit(r) {
			break
		}
		i += size - 1
	}
	if i == 0 || i >= len(line)-1 {
		return false
	}
	return (line[i] == '.' || line[i] == ')') && line[i+1] == ' '
}

func isMarkdownTableLine(line string) bool {
	if !strings.Contains(line, "|") {
		return false
	}
	trimmed := strings.Trim(line, "| ")
	if trimmed == "" {
		return true
	}
	for _, r := range trimmed {
		if r != '-' && r != ':' && r != '|' && !unicode.IsSpace(r) {
			return strings.Count(line, "|") >= 2
		}
	}
	return true
}

func stripMarkdownDecorators(line string) string {
	replacer := strings.NewReplacer(
		"**", "",
		"__", "",
		"`", "",
		"#", "",
		"> ", "",
	)
	return replacer.Replace(line)
}

func truncateSpeechRunes(text string, maxRunes int) string {
	if maxRunes <= 0 || utf8.RuneCountInString(text) <= maxRunes {
		return strings.TrimSpace(text)
	}
	runes := []rune(text)
	minSentenceCut := maxRunes / 3
	if minSentenceCut < 8 {
		minSentenceCut = 8
	}
	sentenceCut := 0
	softCut := 0
	for i := 0; i < maxRunes && i < len(runes); i++ {
		switch runes[i] {
		case '。', '！', '？', '.', '!', '?', ';', '；':
			if i+1 >= minSentenceCut {
				sentenceCut = i + 1
			}
		case '，', ',', '、':
			if i >= maxRunes/2 {
				softCut = i
			}
		}
	}
	if sentenceCut > 0 {
		return strings.TrimSpace(string(runes[:sentenceCut]))
	}
	if softCut > 0 {
		return strings.TrimSpace(string(runes[:softCut]))
	}
	return strings.TrimSpace(string(runes[:maxRunes]))
}

func firstSpeechSentence(text string) string {
	text = strings.TrimSpace(text)
	if text == "" {
		return ""
	}
	runes := []rune(text)
	for i, r := range runes {
		switch r {
		case '。', '！', '？', '.', '!', '?', ';', '；':
			if i >= 5 {
				return strings.TrimSpace(string(runes[:i+1]))
			}
		}
	}
	return ""
}

type speechTextStreamState int

const (
	speechStreamExpectObject speechTextStreamState = iota
	speechStreamExpectKey
	speechStreamInKey
	speechStreamAfterKey
	speechStreamBeforeValue
	speechStreamInTargetString
	speechStreamInOtherString
	speechStreamSkipValue
	speechStreamDone
)

type SpeechTextStreamWriter struct {
	target io.Writer
	field  string
	state  speechTextStreamState

	key     strings.Builder
	pending []byte

	escape        bool
	unicodeEscape bool
	unicodeDigits string
	lastKey       string

	depth         int
	skipValueRoot int
	skipString    bool
}

func NewSpeechTextStreamWriter(target io.Writer, field string) *SpeechTextStreamWriter {
	return &SpeechTextStreamWriter{
		target: target,
		field:  strings.TrimSpace(field),
		state:  speechStreamExpectObject,
	}
}

func (w *SpeechTextStreamWriter) Write(p []byte) (int, error) {
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
			return 0, fmt.Errorf("invalid utf8 in speech text stream")
		}
		if err := w.consumeRune(r); err != nil {
			return 0, err
		}
		p = p[size:]
	}
	return written, nil
}

func (w *SpeechTextStreamWriter) consumeRune(r rune) error {
	switch w.state {
	case speechStreamExpectObject:
		if unicode.IsSpace(r) {
			return nil
		}
		if r == '{' {
			w.depth = 1
			w.state = speechStreamExpectKey
		}
	case speechStreamExpectKey:
		if w.depth != 1 {
			return nil
		}
		if unicode.IsSpace(r) || r == ',' {
			return nil
		}
		if r == '}' {
			w.depth = 0
			w.state = speechStreamDone
			return nil
		}
		if r == '"' {
			w.key.Reset()
			w.escape = false
			w.unicodeEscape = false
			w.unicodeDigits = ""
			w.state = speechStreamInKey
		}
	case speechStreamInKey:
		if complete, decoded, err := w.consumeJSONStringRune(r); err != nil {
			return err
		} else if complete {
			w.lastKey = w.key.String()
			w.key.Reset()
			w.state = speechStreamAfterKey
		} else if decoded != "" {
			w.key.WriteString(decoded)
		}
	case speechStreamAfterKey:
		if unicode.IsSpace(r) {
			return nil
		}
		if r == ':' {
			w.state = speechStreamBeforeValue
			return nil
		}
		w.state = speechStreamExpectKey
	case speechStreamBeforeValue:
		if unicode.IsSpace(r) {
			return nil
		}
		if r == '"' {
			w.escape = false
			w.unicodeEscape = false
			w.unicodeDigits = ""
			if w.lastKey == w.field {
				w.state = speechStreamInTargetString
			} else {
				w.state = speechStreamInOtherString
			}
			return nil
		}
		w.startSkippingValue(r)
	case speechStreamInTargetString:
		if complete, decoded, err := w.consumeJSONStringRune(r); err != nil {
			return err
		} else if complete {
			w.state = speechStreamDone
		} else if decoded != "" {
			if _, err := io.WriteString(w.target, decoded); err != nil {
				return err
			}
		}
	case speechStreamInOtherString:
		if complete, _, err := w.consumeJSONStringRune(r); err != nil {
			return err
		} else if complete {
			w.state = speechStreamExpectKey
		}
	case speechStreamSkipValue:
		return w.skipValueRune(r)
	case speechStreamDone:
	}
	return nil
}

func (w *SpeechTextStreamWriter) startSkippingValue(r rune) {
	switch r {
	case '{', '[':
		w.skipValueRoot = w.depth
		w.depth++
		w.state = speechStreamSkipValue
		w.skipString = false
	case '"':
		w.skipValueRoot = w.depth
		w.escape = false
		w.unicodeEscape = false
		w.unicodeDigits = ""
		w.skipString = true
		w.state = speechStreamSkipValue
	case '}':
		if w.depth > 0 {
			w.depth--
		}
		if w.depth == 0 {
			w.state = speechStreamDone
		} else {
			w.state = speechStreamExpectKey
		}
	case ',':
		w.state = speechStreamExpectKey
	default:
		w.skipValueRoot = w.depth
		w.state = speechStreamSkipValue
	}
}

func (w *SpeechTextStreamWriter) skipValueRune(r rune) error {
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
		w.escape = false
		w.unicodeEscape = false
		w.unicodeDigits = ""
		w.skipString = true
	case '{', '[':
		w.depth++
	case '}', ']':
		if w.depth > 0 {
			w.depth--
		}
		if w.depth == 0 {
			w.state = speechStreamDone
		} else if w.depth == w.skipValueRoot {
			w.state = speechStreamExpectKey
		}
	case ',':
		if w.depth == w.skipValueRoot {
			w.state = speechStreamExpectKey
		}
	}
	return nil
}

func (w *SpeechTextStreamWriter) consumeJSONStringRune(r rune) (bool, string, error) {
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
			return false, "", fmt.Errorf("invalid unicode escape in speech text stream: %w", err)
		}
		return false, string(rune(value)), nil
	}

	if w.escape {
		w.escape = false
		switch r {
		case '"', '\\', '/':
			return false, string(r), nil
		case 'b':
			return false, "\b", nil
		case 'f':
			return false, "\f", nil
		case 'n':
			return false, "\n", nil
		case 'r':
			return false, "\r", nil
		case 't':
			return false, "\t", nil
		case 'u':
			w.unicodeEscape = true
			w.unicodeDigits = ""
			return false, "", nil
		default:
			return false, "", fmt.Errorf("invalid escape in speech text stream: \\%c", r)
		}
	}

	switch r {
	case '\\':
		w.escape = true
		return false, "", nil
	case '"':
		return true, "", nil
	default:
		return false, string(r), nil
	}
}

type structuredFinalAnswer struct {
	SpeechText string `json:"speech_text"`
	Output     string `json:"output"`
}

func parseStructuredFinalAnswer(raw string) (output string, speechText string, ok bool) {
	raw = stripMarkdownCodeFence(raw)
	if raw == "" {
		return "", "", false
	}
	var answer structuredFinalAnswer
	if err := json.Unmarshal([]byte(raw), &answer); err != nil {
		start := strings.Index(raw, "{")
		end := strings.LastIndex(raw, "}")
		if start < 0 || end <= start {
			return "", "", false
		}
		if err := json.Unmarshal([]byte(raw[start:end+1]), &answer); err != nil {
			return "", "", false
		}
	}
	output = strings.TrimSpace(answer.Output)
	speechText = strings.TrimSpace(answer.SpeechText)
	if output == "" || speechText == "" {
		return "", "", false
	}
	return output, speechText, true
}

func finalizeSpeechOutput(raw string, cfg Config) (string, string) {
	output := strings.TrimSpace(raw)
	if parsedOutput, parsedSpeech, ok := parseStructuredFinalAnswer(output); ok {
		return parsedOutput, parsedSpeech
	}
	return output, BuildSpeechText(output, cfg)
}

func speechStreamWriterForConfig(target io.Writer, cfg Config) io.Writer {
	if target == nil {
		return nil
	}
	if cfg.VoiceSpeechSummaryEnabledOrDefault() {
		return NewSpeechTextStreamWriter(target, "speech_text")
	}
	return newStructuredFieldOrPlainStreamWriter(target, "output")
}

type structuredFieldOrPlainStreamWriter struct {
	target     io.Writer
	structured *SpeechTextStreamWriter
	field      string
	prefix     []byte
	mode       int
}

const (
	streamDetectMode = iota
	streamStructuredMode
	streamPlainMode
)

func newStructuredFieldOrPlainStreamWriter(target io.Writer, field string) io.Writer {
	return &structuredFieldOrPlainStreamWriter{
		target: target,
		field:  strings.TrimSpace(field),
	}
}

func (w *structuredFieldOrPlainStreamWriter) Write(p []byte) (int, error) {
	if w == nil || w.target == nil || len(p) == 0 {
		return len(p), nil
	}
	switch w.mode {
	case streamStructuredMode:
		_, err := w.structured.Write(p)
		return len(p), err
	case streamPlainMode:
		_, err := w.target.Write(p)
		return len(p), err
	}

	consumed := 0
	for consumed < len(p) {
		b := p[consumed]
		if isJSONWhitespaceByte(b) {
			w.prefix = append(w.prefix, b)
			consumed++
			continue
		}
		if b == '{' && w.field != "" {
			w.mode = streamStructuredMode
			w.structured = NewSpeechTextStreamWriter(w.target, w.field)
			chunk := append(append([]byte{}, w.prefix...), p[consumed:]...)
			w.prefix = nil
			_, err := w.structured.Write(chunk)
			return len(p), err
		}
		w.mode = streamPlainMode
		chunk := append(append([]byte{}, w.prefix...), p[consumed:]...)
		w.prefix = nil
		_, err := w.target.Write(chunk)
		return len(p), err
	}
	return len(p), nil
}

func isJSONWhitespaceByte(b byte) bool {
	switch b {
	case ' ', '\n', '\r', '\t':
		return true
	default:
		return false
	}
}
