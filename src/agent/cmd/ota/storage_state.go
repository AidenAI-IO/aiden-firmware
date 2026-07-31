package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

// storageStatePath is the runtime state mirror written by the agent's
// StorageManager (docs/04-agent/storage-modes.md).
var storageStatePath = "/run/aiden/storage.state"

var availableBytesForPath = filesystemAvailableBytes

type sdStorageState struct {
	mounted    bool
	mountPoint string
}

// sdOTACacheDir returns the SD-card OTA download cache directory when the
// agent reports a mounted card and the filesystem can hold the configured OTA
// budget. Existing cache and reserve files count toward that budget, so a
// reboot does not reject a card merely because its reserve is already present.
// OTA state files always stay on eMMC; only the download cache is redirected.
func sdOTACacheDir(statePath string, reserveSizeBytes int64) string {
	state := readSDStorageState(statePath)
	if !state.mounted || state.mountPoint == "" {
		return ""
	}
	downloadDir := filepath.Join(state.mountPoint, "aiden", "ota-cache")
	if reserveSizeBytes <= 0 {
		return downloadDir
	}
	available, err := availableBytesForPath(state.mountPoint)
	if err != nil {
		return ""
	}
	allocated, err := otaCacheBytes(downloadDir)
	if err != nil {
		return ""
	}
	if allocated >= reserveSizeBytes || available >= reserveSizeBytes-allocated {
		return downloadDir
	}
	return ""
}

func readSDStorageState(statePath string) sdStorageState {
	data, err := os.ReadFile(statePath)
	if err != nil {
		return sdStorageState{}
	}
	state := map[string]string{}
	for _, line := range strings.Split(string(data), "\n") {
		key, value, found := strings.Cut(line, "=")
		if found {
			state[strings.TrimSpace(key)] = strings.TrimSpace(value)
		}
	}
	if state["SD_MOUNTED"] != "1" {
		return sdStorageState{}
	}
	mountPoint := state["SD_MOUNTPOINT"]
	if mountPoint == "" {
		return sdStorageState{}
	}
	return sdStorageState{mounted: true, mountPoint: mountPoint}
}

func otaCacheBytes(downloadDir string) (int64, error) {
	entries, err := os.ReadDir(downloadDir)
	if os.IsNotExist(err) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	var total int64
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			return 0, err
		}
		if info.Size() > 0 && total > int64(^uint64(0)>>1)-info.Size() {
			return 0, fmt.Errorf("OTA cache size overflow")
		}
		total += info.Size()
	}
	return total, nil
}

func filesystemAvailableBytes(path string) (int64, error) {
	var stat syscall.Statfs_t
	if err := syscall.Statfs(path, &stat); err != nil {
		return 0, err
	}
	return int64(stat.Bavail) * int64(stat.Bsize), nil
}
