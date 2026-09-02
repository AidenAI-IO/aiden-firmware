package agent

import (
	"aiden-agent/internal/agent/messages"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
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
