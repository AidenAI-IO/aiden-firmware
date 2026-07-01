package agent

import (
	"bytes"
	"io"
	"regexp"
	"strings"
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

func speechStreamWriterForConfig(target io.Writer, cfg Config) io.Writer {
	if target == nil {
		return nil
	}
	_ = cfg
	return NewTTSTagStreamWriter(target)
}

type TTSTagStreamWriter struct {
	target  io.Writer
	pending []byte
	inTTS   bool
	emitted bool
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

func (w *TTSTagStreamWriter) Write(p []byte) (int, error) {
	if w == nil || w.target == nil || len(p) == 0 {
		return len(p), nil
	}
	w.pending = append(w.pending, p...)
	for len(w.pending) > 0 {
		if !w.inTTS {
			idx := bytes.Index(asciiLowerBytes(w.pending), []byte(ttsStartTag))
			if idx < 0 {
				w.pending = keepTagSearchSuffix(w.pending, len(ttsStartTag)-1)
				return len(p), nil
			}
			w.pending = w.pending[idx+len(ttsStartTag):]
			w.inTTS = true
			continue
		}

		idx := bytes.Index(asciiLowerBytes(w.pending), []byte(ttsEndTag))
		if idx >= 0 {
			if err := w.writeTTSBytes(w.pending[:idx]); err != nil {
				return 0, err
			}
			w.pending = w.pending[idx+len(ttsEndTag):]
			w.inTTS = false
			continue
		}

		safeLen := len(w.pending) - (len(ttsEndTag) - 1)
		if safeLen <= 0 {
			return len(p), nil
		}
		if err := w.writeTTSBytes(w.pending[:safeLen]); err != nil {
			return 0, err
		}
		w.pending = append(w.pending[:0], w.pending[safeLen:]...)
		return len(p), nil
	}
	return len(p), nil
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
