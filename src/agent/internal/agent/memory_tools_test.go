package agent

import (
	"aiden-agent/internal/agent/messages"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"gopkg.in/yaml.v3"
)

// writeTestChunk persists one conversation chunk for sessionID the same way
// compaction does, so recall tests exercise the real on-disk layout.
func writeTestChunk(t *testing.T, sessionFolder, sessionID string, msgs []messages.Message, summary string, tags, entities []string) {
	t.Helper()
	writer := NewSessionChunkWriter(sessionFolder)
	if err := writer.WriteChunk(context.Background(), sessionID, msgs, summary); err != nil {
		t.Fatalf("WriteChunk() error = %v", err)
	}
	if len(tags) == 0 && len(entities) == 0 {
		return
	}
	// Tags and entities are searchable index fields; set them on the entry the
	// writer just appended.
	indexPath := filepath.Join(sessionFolder, sessionID, "chunks", "index.yaml")
	index, err := loadChunkIndexFromPath(indexPath)
	if err != nil {
		t.Fatalf("loadChunkIndexFromPath() error = %v", err)
	}
	if len(index.Chunks) == 0 {
		t.Fatal("expected the written chunk in the index")
	}
	index.Chunks[len(index.Chunks)-1].Tags = tags
	index.Chunks[len(index.Chunks)-1].Entities = entities
	data, err := yaml.Marshal(index)
	if err != nil {
		t.Fatalf("marshal chunk index: %v", err)
	}
	if err := os.WriteFile(indexPath, data, 0o644); err != nil {
		t.Fatalf("write chunk index: %v", err)
	}
}

// Compaction hands WriteChunk only the messages and a plain-text summary, so
// the writer must derive the tags and entities recall filters on. Without this,
// a compacted span would be reachable only by chunk_id.
func TestSessionChunkWriterDerivesSearchTermsFromContent(t *testing.T) {
	ctx := context.Background()
	sessionFolder := t.TempDir()
	extraction := DefaultMemoryExtractionConfig()

	writer := NewSessionChunkWriter(sessionFolder, extraction)
	if err := writer.WriteChunk(ctx, "s_expense", []messages.Message{
		{Role: messages.MessageRoleUser, Content: "帮我在蓝海报销App提交这笔报销"},
		{Role: messages.MessageRoleAssistant, Content: "已提交，需要你确认金额"},
	}, "报销提交流程"); err != nil {
		t.Fatalf("WriteChunk() error = %v", err)
	}

	index, err := loadChunkIndexFromPath(filepath.Join(sessionFolder, "s_expense", "chunks", "index.yaml"))
	if err != nil {
		t.Fatalf("loadChunkIndexFromPath() error = %v", err)
	}
	if len(index.Chunks) != 1 {
		t.Fatalf("expected 1 indexed chunk, got %d", len(index.Chunks))
	}
	entry := index.Chunks[0]
	if len(entry.Tags) == 0 {
		t.Fatalf("expected derived tags on the chunk entry, got %#v", entry)
	}
	if !slices.Contains(entry.Tags, "报销") || !slices.Contains(entry.Tags, "提交") {
		t.Fatalf("tags = %#v, want the configured candidates present in the text", entry.Tags)
	}
	if !slices.Contains(entry.Entities, "蓝海报销App") {
		t.Fatalf("entities = %#v, want 蓝海报销App", entry.Entities)
	}

	// The derived terms must actually drive recall.
	tool := NewRecallSessionChunksTool(NewMultiSessionChunkStore(sessionFolder))
	out, err := tool.Call(ctx, `{"tags":["报销"],"limit":5}`)
	if err != nil {
		t.Fatalf("Call() error = %v", err)
	}
	var decoded struct {
		Results []ChunkRecallResult `json:"results"`
	}
	if err := json.Unmarshal([]byte(out), &decoded); err != nil {
		t.Fatalf("decode output %q: %v", out, err)
	}
	if len(decoded.Results) != 1 {
		t.Fatalf("expected the chunk to be recallable by a derived tag, got %#v", decoded.Results)
	}
}

// Compaction builds a fresh writer per run, so serialization has to key on the
// index rather than the writer instance. Separate writers appending to one
// session must not drop each other's entries.
func TestSessionChunkWriterSerializesConcurrentWritersOnSameIndex(t *testing.T) {
	ctx := context.Background()
	sessionFolder := t.TempDir()
	const writers = 8

	var wg sync.WaitGroup
	errs := make([]error, writers)
	for i := range writers {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			// A distinct writer per goroutine, matching how each compaction
			// constructs its own.
			errs[i] = NewSessionChunkWriter(sessionFolder).WriteChunk(ctx, "s_shared", []messages.Message{
				{Role: messages.MessageRoleUser, Content: fmt.Sprintf("turn %d", i)},
			}, fmt.Sprintf("summary %d", i))
		}(i)
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("WriteChunk() writer %d error = %v", i, err)
		}
	}

	index, err := loadChunkIndexFromPath(filepath.Join(sessionFolder, "s_shared", "chunks", "index.yaml"))
	if err != nil {
		t.Fatalf("loadChunkIndexFromPath() error = %v", err)
	}
	if len(index.Chunks) != writers {
		t.Fatalf("index has %d chunks, want %d: concurrent writers dropped entries", len(index.Chunks), writers)
	}
	ids := make(map[string]bool, writers)
	for _, chunk := range index.Chunks {
		if ids[chunk.ID] {
			t.Fatalf("duplicate chunk id %q in index", chunk.ID)
		}
		ids[chunk.ID] = true
		if _, err := os.Stat(filepath.Join(sessionFolder, "s_shared", "chunks", chunk.File)); err != nil {
			t.Fatalf("indexed chunk %q has no file: %v", chunk.ID, err)
		}
	}
}

// If the context is canceled after the chunk file is written but before the
// index is updated, the chunk file must be removed: recall expects every .jsonl
// to have a matching index entry.
func TestSessionChunkWriterCleansUpOrphanedChunkOnContextCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	sessionFolder := t.TempDir()
	writer := NewSessionChunkWriter(sessionFolder)

	// Cancel immediately to force the context check to fail.
	cancel()

	err := writer.WriteChunk(ctx, "s_canceled", []messages.Message{
		{Role: messages.MessageRoleUser, Content: "this should be cleaned up"},
	}, "canceled summary")
	if err == nil || err != context.Canceled {
		t.Fatalf("WriteChunk() error = %v, want context.Canceled", err)
	}

	// The chunks directory may not exist if context was checked before MkdirAll,
	// or it may exist but must contain no .jsonl files if checked after.
	chunksDir := filepath.Join(sessionFolder, "s_canceled", "chunks")
	entries, err := os.ReadDir(chunksDir)
	if err != nil {
		if !os.IsNotExist(err) {
			t.Fatalf("ReadDir(%q) error = %v", chunksDir, err)
		}
		return // directory doesn't exist, so no orphaned file
	}

	for _, entry := range entries {
		if strings.HasSuffix(entry.Name(), ".jsonl") {
			t.Fatalf("found orphaned chunk file %q after context cancel", entry.Name())
		}
	}

	// The index should not exist or should be empty.
	indexPath := filepath.Join(chunksDir, "index.yaml")
	if _, err := os.Stat(indexPath); err == nil {
		index, _ := loadChunkIndexFromPath(indexPath)
		if len(index.Chunks) > 0 {
			t.Fatalf("index has %d chunks after context cancel, want 0", len(index.Chunks))
		}
	}
}

// A corrupt chunk index should be logged and skipped, not silently dropped.
func TestMultiSessionChunkStoreLogsCorruptIndex(t *testing.T) {
	ctx := context.Background()
	sessionFolder := t.TempDir()

	// Write one valid session with chunks.
	writeTestChunk(t, sessionFolder, "s_valid", []messages.Message{
		{Role: messages.MessageRoleUser, Content: "valid chunk"},
	}, "valid summary", []string{"valid"}, nil)

	// Write a corrupt index for another session.
	corruptDir := filepath.Join(sessionFolder, "s_corrupt", "chunks")
	if err := os.MkdirAll(corruptDir, 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(corruptDir, "index.yaml"), []byte("not: [valid: yaml"), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	// Capture log output.
	var logBuf strings.Builder
	oldFlags := log.Flags()
	oldOutput := log.Writer()
	log.SetFlags(0)
	log.SetOutput(&logBuf)
	defer func() {
		log.SetFlags(oldFlags)
		log.SetOutput(oldOutput)
	}()

	store := NewMultiSessionChunkStore(sessionFolder)
	results, err := store.RecallChunks(ctx, ChunkRecallQuery{Tags: []string{"valid"}, Limit: 10})
	if err != nil {
		t.Fatalf("RecallChunks() error = %v", err)
	}

	// Should return the valid session's chunk.
	if len(results) != 1 {
		t.Fatalf("expected 1 result from valid session, got %d", len(results))
	}
	if results[0].SessionID != "s_valid" {
		t.Fatalf("result session_id = %q, want s_valid", results[0].SessionID)
	}

	// Should have logged a warning about the corrupt index.
	logOutput := logBuf.String()
	if !strings.Contains(logOutput, "s_corrupt") || !strings.Contains(logOutput, "failed to load chunk index") {
		t.Fatalf("expected warning about corrupt index for s_corrupt, got log output:\n%s", logOutput)
	}
}

func TestRecallSessionChunksToolReturnsMatchingChunk(t *testing.T) {
	ctx := context.Background()
	sessionFolder := t.TempDir()
	writeTestChunk(t, sessionFolder, "s_login", []messages.Message{
		{Role: messages.MessageRoleUser, Content: "登录失败"},
		{Role: messages.MessageRoleAssistant, Content: "验证码已过期"},
	}, "验证码过期登录场景", []string{"登录", "验证码"}, []string{"某政务App"})

	tool := NewRecallSessionChunksTool(NewMultiSessionChunkStore(sessionFolder))
	out, err := tool.Call(ctx, `{"tags":["验证码"],"limit":1}`)
	if err != nil {
		t.Fatalf("Call() error = %v", err)
	}

	var decoded struct {
		Results []ChunkRecallResult `json:"results"`
	}
	if err := json.Unmarshal([]byte(out), &decoded); err != nil {
		t.Fatalf("decode output %q: %v", out, err)
	}
	if len(decoded.Results) != 1 {
		t.Fatalf("expected 1 result, got %#v", decoded.Results)
	}
	got := decoded.Results[0]
	if !strings.HasPrefix(got.ChunkID, "chunk_") {
		t.Fatalf("chunk_id = %q, want chunk_ prefix", got.ChunkID)
	}
	if got.Summary != "验证码过期登录场景" {
		t.Fatalf("summary = %q, want the persisted summary", got.Summary)
	}
	if got.SessionID != "s_login" {
		t.Fatalf("session_id = %q, want s_login", got.SessionID)
	}
}

// Recall spans every session folder, so history compacted before the current
// session revision stays reachable.
func TestRecallSessionChunksToolSearchesAllSessions(t *testing.T) {
	ctx := context.Background()
	sessionFolder := t.TempDir()
	writeTestChunk(t, sessionFolder, "s_first", []messages.Message{
		{Role: messages.MessageRoleUser, Content: "打开邮件"},
	}, "Mail navigation", []string{"navigation"}, nil)
	writeTestChunk(t, sessionFolder, "s_second", []messages.Message{
		{Role: messages.MessageRoleUser, Content: "打开日历"},
	}, "Calendar navigation", []string{"navigation"}, nil)

	tool := NewRecallSessionChunksTool(NewMultiSessionChunkStore(sessionFolder))
	out, err := tool.Call(ctx, `{"tags":["navigation"],"limit":10}`)
	if err != nil {
		t.Fatalf("Call() error = %v", err)
	}

	var decoded struct {
		Results []ChunkRecallResult `json:"results"`
	}
	if err := json.Unmarshal([]byte(out), &decoded); err != nil {
		t.Fatalf("decode output %q: %v", out, err)
	}
	if len(decoded.Results) != 2 {
		t.Fatalf("expected chunks from both sessions, got %#v", decoded.Results)
	}
	sessions := map[string]bool{}
	for _, result := range decoded.Results {
		sessions[result.SessionID] = true
	}
	if !sessions["s_first"] || !sessions["s_second"] {
		t.Fatalf("expected both sessions represented, got %#v", sessions)
	}
}

func TestRecallMemoryToolReturnsMatchingLongTermMemory(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	store := NewLongTermMemoryStore(filepath.Join(root, "long_term"))
	if _, err := store.AddMemory(ctx, MemoryItem{
		ID:               "mem_login",
		Type:             "preference",
		Priority:         80,
		Confidence:       0.9,
		Tags:             []string{"登录", "验证码"},
		Entities:         []string{"某政务App"},
		Title:            "验证码登录偏好",
		Content:          "用户下次登录某政务 App 时，应先重新获取验证码。",
		EvidenceExcerpts: []string{"用户说：下次先重新获取验证码。"},
	}); err != nil {
		t.Fatalf("AddMemory() error = %v", err)
	}

	tool := NewRecallMemoryTool(store)
	out, err := tool.Call(ctx, `{"tags":["验证码"],"entities":["某政务App"],"limit":3}`)
	if err != nil {
		t.Fatalf("Call() error = %v", err)
	}

	var decoded struct {
		Results []struct {
			ID      string `json:"id"`
			Title   string `json:"title"`
			Content string `json:"content"`
		} `json:"results"`
	}
	if err := json.Unmarshal([]byte(out), &decoded); err != nil {
		t.Fatalf("decode output %q: %v", out, err)
	}
	if len(decoded.Results) != 1 {
		t.Fatalf("expected 1 result, got %#v", decoded.Results)
	}
	if decoded.Results[0].ID != "mem_login" || !strings.Contains(decoded.Results[0].Content, "重新获取验证码") {
		t.Fatalf("unexpected memory recall result: %#v", decoded.Results[0])
	}
}

func TestRecallMemoryToolMergesTemporaryBeforeLongTerm(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	longTerm := NewLongTermMemoryStore(filepath.Join(root, "long_term"))
	temporary := NewLongTermMemoryStore(filepath.Join(root, "temporary"))
	for _, entry := range []struct {
		store *LongTermMemoryStore
		item  MemoryItem
	}{
		{longTerm, MemoryItem{ID: "mem_baseline", Type: "fact", Priority: 90, Confidence: 0.9, Tags: []string{"delivery"}, Title: "通常时效", Content: "通常两天送达。", EvidenceExcerpts: []string{"历史记录"}}},
		{temporary, MemoryItem{ID: "tmp_exception", Type: "fact", Priority: 1, Confidence: 0.9, Tags: []string{"delivery"}, Title: "本次延迟", Content: "本次改为周五送达。", ExpiresAt: time.Now().Add(24 * time.Hour).UTC().Format(time.RFC3339), EvidenceExcerpts: []string{"通知"}}},
	} {
		if _, err := entry.store.AddMemory(ctx, entry.item); err != nil {
			t.Fatal(err)
		}
	}
	tool := NewRecallMemoryToolWithTemporary(longTerm, temporary)
	out, err := tool.Call(ctx, `{"tags":["delivery"],"limit":2}`)
	if err != nil {
		t.Fatal(err)
	}
	var decoded struct {
		Results []MemoryResult `json:"results"`
	}
	if err := json.Unmarshal([]byte(out), &decoded); err != nil {
		t.Fatal(err)
	}
	if len(decoded.Results) != 2 || decoded.Results[0].ID != "tmp_exception" || decoded.Results[0].MemoryScope != "temporary" || decoded.Results[1].MemoryScope != "long_term" {
		t.Fatalf("merged results=%#v", decoded.Results)
	}
}

func TestSaveMemoryToolExcludesScreenSnapshotType(t *testing.T) {
	props := NewSaveMemoryTool(nil).ArgsSchema()["properties"].(map[string]any)
	typeSchema := props["type"].(map[string]any)
	got := typeSchema["enum"].([]string)
	want := []string{"preference", "rule", "procedure", "fact", "profile"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("save_memory type enum = %v, want %v", got, want)
	}
}

func TestToolSetRegistersMemoryRecallTools(t *testing.T) {
	tools := NewBuiltinToolSet(HIDConfig{}, AudioConfig{}, SearchConfig{}, ProxyConfig{})
	tools.RegisterMemoryTools(t.TempDir(), filepath.Join(t.TempDir(), "memory"), nil)
	if _, ok := tools.Get("recall_session_chunks"); !ok {
		t.Fatalf("expected recall_session_chunks tool to be registered")
	}
	if _, ok := tools.Get("recall_memory"); !ok {
		t.Fatalf("expected recall_memory tool to be registered")
	}
	if _, ok := tools.Get("recall_device_memory"); !ok {
		t.Fatalf("expected recall_device_memory tool to be registered")
	}
	if _, ok := tools.Get("inspect_episode"); !ok {
		t.Fatalf("expected inspect_episode tool to be registered")
	}
}
