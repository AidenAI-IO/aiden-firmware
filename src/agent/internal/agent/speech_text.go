package agent

import (
	"encoding/json"
	"io"
	"regexp"
	"strings"
)

var (
	markdownImagePattern = regexp.MustCompile(`!\[([^\]]*)\]\(([^)]*)\)`)
	markdownLinkPattern  = regexp.MustCompile(`\[([^\]]+)\]\(([^)]*)\)`)
)

// BuildSpeechText returns the full assistant output normalized for fallback TTS.
func BuildSpeechText(output string, _ Config) string {
	output = strings.TrimSpace(output)
	if output == "" {
		return ""
	}
	return normalizeMarkdownForSpeech(output)
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

type SpeechStreamWriter = JSONFieldStreamWriter

func NewSpeechStreamWriter(target io.Writer) *SpeechStreamWriter {
	return NewJSONFieldStreamWriter(target, "speech")
}

type legacyStructuredFinalAnswer struct {
	Text        string `json:"text"`
	FinalAnswer string `json:"final_answer"`
}

func parseLegacyStructuredFinalAnswer(raw string) (output string, ok bool) {
	raw = stripMarkdownCodeFence(raw)
	if raw == "" {
		return "", false
	}
	var answer legacyStructuredFinalAnswer
	if err := json.Unmarshal([]byte(raw), &answer); err != nil {
		start := strings.Index(raw, "{")
		end := strings.LastIndex(raw, "}")
		if start < 0 || end <= start {
			return "", false
		}
		if err := json.Unmarshal([]byte(raw[start:end+1]), &answer); err != nil {
			return "", false
		}
	}
	output = strings.TrimSpace(answer.Text)
	if output == "" {
		output = strings.TrimSpace(answer.FinalAnswer)
	}
	if output == "" {
		return "", false
	}
	return output, true
}

func finalizeAssistantOutput(raw string) string {
	output := strings.TrimSpace(raw)
	if parsedOutput, ok := parseLegacyStructuredFinalAnswer(output); ok {
		return parsedOutput
	}
	return output
}

func speechStreamWriterForConfig(target io.Writer, cfg Config) io.Writer {
	if target == nil {
		return nil
	}
	_ = cfg
	return NewJSONFieldOrPlainStreamWriter(target, "final_answer")
}
