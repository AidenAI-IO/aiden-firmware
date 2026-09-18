package logging

import (
	"bytes"
	"strings"
	"testing"
	"time"
)

func TestFormatMessageAtUsesExplicitLevelAndComponent(t *testing.T) {
	now := time.Date(2026, 8, 5, 6, 22, 3, 0, time.FixedZone("CST", 8*60*60))
	got := FormatMessageAt(now, Warn, "agent", "http_retry", "transport error on attempt 2/3: timeout")
	want := `2026-08-04T22:22:03Z [WARN][agent][http_retry] log_message message="transport error on attempt 2/3: timeout"`
	if got != want {
		t.Fatalf("FormatMessageAt() = %q, want %q", got, want)
	}
}

func TestFormatMessageAtDoesNotInspectMessageText(t *testing.T) {
	now := time.Date(2026, 8, 5, 6, 22, 3, 0, time.UTC)
	got := FormatMessageAt(now, Info, "agent", "reply", "[ERROR] quoted output")
	want := `2026-08-05T06:22:03Z [INFO][agent][reply] log_message message="[ERROR] quoted output"`
	if got != want {
		t.Fatalf("FormatMessageAt() = %q, want %q", got, want)
	}
}

func TestFormatEventAtEscapesFields(t *testing.T) {
	now := time.Date(2026, 8, 5, 6, 22, 3, 0, time.UTC)
	got := FormatEventAt(now, Error, "frame_service", "camera", "device_open_failed",
		Field{Key: "device", Value: "/dev/video0"},
		Field{Key: "errno", Value: 16},
		Field{Key: "error", Value: "Device or resource busy"},
	)
	want := `2026-08-05T06:22:03Z [ERROR][frame_service][camera] device_open_failed device=/dev/video0 errno=16 error="Device or resource busy"`
	if got != want {
		t.Fatalf("FormatEventAt() = %q, want %q", got, want)
	}
}

func TestLogMessageHonorsMinimumLevel(t *testing.T) {
	var output bytes.Buffer
	restoreOutput := SetOutput(&output)
	defer restoreOutput()
	restoreLevel := SetMinimumLevel(Warn)
	defer restoreLevel()

	Infof("agent", "test", "hidden")
	Errorf("agent", "test", "visible")

	got := output.String()
	if strings.Contains(got, "hidden") || !strings.Contains(got, "visible") {
		t.Fatalf("minimum level not applied: %q", got)
	}
	if !strings.Contains(got, `[ERROR][agent][test] log_message message="visible"`) {
		t.Fatalf("unexpected record format: %q", got)
	}
}

func TestLogEventHonorsMinimumLevel(t *testing.T) {
	var output bytes.Buffer
	restoreOutput := SetOutput(&output)
	defer restoreOutput()
	restoreLevel := SetMinimumLevel(Warn)
	defer restoreLevel()

	if err := LogEvent(Info, "agent", "test", "hidden"); err != nil {
		t.Fatal(err)
	}
	if err := LogEvent(Error, "agent", "test", "visible"); err != nil {
		t.Fatal(err)
	}
	got := output.String()
	if strings.Contains(got, "hidden") || !strings.Contains(got, "visible") {
		t.Fatalf("minimum level not applied: %q", got)
	}
}

func TestParseLevelRejectsUnknownSeverity(t *testing.T) {
	if _, ok := ParseLevel("verbose"); ok {
		t.Fatal("ParseLevel accepted an unsupported severity")
	}
	if level, ok := ParseLevel("warn"); !ok || level != Warn {
		t.Fatalf("ParseLevel(warn) = %q, %v", level, ok)
	}
}

func TestIsStructuredLine(t *testing.T) {
	line := `2026-08-05T06:22:03Z [DEBUG][rknn_vad][model] initialized version=1`
	if !IsStructuredLine(line) {
		t.Fatalf("IsStructuredLine(%q) = false, want true", line)
	}
	if IsStructuredLine(`[DEBUG][rknn_vad] unstructured`) {
		t.Fatal("IsStructuredLine() accepted a line without the common prefix")
	}
}
