package agent

import (
	"bytes"
	"io"
	"regexp"
	"strings"
	"unicode/utf8"
)

var (
	markdownImagePattern = regexp.MustCompile(`!\[([^\]]*)\]\(([^)]*)\)`)
	markdownLinkPattern  = regexp.MustCompile(`\[([^\]]+)\]\(([^)]*)\)`)
)

const (
	ttsStartTag = "<tts>"
	ttsEndTag   = "</tts>"
)

// BuildSpeechText returns only text explicitly marked for TTS.
func BuildSpeechText(output string, _ Config) string {
	speech := extractTTSText(output)
	if speech == "" {
		return ""
	}
	return normalizeMarkdownForSpeech(speech)
}

func extractTTSText(output string) string {
	var parts []string
	remaining := output
	for {
		start := asciiIndexFold(remaining, ttsStartTag)
		if start < 0 {
			break
		}
		remaining = remaining[start+len(ttsStartTag):]
		end := asciiIndexFold(remaining, ttsEndTag)
		if end < 0 {
			// Treat the response boundary as an implicit closing tag. The model can
			// occasionally omit </tts>; keeping the explicit opening tag as the
			// authorization boundary avoids silently dropping that speech.
			if text := strings.TrimSpace(trimPartialTTSEndTagSuffix(remaining)); text != "" {
				parts = append(parts, text)
			}
			break
		}
		if text := strings.TrimSpace(remaining[:end]); text != "" {
			parts = append(parts, text)
		}
		remaining = remaining[end+len(ttsEndTag):]
	}
	return strings.TrimSpace(strings.Join(parts, "\n"))
}

func normalizeMarkdownForSpeech(output string) string {
	lines := strings.Split(output, "\n")
	normalized := make([]string, 0, len(lines))
	inFence := false
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if isMarkdownFenceLine(trimmed) {
			inFence = !inFence
			continue
		}
		if !inFence {
			if isMarkdownTableSeparatorLine(trimmed) {
				continue
			}
			trimmed = stripMarkdownLinePrefix(trimmed)
			trimmed = normalizeMarkdownTableRow(trimmed)
			trimmed = normalizeInlineMarkdown(trimmed)
		}
		normalized = append(normalized, trimmed)
	}
	return strings.TrimSpace(strings.Join(normalized, "\n"))
}

func isMarkdownFenceLine(line string) bool {
	return strings.HasPrefix(line, "```") || strings.HasPrefix(line, "~~~")
}

func isMarkdownTableSeparatorLine(line string) bool {
	if !strings.Contains(line, "|") || !strings.Contains(line, "-") {
		return false
	}
	for _, r := range line {
		if r != '|' && r != '-' && r != ':' && r != ' ' && r != '\t' {
			return false
		}
	}
	return true
}

func stripMarkdownLinePrefix(line string) string {
	line = stripMarkdownBlockquotePrefix(line)
	line = stripMarkdownHeadingPrefix(line)
	line = stripMarkdownListPrefix(line)
	line = stripMarkdownTaskPrefix(line)
	return strings.TrimSpace(line)
}

func stripMarkdownBlockquotePrefix(line string) string {
	for strings.HasPrefix(line, ">") {
		line = strings.TrimSpace(strings.TrimPrefix(line, ">"))
	}
	return line
}

func stripMarkdownHeadingPrefix(line string) string {
	hashes := 0
	for hashes < len(line) && hashes < 6 && line[hashes] == '#' {
		hashes++
	}
	if hashes > 0 && hashes < len(line) && line[hashes] == ' ' {
		return strings.TrimSpace(line[hashes+1:])
	}
	return line
}

func stripMarkdownListPrefix(line string) string {
	if len(line) >= 2 && (line[0] == '-' || line[0] == '*' || line[0] == '+') && line[1] == ' ' {
		return strings.TrimSpace(line[2:])
	}
	i := 0
	for i < len(line) && line[i] >= '0' && line[i] <= '9' {
		i++
	}
	if i > 0 && i+1 < len(line) && (line[i] == '.' || line[i] == ')') && line[i+1] == ' ' {
		return strings.TrimSpace(line[i+2:])
	}
	return line
}

func stripMarkdownTaskPrefix(line string) string {
	if len(line) >= 4 && line[0] == '[' && line[2] == ']' && line[3] == ' ' {
		marker := line[1]
		if marker == ' ' || marker == 'x' || marker == 'X' {
			return strings.TrimSpace(line[4:])
		}
	}
	return line
}

func normalizeMarkdownTableRow(line string) string {
	if strings.Count(line, "|") < 2 {
		return line
	}
	parts := strings.Split(strings.Trim(line, "|"), "|")
	cells := make([]string, 0, len(parts))
	for _, part := range parts {
		cell := strings.TrimSpace(part)
		if cell != "" {
			cells = append(cells, cell)
		}
	}
	if len(cells) == 0 {
		return ""
	}
	return strings.Join(cells, "，")
}

func normalizeInlineMarkdown(text string) string {
	text = markdownImagePattern.ReplaceAllStringFunc(text, func(match string) string {
		parts := markdownImagePattern.FindStringSubmatch(match)
		alt := strings.TrimSpace(parts[1])
		url := strings.TrimSpace(parts[2])
		if alt == "" && url == "" {
			return ""
		}
		if alt == "" {
			return "图片（" + url + "）"
		}
		if url == "" {
			return "图片：" + alt
		}
		return "图片：" + alt + "（" + url + "）"
	})
	text = markdownLinkPattern.ReplaceAllStringFunc(text, func(match string) string {
		parts := markdownLinkPattern.FindStringSubmatch(match)
		label := strings.TrimSpace(parts[1])
		url := strings.TrimSpace(parts[2])
		if url == "" {
			return label
		}
		return label + "（" + url + "）"
	})
	text = strings.ReplaceAll(text, "~~", "")
	text = strings.ReplaceAll(text, "**", "")
	text = strings.ReplaceAll(text, "__", "")
	text = strings.ReplaceAll(text, "`", "")
	text = strings.ReplaceAll(text, "*", "")
	return strings.TrimSpace(text)
}

type SpeechStreamWriter = TTSTagStreamWriter

func NewSpeechStreamWriter(target io.Writer) *SpeechStreamWriter {
	return NewTTSTagStreamWriter(target)
}

func finalizeAssistantOutput(raw string) string {
	return strings.TrimSpace(raw)
}

func speechStreamWriterForConfig(target io.Writer, cfg Config) *TTSTagStreamWriter {
	if target == nil {
		return nil
	}
	_ = cfg
	return NewTTSTagStreamWriter(target)
}

type TTSTagStreamWriter struct {
	target         io.Writer
	pending        []byte
	inTTS          bool
	seenTTS        bool
	streamTTS      bool
	outsideContent bool
	emitted        bool
}

type ttsStreamFlusher interface {
	Flush() error
}

func NewTTSTagStreamWriter(target io.Writer) *TTSTagStreamWriter {
	return &TTSTagStreamWriter{target: target}
}

func (w *TTSTagStreamWriter) ResetStreamState() {
	if w == nil {
		return
	}
	w.pending = nil
	w.inTTS = false
	w.seenTTS = false
	w.streamTTS = false
	w.outsideContent = false
	w.emitted = false
}

func (w *TTSTagStreamWriter) ResetBuffer() {
	if w == nil {
		return
	}
	if resetter, ok := w.target.(ttsBufferResetter); ok {
		resetter.ResetBuffer()
	}
	w.ResetStreamState()
}

func (w *TTSTagStreamWriter) StreamEmitted() bool {
	return w != nil && w.emitted
}

// FinishResponse reports whether the just-completed LLM response streamed a
// leading TTS block, then resets parser state so the next tool iteration or
// final response can stream its own leading block. If the model omitted the
// closing tag, the response boundary acts as an implicit </tts>: held trailing
// bytes are emitted and the provider is flushed immediately.
func (w *TTSTagStreamWriter) FinishResponse() bool {
	if w == nil {
		return false
	}
	if w.inTTS && w.streamTTS {
		pending := trimPartialTTSEndTagSuffixBytes(w.pending)
		for len(pending) > 0 {
			safeLen := validUTF8PrefixLen(pending, len(pending))
			if safeLen <= 0 {
				if len(pending) > 0 && utf8.RuneStart(pending[0]) {
					break
				}
				pending = pending[1:]
				continue
			}
			if err := w.writeTTSBytes(pending[:safeLen]); err != nil {
				w.ResetBuffer()
				return false
			}
			pending = pending[safeLen:]
		}
		if flusher, ok := w.target.(ttsStreamFlusher); ok {
			if err := flusher.Flush(); err != nil {
				w.ResetBuffer()
				return false
			}
		}
	}
	emitted := w.StreamEmitted()
	w.ResetStreamState()
	return emitted
}

func finishToolCallSpeechStream(event RunEvent, writer *TTSTagStreamWriter) bool {
	if writer == nil || event.Type != runEventToolCall {
		return false
	}
	return writer.FinishResponse()
}

func (w *TTSTagStreamWriter) Write(p []byte) (int, error) {
	if w == nil || w.target == nil || len(p) == 0 {
		return len(p), nil
	}
	w.pending = append(w.pending, p...)
	for len(w.pending) > 0 {
		if !w.inTTS {
			idx := bytes.Index(asciiLowerBytes(w.pending), []byte(ttsStartTag))
			if idx < 0 {
				searchSuffixLen := len(ttsStartTag) - 1
				if discardLen := len(w.pending) - searchSuffixLen; discardLen > 0 {
					w.markOutsideContent(w.pending[:discardLen])
				}
				w.pending = keepTagSearchSuffix(w.pending, len(ttsStartTag)-1)
				return len(p), nil
			}
			w.markOutsideContent(w.pending[:idx])
			w.pending = w.pending[idx+len(ttsStartTag):]
			w.inTTS = true
			// Only the first leading TTS block in an LLM response is streamed.
			// Tool-call completion resets this response-level state before the next
			// iteration and suppresses duplicate playback from the tool event.
			w.streamTTS = !w.outsideContent && !w.seenTTS
			w.seenTTS = true
			continue
		}

		idx := bytes.Index(asciiLowerBytes(w.pending), []byte(ttsEndTag))
		if idx >= 0 {
			if w.streamTTS {
				// Find the longest valid UTF-8 prefix before the end tag.
				skipBytes := 0
				for idx > skipBytes {
					safeIdx := validUTF8PrefixLen(w.pending[skipBytes:], idx-skipBytes)
					if safeIdx > 0 {
						if err := w.writeTTSBytes(w.pending[skipBytes : skipBytes+safeIdx]); err != nil {
							return 0, err
						}
						break
					}
					// Skip this invalid leading byte.
					skipBytes++
				}
			}
			w.pending = w.pending[idx+len(ttsEndTag):]
			w.inTTS = false
			if w.streamTTS {
				if flusher, ok := w.target.(ttsStreamFlusher); ok {
					if err := flusher.Flush(); err != nil {
						return 0, err
					}
				}
			}
			w.streamTTS = false
			w.outsideContent = false
			continue
		}

		safeLen := len(w.pending) - (len(ttsEndTag) - 1)
		if safeLen <= 0 {
			return len(p), nil
		}
		if !w.streamTTS {
			w.pending = append(w.pending[:0], w.pending[safeLen:]...)
			return len(p), nil
		}
		safeLen = validUTF8PrefixLen(w.pending, safeLen)
		if safeLen <= 0 {
			// Check if the first byte is a valid rune start
			if len(w.pending) > 0 && utf8.RuneStart(w.pending[0]) {
				// Incomplete multi-byte sequence; keep it and wait for more data
				return len(p), nil
			}
			// Invalid byte; drop it to avoid stalling
			w.pending = w.pending[1:]
			continue
		}
		if err := w.writeTTSBytes(w.pending[:safeLen]); err != nil {
			return 0, err
		}
		w.pending = append(w.pending[:0], w.pending[safeLen:]...)
		return len(p), nil
	}
	return len(p), nil
}

func (w *TTSTagStreamWriter) markOutsideContent(p []byte) {
	if w.outsideContent {
		return
	}
	for _, b := range p {
		switch b {
		case ' ', '\t', '\r', '\n':
			continue
		default:
			w.outsideContent = true
			return
		}
	}
}

func validUTF8PrefixLen(buf []byte, n int) int {
	if n > len(buf) {
		n = len(buf)
	}
	if n <= 0 {
		return 0
	}

	// Fast path: if already valid UTF-8, return immediately
	if utf8.Valid(buf[:n]) {
		return n
	}

	// Slow path: find the last complete rune boundary within the last UTFMax bytes
	start := n - 1
	lower := n - utf8.UTFMax
	if lower < 0 {
		lower = 0
	}
	for start >= lower && !utf8.RuneStart(buf[start]) {
		start--
	}

	// If we found a rune start and there's valid content before it, truncate there
	if start >= lower {
		if start > 0 && utf8.Valid(buf[:start]) {
			return start
		}
		// The rune start we found is at position 0 or itself part of invalid sequence
		// Keep scanning backwards to find a valid boundary (skip position 0)
		for i := start - 1; i > 0; i-- {
			if utf8.Valid(buf[:i]) {
				return i
			}
		}
		// If we're here, start is 0 or all positions checked are invalid
		// Check if position 0 itself is valid
		if start == 0 && utf8.Valid(buf[:1]) {
			return 1
		}
		return 0
	}

	// No rune start found in scan window - scan entire buffer for last valid boundary
	for i := n - 1; i > 0; i-- {
		if utf8.Valid(buf[:i]) {
			return i
		}
	}
	// Last resort: check if first byte is valid
	if utf8.Valid(buf[:1]) {
		return 1
	}
	return 0
}

func (w *TTSTagStreamWriter) writeTTSBytes(p []byte) error {
	if len(p) == 0 {
		return nil
	}
	n, err := w.target.Write(p)
	if n > 0 {
		w.emitted = true
	}
	return err
}

func keepTagSearchSuffix(buf []byte, max int) []byte {
	if max <= 0 || len(buf) <= max {
		return buf
	}
	return append(buf[:0], buf[len(buf)-max:]...)
}

func trimPartialTTSEndTagSuffix(text string) string {
	trimmed := trimPartialTTSEndTagSuffixBytes([]byte(text))
	return string(trimmed)
}

func trimPartialTTSEndTagSuffixBytes(buf []byte) []byte {
	maxLen := len(ttsEndTag) - 1
	if len(buf) < maxLen {
		maxLen = len(buf)
	}
	for suffixLen := maxLen; suffixLen > 0; suffixLen-- {
		start := len(buf) - suffixLen
		matched := true
		for i := 0; i < suffixLen; i++ {
			if asciiLowerByte(buf[start+i]) != asciiLowerByte(ttsEndTag[i]) {
				matched = false
				break
			}
		}
		if matched {
			return buf[:start]
		}
	}
	return buf
}

func asciiIndexFold(s, substr string) int {
	if substr == "" {
		return 0
	}
	last := len(s) - len(substr)
	for i := 0; i <= last; i++ {
		matched := true
		for j := 0; j < len(substr); j++ {
			if asciiLowerByte(s[i+j]) != asciiLowerByte(substr[j]) {
				matched = false
				break
			}
		}
		if matched {
			return i
		}
	}
	return -1
}

func asciiLowerBytes(buf []byte) []byte {
	lower := make([]byte, len(buf))
	for i, b := range buf {
		lower[i] = asciiLowerByte(b)
	}
	return lower
}

func asciiLowerByte(b byte) byte {
	if b >= 'A' && b <= 'Z' {
		return b + ('a' - 'A')
	}
	return b
}
