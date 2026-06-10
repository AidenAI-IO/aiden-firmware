package agent

import (
	"encoding/binary"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/google/uuid"
)

// AudioArchiveManager handles saving and cleanup of audio recordings.
type AudioArchiveManager struct {
	config AudioArchiveConfig
}

// NewAudioArchiveManager creates a new audio archive manager.
func NewAudioArchiveManager(config AudioArchiveConfig) *AudioArchiveManager {
	return &AudioArchiveManager{
		config: config,
	}
}

// SaveAudio saves audio samples to a WAV file and returns the path, duration in ms, and error.
// If archival is disabled, only calculates duration without saving.
func (m *AudioArchiveManager) SaveAudio(samples []int16, sampleRate int) (string, int, error) {
	// Calculate duration in milliseconds
	durationMs := (len(samples) * 1000) / sampleRate

	// If disabled, return only duration
	if !m.config.Enabled {
		return "", durationMs, nil
	}

	// Ensure storage directory exists
	storagePath := m.config.StoragePathOrDefault()
	if err := os.MkdirAll(storagePath, 0755); err != nil {
		return "", 0, fmt.Errorf("create storage dir: %w", err)
	}

	// Generate filename with timestamp and UUID
	timestamp := time.Now().Unix()
	id := uuid.New().String()[:8]
	filename := fmt.Sprintf("msg_%d_%s.wav", timestamp, id)
	filePath := filepath.Join(storagePath, filename)

	// Write WAV file
	if err := writeWAVFile(filePath, samples, sampleRate); err != nil {
		return "", 0, fmt.Errorf("write WAV: %w", err)
	}

	// Cleanup old files if needed
	if err := m.cleanup(); err != nil {
		// Log but don't fail
		fmt.Fprintf(os.Stderr, "[audio_archive] cleanup warning: %v\n", err)
	}

	return filePath, durationMs, nil
}

// cleanup removes old audio files based on max_files and max_size_mb limits.
func (m *AudioArchiveManager) cleanup() error {
	storagePath := m.config.StoragePathOrDefault()

	entries, err := os.ReadDir(storagePath)
	if err != nil {
		return err
	}

	// Collect audio files with metadata
	type fileInfo struct {
		path    string
		modTime time.Time
		size    int64
	}
	var files []fileInfo

	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".wav" {
			continue
		}

		info, err := entry.Info()
		if err != nil {
			continue
		}

		files = append(files, fileInfo{
			path:    filepath.Join(storagePath, entry.Name()),
			modTime: info.ModTime(),
			size:    info.Size(),
		})
	}

	// Sort by modification time (oldest first)
	sort.Slice(files, func(i, j int) bool {
		return files[i].modTime.Before(files[j].modTime)
	})

	maxFiles := m.config.MaxFilesOrDefault()
	maxSizeBytes := int64(m.config.MaxSizeMBOrDefault()) * 1024 * 1024

	// Calculate total size
	var totalSize int64
	for _, f := range files {
		totalSize += f.size
	}

	// Delete oldest files until under limits
	for len(files) > maxFiles || totalSize > maxSizeBytes {
		if len(files) == 0 {
			break
		}

		oldest := files[0]
		files = files[1:]
		totalSize -= oldest.size

		if err := os.Remove(oldest.path); err != nil {
			fmt.Fprintf(os.Stderr, "[audio_archive] remove %s: %v\n", oldest.path, err)
		}
	}

	return nil
}

// writeWAVFile writes PCM samples to a WAV file.
func writeWAVFile(path string, samples []int16, sampleRate int) error {
	file, err := os.Create(path)
	if err != nil {
		return err
	}
	defer file.Close()

	// WAV header (44 bytes)
	dataSize := len(samples) * 2
	fileSize := 36 + dataSize

	// RIFF header
	file.WriteString("RIFF")
	binary.Write(file, binary.LittleEndian, uint32(fileSize))
	file.WriteString("WAVE")

	// fmt chunk
	file.WriteString("fmt ")
	binary.Write(file, binary.LittleEndian, uint32(16))           // fmt chunk size
	binary.Write(file, binary.LittleEndian, uint16(1))            // audio format (PCM)
	binary.Write(file, binary.LittleEndian, uint16(1))            // num channels
	binary.Write(file, binary.LittleEndian, uint32(sampleRate))   // sample rate
	binary.Write(file, binary.LittleEndian, uint32(sampleRate*2)) // byte rate
	binary.Write(file, binary.LittleEndian, uint16(2))            // block align
	binary.Write(file, binary.LittleEndian, uint16(16))           // bits per sample

	// data chunk
	file.WriteString("data")
	binary.Write(file, binary.LittleEndian, uint32(dataSize))

	// Write samples
	return binary.Write(file, binary.LittleEndian, samples)
}
