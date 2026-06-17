package agent

import (
	"encoding/json"
	"io"
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

type SpeechStreamWriter = JSONFieldStreamWriter

func NewSpeechStreamWriter(target io.Writer) *SpeechStreamWriter {
	return NewJSONFieldStreamWriter(target, "speech")
}

type structuredFinalAnswer struct {
	Speech string `json:"speech"`
	Text   string `json:"text"`
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
	output = strings.TrimSpace(answer.Text)
	speechText = strings.TrimSpace(answer.Speech)
	if output == "" {
		return "", "", false
	}
	return output, speechText, true
}

func finalizeSpeechOutput(raw string, cfg Config) (string, string) {
	output := strings.TrimSpace(raw)
	if parsedOutput, parsedSpeech, ok := parseStructuredFinalAnswer(output); ok {
		if !cfg.VoiceSpeechSummaryEnabledOrDefault() {
			return parsedOutput, ""
		}
		if parsedSpeech != "" {
			return parsedOutput, parsedSpeech
		}
		return parsedOutput, BuildSpeechText(parsedOutput, cfg)
	}
	if !cfg.VoiceSpeechSummaryEnabledOrDefault() {
		return output, ""
	}
	return output, BuildSpeechText(output, cfg)
}

func speechStreamWriterForConfig(target io.Writer, cfg Config) io.Writer {
	if target == nil {
		return nil
	}
	if cfg.VoiceSpeechSummaryEnabledOrDefault() {
		return NewJSONFieldStreamWriter(target, "speech")
	}
	return NewJSONFieldOrPlainStreamWriter(target, "text")
}
