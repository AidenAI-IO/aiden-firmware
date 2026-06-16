package agent

import (
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
