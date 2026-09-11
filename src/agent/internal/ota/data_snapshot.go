package ota

import (
	"encoding/json"
	"errors"
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
func SnapshotProtectedData(root, version string) (_ string, resultErr error) {
	name := fmt.Sprintf("pre-%d-%s", time.Now().UTC().UnixNano(), safeSnapshotName(version))
	dir := filepath.Join(root, "transactions", name)
	defer func() {
		if resultErr != nil {
			resultErr = errors.Join(resultErr, os.RemoveAll(dir))
		}
	}()
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
	if _, err = f.Write(append(b, '\n')); err == nil {
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
	// Scan disk, including snapshots never published in state.json. Keep both
	// the new snapshot and the one still needed by the persisted transaction.
	state, err := LoadState(filepath.Join(root, "state.json"))
	if err != nil && !os.IsNotExist(err) {
		return err
	}
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
	remaining := len(snapshots)
	for _, snapshot := range snapshots {
		if remaining <= protectedSnapshotRetention {
			break
		}
		if snapshot.path == filepath.Clean(current) || snapshot.path == filepath.Clean(state.DataSnapshotPath) {
			continue
		}
		if err := os.RemoveAll(snapshot.path); err != nil {
			return err
		}
		removed = true
		remaining--
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
// All replacements and undo copies are staged before the first live rename.
// A commit error restores the originals; the caller keeps the snapshot for retry.
// This is not a cross-file atomic swap on power loss. Boot recovery must finish
// before services read configuration. Append-only user data remains untouched.
func RestoreProtectedData(snapshotDir string) error {
	return restoreProtectedData(snapshotDir, currentProtectedDataRoot(), os.Rename)
}

func restoreProtectedData(snapshotDir, protectedRoot string, renameFile func(string, string) error) error {
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
	seen := make(map[string]bool)
	for _, rel := range manifest.Files {
		if !allowed[rel] || seen[rel] {
			return fmt.Errorf("invalid protected snapshot path %q", rel)
		}
		seen[rel] = true
	}
	type stagedFile struct {
		dst, next, undo string
		existed         bool
	}
	var staged []stagedFile
	var tempDirs []string
	keepUndo := false
	defer func() {
		if !keepUndo {
			for _, dir := range tempDirs {
				_ = os.RemoveAll(dir)
			}
		}
	}()
	for _, rel := range manifest.Files {
		src := filepath.Join(snapshotDir, "userdata", rel)
		dst := filepath.Join(protectedRoot, rel)
		info, err := os.Lstat(src)
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("snapshot source is not a regular file: %s", src)
		}
		if err := os.MkdirAll(filepath.Dir(dst), 0755); err != nil {
			return err
		}
		dir, err := os.MkdirTemp(filepath.Dir(dst), ".ota-restore-")
		if err != nil {
			return err
		}
		tempDirs = append(tempDirs, dir)
		item := stagedFile{dst: dst, next: filepath.Join(dir, "next"), undo: filepath.Join(dir, "undo")}
		if err := copyFile(src, item.next, info.Mode().Perm()); err != nil {
			return err
		}
		original, err := os.Lstat(dst)
		if err != nil && !os.IsNotExist(err) {
			return err
		}
		if err == nil {
			if !original.Mode().IsRegular() {
				return fmt.Errorf("restore destination is not a regular file: %s", dst)
			}
			item.existed = true
			if err := copyFile(dst, item.undo, original.Mode().Perm()); err != nil {
				return err
			}
		}
		if err := syncSnapshotPath(protectedRoot, dir); err != nil {
			return err
		}
		staged = append(staged, item)
	}
	lastReplaced := -1
	for i, item := range staged {
		err := renameFile(item.next, item.dst)
		if err == nil {
			lastReplaced = i
			err = fsyncDirFor(item.dst)
		}
		if err == nil {
			continue
		}
		commitErr := fmt.Errorf("restore %s: %w", item.dst, err)
		for j := lastReplaced; j >= 0; j-- {
			previous := staged[j]
			if previous.existed {
				err = renameFile(previous.undo, previous.dst)
			} else {
				err = os.Remove(previous.dst)
			}
			if err == nil {
				err = fsyncDirFor(previous.dst)
			}
			if err != nil {
				keepUndo = true
				commitErr = errors.Join(commitErr, fmt.Errorf("undo %s (backup %s): %w", previous.dst, previous.undo, err))
			}
		}
		return commitErr
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
