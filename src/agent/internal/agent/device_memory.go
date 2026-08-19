package agent

import (
	"context"
	"crypto/sha256"
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
	writeMu sync.Mutex
	cacheMu sync.Mutex
	cache   deviceMemoryReadCache
}

type deviceMemoryReadCache struct {
	fingerprint string
	items       []DeviceMemoryItem
	valid       bool
}

type deviceMemoryStatus string

type DeviceMemoryQuery struct {
	Terms    []string `json:"terms,omitempty"`
	Tags     []string `json:"tags,omitempty"`
	Entities []string `json:"entities,omitempty"`
	DeviceID string   `json:"device_id,omitempty"`
	Types    []string `json:"types,omitempty"`
	Limit    int      `json:"limit,omitempty"`
}

type EpisodeMemoryCandidateQuery struct {
	Terms        []string
	PreferredIDs []string
	DeviceID     string
	Scope        map[string]string
	Limit        int
	CharBudget   int
}

type DeviceMemoryItem struct {
	ID               string                 `yaml:"id"`
	Type             string                 `yaml:"type"`
	Status           deviceMemoryStatus     `yaml:"status"`
	Revision         int                    `yaml:"revision,omitempty"`
	ExtractorVersion int                    `yaml:"extractor_version,omitempty"`
	LessonKey        string                 `yaml:"lesson_key,omitempty"`
	Title            string                 `yaml:"title,omitempty"`
	Content          string                 `yaml:"content,omitempty"`
	Summary          string                 `yaml:"summary,omitempty"`
	DeviceID         string                 `yaml:"device_id,omitempty"`
	AppID            string                 `yaml:"app_id,omitempty"`
	AppName          string                 `yaml:"app_name,omitempty"`
	PageName         string                 `yaml:"page_name,omitempty"`
	Tags             []string               `yaml:"tags,omitempty"`
	Entities         []string               `yaml:"entities,omitempty"`
	Aliases          []string               `yaml:"aliases,omitempty"`
	Confidence       float64                `yaml:"confidence,omitempty"`
	Priority         int                    `yaml:"priority,omitempty"`
	TTL              string                 `yaml:"ttl,omitempty"`
	ExpiresAt        string                 `yaml:"expires_at,omitempty"`
	UpdatedAt        string                 `yaml:"updated_at,omitempty"`
	LastValidatedAt  string                 `yaml:"last_validated_at,omitempty"`
	SuccessCount     int                    `yaml:"success_count,omitempty"`
	FailureCount     int                    `yaml:"failure_count,omitempty"`
	Applicability    map[string]string      `yaml:"applicability,omitempty"`
	EvidenceRefs     []MemorySourceRef      `yaml:"evidence_refs,omitempty"`
	ConflictsWith    []string               `yaml:"conflicts_with,omitempty"`
	RevisionHistory  []DeviceMemoryRevision `yaml:"revision_history,omitempty"`

	// Procedure-specific fields record the evidenced tool sequence with
	// per-step parameters and direct result observations.
	Steps []ProcedureStep `yaml:"steps,omitempty"`

	// App profile fields accumulate deterministic observations across Episodes.
	PagesSeen     []string `yaml:"pages_seen,omitempty"`
	ToolsUsed     []string `yaml:"tools_used,omitempty"`
	ProcedureRefs []string `yaml:"procedure_refs,omitempty"`
	KnownIssues   []string `yaml:"known_issues,omitempty"`
}

type DeviceMemoryRevision struct {
	Revision      int                `yaml:"revision"`
	Status        deviceMemoryStatus `yaml:"status"`
	Title         string             `yaml:"title,omitempty"`
	Summary       string             `yaml:"summary,omitempty"`
	Content       string             `yaml:"content,omitempty"`
	Tags          []string           `yaml:"tags,omitempty"`
	Applicability map[string]string  `yaml:"applicability,omitempty"`
	Steps         []ProcedureStep    `yaml:"steps,omitempty"`
	UpdatedAt     string             `yaml:"updated_at,omitempty"`
}

// ProcedureStep records one evidenced action. Coordinates and text come from
// the tool input; OutcomeNote comes from the paired tool result.
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
		if item.Status != deviceMemoryStatusActive {
			continue
		}
		if item.Type == string(episodeMemoryTypeFailure) && !hasLegacyReflectionFailureTag(item.Tags) && !hasEpisodeMemoryTag(item.Tags) {
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
		if len(query.Entities) > 0 && !matchesAnyMemoryName(query.Entities, deviceMemoryEntityCandidates(item)) {
			continue
		}
		if len(terms) > 0 && scoreDeviceMemory(item, terms) == 0 {
			continue
		}
		hit := deviceMemoryToHit(item)
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

// deviceMemoryEntityCandidates lists every recorded name an entity filter may
// legitimately refer to. AppName, PageName, and Aliases are all names a caller
// could name the memory by, so excluding them dropped otherwise valid matches.
func deviceMemoryEntityCandidates(item DeviceMemoryItem) []string {
	candidates := make([]string, 0, len(item.Entities)+len(item.Aliases)+4)
	candidates = append(candidates, item.Entities...)
	candidates = append(candidates, item.Aliases...)
	return append(candidates, item.AppID, item.AppName, item.PageName)
}

// matchesAnyMemoryName uses the same normalized exact-or-contained comparison
// as long-term memory recall. Aliases carry known rewordings; arbitrary token
// overlap is intentionally not treated as a name match.
func matchesAnyMemoryName(queryValues []string, candidateValues []string) bool {
	if len(queryValues) == 0 {
		return true
	}
	for _, queryValue := range queryValues {
		queryTerm := normalizeMemorySearchTerm(queryValue)
		if queryTerm == "" {
			continue
		}
		for _, candidateValue := range candidateValues {
			candidateTerm := normalizeMemorySearchTerm(candidateValue)
			if candidateTerm == "" {
				continue
			}
			if queryTerm == candidateTerm || memorySearchTermContains(queryTerm, candidateTerm) {
				return true
			}
		}
	}
	return false
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
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	return s.upsertLocked(item)
}

func (s *DeviceMemoryStore) upsertLocked(item DeviceMemoryItem) (string, error) {
	now := time.Now().UTC()
	if strings.TrimSpace(item.ID) == "" {
		item.ID = "devmem_" + strconvTimeID(now)
	}
	if strings.TrimSpace(item.Type) == "" {
		item.Type = string(episodeMemoryTypeFact)
	}
	if strings.TrimSpace(string(item.Status)) == "" {
		item.Status = deviceMemoryStatusActive
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
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
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
		_, err := s.upsertLocked(item)
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

func (s *DeviceMemoryStore) SearchEpisodeMemoryCandidates(ctx context.Context, query EpisodeMemoryCandidateQuery) ([]DeviceMemoryItem, error) {
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	default:
	}
	if s == nil || s.rootDir == "" {
		return nil, nil
	}
	limit := query.Limit
	if limit <= 0 || limit > 8 {
		limit = 8
	}
	charBudget := query.CharBudget
	if charBudget <= 0 {
		charBudget = 12000
	}
	items, err := s.readAll()
	if err != nil {
		return nil, err
	}
	terms := normalizeSearchTerms(query.Terms)
	preferredIDs := uniqueNonEmpty(query.PreferredIDs)
	preferred := make(map[string]int, len(preferredIDs))
	for index, id := range preferredIDs {
		preferred[id] = len(preferredIDs) - index
	}
	type scoredItem struct {
		item  DeviceMemoryItem
		tier  int
		score int
	}
	var matches []scoredItem
	for _, item := range items {
		switch item.Type {
		case string(episodeMemoryTypeProcedure), string(episodeMemoryTypeNavigation), string(episodeMemoryTypeCalibration), string(episodeMemoryTypeFailure), string(episodeMemoryTypeFact), "device_profile", "app_profile":
		default:
			continue
		}
		if item.Status != deviceMemoryStatusActive && item.Status != deviceMemoryStatusDisputed && item.Status != deviceMemoryStatusConflicted && item.Status != deviceMemoryStatusPending {
			continue
		}
		if query.DeviceID != "" && item.DeviceID != "" && !strings.EqualFold(query.DeviceID, item.DeviceID) {
			continue
		}
		if memoryExpiresAtPassed(item.ExpiresAt, time.Now().UTC()) {
			continue
		}
		score := scoreDeviceMemory(item, terms)
		boost, isPreferred := preferred[item.ID]
		sameScope := episodeMemoryScopeMatchesQuery(item, query.Scope)
		tier := 1
		switch {
		case isPreferred:
			tier = 4
			score += boost
		case sameScope && item.Status == deviceMemoryStatusActive:
			tier = 3
		case sameScope && (item.Status == deviceMemoryStatusDisputed || item.Status == deviceMemoryStatusConflicted):
			tier = 2
		}
		if score == 0 && tier == 1 {
			continue
		}
		matches = append(matches, scoredItem{item: item, tier: tier, score: score})
	}
	sort.SliceStable(matches, func(i, j int) bool {
		if matches[i].tier != matches[j].tier {
			return matches[i].tier > matches[j].tier
		}
		if matches[i].score == matches[j].score {
			return matches[i].item.ID < matches[j].item.ID
		}
		return matches[i].score > matches[j].score
	})
	result := make([]DeviceMemoryItem, 0, limit)
	usedChars := 0
	for _, match := range matches {
		item := cloneDeviceMemoryItem(match.item)
		baseCost := 256 + len(item.ID) + len(item.Title) + len(item.Summary)
		for key, value := range match.item.Applicability {
			baseCost += len(key) + len(value)
		}
		remaining := charBudget - usedChars
		if baseCost >= remaining {
			continue
		}
		maxContent := remaining - baseCost
		if len(item.Content) > maxContent {
			item.Content = truncateForLog(item.Content, maxContent)
		}
		cost := baseCost + len(item.Content)
		result = append(result, item)
		usedChars += cost
		if len(result) == limit {
			break
		}
	}
	return result, nil
}

func (s *DeviceMemoryStore) FindEpisodeMemoryByLesson(ctx context.Context, episodeID, lessonKey string) (DeviceMemoryItem, bool, error) {
	select {
	case <-ctx.Done():
		return DeviceMemoryItem{}, false, ctx.Err()
	default:
	}
	if s == nil || s.rootDir == "" || strings.TrimSpace(episodeID) == "" || strings.TrimSpace(lessonKey) == "" {
		return DeviceMemoryItem{}, false, nil
	}
	items, err := s.readAll()
	if err != nil {
		return DeviceMemoryItem{}, false, err
	}
	for _, item := range items {
		if item.LessonKey == lessonKey && hasEpisodeEvidence(item.EvidenceRefs, episodeID) {
			return cloneDeviceMemoryItem(item), true, nil
		}
	}
	return DeviceMemoryItem{}, false, nil
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
		// Older Device Memory files treated an omitted status as active. Preserve
		// that meaning so the typed status migration does not silently hide them.
		if strings.TrimSpace(string(item.Status)) == "" {
			item.Status = deviceMemoryStatusActive
		}
		if item.ID == "" {
			item.ID = strings.TrimSuffix(entry.Name(), ".yaml")
		}
		if isRetiredCoordinateModeCalibration(item) {
			return nil
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

func isRetiredCoordinateModeCalibration(item DeviceMemoryItem) bool {
	if item.Type != "calibration" {
		return false
	}
	if item.ID == "cal_normalized_coordinates" || item.ID == "cal_normalized_coord_reliable" {
		return true
	}
	text := strings.ToLower(strings.Join([]string{item.Title, item.Content}, " "))
	return strings.Contains(text, "normalized coordinate") ||
		strings.Contains(text, "pixel coordinate") ||
		strings.Contains(text, "absolute coordinate")
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
	h := sha256.New()
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
	item.RevisionHistory = cloneDeviceMemoryRevisions(item.RevisionHistory)
	item.Steps = append([]ProcedureStep(nil), item.Steps...)
	item.PagesSeen = append([]string(nil), item.PagesSeen...)
	item.ToolsUsed = append([]string(nil), item.ToolsUsed...)
	item.ProcedureRefs = append([]string(nil), item.ProcedureRefs...)
	item.KnownIssues = append([]string(nil), item.KnownIssues...)
	return item
}

func cloneDeviceMemoryRevisions(revisions []DeviceMemoryRevision) []DeviceMemoryRevision {
	if len(revisions) == 0 {
		return nil
	}
	cloned := make([]DeviceMemoryRevision, len(revisions))
	for i, revision := range revisions {
		cloned[i] = revision
		cloned[i].Tags = append([]string(nil), revision.Tags...)
		cloned[i].Applicability = cloneStringMap(revision.Applicability)
		cloned[i].Steps = append([]ProcedureStep(nil), revision.Steps...)
	}
	return cloned
}

func (s *DeviceMemoryStore) itemPath(item DeviceMemoryItem) string {
	switch item.Type {
	case "device_profile":
		return filepath.Join(s.rootDir, "profile.yaml")
	case "app_profile":
		return filepath.Join(s.rootDir, "apps", safePathName(item.ID)+".yaml")
	case string(episodeMemoryTypeProcedure):
		return filepath.Join(s.rootDir, "procedures", safePathName(item.ID)+".yaml")
	case string(episodeMemoryTypeNavigation):
		return filepath.Join(s.rootDir, "navigation", safePathName(item.ID)+".yaml")
	case "ui_anchor":
		return filepath.Join(s.rootDir, "ui_anchors", safePathName(item.ID)+".yaml")
	case string(episodeMemoryTypeCalibration):
		return filepath.Join(s.rootDir, "calibration", safePathName(item.ID)+".yaml")
	case string(episodeMemoryTypeFailure):
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
		hit.AppName,
		hit.PageName,
		renderMemoryScopeForSearch(hit.Applicability),
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

func episodeMemoryScopeMatchesQuery(item DeviceMemoryItem, queryScope map[string]string) bool {
	queryScope = normalizeEpisodeMemoryScope(queryScope)
	if len(queryScope) == 0 {
		return false
	}
	itemScope := normalizeEpisodeMemoryScope(item.Applicability)
	if itemScope == nil {
		itemScope = map[string]string{}
	}
	if item.DeviceID != "" {
		itemScope["device_id"] = item.DeviceID
	}
	if item.AppName != "" {
		itemScope["app_name"] = item.AppName
	}
	if item.PageName != "" {
		itemScope["page_name"] = item.PageName
	}
	matched := false
	for key, want := range queryScope {
		got := strings.TrimSpace(itemScope[key])
		if got == "" {
			continue
		}
		matched = true
		if !strings.EqualFold(got, want) {
			return false
		}
	}
	return matched
}

func renderMemoryScopeForSearch(scope map[string]string) string {
	if len(scope) == 0 {
		return ""
	}
	keys := make([]string, 0, len(scope))
	for key := range scope {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys)*2)
	for _, key := range keys {
		parts = append(parts, key, scope[key])
	}
	return strings.Join(parts, " ")
}

func hasLegacyReflectionFailureTag(tags []string) bool {
	for _, tag := range tags {
		if strings.EqualFold(strings.TrimSpace(tag), legacyReflectionFailureTag) {
			return true
		}
	}
	return false
}

func hasEpisodeMemoryTag(tags []string) bool {
	return containsStringFold(tags, episodeMemoryTag)
}
