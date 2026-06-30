package agent

import (
	"strings"
	"testing"
)

func TestBuildSpeechTextExtractsTTSTag(t *testing.T) {
	output := strings.Join([]string{
		"已完成设置，当前音量是 42。",
		"",
		"详细信息如下：",
		"- 我先打开了设置。",
		"- 然后读取了音量状态。",
		"- 最后确认没有继续修改。",
		"",
		"<tts>",
		"已完成设置，请查收。",
		"</tts>",
	}, "\n")

	speech := BuildSpeechText(output, Config{})

	if speech != "已完成设置，请查收。" {
		t.Fatalf("speech = %q", speech)
	}
}

func TestBuildSpeechTextNormalizesMarkdownInsideTTSTag(t *testing.T) {
	output := strings.Join([]string{
		"正文不进入播报。",
		"<tts>",
		"**重点**：已完成 `audio_service` 检查。",
		"- 当前音量是 **42**。",
		"详情见 [PR #237](https://github.com/AidenAI-IO/aiden-hardware-demo/pull/237)。",
		"</tts>",
	}, "\n")

	speech := BuildSpeechText(output, Config{})
	want := strings.Join([]string{
		"重点：已完成 audio_service 检查。",
		"当前音量是 42。",
		"详情见 PR #237（https://github.com/AidenAI-IO/aiden-hardware-demo/pull/237）。",
	}, "\n")

	if speech != want {
		t.Fatalf("speech = %q, want %q", speech, want)
	}
}

func TestBuildSpeechTextReturnsEmptyWithoutTTSTag(t *testing.T) {
	output := "<think>\n需要查当前时间。\n</think>"

	speech := BuildSpeechText(output, Config{})

	if speech != "" {
		t.Fatalf("speech = %q, want empty without tts tag", speech)
	}
}

func TestBuildSpeechTextJoinsMultipleTTSTags(t *testing.T) {
	output := strings.Join([]string{
		"<tts>第一句。</tts>",
		"中间正文。",
		"<tts>第二句。</tts>",
	}, "\n")

	speech := BuildSpeechText(output, Config{})
	want := strings.Join([]string{
		"第一句。",
		"第二句。",
	}, "\n")

	if speech != want {
		t.Fatalf("speech = %q, want %q", speech, want)
	}
}

func TestRunResultSpokenTextReturnsTTSTag(t *testing.T) {
	result := RunResult{Output: "完整回答。\n<tts>播报摘要。</tts>"}
	if got := result.SpokenText(); got != "播报摘要。" {
		t.Fatalf("SpokenText() = %q", got)
	}
}

func TestRunResultSpokenTextForConfigReturnsTTSTag(t *testing.T) {
	result := RunResult{Output: "完整回答应该显示。\n<tts>播报摘要。</tts>"}
	if got := result.SpokenTextForConfig(Config{}); got != "播报摘要。" {
		t.Fatalf("SpokenTextForConfig() = %q", got)
	}
}

func TestSpeechStreamWriterExtractsPartialTTSTag(t *testing.T) {
	var sink strings.Builder
	writer := NewSpeechStreamWriter(&sink)

	chunks := []string{
		"已完成设置。\n<t",
		"ts>已完成",
		"，当前音量是 42。</tts>\n正文继续。",
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

func TestSpeechStreamWriterIgnoresTextOutsideTTSTag(t *testing.T) {
	var sink strings.Builder
	writer := NewSpeechStreamWriter(&sink)

	if _, err := writer.Write([]byte("正文不播报。<tts>播报这个。</tts>尾部不播报。")); err != nil {
		t.Fatalf("Write() error = %v", err)
	}

	if got := sink.String(); got != "播报这个。" {
		t.Fatalf("streamed speech = %q", got)
	}
}

func TestSpeechStreamWriterHandlesSplitUTF8Rune(t *testing.T) {
	var sink strings.Builder
	writer := NewSpeechStreamWriter(&sink)
	payload := []byte(`<tts>好</tts>`)
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

func TestSpeechStreamWriterHandlesMultipleTTSTags(t *testing.T) {
	var sink strings.Builder
	writer := NewSpeechStreamWriter(&sink)
	payload := `<tts>第一句。</tts>正文<tts>第二句。</tts>`
	if _, err := writer.Write([]byte(payload)); err != nil {
		t.Fatalf("Write() error = %v", err)
	}
	if got := sink.String(); got != "第一句。第二句。" {
		t.Fatalf("streamed speech = %q", got)
	}
}

func TestFinalizeAssistantOutputKeepsPlainText(t *testing.T) {
	raw := "  完整回答。  "

	output := finalizeAssistantOutput(raw)

	if output != "完整回答。" {
		t.Fatalf("output = %q", output)
	}
}
