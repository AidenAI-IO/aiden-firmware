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
	mountPoint := t.TempDir()
	path := writeStateFile(t, "SD_PRESENT=1\nSD_MOUNTED=1\nSD_MOUNTPOINT="+mountPoint+"\nEFFECTIVE_MODE=2\n")
	previous := availableBytesForPath
	availableBytesForPath = func(string) (int64, error) { return 300 << 20, nil }
	t.Cleanup(func() { availableBytesForPath = previous })
	if got, want := sdOTACacheDir(path, 200<<20), filepath.Join(mountPoint, "aiden", "ota-cache"); got != want {
		t.Fatalf("sdOTACacheDir = %q, want %q", got, want)
	}
}

func TestSDOTACacheDirRejectsCardBelowOTABudget(t *testing.T) {
	mountPoint := t.TempDir()
	path := writeStateFile(t, "SD_MOUNTED=1\nSD_MOUNTPOINT="+mountPoint+"\n")
	previous := availableBytesForPath
	availableBytesForPath = func(string) (int64, error) { return 100 << 20, nil }
	t.Cleanup(func() { availableBytesForPath = previous })

	if got := sdOTACacheDir(path, 200<<20); got != "" {
		t.Fatalf("sdOTACacheDir = %q, want eMMC fallback", got)
	}
}

func TestSDOTACacheDirCreditsExistingOTABudget(t *testing.T) {
	mountPoint := t.TempDir()
	downloadDir := filepath.Join(mountPoint, "aiden", "ota-cache")
	if err := os.MkdirAll(downloadDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(downloadDir, ".ota-reserve"), make([]byte, 150), 0o600); err != nil {
		t.Fatal(err)
	}
	path := writeStateFile(t, "SD_MOUNTED=1\nSD_MOUNTPOINT="+mountPoint+"\n")
	previous := availableBytesForPath
	availableBytesForPath = func(string) (int64, error) { return 50, nil }
	t.Cleanup(func() { availableBytesForPath = previous })

	if got := sdOTACacheDir(path, 200); got != downloadDir {
		t.Fatalf("sdOTACacheDir = %q, want %q", got, downloadDir)
	}
}

func TestSDOTACacheDirNotMounted(t *testing.T) {
	path := writeStateFile(t, "SD_PRESENT=1\nSD_MOUNTED=0\nSD_MOUNTPOINT=/mnt/sdcard\n")
	if got := sdOTACacheDir(path, 200<<20); got != "" {
		t.Fatalf("sdOTACacheDir = %q, want empty for unmounted card", got)
	}
}

func TestSDOTACacheDirMissingFile(t *testing.T) {
	if got := sdOTACacheDir(filepath.Join(t.TempDir(), "missing"), 200<<20); got != "" {
		t.Fatalf("sdOTACacheDir = %q, want empty when the state file is absent", got)
	}
}

func TestSDOTACacheDirMissingMountPoint(t *testing.T) {
	path := writeStateFile(t, "SD_MOUNTED=1\n")
	if got := sdOTACacheDir(path, 200<<20); got != "" {
		t.Fatalf("sdOTACacheDir = %q, want empty without a mount point", got)
	}
}
