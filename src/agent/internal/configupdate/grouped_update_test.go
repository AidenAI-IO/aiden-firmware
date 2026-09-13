package configupdate

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestUpdateWritesGroupedTOMLPaths(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "agent.toml")
	const source = `[model_settings.providers.local]
type = "openai"
api_key = "secret"

[model_settings.model]
provider = "local"
model = "gpt-old"

[voice_settings.mode]
input_mode = "text"
`
	if err := os.WriteFile(path, []byte(source), 0o600); err != nil {
		t.Fatal(err)
	}
	result, err := NewService().Update(path, []byte(`{"config":{"model":{"model":"gpt-new"}}}`))
	if err != nil {
		t.Fatalf("Update() error = %v", err)
	}
	if !result.OK || len(result.ChangedPaths) != 1 || result.ChangedPaths[0] != "model_settings.model.model" {
		t.Fatalf("unexpected result: %#v", result)
	}
	updated, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	text := string(updated)
	if !strings.Contains(text, "[model_settings.model]") || !strings.Contains(text, `model = "gpt-new"`) {
		t.Fatalf("grouped model section missing from %s", text)
	}
	if strings.Contains(text, "[model]\n") {
		t.Fatalf("legacy model section was written: %s", text)
	}
}
