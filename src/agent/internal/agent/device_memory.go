package agent

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

type DeviceMemoryStore struct {
	rootDir string
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
		if item.Status != "" && item.Status != "active" && !conflicted {
			continue
		}
		if memoryExpiresAtPassed(item.ExpiresAt, time.Now().UTC()) {
			continue
		}
		if query.DeviceID != "" && item.DeviceID != "" && query.DeviceID != item.DeviceID {
			continue
		}
		if !matchesAny(query.Types, []string{item.Type}) {
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
		if conflicted {
			hit.Type = "conflict"
		}
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
	return item.ID, nil
}

func (s *DeviceMemoryStore) Update(ctx context.Context, id string, update func(*DeviceMemoryItem)) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}
	if s == nil || s.rootDir == "" || strings.TrimSpace(id) == "" || update == nil {
		return nil
	}
	items, err := s.readAll()
	if err != nil {
		return err
	}
	for _, item := range items {
		if item.ID != id {
			continue
		}
		update(&item)
		item.UpdatedAt = time.Now().UTC().Format(time.RFC3339Nano)
		_, err := s.Upsert(ctx, item)
		return err
	}
	return nil
}

func (s *DeviceMemoryStore) readAll() ([]DeviceMemoryItem, error) {
	if _, err := os.Stat(s.rootDir); err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var items []DeviceMemoryItem
	err := filepath.WalkDir(s.rootDir, func(path string, entry os.DirEntry, err error) error {
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
	return items, err
}

func (s *DeviceMemoryStore) itemPath(item DeviceMemoryItem) string {
	switch item.Type {
	case "device_profile":
		return filepath.Join(s.rootDir, "profile.yaml")
	case "app_profile":
		return filepath.Join(s.rootDir, "apps", safePathName(item.ID)+".yaml")
	case "procedure":
		return filepath.Join(s.rootDir, "procedures", safePathName(item.ID)+".yaml")
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
