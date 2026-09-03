package agent

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
)

// MultiSessionChunkStore searches for chunks across all sessions in a session folder.
type MultiSessionChunkStore struct {
	sessionFolder string
}

// NewMultiSessionChunkStore creates a store that can search chunks from all sessions.
func NewMultiSessionChunkStore(sessionFolder string) *MultiSessionChunkStore {
	return &MultiSessionChunkStore{sessionFolder: sessionFolder}
}

// RecallChunks searches for chunks across all session directories.
func (s *MultiSessionChunkStore) RecallChunks(ctx context.Context, query ChunkRecallQuery) ([]ChunkRecallResult, error) {
	if s.sessionFolder == "" {
		return nil, nil
	}

	// List all session directories
	entries, err := os.ReadDir(s.sessionFolder)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read session folder: %w", err)
	}

	var allResults []ChunkRecallResult
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		// Skip hidden directories and files
		if strings.HasPrefix(entry.Name(), ".") {
			continue
		}

		sessionID := entry.Name()
		chunksDir := filepath.Join(s.sessionFolder, sessionID, "chunks")
		indexPath := filepath.Join(chunksDir, "index.yaml")

		// Skip if no chunks directory or index
		if _, err := os.Stat(indexPath); os.IsNotExist(err) {
			continue
		}

		// Load and search this session's chunks
		index, err := loadChunkIndexFromPath(indexPath)
		if err != nil {
			// A corrupt index means this session's chunks are unreachable. Log it
			// so the problem is visible rather than silently dropping history.
			log.Printf("[WARN] [agent] [chunk_store] failed to load chunk index for session %q: %v", sessionID, err)
			continue
		}

		for _, chunk := range index.Chunks {
			if matchesChunkQuery(chunk, query) {
				result := ChunkRecallResult{
					ChunkID:   chunk.ID,
					Source:    "session_" + sessionID,
					SessionID: sessionID,
					Summary:   chunk.Summary,
				}
				if chunk.Structured != nil {
					result.Structured = chunk.Structured
				}
				allResults = append(allResults, result)
			}
		}
	}

	// Apply limit
	if query.Limit > 0 && len(allResults) > query.Limit {
		allResults = allResults[:query.Limit]
	}

	return allResults, nil
}

func matchesChunkQuery(chunk chunkIndexEntry, query ChunkRecallQuery) bool {
	// Match by chunk IDs
	if len(query.ChunkIDs) > 0 {
		for _, id := range query.ChunkIDs {
			if chunk.ID == id {
				return true
			}
		}
		return false
	}

	// Match by tags
	if len(query.Tags) > 0 {
		for _, queryTag := range query.Tags {
			for _, chunkTag := range chunk.Tags {
				if strings.EqualFold(queryTag, chunkTag) {
					return true
				}
			}
		}
	}

	// Match by entities
	if len(query.Entities) > 0 {
		for _, queryEntity := range query.Entities {
			for _, chunkEntity := range chunk.Entities {
				if strings.EqualFold(queryEntity, chunkEntity) {
					return true
				}
			}
		}
	}

	// If no specific filters, match all
	if len(query.ChunkIDs) == 0 && len(query.Tags) == 0 && len(query.Entities) == 0 {
		return true
	}

	return false
}