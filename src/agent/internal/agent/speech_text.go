package agent

import (
	"encoding/json"
	"io"
	"strings"
)

// BuildSpeechText returns the full assistant output for fallback TTS. Structured
// speech/text responses remain the preferred path for concise spoken output.
func BuildSpeechText(output string, _ Config) string {
	output = strings.TrimSpace(output)
	if output == "" {
		return ""
	}
	return output
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
