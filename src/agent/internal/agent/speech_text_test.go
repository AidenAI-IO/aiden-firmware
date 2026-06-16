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
