package ota

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSnapshotProtectedDataCopiesOnlyExistingProtectedFiles(t *testing.T) {
	root := t.TempDir()
	dataRoot := filepath.Join(root, "userdata")
	oldRoot := protectedDataRoot
	protectedDataRoot = dataRoot
	defer func() { protectedDataRoot = oldRoot }()
	src := filepath.Join(dataRoot, "agent", "agent.toml")
	if err := os.MkdirAll(filepath.Dir(src), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(src, []byte("input_mode=\"text\"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	snapshot, err := SnapshotProtectedData(filepath.Join(root, "ota"), "v2/rollback")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(snapshot, "userdata", "agent", "agent.toml")); err != nil {
		t.Fatal(err)
	}
}

func TestPruneProtectedSnapshotsRetainsCurrentAndNewest(t *testing.T) {
	root := t.TempDir()
	transactions := filepath.Join(root, "transactions")
	if err := os.MkdirAll(transactions, 0755); err != nil {
		t.Fatal(err)
	}
	var current string
	for i := 0; i < protectedSnapshotRetention+2; i++ {
		name := filepath.Join(transactions, "pre-0000000000000000000"+string(rune('0'+i))+"-v1")
		if i == 2 {
			current = name
		}
		if err := os.Mkdir(name, 0755); err != nil {
			t.Fatal(err)
		}
	}

	if err := pruneProtectedSnapshots(root, current); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(transactions)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != protectedSnapshotRetention {
		t.Fatalf("retained %d snapshots, want %d", len(entries), protectedSnapshotRetention)
	}
	if _, err := os.Stat(current); err != nil {
		t.Fatalf("current snapshot was pruned: %v", err)
	}
}
