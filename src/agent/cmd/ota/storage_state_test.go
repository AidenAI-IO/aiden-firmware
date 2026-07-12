package main

import (
	"os"
	"path/filepath"
	"testing"
)

func writeStateFile(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "storage.state")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestSDOTACacheDirMounted(t *testing.T) {
	path := writeStateFile(t, "SD_PRESENT=1\nSD_MOUNTED=1\nSD_MOUNTPOINT=/mnt/sdcard\nEFFECTIVE_MODE=2\n")
	if got, want := sdOTACacheDir(path), filepath.Join("/mnt/sdcard", "aiden", "ota-cache"); got != want {
		t.Fatalf("sdOTACacheDir = %q, want %q", got, want)
	}
}

func TestSDOTACacheDirNotMounted(t *testing.T) {
	path := writeStateFile(t, "SD_PRESENT=1\nSD_MOUNTED=0\nSD_MOUNTPOINT=/mnt/sdcard\n")
	if got := sdOTACacheDir(path); got != "" {
		t.Fatalf("sdOTACacheDir = %q, want empty for unmounted card", got)
	}
}

func TestSDOTACacheDirMissingFile(t *testing.T) {
	if got := sdOTACacheDir(filepath.Join(t.TempDir(), "missing")); got != "" {
		t.Fatalf("sdOTACacheDir = %q, want empty when the state file is absent", got)
	}
}

func TestSDOTACacheDirMissingMountPoint(t *testing.T) {
	path := writeStateFile(t, "SD_MOUNTED=1\n")
	if got := sdOTACacheDir(path); got != "" {
		t.Fatalf("sdOTACacheDir = %q, want empty without a mount point", got)
	}
}
