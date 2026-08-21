package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestRunNotificationsIOReadsPersistedJSONL(t *testing.T) {
	root := t.TempDir()
	eventsDir := filepath.Join(root, "memory", "notifications", "events")
	if err := os.MkdirAll(eventsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	event := `{"id":"7","source":"ios_ancs","app_identifier":"com.example","title":"Meeting","message":"moved","received_at":"2026-08-21T00:00:00Z"}` + "\n"
	if err := os.WriteFile(filepath.Join(eventsDir, "2026-08-21.jsonl"), []byte(event), 0o644); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	code := runNotificationsIO([]string{"list", "--dir", root, "--text", "MOVED", "--format", "jsonl"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("runNotificationsIO()=%d stderr=%q", code, stderr.String())
	}
	var decoded map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &decoded); err != nil {
		t.Fatalf("jsonl output is invalid: %v; output=%q", err, stdout.String())
	}
	if decoded["id"] != "7" {
		t.Fatalf("output=%#v", decoded)
	}
	if _, err := os.Stat(filepath.Join(root, "memory", "notifications", "state.json")); !os.IsNotExist(err) {
		t.Fatalf("query should not create or update notification state, stat err=%v", err)
	}
}

func TestRunNotificationsIORejectsInvalidSince(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := runNotificationsIO([]string{"--since", "not-a-cursor"}, &stdout, &stderr)
	if code != 1 || stderr.Len() == 0 {
		t.Fatalf("invalid since code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
}
