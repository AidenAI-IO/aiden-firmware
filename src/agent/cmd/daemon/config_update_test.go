package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestUpdateConfigFilePreservesCommentsAndUnknownFields(t *testing.T) {
	source := `locale = "en-US" # locale comment

[device]
device_type = "iOS"

[model_providers.openai-main]
type = "openai"
api_key = "test-key"

[model]
provider = "openai-main"
model = "gpt-5.5"
responses = ["text", "audio"]

[hid]
keyboard_layout = "qwerty" # keep this comment
future_key = "preserve me"
`
	dir := t.TempDir()
	path := filepath.Join(dir, "agent.toml")
	if err := os.WriteFile(path, []byte(source), 0o640); err != nil {
		t.Fatal(err)
	}
	result, err := updateConfigFile(path, []byte(`{"config":{"hid":{"keyboard_layout":"azerty"}}}`))
	if err != nil {
		t.Fatal(err)
	}
	if !result.OK || len(result.ChangedPaths) != 1 || !result.RebootRequired {
		t.Fatalf("unexpected result: %+v", result)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(got), `keyboard_layout = "azerty" # keep this comment`) ||
		!strings.Contains(string(got), `future_key = "preserve me"`) ||
		!strings.Contains(string(got), `responses = ["text", "audio"]`) {
		t.Fatalf("unrelated TOML content was lost:\n%s", got)
	}
}

func TestUpdateConfigFileEmptyPatchDoesNotRewrite(t *testing.T) {
	source := []byte("locale = \"en-US\"\n[hid]\nkeyboard_layout = \"qwerty\"\n")
	path := filepath.Join(t.TempDir(), "agent.toml")
	if err := os.WriteFile(path, source, 0o640); err != nil {
		t.Fatal(err)
	}
	result, err := updateConfigFile(path, []byte(`{"config":{}}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(result.ChangedPaths) != 0 {
		t.Fatalf("changed paths = %v", result.ChangedPaths)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(source) {
		t.Fatalf("empty patch changed file")
	}
}

func TestConfigPatchOperationsUseRecordDeletesAndExplicitZero(t *testing.T) {
	ops, err := configPatchOperations(map[string]json.RawMessage{
		"tts_providers": json.RawMessage(`{"old":null,"new":{"type":"fish-audio"}}`),
		"audio_archive": json.RawMessage(`{"max_files":0,"enabled":false}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(ops) != 4 {
		t.Fatalf("operations = %+v", ops)
	}
	var sawDelete, sawZero, sawFalse bool
	for _, op := range ops {
		if op.DeleteTable && strings.Join(op.Path, ".") == "tts_providers.old" {
			sawDelete = true
		}
		if op.Value == int64(0) {
			sawZero = true
		}
		if op.Value == false {
			sawFalse = true
		}
	}
	if !sawDelete || !sawZero || !sawFalse {
		t.Fatalf("record delete/explicit values missing: %+v", ops)
	}
}
