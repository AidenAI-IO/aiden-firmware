package agent

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

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
