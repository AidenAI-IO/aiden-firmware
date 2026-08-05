package logging

import (
	"bytes"
	"strings"
	"testing"
	"time"
)

func TestFormatLegacyfAtExtractsLevelComponentAndStableEvent(t *testing.T) {
	now := time.Date(2026, 8, 5, 6, 22, 3, 0, time.FixedZone("CST", 8*60*60))
	got := FormatLegacyfAt(
		now,
		Info,
		"agent",
		"models",
		"[WARN] [http-retry] transport error on attempt %d/%d: %v",
		2,
		3,
		"timeout",
	)
	want := `2026-08-04T22:22:03Z [WARN] [agent] [http_retry] transport_error_on_attempt message="transport error on attempt 2/3: timeout"`
	if got != want {
		t.Fatalf("FormatLegacyfAt() = %q, want %q", got, want)
	}
}

func TestFormatEventAtEscapesFields(t *testing.T) {
	now := time.Date(2026, 8, 5, 6, 22, 3, 0, time.UTC)
	got := FormatEventAt(now, Error, "frame_service", "camera", "device_open_failed",
		Field{Key: "device", Value: "/dev/video0"},
		Field{Key: "errno", Value: 16},
		Field{Key: "error", Value: "Device or resource busy"},
	)
	want := `2026-08-05T06:22:03Z [ERROR] [frame_service] [camera] device_open_failed device=/dev/video0 errno=16 error="Device or resource busy"`
	if got != want {
		t.Fatalf("FormatEventAt() = %q, want %q", got, want)
	}
}

func TestLegacyWriterFormatsEachNonBlankLine(t *testing.T) {
	var output bytes.Buffer
	writer := NewLegacyWriter(&output, "agent", "runtime", Info).(*legacyWriter)
	writer.now = func() time.Time { return time.Date(2026, 8, 5, 6, 22, 3, 0, time.UTC) }

	input := "\n[listen] Recording audio...\n[ERROR] [vad] helper failed\n"
	if _, err := writer.Write([]byte(input)); err != nil {
		t.Fatalf("Write() error = %v", err)
	}
	lines := strings.Split(strings.TrimSpace(output.String()), "\n")
	if len(lines) != 2 {
		t.Fatalf("got %d lines, want 2: %q", len(lines), output.String())
	}
	if !strings.Contains(lines[0], "[INFO] [agent] [listen] recording_audio") {
		t.Fatalf("first line = %q", lines[0])
	}
	if !strings.Contains(lines[1], "[ERROR] [agent] [vad] helper_failed") {
		t.Fatalf("second line = %q", lines[1])
	}
}
