package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"aiden-agent/internal/agent"
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

func TestUpdateConfigFileWritesModelLogRawHTTP(t *testing.T) {
	source := `[model]
provider = "openai"
model = "gpt-5.5"
log_raw_http = false
`
	path := filepath.Join(t.TempDir(), "agent.toml")
	if err := os.WriteFile(path, []byte(source), 0o640); err != nil {
		t.Fatal(err)
	}

	result, err := updateConfigFile(path, []byte(`{"config":{"model":{"log_raw_http":true}}}`))
	if err != nil {
		t.Fatal(err)
	}
	if !result.Config.Model.LogRawHTTP {
		t.Fatal("resolved model.log_raw_http = false, want true")
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(got), "log_raw_http = true") {
		t.Fatalf("model.log_raw_http was not updated:\n%s", got)
	}
}

func TestUpdateConfigFileAddsProviderToInlineTable(t *testing.T) {
	source := `model_providers = { old = { type = "openai" } }

[model]
provider = "old"
model = "gpt-5.5"
`
	path := filepath.Join(t.TempDir(), "agent.toml")
	if err := os.WriteFile(path, []byte(source), 0o640); err != nil {
		t.Fatal(err)
	}

	result, err := updateConfigFile(path, []byte(`{"config":{"model_providers":{"new":{"type":"ollama"}}}}`))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(result.ChangedPaths, ",") != "model_providers.new.type" {
		t.Fatalf("changed paths = %v", result.ChangedPaths)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(got), `new = { type = "ollama" }`) ||
		strings.Contains(string(got), "[model_providers.new]") {
		t.Fatalf("provider was not added inside the inline table:\n%s", got)
	}
}

func TestConfigUpdateErrorsExposeStableKinds(t *testing.T) {
	path := filepath.Join(t.TempDir(), "agent.toml")
	if _, err := updateConfigFile(path, []byte("not json")); err == nil {
		t.Fatal("invalid patch error = nil")
	} else if got := configUpdateErrorKind(err); got != configUpdateErrorInvalid {
		t.Fatalf("invalid error kind = %q", got)
	}

	missingParent := filepath.Join(t.TempDir(), "missing", "agent.toml")
	if _, err := updateConfigFile(missingParent, []byte(`{"config":{}}`)); err == nil {
		t.Fatal("missing parent error = nil")
	} else if got := configUpdateErrorKind(err); got != configUpdateErrorInternal {
		t.Fatalf("internal error kind = %q", got)
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

func TestUpdateConfigFileRenamesDottedKeyProviderWithoutLeavingSource(t *testing.T) {
	source := `model_providers.old.type = "openai"
model_providers.old.api_key = "model-secret"
model.provider = "old"
model.model = "gpt-5.5"
`
	path := filepath.Join(t.TempDir(), "agent.toml")
	if err := os.WriteFile(path, []byte(source), 0o640); err != nil {
		t.Fatal(err)
	}
	patch := []byte(`{"config":{"model_providers":{"old":null,"new":{"type":"openai"}},"_provider_renames":{"model_providers":{"new":"old"}},"model":{"provider":"new"}}}`)
	if _, err := updateConfigFile(path, patch); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	text := string(got)
	if strings.Contains(text, "model_providers.old.") {
		t.Fatalf("old dotted-key provider was not removed:\n%s", text)
	}
	if !strings.Contains(text, `api_key = "model-secret"`) {
		t.Fatalf("renamed provider lost credential:\n%s", text)
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

func TestUpdateConfigFilePreservesLiteralProviderCredentialSyntaxWhenEmpty(t *testing.T) {
	source := `[model_providers.openai]
type = 'openai'
api_key = 'sk-secret-value-1234'

[model]
provider = "openai"
model = "gpt-5.5"
`
	path := filepath.Join(t.TempDir(), "agent.toml")
	if err := os.WriteFile(path, []byte(source), 0o640); err != nil {
		t.Fatal(err)
	}
	result, err := updateConfigFile(path, []byte(`{"config":{"model_providers":{"openai":{"type":"openai","api_key":"","base_url":"https://x.test"}}}}`))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(result.ChangedPaths, ",") != "model_providers.openai.base_url" {
		t.Fatalf("changed paths = %v", result.ChangedPaths)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	text := string(got)
	if !strings.Contains(text, "api_key = 'sk-secret-value-1234'") ||
		!strings.Contains(text, `base_url = "https://x.test"`) {
		t.Fatalf("provider credential syntax was not preserved:\n%s", text)
	}
}

func TestUpdateConfigFileAcceptsRedactedResolvedConfigPayload(t *testing.T) {
	source := `[model_providers.openai]
type = "openai"
api_key = "model-secret"

[tts_providers.voice]
type = "fish-audio"
api_key = "tts-secret"

[stt_providers.tencent]
type = "tencent-asr"
api_key = "stt-secret"
secret_id = "secret-id"
secret_key = "secret-key"

[model]
provider = "openai"
model = "gpt-5.5"

[tts]
provider = "voice"

[stt]
provider = "tencent"
language = "zh"

[storage]
monitor_enabled = true
root_path = "/custom/root"
check_interval_seconds = 123
warning_threshold_mb = 81
critical_threshold_mb = 21
emergency_threshold_mb = 9
recovery_hysteresis_mb = 4

[storage.degraded_mode]
disable_llm_http_log = false
disable_audio_archive = true
disable_session_archive = false
max_agent_log_mb = 3

[storage.cleanup]
enabled = false
llm_http_log_retention_days = [8, 4]
audio_archive_retention_days = [20, 2]
session_archive_retention_days = [14]
cleanup_retry_interval_seconds = 42
`
	path := filepath.Join(t.TempDir(), "agent.toml")
	if err := os.WriteFile(path, []byte(source), 0o640); err != nil {
		t.Fatal(err)
	}
	cfg, err := agent.LoadResolvedConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	payload, err := json.Marshal(map[string]any{"config": webConfigDTOFromAgentConfig(cfg)})
	if err != nil {
		t.Fatal(err)
	}
	result, err := updateConfigFile(path, payload)
	if err != nil {
		t.Fatalf("redacted resolved config was rejected: %v", err)
	}
	if len(result.ChangedPaths) != 0 {
		t.Fatalf("unchanged resolved config reported changes: %v", result.ChangedPaths)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != source {
		t.Fatalf("unchanged resolved config rewrote TOML:\n%s", got)
	}
	text := string(got)
	for _, want := range []string{
		`api_key = "model-secret"`,
		`api_key = "tts-secret"`,
		`api_key = "stt-secret"`,
		`secret_id = "secret-id"`,
		`secret_key = "secret-key"`,
		`root_path = "/custom/root"`,
		`max_agent_log_mb = 3`,
		`cleanup_retry_interval_seconds = 42`,
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("resolved config update lost %q:\n%s", want, text)
		}
	}
}

func TestUpdateConfigFileRejectsChangedReadOnlyCredentialStatus(t *testing.T) {
	path := filepath.Join(t.TempDir(), "agent.toml")
	if err := os.WriteFile(path, []byte("[search]\nprovider = \"duckduckgo\"\n"), 0o640); err != nil {
		t.Fatal(err)
	}
	_, err := updateConfigFile(path, []byte(`{"config":{"search":{"has_api_key":true}}}`))
	if err == nil || !strings.Contains(err.Error(), "read-only status field") {
		t.Fatalf("changed read-only status error = %v", err)
	}
}

func TestUpdateConfigFileRejectsChangedDerivedPointerMode(t *testing.T) {
	source := "[device]\ndevice_type = \"iOS\"\n"
	path := filepath.Join(t.TempDir(), "agent.toml")
	if err := os.WriteFile(path, []byte(source), 0o640); err != nil {
		t.Fatal(err)
	}
	_, err := updateConfigFile(path, []byte(`{"config":{"hid":{"pointer_mode":"touchscreen"}}}`))
	if err == nil || !strings.Contains(err.Error(), "hid.pointer_mode is a read-only derived field") {
		t.Fatalf("changed derived field error = %v", err)
	}
	got, readErr := os.ReadFile(path)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if string(got) != source {
		t.Fatalf("rejected derived field changed config:\n%s", got)
	}
}

func TestUpdateConfigFileUpdatesNestedStorageConfig(t *testing.T) {
	source := `[storage]
monitor_enabled = true
root_path = "/userdata"
check_interval_seconds = 300
warning_threshold_mb = 50
critical_threshold_mb = 10
emergency_threshold_mb = 5
recovery_hysteresis_mb = 5

[storage.degraded_mode]
max_agent_log_mb = 1 # keep comment

[storage.cleanup]
enabled = true
llm_http_log_retention_days = [7, 3, 1, 0]
cleanup_retry_interval_seconds = 60
`
	path := filepath.Join(t.TempDir(), "agent.toml")
	if err := os.WriteFile(path, []byte(source), 0o640); err != nil {
		t.Fatal(err)
	}
	result, err := updateConfigFile(path, []byte(`{"config":{"storage":{"degraded_mode":{"max_agent_log_mb":4},"cleanup":{"llm_http_log_retention_days":[5,1]}}}}`))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(result.ChangedPaths, ",") != "storage.cleanup.llm_http_log_retention_days,storage.degraded_mode.max_agent_log_mb" {
		t.Fatalf("changed paths = %v", result.ChangedPaths)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(got), "max_agent_log_mb = 4 # keep comment") ||
		!strings.Contains(string(got), "llm_http_log_retention_days = [5, 1]") {
		t.Fatalf("nested storage fields were not updated losslessly:\n%s", got)
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

func TestUpdateConfigFileRejectsExistingProviderRenameTarget(t *testing.T) {
	source := `[model_providers.old]
type = "openai"
api_key = "old-secret"

[model_providers.existing]
type = "ollama"
base_url = "http://target"
api_key = "target-secret"

[model]
provider = "old"
model = "gpt-5.5"
`
	path := filepath.Join(t.TempDir(), "agent.toml")
	if err := os.WriteFile(path, []byte(source), 0o640); err != nil {
		t.Fatal(err)
	}
	patch := []byte(`{"config":{"model_providers":{"old":null,"existing":{"type":"openai"}},"_provider_renames":{"model_providers":{"existing":"old"}},"model":{"provider":"existing"}}}`)
	if _, err := updateConfigFile(path, patch); err == nil || !strings.Contains(err.Error(), "target model_providers.existing already exists") {
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

func TestUpdateConfigFileCreatesMissingConfig(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "agent.toml")
	result, err := updateConfigFile(path, []byte(`{"config":{"agent":{"locale":"en-US"}}}`))
	if err != nil {
		t.Fatal(err)
	}
	if !result.OK || strings.Join(result.ChangedPaths, ",") != "locale" {
		t.Fatalf("unexpected result: %+v", result)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(got), `locale = "en-US"`) {
		t.Fatalf("missing config was not created:\n%s", got)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o640 {
		t.Fatalf("created config mode = %o, want 640", info.Mode().Perm())
	}
}

func TestConfigPatchOperationsRejectsSectionNull(t *testing.T) {
	for _, section := range []string{"hid", "agent", "model_providers"} {
		t.Run(section, func(t *testing.T) {
			_, err := configPatchOperations(map[string]json.RawMessage{section: json.RawMessage("null")})
			if err == nil || !strings.Contains(err.Error(), section+" must be an object") {
				t.Fatalf("configPatchOperations() error = %v", err)
			}
		})
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
	for _, renames := range []string{
		"null",
		`{"model_providers":null}`,
		`{"model_providers":{"one":"old","two":"old"}}`,
	} {
		patch := `{"config":{"_provider_renames":` + renames + `}}`
		if _, err := updateConfigFile(path, []byte(patch)); err == nil {
			t.Fatalf("updateConfigFile(%s) error = nil", patch)
		}
	}
}

func TestValidateWebConfigPatchReportsScalarTypeErrors(t *testing.T) {
	tests := []struct {
		patch map[string]json.RawMessage
		want  string
	}{
		{map[string]json.RawMessage{"device": json.RawMessage(`{"backend":7}`)}, "device.backend: expected string"},
		{map[string]json.RawMessage{"live_activity": json.RawMessage(`{"enabled":"yes"}`)}, "live_activity.enabled: expected bool"},
		{map[string]json.RawMessage{"telemetry": json.RawMessage(`{"tags":"alpha"}`)}, "telemetry.tags: expected array"},
		{map[string]json.RawMessage{"tts": json.RawMessage(`{"speed":"fast"}`)}, "tts.speed: expected number"},
	}
	for _, tt := range tests {
		if err := validateWebConfigPatch(tt.patch); err == nil || !strings.Contains(err.Error(), tt.want) {
			t.Fatalf("validateWebConfigPatch() error = %v, want %q", err, tt.want)
		}
	}
}

func TestUpdateConfigFileRejectsOutOfRangeNumbersWithoutChangingFile(t *testing.T) {
	source := []byte("[model]\nprovider = \"openai\"\nmodel = \"gpt-5.5\"\n\n[audio_archive]\nmax_files = 5\n\n[tts]\nspeed = 1\n")
	tests := []struct {
		name  string
		patch string
	}{
		{name: "integer overflow", patch: `{"config":{"audio_archive":{"max_files":999999999999999999999}}}`},
		{name: "float overflow", patch: `{"config":{"tts":{"speed":1e999}}}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "agent.toml")
			if err := os.WriteFile(path, source, 0o640); err != nil {
				t.Fatal(err)
			}
			if _, err := updateConfigFile(path, []byte(tt.patch)); err == nil {
				t.Fatal("updateConfigFile() error = nil")
			}
			got, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(got, source) {
				t.Fatalf("rejected numeric patch changed config:\n%s", got)
			}
		})
	}
}

func TestUpdateConfigFileUpdatesInlineTable(t *testing.T) {
	source := []byte("hid = { keyboard_layout = \"qwerty\" } # keep\n\n[model]\nprovider = \"openai\"\nmodel = \"gpt-5.5\"\n")
	path := filepath.Join(t.TempDir(), "agent.toml")
	if err := os.WriteFile(path, source, 0o640); err != nil {
		t.Fatal(err)
	}
	result, err := updateConfigFile(path, []byte(`{"config":{"hid":{"keyboard_device":"/dev/hidg9"}}}`))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(result.ChangedPaths, ",") != "hid.keyboard_device" {
		t.Fatalf("changed paths = %v", result.ChangedPaths)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	want := "hid = { keyboard_layout = \"qwerty\", keyboard_device = \"/dev/hidg9\" } # keep\n"
	if !strings.Contains(string(got), want) || strings.Contains(string(got), "\n[hid]\n") {
		t.Fatalf("inline table was not updated in place:\n%s", got)
	}
}

func TestUpdateConfigFileMigratesLegacyModelCredential(t *testing.T) {
	path := filepath.Join(t.TempDir(), "agent.toml")
	if err := os.WriteFile(path, []byte("[model]\nprovider = \"openai\"\napi_key = \"legacy-secret\"\nmodel = \"gpt-5.5\"\n"), 0o640); err != nil {
		t.Fatal(err)
	}
	patch := []byte(`{"config":{"model":{"provider":"openai","api_key":"legacy-secret","base_url":"https://ignored.example"}}}`)
	if _, err := updateConfigFile(path, patch); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	text := string(got)
	if !strings.Contains(text, "[model_providers.openai]") ||
		!strings.Contains(text, `api_key = "legacy-secret"`) {
		t.Fatalf("legacy credential was not migrated:\n%s", text)
	}
	modelAt := strings.Index(text, "[model]\n")
	if modelAt < 0 {
		t.Fatalf("model section missing:\n%s", text)
	}
	modelEnd := strings.Index(text[modelAt+1:], "\n[")
	modelSection := text[modelAt:]
	if modelEnd >= 0 {
		modelSection = text[modelAt : modelAt+1+modelEnd]
	}
	if strings.Contains(modelSection, "api_key") || strings.Contains(text, "ignored.example") {
		t.Fatalf("legacy model-only fields leaked into TOML:\n%s", text)
	}
}

func TestUpdateConfigFileRejectsNonObjectModelProvidersDuringLegacyCredentialMigration(t *testing.T) {
	source := []byte("[model]\nprovider = \"openai\"\napi_key = \"legacy-secret\"\nmodel = \"gpt-5.5\"\n")
	for _, modelProviders := range []string{"null", `[]`} {
		t.Run(modelProviders, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "agent.toml")
			if err := os.WriteFile(path, source, 0o640); err != nil {
				t.Fatal(err)
			}
			patch := []byte(`{"config":{"model":{"api_key":"legacy-secret"},"model_providers":` + modelProviders + `}}`)
			if _, err := updateConfigFile(path, patch); err == nil || !strings.Contains(err.Error(), "model_providers patch must be an object") {
				t.Fatalf("updateConfigFile() error = %v", err)
			}
			got, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if string(got) != string(source) {
				t.Fatalf("config changed after rejected patch:\n%s", got)
			}
		})
	}
}

func TestUpdateConfigFileAcceptsLegacyProviderTypeAlias(t *testing.T) {
	path := filepath.Join(t.TempDir(), "agent.toml")
	if err := os.WriteFile(path, nil, 0o640); err != nil {
		t.Fatal(err)
	}
	patch := []byte(`{"config":{"tts_providers":{"voice":{"provider":"fish-audio","api_key":"secret"}},"tts":{"provider":"voice"}}}`)
	if _, err := updateConfigFile(path, patch); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(got), `type = "fish-audio"`) || strings.Contains(string(got), "provider = \"fish-audio\"") {
		t.Fatalf("provider alias was not canonicalized:\n%s", got)
	}
}

func TestResolvedWebConfigOmitsLegacyModelCredential(t *testing.T) {
	cfg := agent.DefaultConfig()
	cfg.Model.APIKey = "top-secret"
	encoded, err := json.Marshal(webConfigDTOFromAgentConfig(cfg))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "top-secret") || strings.Contains(string(encoded), `"api_key"`) {
		t.Fatalf("resolved web config exposed legacy model credential: %s", encoded)
	}
}
