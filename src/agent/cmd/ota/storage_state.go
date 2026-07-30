package main

import (
	"os"
	"path/filepath"
	"strings"
)

// storageStatePath is the runtime state mirror written by the agent's
// StorageManager (docs/04-agent/storage-modes.md).
var storageStatePath = "/run/aiden/storage.state"

// sdOTACacheDir returns the SD-card OTA download cache directory when the
// agent reports a mounted card, or "" to keep the eMMC default. OTA state
// files always stay on eMMC; only the download cache is redirected.
func sdOTACacheDir(statePath string) string {
	data, err := os.ReadFile(statePath)
	if err != nil {
		return ""
	}
	state := map[string]string{}
	for _, line := range strings.Split(string(data), "\n") {
		key, value, found := strings.Cut(line, "=")
		if found {
			state[strings.TrimSpace(key)] = strings.TrimSpace(value)
		}
	}
	if state["SD_MOUNTED"] != "1" {
		return ""
	}
	mountPoint := state["SD_MOUNTPOINT"]
	if mountPoint == "" {
		return ""
	}
	return filepath.Join(mountPoint, "aiden", "ota-cache")
}
