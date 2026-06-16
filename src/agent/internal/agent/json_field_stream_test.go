package agent

import (
	"strings"
	"testing"
)

func TestJSONFieldStreamWriterExtractsArbitraryTopLevelField(t *testing.T) {
	var sink strings.Builder
	writer := NewJSONFieldStreamWriter(&sink, "output")

	chunks := []string{
		`{"speech_text":"ignored",`,
		`"output":"完整回答`,
		`保留给屏幕。","metadata":{"output":"ignored"}}`,
	}
	for _, chunk := range chunks {
		if _, err := writer.Write([]byte(chunk)); err != nil {
			t.Fatalf("Write(%q) error = %v", chunk, err)
		}
	}

	if got := sink.String(); got != "完整回答保留给屏幕。" {
		t.Fatalf("streamed field = %q", got)
	}
}

func TestJSONFieldStreamWriterIgnoresNestedField(t *testing.T) {
	var sink strings.Builder
	writer := NewJSONFieldStreamWriter(&sink, "output")
	payload := `{"metadata":{"output":"不要输出"},"output":"输出这个。"}`

	if _, err := writer.Write([]byte(payload)); err != nil {
		t.Fatalf("Write() error = %v", err)
	}

	if got := sink.String(); got != "输出这个。" {
		t.Fatalf("streamed field = %q", got)
	}
}

func TestJSONFieldStreamWriterHandlesEscapesAndSplitUTF8(t *testing.T) {
	var sink strings.Builder
	writer := NewJSONFieldStreamWriter(&sink, "summary")
	payload := []byte(`{"summary":"第一行\n第二行\u3002 好","output":"ignored"}`)
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

	if got := sink.String(); got != "第一行\n第二行。 好" {
		t.Fatalf("streamed field = %q", got)
	}
}

func TestJSONFieldOrPlainStreamWriterExtractsStructuredField(t *testing.T) {
	var sink strings.Builder
	writer := NewJSONFieldOrPlainStreamWriter(&sink, "output")

	if _, err := writer.Write([]byte(`  {"speech_text":"ignored","output":"完整回答。"}`)); err != nil {
		t.Fatalf("Write() error = %v", err)
	}

	if got := sink.String(); got != "完整回答。" {
		t.Fatalf("streamed field = %q", got)
	}
}

func TestJSONFieldOrPlainStreamWriterPassesPlainText(t *testing.T) {
	var sink strings.Builder
	writer := NewJSONFieldOrPlainStreamWriter(&sink, "output")

	if _, err := writer.Write([]byte("plain answer")); err != nil {
		t.Fatalf("Write() error = %v", err)
	}

	if got := sink.String(); got != "plain answer" {
		t.Fatalf("plain stream = %q", got)
	}
}
