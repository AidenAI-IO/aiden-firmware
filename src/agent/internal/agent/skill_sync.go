package agent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type SkillSyncOptions struct {
	ConfigDir        string
	BundledSkillsDir string
	Quiet            bool
}

type SkillSyncReport struct {
	Copied        []string
	Updated       []string
	KeptUser      []string
	DeletedByUser []string
	Stale         []string
}

type BundledManifest struct {
	Version   int                      `json:"version"`
	UpdatedAt string                   `json:"updated_at"`
	Skills    map[string]ManifestEntry `json:"skills"`
}

type ManifestEntry struct {
	OriginHash   string `json:"origin_hash"`
	Status       string `json:"status"`
	BasePath     string `json:"base_path,omitempty"`
	LastSyncedAt string `json:"last_synced_at,omitempty"`
}

const (
	StatusSynced        = "synced"
	StatusUserModified  = "user_modified"
	StatusDeletedByUser = "deleted_by_user"
)

func SyncBundledSkills(ctx context.Context, opts SkillSyncOptions) (*SkillSyncReport, error) {
	if opts.BundledSkillsDir == "" || opts.ConfigDir == "" {
		return &SkillSyncReport{}, nil
	}

	userSkillsDir := filepath.Join(opts.ConfigDir, "skills")
	stateDir := filepath.Join(opts.ConfigDir, "skill-state")
	manifestPath := filepath.Join(stateDir, ".bundled_manifest.json")

	if err := os.MkdirAll(userSkillsDir, 0o755); err != nil {
		return nil, fmt.Errorf("create user skills dir: %w", err)
	}
	if err := os.MkdirAll(stateDir, 0o755); err != nil {
		return nil, fmt.Errorf("create skill-state dir: %w", err)
	}

	manifest := loadManifest(manifestPath)
	bundled := discoverBundledSkills(opts.BundledSkillsDir)
	report := &SkillSyncReport{}

	for name, bundledPath := range bundled {
		syncOneSkill(name, bundledPath, userSkillsDir, stateDir, manifest, report)
	}

	cleanStaleManifestEntries(manifest, bundled, report)
	manifest.UpdatedAt = time.Now().Format(time.RFC3339)
	saveManifest(manifestPath, manifest)

	if !opts.Quiet {
		logSyncReport(report)
	}
	return report, nil
}

func syncOneSkill(name, bundledPath, userSkillsDir, stateDir string, manifest *BundledManifest, report *SkillSyncReport) {
	userDir := filepath.Join(userSkillsDir, name)
	userPath := filepath.Join(userDir, "SKILL.md")
	bundledContent, err := os.ReadFile(bundledPath)
	if err != nil {
		log.Printf("[skill_sync] read bundled %s: %v", name, err)
		return
	}
	bundledHash := hashContent(bundledContent)

	entry, hasManifest := manifest.Skills[name]
	userExists := fileExists(userPath)

	switch {
	case hasManifest && entry.Status == StatusDeletedByUser:
		report.DeletedByUser = append(report.DeletedByUser, name)

	case !userExists && !hasManifest:
		copyBundledToUser(name, bundledContent, userDir, userPath, bundledHash, stateDir, manifest, report)

	case !userExists && hasManifest:
		report.DeletedByUser = append(report.DeletedByUser, name)
		manifest.Skills[name] = ManifestEntry{
			OriginHash:   entry.OriginHash,
			Status:       StatusDeletedByUser,
			BasePath:     entry.BasePath,
			LastSyncedAt: entry.LastSyncedAt,
		}

	case userExists && !hasManifest:
		report.KeptUser = append(report.KeptUser, name)

	case userExists && hasManifest:
		syncExistingSkill(name, userPath, bundledContent, bundledHash, entry, stateDir, manifest, report)
	}
}

func syncExistingSkill(name, userPath string, bundledContent []byte, bundledHash string, entry ManifestEntry, stateDir string, manifest *BundledManifest, report *SkillSyncReport) {
	userContent, err := os.ReadFile(userPath)
	if err != nil {
		log.Printf("[skill_sync] read user %s: %v", name, err)
		return
	}
	userHash := hashContent(userContent)

	userModified := userHash != entry.OriginHash
	bundledUpdated := bundledHash != entry.OriginHash
	basePath := basePathForSkill(stateDir, name)

	switch {
	case !userModified && !bundledUpdated:
		// nothing to do
	case !userModified && bundledUpdated:
		if err := os.WriteFile(userPath, bundledContent, 0o644); err != nil {
			log.Printf("[skill_sync] update %s: %v", name, err)
			return
		}
		saveBase(basePath, bundledContent)
		manifest.Skills[name] = ManifestEntry{
			OriginHash:   bundledHash,
			Status:       StatusSynced,
			BasePath:     basePath,
			LastSyncedAt: time.Now().Format(time.RFC3339),
		}
		report.Updated = append(report.Updated, name)
	case userModified && !bundledUpdated:
		manifest.Skills[name] = ManifestEntry{
			OriginHash:   entry.OriginHash,
			Status:       StatusUserModified,
			BasePath:     entry.BasePath,
			LastSyncedAt: entry.LastSyncedAt,
		}
		report.KeptUser = append(report.KeptUser, name)
	case userModified && bundledUpdated:
		manifest.Skills[name] = ManifestEntry{
			OriginHash:   entry.OriginHash,
			Status:       StatusUserModified,
			BasePath:     entry.BasePath,
			LastSyncedAt: entry.LastSyncedAt,
		}
		report.KeptUser = append(report.KeptUser, name)
	}
}

func copyBundledToUser(name string, content []byte, userDir, userPath, bundledHash, stateDir string, manifest *BundledManifest, report *SkillSyncReport) {
	if err := os.MkdirAll(userDir, 0o755); err != nil {
		log.Printf("[skill_sync] mkdir %s: %v", userDir, err)
		return
	}
	if err := os.WriteFile(userPath, content, 0o644); err != nil {
		log.Printf("[skill_sync] write %s: %v", userPath, err)
		return
	}
	basePath := basePathForSkill(stateDir, name)
	saveBase(basePath, content)
	manifest.Skills[name] = ManifestEntry{
		OriginHash:   bundledHash,
		Status:       StatusSynced,
		BasePath:     basePath,
		LastSyncedAt: time.Now().Format(time.RFC3339),
	}
	report.Copied = append(report.Copied, name)
}

func discoverBundledSkills(bundledDir string) map[string]string {
	result := make(map[string]string)
	entries, err := os.ReadDir(bundledDir)
	if err != nil {
		return result
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		skillPath := filepath.Join(bundledDir, e.Name(), "SKILL.md")
		if fileExists(skillPath) {
			result[e.Name()] = skillPath
		}
	}
	return result
}

func cleanStaleManifestEntries(manifest *BundledManifest, bundled map[string]string, report *SkillSyncReport) {
	for name := range manifest.Skills {
		if _, ok := bundled[name]; !ok {
			report.Stale = append(report.Stale, name)
			delete(manifest.Skills, name)
		}
	}
}

func loadManifest(path string) *BundledManifest {
	m := &BundledManifest{Version: 1, Skills: make(map[string]ManifestEntry)}
	data, err := os.ReadFile(path)
	if err != nil {
		return m
	}
	if err := json.Unmarshal(data, m); err != nil {
		return &BundledManifest{Version: 1, Skills: make(map[string]ManifestEntry)}
	}
	if m.Skills == nil {
		m.Skills = make(map[string]ManifestEntry)
	}
	return m
}

func saveManifest(path string, m *BundledManifest) {
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		log.Printf("[skill_sync] marshal manifest: %v", err)
		return
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		log.Printf("[skill_sync] write manifest: %v", err)
	}
}

func hashContent(data []byte) string {
	h := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(h[:])
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func logSyncReport(report *SkillSyncReport) {
	if len(report.Copied) > 0 {
		log.Printf("[skill_sync] copied %d bundled skill(s): %s", len(report.Copied), strings.Join(report.Copied, ", "))
	}
	if len(report.Updated) > 0 {
		log.Printf("[skill_sync] updated %d skill(s): %s", len(report.Updated), strings.Join(report.Updated, ", "))
	}
	if len(report.KeptUser) > 0 {
		log.Printf("[skill_sync] kept user version for %d skill(s): %s", len(report.KeptUser), strings.Join(report.KeptUser, ", "))
	}
}

func basePathForSkill(stateDir, name string) string {
	return filepath.Join(stateDir, "bases", name, "SKILL.md")
}

func saveBase(basePath string, content []byte) {
	if err := os.MkdirAll(filepath.Dir(basePath), 0o755); err != nil {
		log.Printf("[skill_sync] mkdir base: %v", err)
		return
	}
	if err := os.WriteFile(basePath, content, 0o644); err != nil {
		log.Printf("[skill_sync] write base: %v", err)
	}
}

func RestoreBundledSkill(configDir, bundledSkillsDir, name string) error {
	bundledPath := filepath.Join(bundledSkillsDir, name, "SKILL.md")
	content, err := os.ReadFile(bundledPath)
	if err != nil {
		return fmt.Errorf("read bundled skill %q: %w", name, err)
	}

	userPath := filepath.Join(configDir, "skills", name, "SKILL.md")
	if err := os.MkdirAll(filepath.Dir(userPath), 0o755); err != nil {
		return fmt.Errorf("create skill dir: %w", err)
	}
	if err := os.WriteFile(userPath, content, 0o644); err != nil {
		return fmt.Errorf("write user skill: %w", err)
	}

	stateDir := filepath.Join(configDir, "skill-state")
	basePath := basePathForSkill(stateDir, name)
	saveBase(basePath, content)

	manifestPath := filepath.Join(stateDir, ".bundled_manifest.json")
	manifest := loadManifest(manifestPath)
	manifest.Skills[name] = ManifestEntry{
		OriginHash:   hashContent(content),
		Status:       StatusSynced,
		BasePath:     basePath,
		LastSyncedAt: time.Now().Format(time.RFC3339),
	}
	manifest.UpdatedAt = time.Now().Format(time.RFC3339)
	saveManifest(manifestPath, manifest)
	return nil
}
