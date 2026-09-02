package agent

import (
	"aiden-agent/internal/agent/messages"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/google/uuid"
	"gopkg.in/yaml.v3"
)

// SessionChunkWriter writes conversation chunks to session directories.
type SessionChunkWriter struct {
	sessionFolder string
}

// NewSessionChunkWriter creates a chunk writer for the given session folder.
func NewSessionChunkWriter(sessionFolder string) *SessionChunkWriter {
	return &SessionChunkWriter{sessionFolder: sessionFolder}
}

// WriteChunk writes a chunk of messages with their summary to the session's chunks directory.
func (w *SessionChunkWriter) WriteChunk(ctx context.Context, sessionID string, msgs []messages.Message, summary string) error {
	if w.sessionFolder == "" || sessionID == "" {
		return nil
	}
	if len(msgs) == 0 {
		return nil
	}

	chunksDir := filepath.Join(w.sessionFolder, sessionID, "chunks")
	if err := os.MkdirAll(chunksDir, 0o755); err != nil {
		return fmt.Errorf("create chunks directory: %w", err)
	}

	// Generate chunk ID
	chunkID := "chunk_" + uuid.New().String()

	// Write chunk messages to JSONL
	chunkFile := filepath.Join(chunksDir, chunkID+".jsonl")
	file, err := os.Create(chunkFile)
	if err != nil {
		return fmt.Errorf("create chunk file: %w", err)
	}
	defer file.Close()

	for _, msg := range msgs {
		data, err := json.Marshal(msg)
		if err != nil {
			return fmt.Errorf("marshal message: %w", err)
		}
		if _, err := file.Write(data); err != nil {
			return fmt.Errorf("write message: %w", err)
		}
		if _, err := file.WriteString("\n"); err != nil {
			return fmt.Errorf("write newline: %w", err)
		}
	}

	if err := file.Sync(); err != nil {
		return fmt.Errorf("sync chunk file: %w", err)
	}

	// Update index
	indexPath := filepath.Join(chunksDir, "index.yaml")
	index, err := loadChunkIndexFromPath(indexPath)
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("load chunk index: %w", err)
	}

	// Calculate checksum
	checksum := calculateChunkChecksum(msgs)

	// Create index entry
	entry := chunkIndexEntry{
		ID:         chunkID,
		File:       chunkID + ".jsonl",
		Status:     "active",
		Summary:    summary,
		EventCount: len(msgs),
		Checksum:   checksum,
	}

	index.Version = 1
	index.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
	index.Chunks = append(index.Chunks, entry)

	// Write index as YAML
	indexData, err := yaml.Marshal(index)
	if err != nil {
		return fmt.Errorf("marshal chunk index: %w", err)
	}

	if err := writeFileAtomic(indexPath, indexData, 0o644); err != nil {
		return fmt.Errorf("write chunk index: %w", err)
	}

	return nil
}

func calculateChunkChecksum(msgs []messages.Message) string {
	h := sha256.New()
	for _, msg := range msgs {
		h.Write([]byte(msg.Role))
		h.Write([]byte(msg.Content))
	}
	return hex.EncodeToString(h.Sum(nil))[:16]
}

func loadChunkIndexFromPath(path string) (chunkIndex, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return chunkIndex{Version: 1, Chunks: []chunkIndexEntry{}}, nil
		}
		return chunkIndex{}, err
	}

	var index chunkIndex
	if err := yaml.Unmarshal(data, &index); err != nil {
		return chunkIndex{}, fmt.Errorf("unmarshal chunk index: %w", err)
	}

	return index, nil
}