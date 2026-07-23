package agent

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestAudioArchiveManagerSkipsWriteWhenStorageCapabilityUnavailable(t *testing.T) {
	tmpDir := t.TempDir()
	sampler := &sequenceStorageSampler{samples: []StorageSample{storageSampleWithAvailableMB(8)}}
	storageConfig := DefaultStorageConfig()
	storageConfig.Cleanup.Enabled = false
	monitor := NewStorageMonitor(storageConfig, sampler, nil, nil, nil)
	if _, err := monitor.CheckAndRemediate(context.Background(), StorageCheckRequest{Reason: CheckReasonPeriodic}); err != nil {
		t.Fatalf("CheckAndRemediate() error = %v", err)
	}
	mgr := NewAudioArchiveManager(AudioArchiveConfig{Enabled: true, StoragePath: tmpDir})
	mgr.SetStorageMonitor(monitor)

	path, duration, err := mgr.SaveAudio(make([]int16, 16000), 16000)
	if err != nil {
		t.Fatalf("SaveAudio() error = %v", err)
	}
	if path != "" || duration != 1000 {
		t.Fatalf("SaveAudio() = path %q duration %d, want skipped path and preserved duration", path, duration)
	}
	entries, err := os.ReadDir(tmpDir)
	if err != nil {
		t.Fatalf("ReadDir() error = %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("archive directory contains %d files, want none", len(entries))
	}
}

func TestAudioArchiveManagerSaveAndCleanup(t *testing.T) {
	tmpDir := t.TempDir()

	cfg := AudioArchiveConfig{
		Enabled:     true,
		MaxFiles:    3,
		MaxSizeMB:   1,
		StoragePath: tmpDir,
	}

	mgr := NewAudioArchiveManager(cfg)

	// Save 5 audio files (exceeds max_files=3)
	var savedPaths []string
	for i := 0; i < 5; i++ {
		samples := make([]int16, 16000) // 1 second of silence
		path, duration, err := mgr.SaveAudio(samples, 16000)
		if err != nil {
			t.Fatalf("SaveAudio %d failed: %v", i, err)
		}

		if path == "" {
			t.Fatalf("SaveAudio %d returned empty path", i)
		}
		if duration <= 0 {
			t.Errorf("SaveAudio %d: duration should be positive, got %d", i, duration)
		}

		savedPaths = append(savedPaths, path)
		time.Sleep(10 * time.Millisecond) // Ensure different timestamps
	}

	// Check cleanup: only last 3 files should exist
	for i, path := range savedPaths {
		_, err := os.Stat(path)
		if i < 2 {
			// First 2 files should be deleted
			if !os.IsNotExist(err) {
				t.Errorf("File %d should be deleted: %s", i, path)
			}
		} else {
			// Last 3 files should exist
			if err != nil {
				t.Errorf("File %d should exist: %s (error: %v)", i, path, err)
			}
		}
	}
}

func TestAudioArchiveDefaultStoragePathRoutesThroughStorageManager(t *testing.T) {
	sm := newTestStorageManager(t, &fakeStorageOps{})
	mgr := NewAudioArchiveManager(AudioArchiveConfig{
		Enabled:     true,
		StoragePath: defaultAudioArchiveStoragePath,
	})
	mgr.SetStorageManager(sm)

	wantDir := sm.emmcClassDir(StorageClassAudio)
	if got := mgr.writeDir(); got != wantDir {
		t.Fatalf("writeDir() = %q, want storage-manager dir %q", got, wantDir)
	}

	wantRoots := sm.CleanupRoots(StorageClassAudio)
	gotRoots := mgr.cleanupRoots()
	if len(gotRoots) != len(wantRoots) || gotRoots[0] != wantRoots[0] {
		t.Fatalf("cleanupRoots() = %v, want storage-manager roots %v", gotRoots, wantRoots)
	}
}

func TestAudioArchiveCustomStoragePathBypassesStorageManager(t *testing.T) {
	custom := t.TempDir()
	sm := newTestStorageManager(t, &fakeStorageOps{})
	mgr := NewAudioArchiveManager(AudioArchiveConfig{
		Enabled:     true,
		StoragePath: custom,
	})
	mgr.SetStorageManager(sm)

	if got := mgr.writeDir(); got != custom {
		t.Fatalf("writeDir() = %q, want explicit path %q", got, custom)
	}
	if gotRoots := mgr.cleanupRoots(); len(gotRoots) != 1 || gotRoots[0] != custom {
		t.Fatalf("cleanupRoots() = %v, want [%q]", gotRoots, custom)
	}
}

func TestAudioArchiveManagerDisabled(t *testing.T) {
	cfg := AudioArchiveConfig{
		Enabled: false,
	}

	mgr := NewAudioArchiveManager(cfg)

	samples := make([]int16, 16000)
	path, duration, err := mgr.SaveAudio(samples, 16000)

	if err != nil {
		t.Fatalf("SaveAudio should not error when disabled: %v", err)
	}
	if path != "" {
		t.Errorf("SaveAudio should return empty path when disabled, got %q", path)
	}
	if duration != 1000 {
		t.Errorf("Duration should still be calculated: got %d, want %d", duration, 1000)
	}
}

func TestAudioArchiveManagerRejectsInvalidSampleRate(t *testing.T) {
	mgr := NewAudioArchiveManager(AudioArchiveConfig{Enabled: true, StoragePath: t.TempDir()})

	if _, _, err := mgr.SaveAudio([]int16{1, 2, 3}, 0); err == nil {
		t.Fatal("SaveAudio() error = nil, want invalid sample rate error")
	}
}

func TestAudioArchiveManagerFilenameFormat(t *testing.T) {
	tmpDir := t.TempDir()

	cfg := AudioArchiveConfig{
		Enabled:     true,
		StoragePath: tmpDir,
	}

	mgr := NewAudioArchiveManager(cfg)
	samples := make([]int16, 8000)
	path, _, err := mgr.SaveAudio(samples, 16000)
	if err != nil {
		t.Fatal(err)
	}

	filename := filepath.Base(path)
	// Format: msg_<timestamp>_<uuid>.wav
	if filepath.Ext(filename) != ".wav" {
		t.Errorf("Filename should have .wav extension: %s", filename)
	}
	if len(filename) < 20 {
		t.Errorf("Filename too short (should be msg_timestamp_uuid.wav): %s", filename)
	}
}

func TestAudioArchiveManagerWAVFormat(t *testing.T) {
	tmpDir := t.TempDir()
	cfg := AudioArchiveConfig{
		Enabled:     true,
		StoragePath: tmpDir,
	}

	mgr := NewAudioArchiveManager(cfg)

	// Save known samples
	samples := []int16{100, 200, 300, 400, 500}
	path, _, err := mgr.SaveAudio(samples, 16000)
	if err != nil {
		t.Fatal(err)
	}

	// Verify file exists and has reasonable size
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}

	// WAV header (44 bytes) + 5 samples * 2 bytes = 54 bytes
	expectedSize := 44 + len(samples)*2
	if info.Size() != int64(expectedSize) {
		t.Errorf("File size: got %d, want %d", info.Size(), expectedSize)
	}
}
