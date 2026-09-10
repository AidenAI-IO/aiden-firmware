package ota

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

var protectedDataRoot = "/userdata"

// SnapshotProtectedData copies only configuration and service identity files.
// User memory, skills, notifications, recordings and logs are append/persistent
// data and are intentionally never restored as a whole-directory snapshot.
func SnapshotProtectedData(root, version string) (string, error) {
	name := fmt.Sprintf("pre-%d-%s", time.Now().UTC().UnixNano(), safeSnapshotName(version))
	dir := filepath.Join(root, "transactions", name)
	relativePaths := []string{
		"agent/agent.toml", "system/env", "wpa_supplicant.conf",
		"system/wifi-proxies.json", "audio_service/playback_volume",
	}
	manifest := map[string]any{"created_at": time.Now().UTC(), "version": version, "files": []string{}}
	for _, rel := range relativePaths {
		src := filepath.Join(protectedDataRoot, rel)
		info, err := os.Stat(src)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return "", err
		}
		if !info.Mode().IsRegular() {
			return "", fmt.Errorf("snapshot source is not a regular file: %s", src)
		}
		dst := filepath.Join(dir, "userdata", rel)
		if err := os.MkdirAll(filepath.Dir(dst), 0755); err != nil {
			return "", err
		}
		if err := copyFile(src, dst, info.Mode().Perm()); err != nil {
			return "", err
		}
		manifest["files"] = append(manifest["files"].([]string), rel)
	}
	if err := os.MkdirAll(dir, 0755); err != nil {
		return "", err
	}
	b, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return "", err
	}
	if err := os.WriteFile(filepath.Join(dir, "manifest.json"), append(b, '\n'), 0600); err != nil {
		return "", err
	}
	return dir, nil
}

func safeSnapshotName(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return "unknown"
	}
	var b strings.Builder
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '.' || r == '-' || r == '_' {
			b.WriteRune(r)
		} else {
			b.WriteByte('_')
		}
	}
	return b.String()
}

func copyFile(src, dst string, mode os.FileMode) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode)
	if err != nil {
		return err
	}
	if _, err = io.Copy(out, in); err == nil {
		err = out.Sync()
	}
	closeErr := out.Close()
	if err != nil {
		return err
	}
	return closeErr
}

// RestoreProtectedData restores only the files listed in a snapshot manifest.
// It is intentionally not a recursive userdata restore: memory, skills,
// notifications, recordings and logs remain untouched during rollback.
func RestoreProtectedData(snapshotDir string) error {
	data, err := os.ReadFile(filepath.Join(snapshotDir, "manifest.json"))
	if err != nil {
		return err
	}
	var manifest struct {
		Files []string `json:"files"`
	}
	if err := json.Unmarshal(data, &manifest); err != nil {
		return err
	}
	allowed := map[string]bool{
		"agent/agent.toml":              true,
		"system/env":                    true,
		"wpa_supplicant.conf":           true,
		"system/wifi-proxies.json":      true,
		"audio_service/playback_volume": true,
	}
	for _, rel := range manifest.Files {
		if !allowed[rel] || rel == "" || filepath.IsAbs(rel) || strings.Contains(rel, "..") {
			return fmt.Errorf("invalid protected snapshot path %q", rel)
		}
		src := filepath.Join(snapshotDir, "userdata", rel)
		dst := filepath.Join(protectedDataRoot, rel)
		info, err := os.Stat(src)
		if err != nil {
			return err
		}
		tmp := dst + ".ota-restore.tmp"
		if err := os.MkdirAll(filepath.Dir(dst), 0755); err != nil {
			return err
		}
		if err := copyFile(src, tmp, info.Mode().Perm()); err != nil {
			return err
		}
		if err := os.Rename(tmp, dst); err != nil {
			return err
		}
	}
	return nil
}
