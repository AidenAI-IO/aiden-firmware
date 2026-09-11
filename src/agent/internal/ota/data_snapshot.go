package ota

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

var protectedDataRoot = "/userdata"
var protectedDataRootMu sync.RWMutex

const protectedSnapshotRetention = 8

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
	protectedRoot := currentProtectedDataRoot()
	for _, rel := range relativePaths {
		src := filepath.Join(protectedRoot, rel)
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
		if err := syncSnapshotPath(dir, filepath.Dir(dst)); err != nil {
			return "", err
		}
		manifest["files"] = append(manifest["files"].([]string), rel)
	}
	if err := os.MkdirAll(dir, 0755); err != nil {
		return "", err
	}
	if err := fsyncDirFor(dir); err != nil {
		return "", err
	}
	b, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return "", err
	}
	manifestPath := filepath.Join(dir, "manifest.json")
	f, err := os.OpenFile(manifestPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0600)
	if err != nil {
		return "", err
	}
	if _, err := f.Write(append(b, '\n')); err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil {
		return "", err
	}
	if closeErr != nil {
		return "", closeErr
	}
	if err := fsyncDirFor(manifestPath); err != nil {
		return "", err
	}
	if err := pruneProtectedSnapshots(root, dir); err != nil {
		return "", err
	}
	return dir, nil
}

func syncSnapshotPath(root, path string) error {
	rel, err := filepath.Rel(root, path)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
		return fmt.Errorf("snapshot path escapes root: %s", path)
	}
	for {
		if err := fsyncDirFor(filepath.Join(path, ".dirsync")); err != nil {
			return err
		}
		if path == root {
			return nil
		}
		path = filepath.Dir(path)
	}
}

func pruneProtectedSnapshots(root, current string) error {
	transactionsDir := filepath.Join(root, "transactions")
	entries, err := os.ReadDir(transactionsDir)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	type snapshotEntry struct {
		name string
		path string
	}
	var snapshots []snapshotEntry
	for _, entry := range entries {
		if !entry.IsDir() || !strings.HasPrefix(entry.Name(), "pre-") {
			continue
		}
		snapshots = append(snapshots, snapshotEntry{
			name: entry.Name(),
			path: filepath.Join(transactionsDir, entry.Name()),
		})
	}
	sort.Slice(snapshots, func(i, j int) bool { return snapshots[i].name < snapshots[j].name })
	removed := false
	for len(snapshots) > protectedSnapshotRetention {
		removeAt := 0
		if snapshots[removeAt].path == current {
			removeAt = 1
		}
		if removeAt >= len(snapshots) {
			break
		}
		if err := os.RemoveAll(snapshots[removeAt].path); err != nil {
			return err
		}
		removed = true
		snapshots = append(snapshots[:removeAt], snapshots[removeAt+1:]...)
	}
	if removed {
		return fsyncDirFor(filepath.Join(transactionsDir, ".dirsync"))
	}
	return nil
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
	protectedRoot := currentProtectedDataRoot()
	for _, rel := range manifest.Files {
		if !allowed[rel] || rel == "" || filepath.IsAbs(rel) || strings.Contains(rel, "..") {
			return fmt.Errorf("invalid protected snapshot path %q", rel)
		}
		src := filepath.Join(snapshotDir, "userdata", rel)
		dst := filepath.Join(protectedRoot, rel)
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

func currentProtectedDataRoot() string {
	protectedDataRootMu.RLock()
	defer protectedDataRootMu.RUnlock()
	return protectedDataRoot
}

func setProtectedDataRoot(root string) (restore func()) {
	protectedDataRootMu.Lock()
	previous := protectedDataRoot
	protectedDataRoot = root
	protectedDataRootMu.Unlock()
	return func() {
		protectedDataRootMu.Lock()
		protectedDataRoot = previous
		protectedDataRootMu.Unlock()
	}
}
