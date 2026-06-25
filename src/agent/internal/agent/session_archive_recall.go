package agent

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// ArchivedSessionStore searches chunks across closed sessions persisted under
// session_archive/. Each archived session keeps the same on-disk layout as an
// active session, so we can reuse SessionMemoryStore for the actual chunk
// lookup.
type ArchivedSessionStore struct {
	rootDir string
}

// NewArchivedSessionStore returns a store rooted at the parent directory of
// archived sessions (typically memory/session_archive/). A nil result is
// returned when rootDir is empty so callers can opt-out of archive search.
func NewArchivedSessionStore(rootDir string) *ArchivedSessionStore {
	if strings.TrimSpace(rootDir) == "" {
		return nil
	}
	return &ArchivedSessionStore{rootDir: rootDir}
}

// RecallChunks scans archived session directories newer than the time range
// implied by query.ArchivedTimeRange and returns matching chunks. The query is
// reused as-is against each archived SessionMemoryStore.
func (a *ArchivedSessionStore) RecallChunks(ctx context.Context, query ChunkRecallQuery) ([]ChunkRecallResult, error) {
	if a == nil || a.rootDir == "" {
		return nil, nil
	}
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	default:
	}

	entries, err := os.ReadDir(a.rootDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}

	cutoff := archivedTimeRangeCutoff(query.ArchivedTimeRange, time.Now().UTC())

	type archivedDir struct {
		sessionID string
		path      string
		startedAt time.Time
	}
	candidates := make([]archivedDir, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		sessionPath := filepath.Join(a.rootDir, entry.Name())
		startedAt := readArchivedSessionStartedAt(sessionPath)
		if !cutoff.IsZero() && !startedAt.IsZero() && startedAt.Before(cutoff) {
			continue
		}
		candidates = append(candidates, archivedDir{
			sessionID: entry.Name(),
			path:      sessionPath,
			startedAt: startedAt,
		})
	}

	// Newest-first so explicit chunk_id lookups and topical searches both
	// surface recent matches before diving into deep history.
	sort.SliceStable(candidates, func(i, j int) bool {
		return candidates[i].startedAt.After(candidates[j].startedAt)
	})

	// When searching archived chunks, we want to find matches across all
	// sessions, not just the first N from each. Push the per-session limit
	// down only when query.Limit is set and small.
	perSessionLimit := query.Limit
	if perSessionLimit <= 0 {
		perSessionLimit = 5
	}

	var results []ChunkRecallResult
	for _, candidate := range candidates {
		select {
		case <-ctx.Done():
			return results, ctx.Err()
		default:
		}

		store := NewSessionMemoryStore(candidate.path)
		archiveQuery := query
		archiveQuery.IncludeArchived = false // never recurse
		archiveQuery.ArchivedTimeRange = ""  // not relevant per-session
		archiveQuery.Limit = perSessionLimit

		hits, err := store.RecallChunks(ctx, archiveQuery)
		if err != nil {
			// One bad archive directory shouldn't block the rest; skip it.
			continue
		}
		for i := range hits {
			hits[i].Source = chunkRecallSourceArchived
			hits[i].SessionID = candidate.sessionID
		}
		results = append(results, hits...)

		if query.Limit > 0 && len(results) >= query.Limit {
			break
		}
	}

	if query.Limit > 0 && len(results) > query.Limit {
		results = results[:query.Limit]
	}
	return results, nil
}

// archivedTimeRangeCutoff returns the lower bound for archived session
// timestamps. Archives older than the cutoff are excluded. A zero time means
// "no lower bound" (search everything).
func archivedTimeRangeCutoff(rangeKey string, now time.Time) time.Time {
	switch strings.TrimSpace(rangeKey) {
	case archivedTimeRangeAll:
		return time.Time{}
	case archivedTimeRangeLast7d:
		return now.Add(-7 * 24 * time.Hour)
	case archivedTimeRangeLast24h, "":
		return now.Add(-24 * time.Hour)
	default:
		// Unknown values fall back to the safe default rather than erroring.
		return now.Add(-24 * time.Hour)
	}
}

// readArchivedSessionStartedAt extracts the session start time from
// session.yaml metadata. Falls back to the directory mtime if metadata is
// missing or unparseable, since archived sessions still have their original
// timestamps preserved by the rotation rename.
func readArchivedSessionStartedAt(sessionDir string) time.Time {
	meta, err := readSessionMetadata(filepath.Join(sessionDir, sessionMetadataFileName))
	if err == nil && strings.TrimSpace(meta.CreatedAt) != "" {
		if t, err := time.Parse(time.RFC3339Nano, meta.CreatedAt); err == nil {
			return t.UTC()
		}
	}
	if info, err := os.Stat(sessionDir); err == nil {
		return info.ModTime().UTC()
	}
	return time.Time{}
}
