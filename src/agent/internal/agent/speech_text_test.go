package agent

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestBuildSpeechTextSummarizesVerboseOutputForTTS(t *testing.T) {
	output := strings.Join([]string{
		"已完成设置，当前音量是 42。",
		"",
		"详细信息如下：",
		"- 我先打开了设置。",
		"- 然后读取了音量状态。",
		"- 最后确认没有继续修改。",
		"",
		"```json",
		`{"volume":42}`,
		"```",
		"",
		"这段额外说明不应该进入播报摘要，因为它太长也不适合口播。",
	}, "\n")

	speech := BuildSpeechText(output, Config{VoiceSpeechMaxRunes: 40})

	if speech == "" {
		t.Fatal("BuildSpeechText() returned empty speech")
	}
	if strings.Contains(speech, "```") || strings.Contains(speech, `{"volume":42}`) {
		t.Fatalf("speech should drop code blocks, got %q", speech)
	}
	if strings.Contains(speech, "- 我先打开") {
		t.Fatalf("speech should drop markdown list detail, got %q", speech)
	}
	if !strings.Contains(speech, "当前音量是 42") {
		t.Fatalf("speech should keep the main conclusion, got %q", speech)
	}
	if utf8.RuneCountInString(speech) > 40 {
		t.Fatalf("speech length = %d runes, want <= 40: %q", utf8.RuneCountInString(speech), speech)
	}
}

func TestBuildSpeechTextKeepsOutputWhenSummaryDisabled(t *testing.T) {
	disabled := false
	output := "第一句很长，但用户仍然应该能在关闭摘要时听到完整内容。\n\n第二段也要保留。"

	speech := BuildSpeechText(output, Config{VoiceSpeechSummaryEnabled: &disabled, VoiceSpeechMaxRunes: 10})

	if speech != strings.TrimSpace(output) {
		t.Fatalf("speech = %q, want original output", speech)
	}
}

func TestRunResultSpeechTextReturnsSummaryWhenPresent(t *testing.T) {
	result := RunResult{Output: "完整回答。", SpeechText: "播报摘要。"}
	if got := result.SpokenText(); got != "播报摘要。" {
		t.Fatalf("SpokenText() = %q, want speech text", got)
	}
}

func TestRunResultSpeechTextFallsBackToOutput(t *testing.T) {
	result := RunResult{Output: "完整回答。"}
	if got := result.SpokenText(); got != "完整回答。" {
		t.Fatalf("SpokenText() = %q, want output fallback", got)
	}
}

func TestRunResultSpokenTextForConfigPrefersSpeechText(t *testing.T) {
	result := RunResult{Output: "完整回答应该显示。", SpeechText: "短口播。"}
	if got := result.SpokenTextForConfig(Config{}); got != "短口播。" {
		t.Fatalf("SpokenTextForConfig() = %q, want speech text", got)
	}
}

func TestRunResultSpokenTextForConfigIgnoresSpeechTextWhenSummaryDisabled(t *testing.T) {
	disabled := false
	result := RunResult{Output: "完整回答应该显示。", SpeechText: "短口播。"}

	if got := result.SpokenTextForConfig(Config{VoiceSpeechSummaryEnabled: &disabled}); got != "完整回答应该显示。" {
		t.Fatalf("SpokenTextForConfig() = %q, want output when summary is disabled", got)
	}
}

func TestSpeechStreamWriterExtractsPartialJSONField(t *testing.T) {
	var sink strings.Builder
	writer := NewSpeechStreamWriter(&sink)

	chunks := []string{
		`{"speech":"已完成`,
		`，当前音量是 42。","text":"`,
		`完整回答不应该被播报。`,
	}
	for _, chunk := range chunks {
		if _, err := writer.Write([]byte(chunk)); err != nil {
			t.Fatalf("Write(%q) error = %v", chunk, err)
		}
	}

	if got := sink.String(); got != "已完成，当前音量是 42。" {
		t.Fatalf("streamed speech = %q", got)
	}
}

func TestSpeechStreamWriterDecodesEscapes(t *testing.T) {
	var sink strings.Builder
	writer := NewSpeechStreamWriter(&sink)

	if _, err := writer.Write([]byte(`{"speech":"第一行\n第二行\u3002","text":"ignored"}`)); err != nil {
		t.Fatalf("Write() error = %v", err)
	}

	if got := sink.String(); got != "第一行\n第二行。" {
		t.Fatalf("streamed speech = %q", got)
	}
}

func TestSpeechStreamWriterHandlesSplitUTF8Rune(t *testing.T) {
	var sink strings.Builder
	writer := NewSpeechStreamWriter(&sink)
	payload := []byte(`{"speech":"好","text":"ignored"}`)
	split := strings.Index(string(payload), "好")
	if split < 0 {
		t.Fatal("test payload missing split rune")
	}
	if _, err := writer.Write(payload[:split+1]); err != nil {
		t.Fatalf("Write(first) error = %v", err)
	}
	if _, err := writer.Write(payload[split+1:]); err != nil {
		t.Fatalf("Write(second) error = %v", err)
	}
	if got := sink.String(); got != "好" {
		t.Fatalf("streamed speech = %q", got)
	}
}

func TestSpeechStreamWriterIgnoresNestedField(t *testing.T) {
	var sink strings.Builder
	writer := NewSpeechStreamWriter(&sink)
	payload := `{"metadata":{"speech":"不要播报, {bad}"},"speech":"播报这个。","text":"ignored"}`
	if _, err := writer.Write([]byte(payload)); err != nil {
		t.Fatalf("Write() error = %v", err)
	}
	if got := sink.String(); got != "播报这个。" {
		t.Fatalf("streamed speech = %q", got)
	}
}

func TestFinalizeSpeechOutputParsesStructuredAnswer(t *testing.T) {
	raw := `{"speech":"短口播。","text":"完整回答。\n\n保留给屏幕。"}`
	output, speech := finalizeSpeechOutput(raw, Config{})
	if output != "完整回答。\n\n保留给屏幕。" {
		t.Fatalf("output = %q", output)
	}
	if speech != "短口播。" {
		t.Fatalf("speech = %q", speech)
	}
}

func TestFinalizeSpeechOutputIgnoresStructuredSpeechWhenSummaryDisabled(t *testing.T) {
	disabled := false
	raw := `{"speech":"短口播。","text":"完整回答。"}`

	output, speech := finalizeSpeechOutput(raw, Config{VoiceSpeechSummaryEnabled: &disabled})

	if output != "完整回答。" {
		t.Fatalf("output = %q", output)
	}
	if speech != "" {
		t.Fatalf("speech = %q, want empty when summary is disabled", speech)
	}
}

func TestFinalizeSpeechOutputKeepsPlainTextWhenSummaryDisabled(t *testing.T) {
	disabled := false
	raw := "完整回答。"

	output, speech := finalizeSpeechOutput(raw, Config{VoiceSpeechSummaryEnabled: &disabled})

	if output != "完整回答。" {
		t.Fatalf("output = %q", output)
	}
	if speech != "" {
		t.Fatalf("speech = %q, want empty for plain answer when summary is disabled", speech)
	}
}

func TestFinalizeSpeechOutputParsesTextOnlyWhenSummaryDisabled(t *testing.T) {
	disabled := false
	raw := `{"text":"完整回答。"}`

	output, speech := finalizeSpeechOutput(raw, Config{VoiceSpeechSummaryEnabled: &disabled})

	if output != "完整回答。" {
		t.Fatalf("output = %q", output)
	}
	if speech != "" {
		t.Fatalf("speech = %q, want empty for text-only structured answer", speech)
	}
}
