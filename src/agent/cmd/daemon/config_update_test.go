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

func TestUpdateConfigFileRenamesProviderWithoutLosingCredentials(t *testing.T) {
	source := `[model_providers.old]
type = "openai"
api_key = "model-secret"

[tts_providers.old]
type = "fish-audio"
api_key = "tts-secret"

[stt_providers.old]
type = "tencent-asr"
api_key = "stt-secret"
secret_id = "stt-id"
secret_key = "stt-key"

[model]
provider = "old"
model = "gpt-5.5"

[tts]
provider = "old"

[stt]
provider = "old"
`
	path := filepath.Join(t.TempDir(), "agent.toml")
	if err := os.WriteFile(path, []byte(source), 0o640); err != nil {
		t.Fatal(err)
	}
	patch := []byte(`{"config":{"model_providers":{"old":null,"new":{"type":"openai"}},"tts_providers":{"old":null,"new":{"type":"fish-audio"}},"stt_providers":{"old":null,"new":{"type":"tencent-asr"}},"_provider_renames":{"model_providers":{"new":"old"},"tts_providers":{"new":"old"},"stt_providers":{"new":"old"}},"model":{"provider":"new"},"tts":{"provider":"new"},"stt":{"provider":"new"}}}`)
	if _, err := updateConfigFile(path, patch); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	text := string(got)
	for _, want := range []string{
		"[model_providers.new]", "api_key = \"model-secret\"",
		"[tts_providers.new]", "api_key = \"tts-secret\"",
		"[stt_providers.new]", "api_key = \"stt-secret\"",
		"secret_id = \"stt-id\"", "secret_key = \"stt-key\"",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("renamed provider lost %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, "[model_providers.old]") ||
		strings.Contains(text, "[tts_providers.old]") ||
		strings.Contains(text, "[stt_providers.old]") {
		t.Fatalf("old provider records were not removed:\n%s", text)
	}
}

func TestUpdateConfigFilePreservesMaskedProviderCredential(t *testing.T) {
	source := `[model_providers.openai]
type = "openai"
api_key = "model-secret"

[model]
provider = "openai"
model = "gpt-5.5"
`
	path := filepath.Join(t.TempDir(), "agent.toml")
	if err := os.WriteFile(path, []byte(source), 0o640); err != nil {
		t.Fatal(err)
	}
	patch := []byte(`{"config":{"model_providers":{"openai":{"type":"openai","api_key":"mode***cret"}}}}`)
	if _, err := updateConfigFile(path, patch); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(got), `api_key = "model-secret"`) || strings.Contains(string(got), "mode***cret") {
		t.Fatalf("masked credential was not preserved:\n%s", got)
	}
}

func TestUpdateConfigFileRejectsMissingProviderRenameSource(t *testing.T) {
	source := `[model_providers.current]
type = "openai"

[model]
provider = "current"
model = "gpt-5.5"
`
	path := filepath.Join(t.TempDir(), "agent.toml")
	if err := os.WriteFile(path, []byte(source), 0o640); err != nil {
		t.Fatal(err)
	}
	patch := []byte(`{"config":{"model_providers":{"missing":null,"new":{"type":"openai"}},"_provider_renames":{"model_providers":{"new":"missing"}}}}`)
	if _, err := updateConfigFile(path, patch); err == nil || !strings.Contains(err.Error(), "does not exist") {
		t.Fatalf("updateConfigFile() error = %v", err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != source {
		t.Fatalf("rejected rename changed config:\n%s", got)
	}
}

func TestUpdateConfigFileAcceptsScalarMapEntries(t *testing.T) {
	path := filepath.Join(t.TempDir(), "agent.toml")
	if err := os.WriteFile(path, nil, 0o640); err != nil {
		t.Fatal(err)
	}
	patch := []byte(`{"config":{"voice_notifications":{"expiration":{"code_ttl_seconds":{"network":123}}}}}`)
	if _, err := updateConfigFile(path, patch); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(got), "[voice_notifications.expiration.code_ttl_seconds]") ||
		!strings.Contains(string(got), "network = 123") {
		t.Fatalf("scalar map entry was not written:\n%s", got)
	}
}

func TestUpdateConfigFileRejectsNonObjectPatch(t *testing.T) {
	path := filepath.Join(t.TempDir(), "agent.toml")
	if err := os.WriteFile(path, nil, 0o640); err != nil {
		t.Fatal(err)
	}
	for _, patch := range []string{"null", `[]`, `{"config":null}`} {
		if _, err := updateConfigFile(path, []byte(patch)); err == nil {
			t.Fatalf("updateConfigFile(%s) error = nil", patch)
		}
	}
}

func TestUpdateConfigFileRejectsMalformedProviderRenames(t *testing.T) {
	path := filepath.Join(t.TempDir(), "agent.toml")
	if err := os.WriteFile(path, nil, 0o640); err != nil {
		t.Fatal(err)
	}
	for _, renames := range []string{"null", `{"model_providers":null}`} {
		patch := `{"config":{"_provider_renames":` + renames + `}}`
		if _, err := updateConfigFile(path, []byte(patch)); err == nil {
			t.Fatalf("updateConfigFile(%s) error = nil", patch)
		}
	}
}
