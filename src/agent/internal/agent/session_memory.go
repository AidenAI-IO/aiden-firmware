package agent

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"gopkg.in/yaml.v3"
)

type SessionMemoryStore struct {
	mu               sync.Mutex
	rootDir          string
	summaryMaxChunks int
}

type CompressOption struct {
	ChunkID    string
	Summary    string
	Structured ChunkStructuredSummary
	Tags       []string
	Entities   []string
	RiskTypes  []string
	// CutMeta carries optional token-based cut-point diagnostics. It does not
	// affect recall or summary rendering; it is persisted on the chunk index
	// entry for debugging and benchmarking the compaction boundary.
	CutMeta ChunkCutMetadata
}

// ChunkCutMetadata records how a token-based cut point produced this chunk.
// All fields are optional and omitted from YAML when zero so older indexes and
// count-based compactions remain byte-compatible.
type ChunkCutMetadata struct {
	FirstKeptEventID   string `json:"first_kept_event_id,omitempty" yaml:"first_kept_event_id,omitempty"`
	TokensBefore       int    `json:"tokens_before,omitempty" yaml:"tokens_before,omitempty"`
	KeptTokensEstimate int    `json:"kept_tokens_estimate,omitempty" yaml:"kept_tokens_estimate,omitempty"`
	IsSplitTurn        bool   `json:"is_split_turn,omitempty" yaml:"is_split_turn,omitempty"`
	TurnStartEventID   string `json:"turn_start_event_id,omitempty" yaml:"turn_start_event_id,omitempty"`
}

// Empty reports whether no cut metadata was recorded.
func (c ChunkCutMetadata) Empty() bool {
	return c.FirstKeptEventID == "" &&
		c.TokensBefore == 0 &&
		c.KeptTokensEstimate == 0 &&
		!c.IsSplitTurn &&
		c.TurnStartEventID == ""
}

type ChunkStructuredSummary struct {
	Summary          string   `json:"summary,omitempty" yaml:"summary,omitempty"`
	UserGoals        []string `json:"user_goals,omitempty" yaml:"user_goals,omitempty"`
	ConfirmedFacts   []string `json:"confirmed_facts,omitempty" yaml:"confirmed_facts,omitempty"`
	Decisions        []string `json:"decisions,omitempty" yaml:"decisions,omitempty"`
	Proposals        []string `json:"proposals,omitempty" yaml:"proposals,omitempty"`
	OpenTasks        []string `json:"open_tasks,omitempty" yaml:"open_tasks,omitempty"`
	RisksOrPitfalls  []string `json:"risks_or_pitfalls,omitempty" yaml:"risks_or_pitfalls,omitempty"`
	MemoryCandidates []string `json:"memory_candidates,omitempty" yaml:"memory_candidates,omitempty"`
}

func (s ChunkStructuredSummary) Empty() bool {
	return strings.TrimSpace(s.Summary) == "" &&
		len(s.UserGoals) == 0 &&
		len(s.ConfirmedFacts) == 0 &&
		len(s.Decisions) == 0 &&
		len(s.Proposals) == 0 &&
		len(s.OpenTasks) == 0 &&
		len(s.RisksOrPitfalls) == 0 &&
		len(s.MemoryCandidates) == 0
}

type ChunkSummary struct {
	ID         string                  `json:"id"`
	Summary    string                  `json:"summary"`
	Structured *ChunkStructuredSummary `json:"structured,omitempty"`
	Tags       []string                `json:"tags,omitempty"`
	Entities   []string                `json:"entities,omitempty"`
	RiskTypes  []string                `json:"risk_types,omitempty"`
	EventCount int                     `json:"event_count"`
	Checksum   string                  `json:"checksum"`
}

type ChunkRecallQuery struct {
	ChunkIDs []string `json:"chunk_ids,omitempty"`
	Tags     []string `json:"tags,omitempty"`
	Entities []string `json:"entities,omitempty"`
	AppName  string   `json:"app_name,omitempty"`
	Limit    int      `json:"limit,omitempty"`
}

type ChunkRecallResult struct {
	ChunkID    string                  `json:"chunk_id"`
	Summary    string                  `json:"summary"`
	Structured *ChunkStructuredSummary `json:"structured,omitempty"`
	Evidence   []SessionEvent          `json:"evidence"`
}

type chunkIndex struct {
	Version   int               `yaml:"version"`
	UpdatedAt string            `yaml:"updated_at"`
	Chunks    []chunkIndexEntry `yaml:"chunks"`
}

type chunkIndexEntry struct {
	ID         string                  `yaml:"id"`
	File       string                  `yaml:"file"`
	Status     string                  `yaml:"status"`
	Summary    string                  `yaml:"summary"`
	Structured *ChunkStructuredSummary `yaml:"structured,omitempty"`
	AppName    string                  `yaml:"app_name,omitempty"`
	Tags       []string                `yaml:"tags,omitempty"`
	Entities   []string                `yaml:"entities,omitempty"`
	RiskTypes  []string                `yaml:"risk_types,omitempty"`
	EventCount int                     `yaml:"event_count"`
	Checksum   string                  `yaml:"checksum"`
	CutMeta    *ChunkCutMetadata       `yaml:"cut_meta,omitempty"`
}

const defaultSummaryMaxChunks = 10

func NewSessionMemoryStore(rootDir string, summaryMaxChunks ...int) *SessionMemoryStore {
	maxChunks := defaultSummaryMaxChunks
	if len(summaryMaxChunks) > 0 && summaryMaxChunks[0] > 0 {
		maxChunks = summaryMaxChunks[0]
	}
	return &SessionMemoryStore{rootDir: rootDir, summaryMaxChunks: maxChunks}
}

func (s *SessionMemoryStore) AppendEvent(ctx context.Context, event SessionEvent) (string, error) {
	select {
	case <-ctx.Done():
		return "", ctx.Err()
	default:
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now().UTC()
	if event.EventID == "" {
		event.EventID = "evt_" + strconvTimeID(now)
	}
	if event.Ts == "" {
		event.Ts = now.Format(time.RFC3339Nano)
	}
	if event.Type == "" {
		event.Type = "system_event"
	}
	if event.Role == "" {
		event.Role = "system"
	}
	if err := os.MkdirAll(s.rootDir, 0o755); err != nil {
		return "", fmt.Errorf("create session directory: %w", err)
	}
	file, err := os.OpenFile(s.eventsPath(), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return "", fmt.Errorf("open session events: %w", err)
	}
	defer file.Close()
	if err := json.NewEncoder(file).Encode(event); err != nil {
		return "", fmt.Errorf("append session event: %w", err)
	}
	return event.EventID, nil
}

func (s *SessionMemoryStore) Compress(ctx context.Context, opt CompressOption) (ChunkSummary, error) {
	select {
	case <-ctx.Done():
		return ChunkSummary{}, ctx.Err()
	default:
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	events, err := s.readEvents(s.eventsPath())
	if err != nil {
		return ChunkSummary{}, err
	}
	return s.compressEvents(ctx, events, opt)
}

func (s *SessionMemoryStore) compressEvents(ctx context.Context, events []SessionEvent, opt CompressOption) (ChunkSummary, error) {
	select {
	case <-ctx.Done():
		return ChunkSummary{}, ctx.Err()
	default:
	}
	if len(events) == 0 {
		return ChunkSummary{}, fmt.Errorf("no session events to compress")
	}
	if opt.ChunkID == "" {
		opt.ChunkID = "chunk_" + strconvTimeID(time.Now().UTC())
	}
	chunkPath := filepath.Join(s.chunksDir(), opt.ChunkID+".jsonl")
	data, checksum, err := encodeSessionEventsJSONL(events)
	if err != nil {
		return ChunkSummary{}, err
	}
	if err := writeFileAtomic(chunkPath, data, 0o644); err != nil {
		return ChunkSummary{}, fmt.Errorf("write session chunk: %w", err)
	}

	entry := chunkIndexEntry{
		ID:         opt.ChunkID,
		File:       opt.ChunkID + ".jsonl",
		Status:     "active",
		Summary:    opt.Summary,
		Structured: structuredOrNil(opt.Structured),
		AppName:    firstNonEmptyAppName(events),
		Tags:       append([]string(nil), opt.Tags...),
		Entities:   append([]string(nil), opt.Entities...),
		RiskTypes:  append([]string(nil), opt.RiskTypes...),
		EventCount: len(events),
		Checksum:   checksum,
		CutMeta:    cutMetaOrNil(opt.CutMeta),
	}
	index, err := s.loadChunkIndex()
	if err != nil {
		return ChunkSummary{}, err
	}
	index = upsertChunkIndexEntry(index, entry)
	if err := s.writeChunkIndex(index); err != nil {
		return ChunkSummary{}, err
	}
	existingSummary, _ := os.ReadFile(s.summaryPath())
	existingArchive, _ := os.ReadFile(s.summaryArchivePath())
	summaryContent, archiveContent := formatSessionSummaryWithWindow(existingSummary, existingArchive, entry, s.summaryMaxChunks)
	if err := writeFileAtomic(s.summaryPath(), []byte(summaryContent), 0o644); err != nil {
		return ChunkSummary{}, fmt.Errorf("write session summary: %w", err)
	}
	if archiveContent != "" {
		if err := writeFileAtomic(s.summaryArchivePath(), []byte(archiveContent), 0o644); err != nil {
			return ChunkSummary{}, fmt.Errorf("write session summary archive: %w", err)
		}
	}
	return ChunkSummary{
		ID:         entry.ID,
		Summary:    entry.Summary,
		Structured: entry.Structured,
		Tags:       append([]string(nil), entry.Tags...),
		Entities:   append([]string(nil), entry.Entities...),
		RiskTypes:  append([]string(nil), entry.RiskTypes...),
		EventCount: entry.EventCount,
		Checksum:   entry.Checksum,
	}, nil
}

func (s *SessionMemoryStore) replaceEvents(events []SessionEvent) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(events) == 0 {
		if err := os.Remove(s.eventsPath()); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("remove session events: %w", err)
		}
		return nil
	}
	data, _, err := encodeSessionEventsJSONL(events)
	if err != nil {
		return err
	}
	if err := writeFileAtomic(s.eventsPath(), data, 0o644); err != nil {
		return fmt.Errorf("replace session events: %w", err)
	}
	return nil
}

func (s *SessionMemoryStore) RecallChunks(ctx context.Context, query ChunkRecallQuery) ([]ChunkRecallResult, error) {
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	default:
	}
	index, err := s.loadChunkIndex()
	if err != nil {
		return nil, err
	}
	limit := query.Limit
	if limit <= 0 {
		limit = 3
	}

	if len(query.ChunkIDs) > 0 {
		idSet := make(map[string]bool, len(query.ChunkIDs))
		for _, id := range query.ChunkIDs {
			idSet[id] = true
		}
		var results []ChunkRecallResult
		for _, entry := range index.Chunks {
			if !idSet[entry.ID] {
				continue
			}
			events, err := s.readEvents(filepath.Join(s.chunksDir(), entry.File))
			if err != nil {
				return nil, err
			}
			results = append(results, ChunkRecallResult{ChunkID: entry.ID, Summary: entry.Summary, Structured: entry.Structured, Evidence: events})
		}
		return results, nil
	}

	entries := make([]chunkIndexEntry, 0)
	allActive := make([]chunkIndexEntry, 0)
	hasFilter := query.AppName != "" || len(query.Tags) > 0 || len(query.Entities) > 0
	for _, entry := range index.Chunks {
		if entry.Status != "active" {
			continue
		}
		allActive = append(allActive, entry)
		if query.AppName != "" && entry.AppName != query.AppName {
			continue
		}
		if hasFilter && len(query.Tags) > 0 && !matchesAny(query.Tags, entry.Tags) {
			continue
		}
		if hasFilter && len(query.Entities) > 0 && !matchesAny(query.Entities, entry.Entities) {
			continue
		}
		entries = append(entries, entry)
	}
	if len(entries) == 0 && hasFilter {
		entries = allActive
	}
	sort.SliceStable(entries, func(i, j int) bool {
		return entries[i].ID > entries[j].ID
	})
	results := make([]ChunkRecallResult, 0, minInt(limit, len(entries)))
	for _, entry := range entries {
		if len(results) >= limit {
			break
		}
		events, err := s.readEvents(filepath.Join(s.chunksDir(), entry.File))
		if err != nil {
			return nil, err
		}
		results = append(results, ChunkRecallResult{ChunkID: entry.ID, Summary: entry.Summary, Structured: entry.Structured, Evidence: events})
	}
	return results, nil
}

func (s *SessionMemoryStore) eventsPath() string {
	return filepath.Join(s.rootDir, "events.jsonl")
}

func (s *SessionMemoryStore) summaryPath() string {
	return filepath.Join(s.rootDir, "summary.md")
}

func (s *SessionMemoryStore) summaryArchivePath() string {
	return filepath.Join(s.rootDir, "summary_archive.md")
}

func (s *SessionMemoryStore) chunksDir() string {
	return filepath.Join(s.rootDir, "chunks")
}

func (s *SessionMemoryStore) chunkIndexPath() string {
	return filepath.Join(s.chunksDir(), "index.yaml")
}

func (s *SessionMemoryStore) readEvents(path string) ([]SessionEvent, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("read session events %q: %w", path, err)
	}
	defer file.Close()
	var events []SessionEvent
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 0), 1<<20)
	for scanner.Scan() {
		line := bytes.Trim(scanner.Bytes(), "\x00 \t\r\n")
		if len(line) == 0 {
			continue
		}
		var event SessionEvent
		if err := json.Unmarshal(line, &event); err != nil {
			if isTruncatedJSONLineError(err) && filepath.Clean(path) == filepath.Clean(s.eventsPath()) {
				break
			}
			return nil, fmt.Errorf("decode session event %q: %w", path, err)
		}
		events = append(events, event)
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("scan session events %q: %w", path, err)
	}
	return events, nil
}

func (s *SessionMemoryStore) loadChunkIndex() (chunkIndex, error) {
	data, err := os.ReadFile(s.chunkIndexPath())
	if err != nil {
		if os.IsNotExist(err) {
			return chunkIndex{Version: 1, UpdatedAt: time.Now().UTC().Format(time.RFC3339Nano)}, nil
		}
		return chunkIndex{}, fmt.Errorf("read chunk index: %w", err)
	}
	var index chunkIndex
	if err := yaml.Unmarshal(data, &index); err != nil {
		return chunkIndex{}, fmt.Errorf("decode chunk index: %w", err)
	}
	return index, nil
}

func (s *SessionMemoryStore) writeChunkIndex(index chunkIndex) error {
	index.Version = 1
	index.UpdatedAt = time.Now().UTC().Format(time.RFC3339Nano)
	data, err := yaml.Marshal(index)
	if err != nil {
		return fmt.Errorf("encode chunk index: %w", err)
	}
	return writeFileAtomic(s.chunkIndexPath(), data, 0o644)
}

func upsertChunkIndexEntry(index chunkIndex, entry chunkIndexEntry) chunkIndex {
	for i := range index.Chunks {
		if index.Chunks[i].ID == entry.ID {
			index.Chunks[i] = entry
			return index
		}
	}
	index.Chunks = append(index.Chunks, entry)
	return index
}

func encodeSessionEventsJSONL(events []SessionEvent) ([]byte, string, error) {
	var builder strings.Builder
	encoder := json.NewEncoder(&builder)
	for _, event := range events {
		if err := encoder.Encode(event); err != nil {
			return nil, "", fmt.Errorf("encode session events: %w", err)
		}
	}
	data := []byte(builder.String())
	sum := sha256.Sum256(data)
	return data, "sha256:" + hex.EncodeToString(sum[:]), nil
}

func firstNonEmptyAppName(events []SessionEvent) string {
	for _, event := range events {
		if event.AppName != "" {
			return event.AppName
		}
	}
	return ""
}

func formatSessionSummary(existingContent []byte, newChunk chunkIndexEntry) string {
	summary, _ := formatSessionSummaryWithWindow(existingContent, nil, newChunk, 0)
	return summary
}

type chunkLine struct {
	ID      string
	Summary string
}

func parseChunkLines(content []byte) []chunkLine {
	if len(content) == 0 {
		return nil
	}
	var chunks []chunkLine
	lines := strings.Split(string(content), "\n")
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if !strings.HasPrefix(trimmed, "- **") {
			continue
		}
		id := strings.TrimPrefix(trimmed, "- **")
		if idx := strings.Index(id, "**"); idx > 0 {
			id = id[:idx]
		}
		summary := ""
		if i+1 < len(lines) {
			summary = strings.TrimSpace(lines[i+1])
		}
		chunks = append(chunks, chunkLine{ID: id, Summary: summary})
	}
	return chunks
}

func renderSummaryMD(chunks []chunkLine) string {
	var b strings.Builder
	b.WriteString("# Session History (compressed chunks)\n\n")
	b.WriteString("Use recall_session_chunks with a chunk_id to retrieve full conversation details.\n\n")
	for _, c := range chunks {
		b.WriteString(fmt.Sprintf("- **%s**\n  %s\n", c.ID, c.Summary))
	}
	return b.String()
}

func renderArchiveMD(chunks []chunkLine) string {
	var b strings.Builder
	b.WriteString("# Session History Archive\n\n")
	b.WriteString("Older chunk summaries moved out of the active session summary.\n")
	b.WriteString("Use recall_session_chunks with a chunk_id to retrieve full conversation details.\n\n")
	for _, c := range chunks {
		b.WriteString(fmt.Sprintf("- **%s**\n  %s\n", c.ID, c.Summary))
	}
	return b.String()
}

func formatSessionSummaryWithWindow(existingSummary []byte, existingArchive []byte, newChunk chunkIndexEntry, maxChunks int) (summaryContent string, archiveContent string) {
	chunks := parseChunkLines(existingSummary)
	chunks = append(chunks, chunkLine{ID: newChunk.ID, Summary: strings.TrimSpace(newChunk.Summary)})

	if maxChunks <= 0 || len(chunks) <= maxChunks {
		return renderSummaryMD(chunks), ""
	}

	overflow := chunks[:len(chunks)-maxChunks]
	keep := chunks[len(chunks)-maxChunks:]

	archived := parseChunkLines(existingArchive)
	archived = append(archived, overflow...)

	return renderSummaryMD(keep), renderArchiveMD(archived)
}

func structuredOrNil(s ChunkStructuredSummary) *ChunkStructuredSummary {
	if s.Empty() {
		return nil
	}
	return &s
}

func cutMetaOrNil(c ChunkCutMetadata) *ChunkCutMetadata {
	if c.Empty() {
		return nil
	}
	return &c
}

func minInt(a int, b int) int {
	if a < b {
		return a
	}
	return b
}
