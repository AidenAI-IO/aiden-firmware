package configupdate

import (
	"os"
	"path/filepath"
	"testing"
)

func TestServiceUpdateAppliesPatchWithoutCLI(t *testing.T) {
	path := filepath.Join(t.TempDir(), "agent.toml")
	if err := os.WriteFile(path, []byte("[hid]\nkeyboard_layout = \"qwerty\"\n"), 0o640); err != nil {
		t.Fatal(err)
	}

	result, err := NewService().Update(path, []byte(`{"config":{"hid":{"keyboard_layout":"azerty"}}}`))
	if err != nil {
		t.Fatal(err)
	}
	if !result.OK || !result.RebootRequired {
		t.Fatalf("result = %+v, want successful reboot-requiring update", result)
	}
	if len(result.ChangedPaths) != 1 || result.ChangedPaths[0] != "hid.keyboard_layout" {
		t.Fatalf("changed paths = %v", result.ChangedPaths)
	}
}

func TestServiceUpdateClassifiesInvalidRequest(t *testing.T) {
	_, err := NewService().Update(filepath.Join(t.TempDir(), "agent.toml"), []byte("not json"))
	if err == nil {
		t.Fatal("Update() error = nil")
	}
	if got := ErrorKind(err); got != ErrorKindInvalidRequest {
		t.Fatalf("ErrorKind() = %q, want %q", got, ErrorKindInvalidRequest)
	}
}
