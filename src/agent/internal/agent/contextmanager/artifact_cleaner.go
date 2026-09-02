package contextmanager

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const artifactOrphanGracePeriod = 10 * time.Minute

// ArtifactStoreCleaner reclaims expired tool-result artifacts and per-session
// artifact directories that are no longer referenced by persisted sessions.
type ArtifactStoreCleaner struct {
	sessionFolder string
	priority      int
	now           func() time.Time
}

func NewArtifactStoreCleaner(sessionFolder string, priority int) *ArtifactStoreCleaner {
	return &ArtifactStoreCleaner{
		sessionFolder: strings.TrimSpace(sessionFolder),
		priority:      priority,
		now:           time.Now,
	}
}

func (c *ArtifactStoreCleaner) Name() string { return "tool_result_artifacts" }

func (c *ArtifactStoreCleaner) Priority() int {
	if c == nil {
		return 0
	}
	return c.priority
}

func (c *ArtifactStoreCleaner) EstimateReclaimable(ctx context.Context) (uint64, error) {
	return c.reclaim(ctx, false)
}

func (c *ArtifactStoreCleaner) Clean(ctx context.Context) (uint64, error) {
	return c.reclaim(ctx, true)
}

func (c *ArtifactStoreCleaner) reclaim(ctx context.Context, remove bool) (uint64, error) {
	if c == nil || c.sessionFolder == "" {
		return 0, nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	entries, err := os.ReadDir(c.sessionFolder)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, nil
		}
		return 0, fmt.Errorf("list context sessions for artifact cleanup: %w", err)
	}
	referencedSessions, referencesComplete, referenceErr := referencedArtifactSessions(c.sessionFolder)
	now := time.Now()
	if c.now != nil {
		now = c.now()
	}

	var reclaimed uint64
	var cleanupErrors []error
	if referenceErr != nil {
		cleanupErrors = append(cleanupErrors, referenceErr)
	}
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return reclaimed, err
		}
		if !entry.IsDir() {
			continue
		}
		artifactSessionID := entry.Name()
		artifactRoot := filepath.Join(c.sessionFolder, artifactSessionID, "tool-results")
		artifactRootInfo, err := os.Stat(artifactRoot)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			cleanupErrors = append(cleanupErrors, fmt.Errorf("stat artifact session %s: %w", artifactSessionID, err))
			continue
		}
		if !referencesComplete {
			bytes, errs := reclaimExpiredArtifacts(ctx, artifactRoot, now, remove)
			reclaimed += bytes
			cleanupErrors = append(cleanupErrors, errs...)
			continue
		}
		if _, referenced := referencedSessions[artifactSessionID]; !referenced {
			if now.Sub(artifactRootInfo.ModTime()) < artifactOrphanGracePeriod {
				continue
			}
			bytes, err := directoryFileBytes(artifactRoot)
			if err != nil {
				cleanupErrors = append(cleanupErrors, err)
				continue
			}
			if remove {
				latestReferences, latestComplete, err := referencedArtifactSessions(c.sessionFolder)
				if err != nil {
					cleanupErrors = append(cleanupErrors, err)
				}
				if !latestComplete {
					continue
				}
				if _, referenced := latestReferences[artifactSessionID]; referenced {
					continue
				}
				if err := os.RemoveAll(artifactRoot); err != nil {
					cleanupErrors = append(cleanupErrors, fmt.Errorf("remove orphan artifact session %s: %w", artifactSessionID, err))
					continue
				}
			}
			reclaimed += bytes
			continue
		}

		bytes, errs := reclaimExpiredArtifacts(ctx, artifactRoot, now, remove)
		reclaimed += bytes
		cleanupErrors = append(cleanupErrors, errs...)
	}
	return reclaimed, errors.Join(cleanupErrors...)
}

// referencedArtifactSessions reports which per-session artifact directories are
// still reachable. A live transcript owns its own directory, and it can also
// reference directories owned by earlier sessions through the absolute
// artifact paths persisted in its tool-result metadata. The `.meta.json`
// sidecars carry session lineage only and never pin an artifact directory.
func referencedArtifactSessions(sessionFolder string) (map[string]struct{}, bool, error) {
	referenced := make(map[string]struct{})
	referencesComplete := true
	var referenceErrors []error

	transcripts, err := filepath.Glob(filepath.Join(sessionFolder, "*.jsonl"))
	if err != nil {
		return nil, false, fmt.Errorf("list context session transcripts: %w", err)
	}
	for _, transcriptPath := range transcripts {
		sessionID := strings.TrimSuffix(filepath.Base(transcriptPath), ".jsonl")
		if _, err := validateArtifactSessionID(sessionID); err != nil {
			referencesComplete = false
			referenceErrors = append(referenceErrors, fmt.Errorf("invalid context session ID %q", sessionID))
			continue
		}
		referenced[sessionID] = struct{}{}
		messages, err := loadSession(sessionFolder, sessionID)
		if err != nil {
			referencesComplete = false
			referenceErrors = append(referenceErrors, err)
			continue
		}
		for _, message := range messages {
			for _, result := range message.ToolResults {
				if result.Meta != nil {
					addArtifactPathScopeReference(referenced, sessionFolder, result.Meta.ArtifactPath)
				}
			}
			for _, result := range message.RecoverableToolResults {
				addArtifactPathScopeReference(referenced, sessionFolder, result.ArtifactPath)
			}
		}
	}
	return referenced, referencesComplete, errors.Join(referenceErrors...)
}

func addArtifactPathScopeReference(referenced map[string]struct{}, sessionFolder, artifactPath string) {
	if referenced == nil || !filepath.IsAbs(strings.TrimSpace(artifactPath)) {
		return
	}
	root, err := filepath.Abs(sessionFolder)
	if err != nil {
		return
	}
	relative, err := filepath.Rel(root, filepath.Clean(artifactPath))
	if err != nil || relative == "." || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return
	}
	parts := strings.Split(relative, string(filepath.Separator))
	if len(parts) != 3 || parts[1] != "tool-results" || !strings.HasSuffix(parts[2], ".data") {
		return
	}
	artifactSessionID, err := validateArtifactSessionID(parts[0])
	if err != nil {
		return
	}
	referenced[artifactSessionID] = struct{}{}
}

func reclaimExpiredArtifacts(ctx context.Context, root string, now time.Time, remove bool) (uint64, []error) {
	entries, err := os.ReadDir(root)
	if err != nil {
		return 0, []error{fmt.Errorf("list artifact files in %s: %w", root, err)}
	}
	var reclaimed uint64
	var cleanupErrors []error
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			cleanupErrors = append(cleanupErrors, err)
			break
		}
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		metadataPath := filepath.Join(root, entry.Name())
		data, err := os.ReadFile(metadataPath)
		if err != nil {
			cleanupErrors = append(cleanupErrors, fmt.Errorf("read artifact metadata %s: %w", entry.Name(), err))
			continue
		}
		var metadata ArtifactMetadata
		if err := json.Unmarshal(data, &metadata); err != nil {
			cleanupErrors = append(cleanupErrors, fmt.Errorf("decode artifact metadata %s: %w", entry.Name(), err))
			continue
		}
		if metadata.ExpiresAt.IsZero() || metadata.ExpiresAt.After(now) {
			continue
		}
		id := strings.TrimSuffix(entry.Name(), ".json")
		dataPath := filepath.Join(root, id+".data")
		pairBytes, err := artifactPairBytes(dataPath, metadataPath)
		if err != nil {
			cleanupErrors = append(cleanupErrors, err)
			continue
		}
		if remove {
			if err := removeArtifactPair(dataPath, metadataPath); err != nil {
				cleanupErrors = append(cleanupErrors, err)
				continue
			}
		}
		reclaimed += pairBytes
	}
	staleBytes, staleErrors := reclaimStaleArtifactFiles(ctx, root, entries, now, remove)
	reclaimed += staleBytes
	cleanupErrors = append(cleanupErrors, staleErrors...)
	return reclaimed, cleanupErrors
}

func reclaimStaleArtifactFiles(ctx context.Context, root string, entries []os.DirEntry, now time.Time, remove bool) (uint64, []error) {
	cutoff := now.Add(-artifactOrphanGracePeriod)
	var reclaimed uint64
	var cleanupErrors []error
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			cleanupErrors = append(cleanupErrors, err)
			break
		}
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		staleCandidate := strings.HasPrefix(name, ".artifact-")
		if filepath.Ext(name) == ".data" {
			metadataPath := filepath.Join(root, strings.TrimSuffix(name, ".data")+".json")
			if _, err := os.Stat(metadataPath); os.IsNotExist(err) {
				staleCandidate = true
			} else if err != nil {
				cleanupErrors = append(cleanupErrors, fmt.Errorf("stat artifact metadata %s: %w", filepath.Base(metadataPath), err))
				continue
			}
		}
		if !staleCandidate {
			continue
		}
		path := filepath.Join(root, name)
		info, err := os.Stat(path)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			cleanupErrors = append(cleanupErrors, fmt.Errorf("stat stale artifact file %s: %w", name, err))
			continue
		}
		if info.ModTime().After(cutoff) {
			continue
		}
		if remove {
			if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
				cleanupErrors = append(cleanupErrors, fmt.Errorf("remove stale artifact file %s: %w", name, err))
				continue
			}
		}
		reclaimed += uint64(info.Size())
	}
	return reclaimed, cleanupErrors
}

func directoryFileBytes(root string) (uint64, error) {
	var total uint64
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.Type().IsRegular() {
			info, err := entry.Info()
			if err != nil {
				return err
			}
			total += uint64(info.Size())
		}
		return nil
	})
	if err != nil {
		return 0, fmt.Errorf("measure artifact directory %s: %w", root, err)
	}
	return total, nil
}

func artifactPairBytes(paths ...string) (uint64, error) {
	var total uint64
	for _, path := range paths {
		info, err := os.Stat(path)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return 0, fmt.Errorf("stat artifact file %s: %w", filepath.Base(path), err)
		}
		total += uint64(info.Size())
	}
	return total, nil
}

func removeArtifactPair(dataPath, metadataPath string) error {
	if err := os.Remove(dataPath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove artifact data %s: %w", filepath.Base(dataPath), err)
	}
	if err := os.Remove(metadataPath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove artifact metadata %s: %w", filepath.Base(metadataPath), err)
	}
	return nil
}
