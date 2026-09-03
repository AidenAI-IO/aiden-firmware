package agent

import (
	"context"
	"log"
	"os"
	"path/filepath"
	"sort"
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

// RecallChunks searches for chunks in the global flat index.
func (s *MultiSessionChunkStore) RecallChunks(ctx context.Context, query ChunkRecallQuery) ([]ChunkRecallResult, error) {
	if s.sessionFolder == "" {
		return nil, nil
	}

	// Load the global index from the flat chunks directory
	chunksDir := filepath.Join(s.sessionFolder, "chunks")
	indexPath := filepath.Join(chunksDir, "index.yaml")

	// Skip if no chunks directory or index
	if _, err := os.Stat(indexPath); os.IsNotExist(err) {
		return nil, nil
	}

	// Load the global index
	index, err := loadChunkIndexFromPath(indexPath)
	if err != nil {
		log.Printf("[WARN] [agent] [chunk_store] failed to load global chunk index: %v", err)
		return nil, nil
	}

	// Build a map of chunk ID to created_at for efficient sorting
	chunkCreatedAt := make(map[string]string, len(index.Chunks))
	for _, chunk := range index.Chunks {
		chunkCreatedAt[chunk.ID] = chunk.CreatedAt
	}

	var allResults []ChunkRecallResult
	for _, chunk := range index.Chunks {
		if matchesChunkQuery(chunk, query) {
			result := ChunkRecallResult{
				ChunkID:   chunk.ID,
				Source:    "session_" + chunk.SessionID,
				SessionID: chunk.SessionID,
				Summary:   chunk.Summary,
			}
			if chunk.Structured != nil {
				result.Structured = chunk.Structured
			}
			allResults = append(allResults, result)
		}
	}

	// Sort by created_at descending (newest first)
	sort.Slice(allResults, func(i, j int) bool {
		iCreatedAt := chunkCreatedAt[allResults[i].ChunkID]
		jCreatedAt := chunkCreatedAt[allResults[j].ChunkID]
		// Descending order: newer timestamps come first
		return iCreatedAt > jCreatedAt
	})

	// Apply limit after sorting
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