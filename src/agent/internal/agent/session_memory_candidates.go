package agent

import (
	"context"
	"strings"
)

// extractMemoryCandidatesFromSession reads all chunks in the session directory
// and collects unique MemoryCandidates from their structured summaries. Each
// candidate is converted to a MemoryItem and passed to the provided save function.
// Non-fatal errors are logged but do not block extraction.
func extractMemoryCandidatesFromSession(ctx context.Context, sessionDir string, logger *Logger, saveFn func(context.Context, MemoryItem) error) (int, error) {
	select {
	case <-ctx.Done():
		return 0, ctx.Err()
	default:
	}

	store := NewSessionMemoryStore(sessionDir)
	index, err := store.loadChunkIndex()
	if err != nil {
		// No chunks or index missing: nothing to extract.
		return 0, nil
	}

	seen := make(map[string]*MemoryItem)
	saved := 0

	for _, entry := range index.Chunks {
		if entry.Status != "active" || entry.Structured == nil {
			continue
		}
		for _, candidate := range entry.Structured.MemoryCandidates {
			normalized := strings.TrimSpace(candidate)
			if normalized == "" {
				continue
			}

			if existing, found := seen[normalized]; found {
				// Merge tags, entities, and evidence from duplicate candidates
				existing.Tags = mergeUnique(existing.Tags, entry.Tags)
				existing.Entities = mergeUnique(existing.Entities, entry.Entities)
				existing.EvidenceExcerpts = mergeUnique(existing.EvidenceExcerpts, []string{entry.Summary})
				continue
			}

			item := MemoryItem{
				Type:             "fact",
				Priority:         70,
				Confidence:       0.8,
				Title:            truncateForMemoryTitle(normalized),
				Content:          normalized,
				Tags:             entry.Tags,
				Entities:         entry.Entities,
				EvidenceExcerpts: []string{entry.Summary},
				TimeScope:        "long_term",
			}
			seen[normalized] = &item

			if saveFn != nil {
				if err := saveFn(ctx, item); err != nil {
					if logger != nil {
						logger.Warn("[memory] failed to save memory candidate %q: %v", item.Title, err)
					}
					continue
				}
				saved++
			}
		}
	}

	if logger != nil && saved > 0 {
		logger.Info("[memory] extracted %d memory candidates from session before rotation", saved)
	}
	return saved, nil
}

// truncateForMemoryTitle returns a short title derived from the content.
// Long candidates are capped at 80 runes for readability in the index.
func truncateForMemoryTitle(content string) string {
	const maxLen = 80
	runes := []rune(content)
	if len(runes) <= maxLen {
		return content
	}
	// Find a word boundary near the cap rather than cutting mid-word.
	truncated := string(runes[:maxLen])
	if idx := strings.LastIndexAny(truncated, " ,，。."); idx > len([]rune(truncated))/2 {
		truncated = string([]rune(truncated)[:idx])
	}
	return truncated + "..."
}

// mergeUnique appends items from b to a, skipping duplicates.
func mergeUnique(a, b []string) []string {
	seen := make(map[string]bool, len(a))
	for _, item := range a {
		seen[item] = true
	}
	for _, item := range b {
		if !seen[item] {
			a = append(a, item)
			seen[item] = true
		}
	}
	return a
}
