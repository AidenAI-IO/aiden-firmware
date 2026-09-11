package ota

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

func TestSnapshotProtectedDataCopiesOnlyExistingProtectedFiles(t *testing.T) {
	root := t.TempDir()
	dataRoot := filepath.Join(root, "userdata")
	defer setProtectedDataRoot(dataRoot)()
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
	var current, referenced string
	for i := 0; i < protectedSnapshotRetention+2; i++ {
		name := filepath.Join(transactions, fmt.Sprintf("pre-%019d-v1", i))
		if i == 0 {
			current = name
		}
		if i == 1 {
			referenced = name
		}
		if err := os.Mkdir(name, 0755); err != nil {
			t.Fatal(err)
		}
	}
	if err := SaveState(filepath.Join(root, "state.json"), State{DataSnapshotPath: referenced}); err != nil {
		t.Fatal(err)
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
	if _, err := os.Stat(referenced); err != nil {
		t.Fatalf("referenced snapshot was pruned: %v", err)
	}
	// All remaining directories have no manifest or state reference, modelling
	// a crash during snapshot creation. They still count towards retention.
	for _, i := range []int{2, 3} {
		if _, err := os.Stat(filepath.Join(transactions, fmt.Sprintf("pre-%019d-v1", i))); !os.IsNotExist(err) {
			t.Fatalf("orphan %d was not pruned: %v", i, err)
		}
	}
}

func TestSnapshotFailureRemovesIncompleteDirectory(t *testing.T) {
	root := t.TempDir()
	defer setProtectedDataRoot(filepath.Join(root, "userdata"))()
	writeSnapshotTestFile(t, filepath.Join(root, "userdata/agent/agent.toml"), "old-agent")
	// The second source cannot be copied as a regular file.
	if err := os.MkdirAll(filepath.Join(root, "userdata/system/env"), 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := SnapshotProtectedData(filepath.Join(root, "ota"), "v2"); err == nil {
		t.Fatal("invalid source unexpectedly succeeded")
	}
	entries, err := os.ReadDir(filepath.Join(root, "ota/transactions"))
	if err != nil || len(entries) != 0 {
		t.Fatalf("incomplete snapshot remains: %v, %v", entries, err)
	}
}

func writeSnapshotTestFile(t *testing.T, path, contents string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(contents), 0600); err != nil {
		t.Fatal(err)
	}
}

func makeRestoreFixture(t *testing.T) (string, string, []string) {
	t.Helper()
	root := t.TempDir()
	snapshot, dataRoot := filepath.Join(root, "snapshot"), filepath.Join(root, "userdata")
	files := []string{"agent/agent.toml", "system/env", "wpa_supplicant.conf"}
	manifest, err := json.Marshal(map[string]any{"files": files})
	if err != nil {
		t.Fatal(err)
	}
	writeSnapshotTestFile(t, filepath.Join(snapshot, "manifest.json"), string(manifest))
	for _, rel := range files {
		writeSnapshotTestFile(t, filepath.Join(snapshot, "userdata", rel), "old:"+rel)
		writeSnapshotTestFile(t, filepath.Join(dataRoot, rel), "new:"+rel)
	}
	return snapshot, dataRoot, files
}

func TestRestoreStagesAllFilesBeforeReplacingConfiguration(t *testing.T) {
	for _, failure := range []string{"missing-source", "invalid-destination", "invalid-manifest"} {
		t.Run(failure, func(t *testing.T) {
			snapshot, dataRoot, files := makeRestoreFixture(t)
			last := files[len(files)-1]
			switch failure {
			case "missing-source":
				if err := os.Remove(filepath.Join(snapshot, "userdata", last)); err != nil {
					t.Fatal(err)
				}
			case "invalid-destination":
				if err := os.Remove(filepath.Join(dataRoot, last)); err != nil {
					t.Fatal(err)
				}
				if err := os.Mkdir(filepath.Join(dataRoot, last), 0700); err != nil {
					t.Fatal(err)
				}
			case "invalid-manifest":
				writeSnapshotTestFile(t, filepath.Join(snapshot, "manifest.json"), `{"files":["agent/agent.toml","../escape"]}`)
			}
			if err := restoreProtectedData(snapshot, dataRoot, os.Rename); err == nil {
				t.Fatal("invalid restore unexpectedly succeeded")
			}
			for _, rel := range files[:2] {
				assertFileContent(t, filepath.Join(dataRoot, rel), "new:"+rel)
			}
		})
	}
}

func TestRestoreCommitFailureUndoesEarlierReplacements(t *testing.T) {
	for _, missingOriginal := range []bool{false, true} {
		t.Run(fmt.Sprintf("missing-original=%t", missingOriginal), func(t *testing.T) {
			snapshot, dataRoot, files := makeRestoreFixture(t)
			if missingOriginal {
				if err := os.Remove(filepath.Join(dataRoot, files[0])); err != nil {
					t.Fatal(err)
				}
			}
			rename := func(src, dst string) error {
				if filepath.Base(src) == "next" && dst == filepath.Join(dataRoot, files[2]) {
					return syscall.EIO
				}
				return os.Rename(src, dst)
			}
			if err := restoreProtectedData(snapshot, dataRoot, rename); !errors.Is(err, syscall.EIO) {
				t.Fatalf("restore error = %v, want EIO", err)
			}
			for i, rel := range files {
				if missingOriginal && i == 0 {
					if _, err := os.Stat(filepath.Join(dataRoot, rel)); !os.IsNotExist(err) {
						t.Fatalf("created destination remains: %v", err)
					}
					continue
				}
				assertFileContent(t, filepath.Join(dataRoot, rel), "new:"+rel)
			}
			// Retry succeeds, restores every file, and is idempotent.
			for attempt := 0; attempt < 2; attempt++ {
				if err := restoreProtectedData(snapshot, dataRoot, os.Rename); err != nil {
					t.Fatal(err)
				}
			}
			for _, rel := range files {
				assertFileContent(t, filepath.Join(dataRoot, rel), "old:"+rel)
			}
		})
	}
}

func TestRestoreUndoFailureRetainsBackupAndSnapshot(t *testing.T) {
	snapshot, dataRoot, files := makeRestoreFixture(t)
	rename := func(src, dst string) error {
		if filepath.Base(src) == "undo" || dst == filepath.Join(dataRoot, files[1]) {
			return syscall.EIO
		}
		return os.Rename(src, dst)
	}
	err := restoreProtectedData(snapshot, dataRoot, rename)
	if !errors.Is(err, syscall.EIO) || !strings.Contains(err.Error(), "backup") {
		t.Fatalf("restore error = %v, want undo error with backup path", err)
	}
	backups, err := filepath.Glob(filepath.Join(dataRoot, "agent/.ota-restore-*/undo"))
	if err != nil || len(backups) != 1 {
		t.Fatalf("undo backup not retained: %v, %v", backups, err)
	}
	assertFileContent(t, backups[0], "new:"+files[0])
	assertFileContent(t, filepath.Join(snapshot, "userdata", files[0]), "old:"+files[0])
}
