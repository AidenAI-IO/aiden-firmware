package agent

import (
	"strings"
	"testing"
)

func TestBuildSpeechTextKeepsFullOutputWhenSummaryEnabled(t *testing.T) {
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

	speech := BuildSpeechText(output, Config{})
	want := strings.Join([]string{
		"已完成设置，当前音量是 42。",
		"",
		"详细信息如下：",
		"我先打开了设置。",
		"然后读取了音量状态。",
		"最后确认没有继续修改。",
		"",
		`{"volume":42}`,
		"",
		"这段额外说明不应该进入播报摘要，因为它太长也不适合口播。",
	}, "\n")

	if speech != want {
		t.Fatalf("speech = %q, want normalized full output %q", speech, want)
	}
}

func TestBuildSpeechTextNormalizesMarkdownWithoutDroppingContent(t *testing.T) {
	output := strings.Join([]string{
		"# 状态更新",
		"",
		"**重点**：已完成 `audio_service` 检查。",
		"",
		"- 当前音量是 **42**。",
		"1. 播放 fallback output。",
		"",
		"详情见 [PR #237](https://github.com/AidenAI-IO/aiden-hardware-demo/pull/237)。",
		"",
		"> 请继续验证。",
	}, "\n")

	speech := BuildSpeechText(output, Config{})
	want := strings.Join([]string{
		"状态更新",
		"",
		"重点：已完成 audio_service 检查。",
		"",
		"当前音量是 42。",
		"播放 fallback output。",
		"",
		"详情见 PR #237（https://github.com/AidenAI-IO/aiden-hardware-demo/pull/237）。",
		"",
		"请继续验证。",
	}, "\n")

	if speech != want {
		t.Fatalf("speech = %q, want %q", speech, want)
	}
}

func TestBuildSpeechTextNormalizesTablesAndTasksWithoutDroppingContent(t *testing.T) {
	output := strings.Join([]string{
		"| 项目 | 状态 |",
		"| --- | --- |",
		"| 音频 | 已修复 |",
		"",
		"- [x] 保留正文",
		"- [ ] 继续验证",
	}, "\n")

	speech := BuildSpeechText(output, Config{})
	want := strings.Join([]string{
		"项目，状态",
		"音频，已修复",
		"",
		"保留正文",
		"继续验证",
	}, "\n")

	if speech != want {
		t.Fatalf("speech = %q, want %q", speech, want)
	}
}

func TestBuildSpeechTextKeepsOutput(t *testing.T) {
	output := "第一句很长，但用户仍然应该能在关闭摘要时听到完整内容。\n\n第二段也要保留。"

	speech := BuildSpeechText(output, Config{})

	if speech != strings.TrimSpace(output) {
		t.Fatalf("speech = %q, want original output", speech)
	}
}

func TestBuildSpeechTextDoesNotKeepOnlyFirstSentence(t *testing.T) {
	output := "CodeFace，你好！\n\n我仔细搜寻了记忆，但没有找到今天的对话历史记录。你可以再问我一次，我会尽力回答。"

	speech := BuildSpeechText(output, Config{})

	if speech != strings.TrimSpace(output) {
		t.Fatalf("speech = %q, want full output %q", speech, strings.TrimSpace(output))
	}
}

func TestRunResultSpokenTextReturnsOutputWhenSpeechTextPresent(t *testing.T) {
	result := RunResult{Output: "完整回答。", SpeechText: "播报摘要。"}
	if got := result.SpokenText(); got != "完整回答。" {
		t.Fatalf("SpokenText() = %q, want output", got)
	}
}

func TestRunResultSpeechTextFallsBackToOutput(t *testing.T) {
	result := RunResult{Output: "完整回答。"}
	if got := result.SpokenText(); got != "完整回答。" {
		t.Fatalf("SpokenText() = %q, want output fallback", got)
	}
}

func TestRunResultSpokenTextForConfigReturnsOutput(t *testing.T) {
	result := RunResult{Output: "完整回答应该显示。", SpeechText: "短口播。"}
	if got := result.SpokenTextForConfig(Config{}); got != "完整回答应该显示。" {
		t.Fatalf("SpokenTextForConfig() = %q, want output", got)
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
	if speech != "" {
		t.Fatalf("speech = %q, want empty", speech)
	}
}

func TestFinalizeSpeechOutputParsesFinalAnswerField(t *testing.T) {
	raw := `{"speech":"短口播。","final_answer":"完整回答。"}`

	output, speech := finalizeSpeechOutput(raw, Config{})

	if output != "完整回答。" {
		t.Fatalf("output = %q", output)
	}
	if speech != "" {
		t.Fatalf("speech = %q, want empty", speech)
	}
}

func TestFinalizeSpeechOutputKeepsPlainText(t *testing.T) {
	raw := "完整回答。"

	output, speech := finalizeSpeechOutput(raw, Config{})

	if output != "完整回答。" {
		t.Fatalf("output = %q", output)
	}
	if speech != "" {
		t.Fatalf("speech = %q, want empty for plain answer", speech)
	}
}

func TestFinalizeSpeechOutputParsesTextOnlyStructuredAnswer(t *testing.T) {
	raw := `{"text":"完整回答。"}`

	output, speech := finalizeSpeechOutput(raw, Config{})

	if output != "完整回答。" {
		t.Fatalf("output = %q", output)
	}
	if speech != "" {
		t.Fatalf("speech = %q, want empty for text-only structured answer", speech)
	}
}
