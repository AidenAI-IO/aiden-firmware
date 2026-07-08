package agent

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"gopkg.in/yaml.v3"
)

type DeviceMemoryStore struct {
	rootDir string
	cacheMu sync.Mutex
	cache   deviceMemoryReadCache
}

type deviceMemoryReadCache struct {
	fingerprint string
	items       []DeviceMemoryItem
	valid       bool
}

type DeviceMemoryQuery struct {
	Terms    []string `json:"terms,omitempty"`
	Tags     []string `json:"tags,omitempty"`
	Entities []string `json:"entities,omitempty"`
	DeviceID string   `json:"device_id,omitempty"`
	Types    []string `json:"types,omitempty"`
	Limit    int      `json:"limit,omitempty"`
}

type DeviceMemoryItem struct {
	ID              string            `yaml:"id"`
	Type            string            `yaml:"type"`
	Status          string            `yaml:"status"`
	Title           string            `yaml:"title,omitempty"`
	Content         string            `yaml:"content,omitempty"`
	Summary         string            `yaml:"summary,omitempty"`
	DeviceID        string            `yaml:"device_id,omitempty"`
	AppID           string            `yaml:"app_id,omitempty"`
	AppName         string            `yaml:"app_name,omitempty"`
	PageName        string            `yaml:"page_name,omitempty"`
	Tags            []string          `yaml:"tags,omitempty"`
	Entities        []string          `yaml:"entities,omitempty"`
	Aliases         []string          `yaml:"aliases,omitempty"`
	Confidence      float64           `yaml:"confidence,omitempty"`
	Priority        int               `yaml:"priority,omitempty"`
	TTL             string            `yaml:"ttl,omitempty"`
	ExpiresAt       string            `yaml:"expires_at,omitempty"`
	UpdatedAt       string            `yaml:"updated_at,omitempty"`
	LastValidatedAt string            `yaml:"last_validated_at,omitempty"`
	SuccessCount    int               `yaml:"success_count,omitempty"`
	FailureCount    int               `yaml:"failure_count,omitempty"`
	Applicability   map[string]string `yaml:"applicability,omitempty"`
	EvidenceRefs    []MemorySourceRef `yaml:"evidence_refs,omitempty"`
	ConflictsWith   []string          `yaml:"conflicts_with,omitempty"`

	// Procedure-specific fields (改进 1): records the exact tool sequence with
	// per-step parameters and observations so Planner can replay the path.
	Steps []ProcedureStep `yaml:"steps,omitempty"`

	// App profile累积字段 (改进 5): 跨多次成功 episode 累积该 app 的使用知识。
	PagesSeen     []string `yaml:"pages_seen,omitempty"`
	ToolsUsed     []string `yaml:"tools_used,omitempty"`
	ProcedureRefs []string `yaml:"procedure_refs,omitempty"`
	KnownIssues   []string `yaml:"known_issues,omitempty"`
}

// ProcedureStep 记录 procedure 中的一步动作详情，足以让 Planner 复用具体路径
// 而不只是工具名。坐标和文本来自工具调用参数；OutcomeNote 来自 verifier 紧随
// 的观察（page_name 变化等）。
type ProcedureStep struct {
	Tool        string `yaml:"tool"`
	Description string `yaml:"description,omitempty"`
	Coords      string `yaml:"coords,omitempty"`
	Text        string `yaml:"text,omitempty"`
	AppName     string `yaml:"app_name,omitempty"`
	PageName    string `yaml:"page_name,omitempty"`
	OutcomeNote string `yaml:"outcome_note,omitempty"`
}

func NewDeviceMemoryStore(rootDir string) *DeviceMemoryStore {
	return &DeviceMemoryStore{rootDir: rootDir}
}

func (s *DeviceMemoryStore) Search(ctx context.Context, query DeviceMemoryQuery) ([]MemoryHit, error) {
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	default:
	}
	if s == nil || s.rootDir == "" {
		return nil, nil
	}
	items, err := s.readAll()
	if err != nil {
		return nil, err
	}
	limit := query.Limit
	if limit <= 0 {
		limit = 8
	}
	terms := normalizeSearchTerms(append(append([]string(nil), query.Terms...), append(query.Tags, query.Entities...)...))
	var hits []MemoryHit
	for _, item := range items {
		conflicted := item.Status == "conflicted"
		// Conflicted records are surfaced under the synthetic "conflict" type, so
		// type filtering must run against the effective type, not the stored one.
		// Otherwise a conflicted procedure still matches query.Types=["procedure"]
		// and can never be retrieved via query.Types=["conflict"].
		effectiveType := item.Type
		if conflicted {
			effectiveType = "conflict"
		}
		if item.Status != "" && item.Status != "active" && !conflicted {
			continue
		}
		if memoryExpiresAtPassed(item.ExpiresAt, time.Now().UTC()) {
			continue
		}
		if query.DeviceID != "" && item.DeviceID != "" && query.DeviceID != item.DeviceID {
			continue
		}
		if !matchesAny(query.Types, []string{effectiveType}) {
			continue
		}
		if len(query.Tags) > 0 && !matchesAny(query.Tags, item.Tags) {
			continue
		}
		if len(query.Entities) > 0 && !matchesAny(query.Entities, append(append([]string(nil), item.Entities...), item.AppID)) {
			continue
		}
		if len(terms) > 0 && scoreDeviceMemory(item, terms) == 0 {
			continue
		}
		hit := deviceMemoryToHit(item)
		hit.Type = effectiveType
		hits = append(hits, hit)
	}
	sort.SliceStable(hits, func(i, j int) bool {
		scoreI := scoreMemoryHit(hits[i], terms)
		scoreJ := scoreMemoryHit(hits[j], terms)
		if scoreI == scoreJ {
			if hits[i].Priority == hits[j].Priority {
				return hits[i].ID < hits[j].ID
			}
			return hits[i].Priority > hits[j].Priority
		}
		return scoreI > scoreJ
	})
	if len(hits) > limit {
		hits = hits[:limit]
	}
	return hits, nil
}

func (s *DeviceMemoryStore) Upsert(ctx context.Context, item DeviceMemoryItem) (string, error) {
	select {
	case <-ctx.Done():
		return "", ctx.Err()
	default:
	}
	if s == nil || s.rootDir == "" {
		return "", nil
	}
	now := time.Now().UTC()
	if strings.TrimSpace(item.ID) == "" {
		item.ID = "devmem_" + strconvTimeID(now)
	}
	if strings.TrimSpace(item.Type) == "" {
		item.Type = "fact"
	}
	if strings.TrimSpace(item.Status) == "" {
		item.Status = "active"
	}
	if strings.TrimSpace(item.UpdatedAt) == "" {
		item.UpdatedAt = now.Format(time.RFC3339Nano)
	}
	if strings.TrimSpace(item.ExpiresAt) == "" {
		item.ExpiresAt = ttlExpiresAt(now, item.TTL)
	}
	path := s.itemPath(item)
	if err := writeYAMLAtomic(path, item); err != nil {
		return "", err
	}
	s.invalidateReadAllCache()
	return item.ID, nil
}

func (s *DeviceMemoryStore) Update(ctx context.Context, id string, update func(*DeviceMemoryItem)) error {
	return s.UpdateMany(ctx, []string{id}, update)
}

func (s *DeviceMemoryStore) UpdateMany(ctx context.Context, ids []string, update func(*DeviceMemoryItem)) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}
	idSet := map[string]bool{}
	for _, id := range uniqueNonEmpty(ids) {
		idSet[id] = true
	}
	if s == nil || s.rootDir == "" || len(idSet) == 0 || update == nil {
		return nil
	}
	items, err := s.readAll()
	if err != nil {
		return err
	}
	for _, item := range items {
		if !idSet[item.ID] {
			continue
		}
		oldPath := s.itemPath(item)
		update(&item)
		item.UpdatedAt = time.Now().UTC().Format(time.RFC3339Nano)
		newPath := s.itemPath(item)
		_, err := s.Upsert(ctx, item)
		// Upsert writes to a type-specific path. If the callback changed item.Type,
		// the old YAML would otherwise linger and readAll() would surface a stale
		// duplicate ID, so remove it once the new file is written.
		if err == nil && oldPath != newPath {
			if removeErr := os.Remove(oldPath); removeErr != nil && !os.IsNotExist(removeErr) {
				return removeErr
			}
		}
		if err != nil {
			return err
		}
	}
	s.invalidateReadAllCache()
	return nil
}

// Get returns the stored item with the given ID. The second return value is
// false when no such item exists (without an error), so callers can decide
// whether to create a fresh record.
func (s *DeviceMemoryStore) Get(ctx context.Context, id string) (DeviceMemoryItem, bool, error) {
	select {
	case <-ctx.Done():
		return DeviceMemoryItem{}, false, ctx.Err()
	default:
	}
	if s == nil || s.rootDir == "" || strings.TrimSpace(id) == "" {
		return DeviceMemoryItem{}, false, nil
	}
	items, err := s.readAll()
	if err != nil {
		return DeviceMemoryItem{}, false, err
	}
	for _, item := range items {
		if item.ID == id {
			return item, true, nil
		}
	}
	return DeviceMemoryItem{}, false, nil
}

func (s *DeviceMemoryStore) readAll() ([]DeviceMemoryItem, error) {
	if _, err := os.Stat(s.rootDir); err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	fingerprint, err := s.readAllFingerprint()
	if err != nil {
		return nil, err
	}
	if cached, ok := s.cachedReadAll(fingerprint); ok {
		return cached, nil
	}
	var items []DeviceMemoryItem
	err = filepath.WalkDir(s.rootDir, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".yaml") {
			return nil
		}
		if strings.HasSuffix(entry.Name(), "index.yaml") {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		var item DeviceMemoryItem
		if err := yaml.Unmarshal(data, &item); err != nil {
			return fmt.Errorf("decode device memory %q: %w", path, err)
		}
		if item.ID == "" {
			item.ID = strings.TrimSuffix(entry.Name(), ".yaml")
		}
		items = append(items, item)
		return nil
	})
	if err != nil {
		return nil, err
	}
	s.storeReadAllCache(fingerprint, items)
	return cloneDeviceMemoryItems(items), nil
}

func (s *DeviceMemoryStore) cachedReadAll(fingerprint string) ([]DeviceMemoryItem, bool) {
	s.cacheMu.Lock()
	defer s.cacheMu.Unlock()
	if !s.cache.valid || s.cache.fingerprint != fingerprint {
		return nil, false
	}
	return cloneDeviceMemoryItems(s.cache.items), true
}

func (s *DeviceMemoryStore) storeReadAllCache(fingerprint string, items []DeviceMemoryItem) {
	s.cacheMu.Lock()
	defer s.cacheMu.Unlock()
	s.cache = deviceMemoryReadCache{fingerprint: fingerprint, items: cloneDeviceMemoryItems(items), valid: true}
}

func (s *DeviceMemoryStore) invalidateReadAllCache() {
	s.cacheMu.Lock()
	defer s.cacheMu.Unlock()
	s.cache = deviceMemoryReadCache{}
}

func (s *DeviceMemoryStore) readAllFingerprint() (string, error) {
	h := sha1.New()
	err := filepath.WalkDir(s.rootDir, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".yaml") || strings.HasSuffix(entry.Name(), "index.yaml") {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(s.rootDir, path)
		if err != nil {
			rel = path
		}
		_, _ = fmt.Fprintf(h, "%s\x00%d\x00%d\x00", filepath.ToSlash(rel), info.ModTime().UnixNano(), info.Size())
		return nil
	})
	if err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func cloneDeviceMemoryItems(items []DeviceMemoryItem) []DeviceMemoryItem {
	if len(items) == 0 {
		return nil
	}
	cloned := make([]DeviceMemoryItem, len(items))
	for i, item := range items {
		cloned[i] = cloneDeviceMemoryItem(item)
	}
	return cloned
}

func cloneDeviceMemoryItem(item DeviceMemoryItem) DeviceMemoryItem {
	item.Tags = append([]string(nil), item.Tags...)
	item.Entities = append([]string(nil), item.Entities...)
	item.Aliases = append([]string(nil), item.Aliases...)
	item.Applicability = cloneStringMap(item.Applicability)
	item.EvidenceRefs = cloneMemorySourceRefs(item.EvidenceRefs)
	item.ConflictsWith = append([]string(nil), item.ConflictsWith...)
	item.Steps = append([]ProcedureStep(nil), item.Steps...)
	item.PagesSeen = append([]string(nil), item.PagesSeen...)
	item.ToolsUsed = append([]string(nil), item.ToolsUsed...)
	item.ProcedureRefs = append([]string(nil), item.ProcedureRefs...)
	item.KnownIssues = append([]string(nil), item.KnownIssues...)
	return item
}

func (s *DeviceMemoryStore) itemPath(item DeviceMemoryItem) string {
	switch item.Type {
	case "device_profile":
		return filepath.Join(s.rootDir, "profile.yaml")
	case "app_profile":
		return filepath.Join(s.rootDir, "apps", safePathName(item.ID)+".yaml")
	case "procedure":
		return filepath.Join(s.rootDir, "procedures", safePathName(item.ID)+".yaml")
	case "navigation":
		return filepath.Join(s.rootDir, "navigation", safePathName(item.ID)+".yaml")
	case "ui_anchor":
		return filepath.Join(s.rootDir, "ui_anchors", safePathName(item.ID)+".yaml")
	case "calibration":
		return filepath.Join(s.rootDir, "calibration", safePathName(item.ID)+".yaml")
	case "failure":
		return filepath.Join(s.rootDir, "failures", safePathName(item.ID)+".yaml")
	default:
		return filepath.Join(s.rootDir, "memories", safePathName(item.ID)+".yaml")
	}
}

func deviceMemoryToHit(item DeviceMemoryItem) MemoryHit {
	title := firstNonEmptyString([]string{item.Title, item.Summary, item.Content, item.ID})
	return MemoryHit{
		ID:            item.ID,
		Type:          item.Type,
		Title:         title,
		Summary:       item.Summary,
		Content:       item.Content,
		Priority:      item.Priority,
		Confidence:    item.Confidence,
		Tags:          append([]string(nil), item.Tags...),
		Entities:      append([]string(nil), item.Entities...),
		Source:        "device",
		Applicability: cloneStringMap(item.Applicability),
		EvidenceRefs:  append([]MemorySourceRef(nil), item.EvidenceRefs...),
		Steps:         append([]ProcedureStep(nil), item.Steps...),
		AppName:       item.AppName,
		PageName:      item.PageName,
	}
}

func scoreDeviceMemory(item DeviceMemoryItem, terms []string) int {
	return scoreMemoryHit(deviceMemoryToHit(item), terms)
}

func scoreMemoryHit(hit MemoryHit, terms []string) int {
	if len(terms) == 0 {
		return 1
	}
	haystack := strings.ToLower(strings.Join([]string{
		hit.ID,
		hit.Type,
		hit.Title,
		hit.Summary,
		hit.Content,
		strings.Join(hit.Tags, " "),
		strings.Join(hit.Entities, " "),
	}, " "))
	score := 0
	for _, term := range terms {
		term = strings.ToLower(strings.TrimSpace(term))
		if term != "" && strings.Contains(haystack, term) {
			score++
		}
	}
	return score
}
