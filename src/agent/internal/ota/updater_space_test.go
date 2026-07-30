package ota

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
)

func TestDefaultUpdaterConfigUses64MiBReserve(t *testing.T) {
	config := DefaultUpdaterConfig()
	if config.ReserveSizeBytes != 64<<20 {
		t.Fatalf("ReserveSizeBytes = %d, want %d", config.ReserveSizeBytes, 64<<20)
	}
	if config.ReserveSafetyMarginBytes != 4<<20 {
		t.Fatalf("ReserveSafetyMarginBytes = %d, want %d", config.ReserveSafetyMarginBytes, 4<<20)
	}
}

func TestNewUpdaterRejectsReserveMarginAtLeastBudget(t *testing.T) {
	_, err := NewUpdater(UpdaterConfig{ReserveSizeBytes: 1024, ReserveSafetyMarginBytes: 1024}, nil)
	if err == nil || !strings.Contains(err.Error(), "smaller than reserve_size_bytes") {
		t.Fatalf("NewUpdater() error = %v, want reserve margin validation", err)
	}
}

func TestProcessPendingHealthOnceCreatesOTAReserve(t *testing.T) {
	env := newUpdaterTestEnv(t)
	env.config.ReserveSizeBytes = 4096

	if err := env.updater().ProcessPendingHealthOnce(context.Background()); err != nil {
		t.Fatalf("ProcessPendingHealthOnce() error = %v", err)
	}

	info, err := os.Stat(filepath.Join(env.downloadDir, otaReserveFileName))
	if err != nil {
		t.Fatalf("Stat(reserve) error = %v", err)
	}
	if info.Size() != env.config.ReserveSizeBytes {
		t.Fatalf("reserve size = %d, want %d", info.Size(), env.config.ReserveSizeBytes)
	}
}

func TestProcessPendingHealthOnceCountsPartialDownloadAgainstReserve(t *testing.T) {
	env := newUpdaterTestEnv(t)
	env.config.ReserveSizeBytes = 4096
	if err := os.MkdirAll(env.downloadDir, 0o755); err != nil {
		t.Fatalf("MkdirAll(downloadDir) error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(env.downloadDir, "oem.img.tar.gz.part"), make([]byte, 1024), 0o644); err != nil {
		t.Fatalf("WriteFile(part) error = %v", err)
	}

	if err := env.updater().ProcessPendingHealthOnce(context.Background()); err != nil {
		t.Fatalf("ProcessPendingHealthOnce() error = %v", err)
	}

	info, err := os.Stat(filepath.Join(env.downloadDir, otaReserveFileName))
	if err != nil {
		t.Fatalf("Stat(reserve) error = %v", err)
	}
	if info.Size() != 3072 {
		t.Fatalf("reserve size = %d, want 3072", info.Size())
	}
}

func TestProcessPendingHealthOnceDoesNotAllocateReserveWithoutUpdateLock(t *testing.T) {
	env := newUpdaterTestEnv(t)
	env.config.ReserveSizeBytes = 4096
	if err := os.MkdirAll(env.stateDir, 0o755); err != nil {
		t.Fatalf("MkdirAll(stateDir) error = %v", err)
	}
	lockPath := filepath.Join(env.stateDir, DefaultOTAUpdateLockName)
	lockFile, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatalf("OpenFile(lock) error = %v", err)
	}
	t.Cleanup(func() {
		_ = syscall.Flock(int(lockFile.Fd()), syscall.LOCK_UN)
		_ = lockFile.Close()
	})
	if err := syscall.Flock(int(lockFile.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		t.Fatalf("Flock(lock) error = %v", err)
	}

	err = env.updater().ProcessPendingHealthOnce(context.Background())
	if err == nil || !strings.Contains(err.Error(), "ota update already running") {
		t.Fatalf("ProcessPendingHealthOnce() error = %v, want update lock error", err)
	}
	if _, statErr := os.Stat(filepath.Join(env.downloadDir, otaReserveFileName)); !os.IsNotExist(statErr) {
		t.Fatalf("reserve was allocated without update lock: %v", statErr)
	}
}

func TestUpdaterReleasesReserveDuringDownloadAndRestoresBudget(t *testing.T) {
	env := newUpdaterTestEnv(t)
	env.config.ReserveSizeBytes = 4096
	env.config.ReserveSafetyMarginBytes = 64
	assets := map[string][]byte{
		"boot_a.img": []byte("boot-a-v2"),
		"boot_b.img": []byte("boot-b-v2"),
		"oem_a.img":  []byte("oem-a-v2"),
		"oem_b.img":  []byte("oem-b-v2"),
		"rootfs.img": []byte("rootfs-v2"),
	}
	manifest := env.signedManifest(assets, nil)
	reservePath := filepath.Join(env.downloadDir, otaReserveFileName)
	if err := env.updater().ProcessPendingHealthOnce(context.Background()); err != nil {
		t.Fatalf("ProcessPendingHealthOnce() error = %v", err)
	}

	if err := os.WriteFile(filepath.Join(env.downloadDir, "stale.img"), []byte("stale"), 0o644); err != nil {
		t.Fatalf("WriteFile(stale cache) error = %v", err)
	}

	var reserveReleased atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/repos/AidenAI-IO/aiden-firmware/releases/latest") {
			var release struct {
				Assets []githubAsset `json:"assets"`
			}
			release.Assets = append(release.Assets, githubAsset{Name: "manifest.json", BrowserDownloadURL: "http://" + r.Host + "/assets/manifest.json"})
			for name := range assets {
				release.Assets = append(release.Assets, githubAsset{Name: name, BrowserDownloadURL: "http://" + r.Host + "/assets/" + name})
			}
			_ = json.NewEncoder(w).Encode(release)
			return
		}
		if r.URL.Path == "/assets/manifest.json" {
			_, _ = w.Write(manifest)
			return
		}
		name := strings.TrimPrefix(r.URL.Path, "/assets/")
		if body, ok := assets[name]; ok {
			if _, err := os.Stat(reservePath); os.IsNotExist(err) {
				reserveReleased.Store(true)
			}
			w.Header().Set("Content-Length", fmt.Sprint(len(body)))
			_, _ = w.Write(body)
			return
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(server.Close)
	env.config.ReleaseURL = server.URL + "/repos/AidenAI-IO/aiden-firmware/releases/latest"

	if _, err := env.updater().CheckOnce(context.Background()); err != nil {
		t.Fatalf("CheckOnce() error = %v", err)
	}
	if !reserveReleased.Load() {
		t.Fatal("reserve file still existed while assets were downloading")
	}
	if _, err := os.Stat(filepath.Join(env.downloadDir, "stale.img")); !os.IsNotExist(err) {
		t.Fatalf("stale cache still exists after update: %v", err)
	}

	if got := otaCacheBudgetBytes(t, env.downloadDir); got != env.config.ReserveSizeBytes {
		t.Fatalf("cache plus reserve = %d, want %d", got, env.config.ReserveSizeBytes)
	}
}

func TestUpdaterRejectsDownloadLargerThanReserveBeforeAssetRequest(t *testing.T) {
	env := newUpdaterTestEnv(t)
	env.config.ReserveSizeBytes = 32
	env.config.ReserveSafetyMarginBytes = 8
	bootBody := []byte(strings.Repeat("b", 40))
	assets := map[string][]byte{
		"boot_a.img": []byte("boot-a-v2"),
		"boot_b.img": bootBody,
		"oem_a.img":  []byte("oem-a-v2"),
		"oem_b.img":  []byte("oem-b-v2"),
		"rootfs.img": []byte("rootfs-v2"),
	}
	manifest := env.signedManifest(assets, nil)
	assetRequests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/repos/AidenAI-IO/aiden-firmware/releases/latest") {
			_ = json.NewEncoder(w).Encode(struct {
				Assets []githubAsset `json:"assets"`
			}{Assets: []githubAsset{
				{Name: "manifest.json", BrowserDownloadURL: "http://" + r.Host + "/assets/manifest.json"},
				{Name: "boot_b.img", BrowserDownloadURL: "http://" + r.Host + "/assets/boot_b.img"},
				{Name: "oem_b.img", BrowserDownloadURL: "http://" + r.Host + "/assets/oem_b.img"},
				{Name: "rootfs.img", BrowserDownloadURL: "http://" + r.Host + "/assets/rootfs.img"},
			}})
			return
		}
		if r.URL.Path == "/assets/manifest.json" {
			_, _ = w.Write(manifest)
			return
		}
		if r.URL.Path == "/assets/boot_b.img" {
			assetRequests++
			_, _ = w.Write(bootBody)
			return
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(server.Close)
	env.config.ReleaseURL = server.URL + "/repos/AidenAI-IO/aiden-firmware/releases/latest"

	_, err := env.updater().CheckOnce(context.Background())
	if err == nil || !strings.Contains(err.Error(), "OTA reserve too small") {
		t.Fatalf("CheckOnce() error = %v, want reserve capacity error", err)
	}
	if assetRequests != 0 {
		t.Fatalf("assetRequests = %d, want 0", assetRequests)
	}
	state, loadErr := LoadState(filepath.Join(env.stateDir, "state.json"))
	if loadErr != nil {
		t.Fatalf("LoadState() error = %v", loadErr)
	}
	if state.Phase != "reserve" {
		t.Fatalf("state phase = %q, want reserve", state.Phase)
	}
}

func otaCacheBudgetBytes(t *testing.T, downloadDir string) int64 {
	t.Helper()
	entries, err := os.ReadDir(downloadDir)
	if err != nil {
		t.Fatalf("ReadDir(downloadDir) error = %v", err)
	}
	var total int64
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			t.Fatalf("Info(%s) error = %v", entry.Name(), err)
		}
		total += info.Size()
	}
	return total
}
