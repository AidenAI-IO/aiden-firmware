package ota

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

type PendingBoot struct {
	TargetSlot      string `json:"target_slot"`
	TargetVersion   string `json:"target_version"`
	TargetBuildTime string `json:"target_build_time"`
	Nonce           string `json:"nonce"`
}

type HealthMarker struct {
	Slot      string `json:"slot"`
	Version   string `json:"version"`
	BuildTime string `json:"build_time"`
	Nonce     string `json:"nonce"`
	BootID    string `json:"boot_id"`
}

func DeleteStaleHealthMarker(path string) error {
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

func WritePendingBoot(path string, pending PendingBoot) error {
	data, err := json.MarshalIndent(pending, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp := path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return err
	}
	writeErr := error(nil)
	if _, writeErr = f.Write(data); writeErr == nil {
		writeErr = f.Sync()
	}
	closeErr := f.Close()
	if writeErr != nil {
		return writeErr
	}
	if closeErr != nil {
		return closeErr
	}
	if err := os.Rename(tmp, path); err != nil {
		return err
	}
	return fsyncDirFor(path)
}

func ValidateHealthMarker(path string, pending PendingBoot, currentBootID string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	var marker HealthMarker
	if err := json.Unmarshal(data, &marker); err != nil {
		return err
	}
	if marker.Slot != pending.TargetSlot {
		return fmt.Errorf("health slot %q, want %q", marker.Slot, pending.TargetSlot)
	}
	if marker.Version != pending.TargetVersion {
		return fmt.Errorf("health version %q, want %q", marker.Version, pending.TargetVersion)
	}
	if marker.BuildTime != pending.TargetBuildTime {
		return fmt.Errorf("health build_time %q, want %q", marker.BuildTime, pending.TargetBuildTime)
	}
	if marker.Nonce != pending.Nonce {
		return fmt.Errorf("health nonce %q, want %q", marker.Nonce, pending.Nonce)
	}
	if marker.BootID != currentBootID {
		return fmt.Errorf("health boot_id %q, want %q", marker.BootID, currentBootID)
	}
	return nil
}

func WaitForHealth(ctx context.Context, markerPath string, pending PendingBoot, currentBootID string, timeout time.Duration, interval time.Duration, reboot func() error) error {
	if interval <= 0 {
		return fmt.Errorf("health interval must be positive")
	}
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		if err := ValidateHealthMarker(markerPath, pending, currentBootID); err == nil {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-deadline.C:
			if reboot != nil {
				if err := reboot(); err != nil {
					return err
				}
			}
			return fmt.Errorf("health timeout waiting for %s", markerPath)
		case <-ticker.C:
		}
	}
}
