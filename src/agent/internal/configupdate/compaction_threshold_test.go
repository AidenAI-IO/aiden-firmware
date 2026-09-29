package configupdate

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Config Web lists the compaction threshold next to the prune threshold, so it
// must round-trip through the wire format like its sibling.
func TestUpdateWritesContextCompactionThreshold(t *testing.T) {
	path := filepath.Join(t.TempDir(), "agent.toml")
	if err := os.WriteFile(path, []byte("[basic_settings.language_timezone]\nlocale = \"zh-CN\"\n"), 0o640); err != nil {
		t.Fatal(err)
	}
	result, err := NewService().Update(path, []byte(`{"config":{"agent":{"context_compaction_threshold":0.7}}}`))
	if err != nil {
		t.Fatalf("Update() error = %v", err)
	}
	if result.Config.Agent.ContextCompactionThreshold != 0.7 || result.Apply != "live" {
		t.Fatalf("result = %+v", result)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "context_compaction_threshold = 0.7") {
		t.Fatalf("threshold not persisted:\n%s", data)
	}
	if _, err := NewService().Plan(path, []byte(`{"config":{"agent":{"context_compaction_threshold":1.5}}}`)); err == nil {
		t.Fatal("a threshold outside [0, 1) must be rejected")
	}
}
