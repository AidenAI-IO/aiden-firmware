package agent

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// ProfileFn synthesizes a user profile from a list of memory entries.
// Each entry has Type (profile/rule/preference) and Content.
type ProfileFn func(ctx context.Context, entries []ProfileEntry) string

type ProfileEntry struct {
	Type    string
	Content string
}

type LongTermMemoryStore struct {
	rootDir      string
	lifecycleDir string
	profileFn    ProfileFn
}

type LongTermMemoryOption func(*LongTermMemoryStore)

func WithLifecycleDir(dir string) LongTermMemoryOption {
	return func(s *LongTermMemoryStore) { s.lifecycleDir = dir }
}

func WithStoreProfileFn(fn ProfileFn) LongTermMemoryOption {
	return func(s *LongTermMemoryStore) { s.profileFn = fn }
}

type MemorySourceRef struct {
	Type     string   `yaml:"type" json:"type"`
	ID       string   `yaml:"id" json:"id"`
	EventIDs []string `yaml:"event_ids,omitempty" json:"event_ids,omitempty"`
}

type MemoryItem struct {
	ID               string            `yaml:"id"`
	Type             string            `yaml:"type"`
	Status           string            `yaml:"status"`
	Priority         int               `yaml:"priority"`
	Confidence       float64           `yaml:"confidence"`
	Tags             []string          `yaml:"tags,omitempty"`
	Entities         []string          `yaml:"entities,omitempty"`
	TimeScope        string            `yaml:"time_scope"`
	CreatedAt        string            `yaml:"created_at"`
	UpdatedAt        string            `yaml:"updated_at"`
	SourceRefs       []MemorySourceRef `yaml:"source_refs,omitempty"`
	Supersedes       string            `yaml:"supersedes,omitempty"`
	SupersededBy     string            `yaml:"superseded_by,omitempty"`
	Traceability     string            `yaml:"traceability"`
	Title            string            `yaml:"-"`
	Content          string            `yaml:"-"`
	EvidenceExcerpts []string          `yaml:"-"`
}

type MemoryQuery struct {
	Tags     []string
	Entities []string
	Types    []string
	Limit    int
}

type MemoryResult struct {
	ID         string   `json:"id"`
	Type       string   `json:"type"`
	Status     string   `json:"status"`
	Title      string   `json:"title"`
	Summary    string   `json:"summary"`
	Content    string   `json:"content"`
	Priority   int      `json:"priority"`
	Confidence float64  `json:"confidence"`
	Tags       []string `json:"tags,omitempty"`
	Entities   []string `json:"entities,omitempty"`
	FilePath   string   `json:"file_path"`
}

type memoryIndex struct {
	Version   int                `yaml:"version"`
	UpdatedAt string             `yaml:"updated_at"`
	Memories  []memoryIndexEntry `yaml:"memories"`
}

type memoryIndexEntry struct {
	ID         string   `yaml:"id"`
	File       string   `yaml:"file"`
	Type       string   `yaml:"type"`
	Status     string   `yaml:"status"`
	Priority   int      `yaml:"priority"`
	Confidence float64  `yaml:"confidence"`
	Tags       []string `yaml:"tags,omitempty"`
	Entities   []string `yaml:"entities,omitempty"`
	Summary    string   `yaml:"summary"`
}

func NewLongTermMemoryStore(rootDir string, opts ...LongTermMemoryOption) *LongTermMemoryStore {
	s := &LongTermMemoryStore{rootDir: rootDir}
	for _, opt := range opts {
		opt(s)
	}
	if s.lifecycleDir == "" {
		s.lifecycleDir = filepath.Join(rootDir, "..", "lifecycle")
	}
	return s
}

func (s *LongTermMemoryStore) RootDir() string {
	return s.rootDir
}

func (s *LongTermMemoryStore) AddMemory(ctx context.Context, item MemoryItem) (string, error) {
	select {
	case <-ctx.Done():
		return "", ctx.Err()
	default:
	}
	if strings.TrimSpace(item.Content) == "" {
		return "", errors.New("memory content is required")
	}
	if len(item.EvidenceExcerpts) == 0 {
		return "", errors.New("memory evidence excerpt is required")
	}
	item = normalizeMemoryItem(item, time.Now().UTC())
	path := s.memoryPath(item.ID)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return "", fmt.Errorf("create memory directory: %w", err)
	}
	if err := writeFileAtomic(path, []byte(formatMemoryMarkdown(item)), 0o644); err != nil {
		return "", fmt.Errorf("write memory %q: %w", item.ID, err)
	}
	if err := s.RebuildIndex(ctx); err != nil {
		return "", err
	}
	return item.ID, nil
}

func (s *LongTermMemoryStore) Search(ctx context.Context, query MemoryQuery) ([]MemoryResult, error) {
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	default:
	}
	index, err := s.loadIndex(ctx)
	if err != nil {
		return nil, err
	}
	limit := query.Limit
	if limit <= 0 {
		limit = 5
	}

	results := make([]MemoryResult, 0)
	for _, entry := range index.Memories {
		if entry.Status != "active" {
			continue
		}
		if !matchesAny(query.Types, []string{entry.Type}) {
			continue
		}
		if !matchesAny(query.Tags, entry.Tags) {
			continue
		}
		if !matchesAny(query.Entities, entry.Entities) {
			continue
		}
		path := filepath.Join(s.rootDir, entry.File)
		parsed, err := readMemoryMarkdown(path)
		if err != nil {
			return nil, err
		}
		results = append(results, MemoryResult{
			ID:         entry.ID,
			Type:       entry.Type,
			Status:     entry.Status,
			Title:      parsed.Title,
			Summary:    entry.Summary,
			Content:    parsed.Content,
			Priority:   entry.Priority,
			Confidence: entry.Confidence,
			Tags:       append([]string(nil), entry.Tags...),
			Entities:   append([]string(nil), entry.Entities...),
			FilePath:   path,
		})
		if len(results) >= limit {
			break
		}
	}
	return results, nil
}

func (s *LongTermMemoryStore) Forget(ctx context.Context, id string, reason string) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}
	path := s.memoryPath(id)
	parsed, err := readMemoryMarkdown(path)
	if err != nil {
		return err
	}
	parsed.Item.Status = "deleted"
	parsed.Item.UpdatedAt = time.Now().UTC().Format(time.RFC3339Nano)
	if err := writeFileAtomic(path, []byte(formatMemoryMarkdown(parsed.Item)), 0o644); err != nil {
		return fmt.Errorf("write forgotten memory %q: %w", id, err)
	}
	if err := s.appendTombstone(id, reason); err != nil {
		return err
	}
	return s.RebuildIndex(ctx)
}

func (s *LongTermMemoryStore) SupersedeMemory(ctx context.Context, oldID string, newItem MemoryItem) (string, error) {
	select {
	case <-ctx.Done():
		return "", ctx.Err()
	default:
	}
	oldPath := s.memoryPath(oldID)
	oldParsed, err := readMemoryMarkdown(oldPath)
	if err != nil {
		return "", fmt.Errorf("read old memory %q for supersede: %w", oldID, err)
	}

	newItem = normalizeMemoryItem(newItem, time.Now().UTC())
	newItem.Supersedes = oldID

	oldParsed.Item.Status = "replaced"
	oldParsed.Item.SupersededBy = newItem.ID
	oldParsed.Item.UpdatedAt = time.Now().UTC().Format(time.RFC3339Nano)
	if err := writeFileAtomic(oldPath, []byte(formatMemoryMarkdown(oldParsed.Item)), 0o644); err != nil {
		return "", fmt.Errorf("update superseded memory %q: %w", oldID, err)
	}

	newPath := s.memoryPath(newItem.ID)
	if err := os.MkdirAll(filepath.Dir(newPath), 0o755); err != nil {
		return "", fmt.Errorf("create memory directory: %w", err)
	}
	if err := writeFileAtomic(newPath, []byte(formatMemoryMarkdown(newItem)), 0o644); err != nil {
		return "", fmt.Errorf("write superseding memory %q: %w", newItem.ID, err)
	}
	if err := s.RebuildIndex(ctx); err != nil {
		return "", err
	}
	return newItem.ID, nil
}

func (s *LongTermMemoryStore) MarkConflict(ctx context.Context, aID string, bID string, reason string) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}
	for _, id := range []string{aID, bID} {
		path := s.memoryPath(id)
		parsed, err := readMemoryMarkdown(path)
		if err != nil {
			continue
		}
		if parsed.Item.Status == "active" {
			parsed.Item.Status = "conflicted"
			parsed.Item.UpdatedAt = time.Now().UTC().Format(time.RFC3339Nano)
			_ = writeFileAtomic(path, []byte(formatMemoryMarkdown(parsed.Item)), 0o644)
		}
	}
	return s.RebuildIndex(ctx)
}

func (s *LongTermMemoryStore) DecideAction(ctx context.Context, candidate MemoryItem) (string, string, error) {
	results, err := s.Search(ctx, MemoryQuery{
		Tags:     candidate.Tags,
		Entities: candidate.Entities,
		Types:    []string{candidate.Type},
		Limit:    10,
	})
	if err != nil {
		return "add", "", err
	}
	for _, existing := range results {
		if hasOverlappingSourceEvents(candidate, existing) {
			return "ignore", existing.ID, nil
		}
		candidateContent := strings.TrimSpace(candidate.Content)
		existingContent := strings.TrimSpace(existing.Content)
		if candidateContent == existingContent {
			return "ignore", existing.ID, nil
		}
		if strings.Contains(existingContent, candidateContent) {
			return "ignore", existing.ID, nil
		}
		if strings.Contains(candidateContent, existingContent) {
			return "supersede", existing.ID, nil
		}
		if candidate.Type == existing.Type && hasOverlappingEntities(candidate.Entities, existing.Entities) && hasOverlappingTags(candidate.Tags, existing.Tags) {
			return "supersede", existing.ID, nil
		}
	}
	return "add", "", nil
}

func hasOverlappingSourceEvents(candidate MemoryItem, existing MemoryResult) bool {
	if len(candidate.SourceRefs) == 0 {
		return false
	}
	existingPath := existing.FilePath
	if existingPath == "" {
		return false
	}
	parsed, err := readMemoryMarkdown(existingPath)
	if err != nil {
		return false
	}
	for _, cRef := range candidate.SourceRefs {
		for _, eRef := range parsed.Item.SourceRefs {
			if cRef.ID == eRef.ID {
				for _, cEvt := range cRef.EventIDs {
					for _, eEvt := range eRef.EventIDs {
						if cEvt == eEvt {
							return true
						}
					}
				}
			}
		}
	}
	return false
}

func hasOverlappingEntities(a []string, b []string) bool {
	if len(a) == 0 || len(b) == 0 {
		return false
	}
	for _, ae := range a {
		for _, be := range b {
			if strings.EqualFold(ae, be) {
				return true
			}
		}
	}
	return false
}

func hasOverlappingTags(a []string, b []string) bool {
	if len(a) == 0 || len(b) == 0 {
		return false
	}
	for _, at := range a {
		for _, bt := range b {
			if strings.EqualFold(at, bt) {
				return true
			}
		}
	}
	return false
}

func (s *LongTermMemoryStore) RegenerateProfileMD(ctx context.Context) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}
	index, err := s.loadIndex(ctx)
	if err != nil {
		return err
	}
	var entries []ProfileEntry
	for _, entry := range index.Memories {
		if entry.Status != "active" {
			continue
		}
		if !isProfileRelevantType(entry.Type) {
			continue
		}
		parsed, err := readMemoryMarkdown(filepath.Join(s.rootDir, entry.File))
		if err != nil {
			continue
		}
		entries = append(entries, ProfileEntry{Type: entry.Type, Content: strings.TrimSpace(parsed.Content)})
	}
	if len(entries) == 0 {
		return nil
	}

	var profileContent string
	if s.profileFn != nil {
		profileContent = s.profileFn(ctx, entries)
	}
	if profileContent == "" {
		profileContent = fallbackProfile(entries)
	}

	return writeFileAtomic(filepath.Join(s.rootDir, "profile.md"), []byte(profileContent), 0o644)
}

func isProfileRelevantType(t string) bool {
	switch t {
	case "profile", "rule", "preference":
		return true
	}
	return false
}

func fallbackProfile(entries []ProfileEntry) string {
	var b strings.Builder
	b.WriteString("# User Profile\n\n")
	b.WriteString(fmt.Sprintf("updated_at: %s\n\n", time.Now().UTC().Format(time.RFC3339)))
	for _, e := range entries {
		b.WriteString(fmt.Sprintf("## [%s]\n\n%s\n\n", e.Type, e.Content))
	}
	return b.String()
}

func (s *LongTermMemoryStore) RebuildIndex(ctx context.Context) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}
	entries, err := os.ReadDir(s.memoriesDir())
	if err != nil {
		if os.IsNotExist(err) {
			return s.writeIndex(memoryIndex{Version: 1, UpdatedAt: time.Now().UTC().Format(time.RFC3339Nano)})
		}
		return fmt.Errorf("read memories directory: %w", err)
	}
	index := memoryIndex{
		Version:   1,
		UpdatedAt: time.Now().UTC().Format(time.RFC3339Nano),
		Memories:  make([]memoryIndexEntry, 0, len(entries)),
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".md") {
			continue
		}
		path := filepath.Join(s.memoriesDir(), entry.Name())
		parsed, err := readMemoryMarkdown(path)
		if err != nil {
			return err
		}
		index.Memories = append(index.Memories, memoryIndexEntry{
			ID:         parsed.Item.ID,
			File:       filepath.ToSlash(filepath.Join("memories", entry.Name())),
			Type:       parsed.Item.Type,
			Status:     parsed.Item.Status,
			Priority:   parsed.Item.Priority,
			Confidence: parsed.Item.Confidence,
			Tags:       append([]string(nil), parsed.Item.Tags...),
			Entities:   append([]string(nil), parsed.Item.Entities...),
			Summary:    firstSentence(parsed.Content),
		})
	}
	sort.SliceStable(index.Memories, func(i, j int) bool {
		if index.Memories[i].Priority == index.Memories[j].Priority {
			return index.Memories[i].ID < index.Memories[j].ID
		}
		return index.Memories[i].Priority > index.Memories[j].Priority
	})
	return s.writeIndex(index)
}

func (s *LongTermMemoryStore) memoriesDir() string {
	return filepath.Join(s.rootDir, "memories")
}

func (s *LongTermMemoryStore) indexPath() string {
	return filepath.Join(s.rootDir, "index.yaml")
}

func (s *LongTermMemoryStore) memoryPath(id string) string {
	return filepath.Join(s.memoriesDir(), safeMemoryFileName(id))
}

func (s *LongTermMemoryStore) loadIndex(ctx context.Context) (memoryIndex, error) {
	data, err := os.ReadFile(s.indexPath())
	if err != nil {
		if !os.IsNotExist(err) {
			return memoryIndex{}, fmt.Errorf("read memory index: %w", err)
		}
		if err := s.RebuildIndex(ctx); err != nil {
			return memoryIndex{}, err
		}
		data, err = os.ReadFile(s.indexPath())
		if err != nil {
			return memoryIndex{}, fmt.Errorf("read rebuilt memory index: %w", err)
		}
	}
	var index memoryIndex
	if err := yaml.Unmarshal(data, &index); err != nil {
		return memoryIndex{}, fmt.Errorf("decode memory index: %w", err)
	}
	return index, nil
}

func (s *LongTermMemoryStore) writeIndex(index memoryIndex) error {
	data, err := yaml.Marshal(index)
	if err != nil {
		return fmt.Errorf("encode memory index: %w", err)
	}
	if err := os.MkdirAll(s.rootDir, 0o755); err != nil {
		return fmt.Errorf("create long-term memory directory: %w", err)
	}
	return writeFileAtomic(s.indexPath(), data, 0o644)
}

func (s *LongTermMemoryStore) appendTombstone(id string, reason string) error {
	if err := os.MkdirAll(s.lifecycleDir, 0o755); err != nil {
		return fmt.Errorf("create lifecycle directory: %w", err)
	}
	path := filepath.Join(s.lifecycleDir, "tombstones.jsonl")
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return fmt.Errorf("open tombstones: %w", err)
	}
	defer file.Close()

	record := map[string]any{
		"id":     "tomb_" + strconvTimeID(time.Now().UTC()),
		"ts":     time.Now().UTC().Format(time.RFC3339Nano),
		"reason": reason,
		"ref": map[string]string{
			"type": "memory",
			"id":   id,
		},
	}
	if err := json.NewEncoder(file).Encode(record); err != nil {
		return fmt.Errorf("append tombstone: %w", err)
	}
	return nil
}

type parsedMemoryMarkdown struct {
	Item    MemoryItem
	Title   string
	Content string
}

func normalizeMemoryItem(item MemoryItem, now time.Time) MemoryItem {
	if item.ID == "" {
		item.ID = "mem_" + strconvTimeID(now)
	}
	if item.Type == "" {
		item.Type = "fact"
	}
	if item.Status == "" {
		item.Status = "active"
	}
	if item.TimeScope == "" {
		item.TimeScope = "long_term"
	}
	if item.CreatedAt == "" {
		item.CreatedAt = now.Format(time.RFC3339Nano)
	}
	item.UpdatedAt = now.Format(time.RFC3339Nano)
	if item.Traceability == "" {
		item.Traceability = "full"
	}
	if item.Title == "" {
		item.Title = item.ID
	}
	return item
}

func formatMemoryMarkdown(item MemoryItem) string {
	frontmatter := item
	data, _ := yaml.Marshal(frontmatter)
	var builder strings.Builder
	builder.WriteString("---\n")
	builder.Write(data)
	builder.WriteString("---\n\n")
	builder.WriteString("# ")
	builder.WriteString(item.Title)
	builder.WriteString("\n\n## 内容\n\n")
	builder.WriteString(strings.TrimSpace(item.Content))
	builder.WriteString("\n\n## 证据摘录\n\n")
	for _, excerpt := range item.EvidenceExcerpts {
		builder.WriteString("- ")
		builder.WriteString(strings.TrimSpace(excerpt))
		builder.WriteString("\n")
	}
	return builder.String()
}

func readMemoryMarkdown(path string) (parsedMemoryMarkdown, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return parsedMemoryMarkdown{}, fmt.Errorf("read memory markdown %q: %w", path, err)
	}
	parts := bytes.SplitN(data, []byte("---\n"), 3)
	if len(parts) != 3 || len(parts[0]) != 0 {
		return parsedMemoryMarkdown{}, fmt.Errorf("memory markdown %q missing frontmatter", path)
	}
	var item MemoryItem
	if err := yaml.Unmarshal(parts[1], &item); err != nil {
		return parsedMemoryMarkdown{}, fmt.Errorf("decode memory frontmatter %q: %w", path, err)
	}
	title, content, evidence := parseMemoryBody(string(parts[2]))
	item.Title = title
	item.Content = content
	item.EvidenceExcerpts = evidence
	return parsedMemoryMarkdown{Item: item, Title: title, Content: content}, nil
}

func parseMemoryBody(body string) (string, string, []string) {
	scanner := bufio.NewScanner(strings.NewReader(body))
	title := ""
	var contentLines []string
	var evidence []string
	inContent := false
	inEvidence := false
	for scanner.Scan() {
		line := scanner.Text()
		if title == "" && strings.HasPrefix(line, "# ") {
			title = strings.TrimSpace(strings.TrimPrefix(line, "# "))
			continue
		}
		if strings.TrimSpace(line) == "## 内容" {
			inContent = true
			inEvidence = false
			continue
		}
		if strings.TrimSpace(line) == "## 证据摘录" {
			inContent = false
			inEvidence = true
			continue
		}
		if strings.HasPrefix(strings.TrimSpace(line), "## ") {
			inContent = false
			inEvidence = false
		}
		if inContent {
			contentLines = append(contentLines, line)
		}
		if inEvidence {
			trimmed := strings.TrimSpace(line)
			if strings.HasPrefix(trimmed, "- ") {
				evidence = append(evidence, strings.TrimSpace(strings.TrimPrefix(trimmed, "- ")))
			}
		}
	}
	return title, strings.TrimSpace(strings.Join(contentLines, "\n")), evidence
}

func matchesAny(queryValues []string, candidateValues []string) bool {
	if len(queryValues) == 0 {
		return true
	}
	for _, queryValue := range queryValues {
		for _, candidateValue := range candidateValues {
			if strings.EqualFold(queryValue, candidateValue) {
				return true
			}
		}
	}
	return false
}

func firstSentence(content string) string {
	content = strings.TrimSpace(content)
	if content == "" {
		return ""
	}
	for _, sep := range []string{"。", ".", "\n"} {
		if idx := strings.Index(content, sep); idx >= 0 {
			return strings.TrimSpace(content[:idx+len(sep)])
		}
	}
	return content
}

func safeMemoryFileName(id string) string {
	id = strings.TrimSpace(id)
	if id == "" {
		id = "memory"
	}
	safe := strings.Map(func(r rune) rune {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' {
			return r
		}
		switch r {
		case '-', '_', '.':
			return r
		default:
			return '_'
		}
	}, id)
	return safe + ".md"
}

func strconvTimeID(ts time.Time) string {
	var buf [4]byte
	_, _ = rand.Read(buf[:])
	return fmt.Sprintf("%d_%s", ts.UnixNano(), hex.EncodeToString(buf[:]))
}

func writeFileAtomic(path string, data []byte, perm os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmpPath := path + ".tmp"
	if err := os.WriteFile(tmpPath, data, perm); err != nil {
		return err
	}
	return os.Rename(tmpPath, path)
}
