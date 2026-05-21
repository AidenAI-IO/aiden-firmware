package agent

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

type RetentionPolicy struct {
	EventCompactAfter   string `yaml:"event_compact_after"`
	ChunkNoRefTTL       string `yaml:"chunk_no_ref_ttl"`
	ChunkReferencedKeep string `yaml:"chunk_referenced_keep"`
	MemoryDeletedTTL    string `yaml:"memory_deleted_ttl"`
	TombstoneTTL        string `yaml:"tombstone_ttl"`
	GCInterval          string `yaml:"gc_interval"`
}

func DefaultRetentionPolicy() RetentionPolicy {
	return RetentionPolicy{
		EventCompactAfter:   "7d",
		ChunkNoRefTTL:       "30d",
		ChunkReferencedKeep: "forever",
		MemoryDeletedTTL:    "90d",
		TombstoneTTL:        "30d",
		GCInterval:          "24h",
	}
}

type LifecycleManager struct {
	rootDir   string
	retention RetentionPolicy
}

func NewLifecycleManager(rootDir string) *LifecycleManager {
	lm := &LifecycleManager{rootDir: rootDir}
	lm.retention = lm.loadRetention()
	return lm
}

func (lm *LifecycleManager) loadRetention() RetentionPolicy {
	policy := DefaultRetentionPolicy()
	path := filepath.Join(lm.rootDir, "lifecycle", "retention.yaml")
	data, err := os.ReadFile(path)
	if err != nil {
		return policy
	}
	_ = yaml.Unmarshal(data, &policy)
	return policy
}

func (lm *LifecycleManager) EnsureRetentionFile() error {
	dir := filepath.Join(lm.rootDir, "lifecycle")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create lifecycle directory: %w", err)
	}
	path := filepath.Join(dir, "retention.yaml")
	if _, err := os.Stat(path); err == nil {
		return nil
	}
	data, err := yaml.Marshal(lm.retention)
	if err != nil {
		return fmt.Errorf("marshal retention policy: %w", err)
	}
	return writeFileAtomic(path, data, 0o644)
}

type VerifyReport struct {
	IndexRebuilt       bool
	OrphanedIndexItems int
	StaleTraceability  int
	ProfileFixups      int
}

func (lm *LifecycleManager) Verify(ctx context.Context) (VerifyReport, error) {
	var report VerifyReport
	longTermDir := filepath.Join(lm.rootDir, "long_term")
	store := NewLongTermMemoryStore(longTermDir, WithLifecycleDir(filepath.Join(lm.rootDir, "lifecycle")))

	indexPath := store.indexPath()
	data, err := os.ReadFile(indexPath)
	if err != nil {
		if os.IsNotExist(err) {
			if err := store.RebuildIndex(ctx); err != nil {
				return report, err
			}
			report.IndexRebuilt = true
			return report, nil
		}
		return report, fmt.Errorf("read index for verify: %w", err)
	}

	var index memoryIndex
	if err := yaml.Unmarshal(data, &index); err != nil {
		if err := store.RebuildIndex(ctx); err != nil {
			return report, err
		}
		report.IndexRebuilt = true
		return report, nil
	}

	needsRebuild := false
	for _, entry := range index.Memories {
		memPath := filepath.Join(longTermDir, entry.File)
		if _, err := os.Stat(memPath); os.IsNotExist(err) {
			report.OrphanedIndexItems++
			needsRebuild = true
		}
	}

	if needsRebuild {
		if err := store.RebuildIndex(ctx); err != nil {
			return report, err
		}
		report.IndexRebuilt = true
	}

	entries, err := os.ReadDir(store.memoriesDir())
	if err != nil && !os.IsNotExist(err) {
		return report, err
	}
	chunksDir := filepath.Join(lm.rootDir, "session", "chunks")
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".md") {
			continue
		}
		path := filepath.Join(store.memoriesDir(), entry.Name())
		parsed, err := readMemoryMarkdown(path)
		if err != nil {
			continue
		}
		if parsed.Item.Traceability == "excerpt_only" {
			continue
		}
		for _, ref := range parsed.Item.SourceRefs {
			if ref.Type != "chunk" {
				continue
			}
			chunkPath := filepath.Join(chunksDir, ref.ID+".jsonl")
			if _, err := os.Stat(chunkPath); os.IsNotExist(err) {
				parsed.Item.Traceability = "excerpt_only"
				parsed.Item.UpdatedAt = time.Now().UTC().Format(time.RFC3339Nano)
				if err := writeFileAtomic(path, []byte(formatMemoryMarkdown(parsed.Item)), 0o644); err == nil {
					report.StaleTraceability++
				}
				break
			}
		}
	}

	profilePath := filepath.Join(longTermDir, "profile.md")
	if _, err := os.Stat(profilePath); os.IsNotExist(err) {
		if err := store.RegenerateProfileMD(ctx); err == nil {
			report.ProfileFixups++
		}
	}

	return report, nil
}
