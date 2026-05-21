package agent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestStrconvTimeIDUniqueness(t *testing.T) {
	ts := time.Now().UTC()
	seen := map[string]bool{}
	for i := 0; i < 1000; i++ {
		id := strconvTimeID(ts)
		if seen[id] {
			t.Fatalf("duplicate ID generated at iteration %d: %s", i, id)
		}
		seen[id] = true
	}
}

func TestAppendEventConcurrentSafety(t *testing.T) {
	ctx := context.Background()
	store := NewSessionMemoryStore(filepath.Join(t.TempDir(), "session"))

	var wg sync.WaitGroup
	goroutines := 10
	eventsPerGoroutine := 10
	wg.Add(goroutines)
	for g := 0; g < goroutines; g++ {
		go func(gid int) {
			defer wg.Done()
			for i := 0; i < eventsPerGoroutine; i++ {
				_, err := store.AppendEvent(ctx, SessionEvent{
					Type:    "user_input",
					Role:    "user",
					Content: "concurrent event",
				})
				if err != nil {
					t.Errorf("goroutine %d event %d: %v", gid, i, err)
				}
			}
		}(g)
	}
	wg.Wait()

	events, err := store.readEvents(store.eventsPath())
	if err != nil {
		t.Fatalf("readEvents() error = %v", err)
	}
	expected := goroutines * eventsPerGoroutine
	if len(events) != expected {
		t.Fatalf("expected %d events, got %d", expected, len(events))
	}
}

func TestReadEventsHandlesLargeLines(t *testing.T) {
	ctx := context.Background()
	store := NewSessionMemoryStore(filepath.Join(t.TempDir(), "session"))

	largeContent := strings.Repeat("大", 70000)
	_, err := store.AppendEvent(ctx, SessionEvent{
		Type:    "screen_context",
		Role:    "screen",
		Content: largeContent,
	})
	if err != nil {
		t.Fatalf("AppendEvent() large content error = %v", err)
	}

	events, err := store.readEvents(store.eventsPath())
	if err != nil {
		t.Fatalf("readEvents() error = %v", err)
	}
	if len(events) != 1 || events[0].Content != largeContent {
		t.Fatalf("expected large content to round-trip, got len=%d content_len=%d", len(events), len(events[0].Content))
	}
}

func TestSupersedeMemory(t *testing.T) {
	ctx := context.Background()
	store := NewLongTermMemoryStore(filepath.Join(t.TempDir(), "long_term"))

	if _, err := store.AddMemory(ctx, MemoryItem{
		ID:               "mem_old",
		Type:             "preference",
		Priority:         80,
		Tags:             []string{"登录"},
		Entities:         []string{"某App"},
		Title:            "旧偏好",
		Content:          "用密码登录。",
		EvidenceExcerpts: []string{"用户说用密码。"},
	}); err != nil {
		t.Fatalf("AddMemory() error = %v", err)
	}

	newID, err := store.SupersedeMemory(ctx, "mem_old", MemoryItem{
		Type:             "preference",
		Priority:         80,
		Tags:             []string{"登录"},
		Entities:         []string{"某App"},
		Title:            "新偏好",
		Content:          "用验证码登录。",
		EvidenceExcerpts: []string{"用户说用验证码。"},
	})
	if err != nil {
		t.Fatalf("SupersedeMemory() error = %v", err)
	}

	results, err := store.Search(ctx, MemoryQuery{Tags: []string{"登录"}, Limit: 10})
	if err != nil {
		t.Fatalf("Search() error = %v", err)
	}
	if len(results) != 1 || results[0].ID != newID {
		t.Fatalf("expected only new memory in search, got %#v", results)
	}

	oldParsed, err := readMemoryMarkdown(store.memoryPath("mem_old"))
	if err != nil {
		t.Fatalf("read old memory: %v", err)
	}
	if oldParsed.Item.Status != "replaced" || oldParsed.Item.SupersededBy != newID {
		t.Fatalf("expected old memory to be replaced, got status=%s superseded_by=%s", oldParsed.Item.Status, oldParsed.Item.SupersededBy)
	}
}

func TestMarkConflict(t *testing.T) {
	ctx := context.Background()
	store := NewLongTermMemoryStore(filepath.Join(t.TempDir(), "long_term"))

	for _, item := range []MemoryItem{
		{ID: "mem_a", Type: "rule", Priority: 90, Tags: []string{"支付"}, Title: "A", Content: "先确认。", EvidenceExcerpts: []string{"a"}},
		{ID: "mem_b", Type: "rule", Priority: 90, Tags: []string{"支付"}, Title: "B", Content: "不用确认。", EvidenceExcerpts: []string{"b"}},
	} {
		if _, err := store.AddMemory(ctx, item); err != nil {
			t.Fatalf("AddMemory(%s) error = %v", item.ID, err)
		}
	}

	if err := store.MarkConflict(ctx, "mem_a", "mem_b", "contradictory rules"); err != nil {
		t.Fatalf("MarkConflict() error = %v", err)
	}

	results, err := store.Search(ctx, MemoryQuery{Tags: []string{"支付"}, Limit: 10})
	if err != nil {
		t.Fatalf("Search() error = %v", err)
	}
	if len(results) != 0 {
		t.Fatalf("expected conflicted memories excluded from search, got %d", len(results))
	}
}

func TestDecideActionIgnoresDuplicate(t *testing.T) {
	ctx := context.Background()
	store := NewLongTermMemoryStore(filepath.Join(t.TempDir(), "long_term"))

	if _, err := store.AddMemory(ctx, MemoryItem{
		ID:               "mem_existing",
		Type:             "preference",
		Priority:         80,
		Tags:             []string{"登录"},
		Entities:         []string{"某App"},
		Title:            "偏好",
		Content:          "用验证码登录。",
		EvidenceExcerpts: []string{"用户说用验证码。"},
		SourceRefs:       []MemorySourceRef{{Type: "chunk", ID: "chunk_1", EventIDs: []string{"evt_1"}}},
	}); err != nil {
		t.Fatalf("AddMemory() error = %v", err)
	}

	action, _, err := store.DecideAction(ctx, MemoryItem{
		Type:       "preference",
		Tags:       []string{"登录"},
		Entities:   []string{"某App"},
		Content:    "用验证码登录。",
		SourceRefs: []MemorySourceRef{{Type: "chunk", ID: "chunk_1", EventIDs: []string{"evt_1"}}},
	})
	if err != nil {
		t.Fatalf("DecideAction() error = %v", err)
	}
	if action != "ignore" {
		t.Fatalf("expected ignore, got %s", action)
	}
}

func TestDecideActionSupersedes(t *testing.T) {
	ctx := context.Background()
	store := NewLongTermMemoryStore(filepath.Join(t.TempDir(), "long_term"))

	if _, err := store.AddMemory(ctx, MemoryItem{
		ID:               "mem_short",
		Type:             "preference",
		Priority:         80,
		Tags:             []string{"登录"},
		Entities:         []string{"某App"},
		Title:            "偏好",
		Content:          "用验证码。",
		EvidenceExcerpts: []string{"e"},
	}); err != nil {
		t.Fatalf("AddMemory() error = %v", err)
	}

	action, existingID, err := store.DecideAction(ctx, MemoryItem{
		Type:     "preference",
		Tags:     []string{"登录"},
		Entities: []string{"某App"},
		Content:  "用验证码。验证码过期时先重新获取。",
	})
	if err != nil {
		t.Fatalf("DecideAction() error = %v", err)
	}
	if action != "supersede" || existingID != "mem_short" {
		t.Fatalf("expected supersede of mem_short, got action=%s existingID=%s", action, existingID)
	}
}

func TestDecideActionDetectsConflict(t *testing.T) {
	ctx := context.Background()
	store := NewLongTermMemoryStore(filepath.Join(t.TempDir(), "long_term"))

	if _, err := store.AddMemory(ctx, MemoryItem{
		ID:               "mem_rule_a",
		Type:             "rule",
		Priority:         90,
		Tags:             []string{"支付"},
		Entities:         []string{"报销App"},
		Title:            "规则A",
		Content:          "支付前必须确认。",
		EvidenceExcerpts: []string{"e"},
	}); err != nil {
		t.Fatalf("AddMemory() error = %v", err)
	}

	// Same type + same entities + overlapping tags → supersede (not conflict)
	action, existingID, err := store.DecideAction(ctx, MemoryItem{
		Type:     "rule",
		Tags:     []string{"支付"},
		Entities: []string{"报销App"},
		Content:  "支付不需要确认。",
	})
	if err != nil {
		t.Fatalf("DecideAction() error = %v", err)
	}
	if action != "supersede" || existingID != "mem_rule_a" {
		t.Fatalf("expected supersede of mem_rule_a (same topic update), got action=%s existingID=%s", action, existingID)
	}

	// Same type + same entities but NO tag overlap → add (different topic)
	action2, _, err := store.DecideAction(ctx, MemoryItem{
		Type:     "rule",
		Tags:     []string{"转账"},
		Entities: []string{"报销App"},
		Content:  "转账前需要双重验证。",
	})
	if err != nil {
		t.Fatalf("DecideAction() no-overlap error = %v", err)
	}
	if action2 != "add" {
		t.Fatalf("expected add for different topic, got %s", action2)
	}
}

func TestRegenerateProfileMD(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	store := NewLongTermMemoryStore(filepath.Join(root, "long_term"))

	for _, item := range []MemoryItem{
		{ID: "mem_profile", Type: "profile", Priority: 70, Tags: []string{"画像"}, Title: "画像", Content: "用户是产品经理。", EvidenceExcerpts: []string{"e"}},
		{ID: "mem_rule", Type: "rule", Priority: 100, Tags: []string{"支付"}, Title: "规则", Content: "支付前必须确认。", EvidenceExcerpts: []string{"e"}},
		{ID: "mem_low", Type: "fact", Priority: 50, Tags: []string{"低"}, Title: "低优先级", Content: "不重要。", EvidenceExcerpts: []string{"e"}},
	} {
		if _, err := store.AddMemory(ctx, item); err != nil {
			t.Fatalf("AddMemory(%s) error = %v", item.ID, err)
		}
	}

	if err := store.RegenerateProfileMD(ctx); err != nil {
		t.Fatalf("RegenerateProfileMD() error = %v", err)
	}

	data, err := os.ReadFile(filepath.Join(root, "long_term", "profile.md"))
	if err != nil {
		t.Fatalf("read profile.md: %v", err)
	}
	content := string(data)
	if !strings.Contains(content, "产品经理") {
		t.Fatalf("expected profile to contain user profile, got:\n%s", content)
	}
	if !strings.Contains(content, "支付前必须确认") {
		t.Fatalf("expected profile to contain rule, got:\n%s", content)
	}
	if strings.Contains(content, "不重要") {
		t.Fatalf("expected profile to exclude non-profile/rule/preference type memory, got:\n%s", content)
	}
}

func TestSummaryAccumulatesChunks(t *testing.T) {
	ctx := context.Background()
	store := NewSessionMemoryStore(filepath.Join(t.TempDir(), "session"))

	for i := 0; i < 5; i++ {
		_, _ = store.AppendEvent(ctx, SessionEvent{Type: "user_input", Role: "user", Content: "event batch 1"})
	}
	_, err := store.Compress(ctx, CompressOption{Summary: "第一次压缩摘要", Tags: []string{"t1"}})
	if err != nil {
		t.Fatalf("first Compress() error = %v", err)
	}

	for i := 0; i < 5; i++ {
		_, _ = store.AppendEvent(ctx, SessionEvent{Type: "user_input", Role: "user", Content: "event batch 2"})
	}
	_, err = store.Compress(ctx, CompressOption{Summary: "第二次压缩摘要", Tags: []string{"t2"}})
	if err != nil {
		t.Fatalf("second Compress() error = %v", err)
	}

	data, err := os.ReadFile(store.summaryPath())
	if err != nil {
		t.Fatalf("read summary.md: %v", err)
	}
	content := string(data)
	if !strings.Contains(content, "第二次压缩摘要") {
		t.Fatalf("expected latest summary in content, got:\n%s", content)
	}
	if strings.Count(content, "- chunk_") < 2 {
		t.Fatalf("expected at least 2 chunk entries in summary, got:\n%s", content)
	}
}

func TestSaveMemoryToolEndToEnd(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	store := NewLongTermMemoryStore(filepath.Join(root, "long_term"))
	tool := NewSaveMemoryTool(store)

	out, err := tool.Call(ctx, `{"type":"rule","title":"支付规则","content":"支付前必须确认金额。","tags":["支付"],"entities":["报销App"],"evidence":["用户说：支付前先确认金额"]}`)
	if err != nil {
		t.Fatalf("SaveMemoryTool.Call() error = %v", err)
	}
	if !strings.Contains(out, `"status":"saved"`) {
		t.Fatalf("expected saved status, got: %s", out)
	}

	out2, err := tool.Call(ctx, `{"type":"rule","title":"支付规则","content":"支付前必须确认金额。","tags":["支付"],"entities":["报销App"],"evidence":["same"]}`)
	if err != nil {
		t.Fatalf("SaveMemoryTool.Call() duplicate error = %v", err)
	}
	if !strings.Contains(out2, `"status":"ignored"`) {
		t.Fatalf("expected ignored for duplicate, got: %s", out2)
	}
}

func TestForgetMemoryToolEndToEnd(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	store := NewLongTermMemoryStore(filepath.Join(root, "long_term"))

	id, err := store.AddMemory(ctx, MemoryItem{
		ID: "mem_to_forget", Type: "fact", Priority: 50,
		Tags: []string{"test"}, Title: "fact", Content: "some fact",
		EvidenceExcerpts: []string{"e"},
	})
	if err != nil {
		t.Fatalf("AddMemory() error = %v", err)
	}

	tool := NewForgetMemoryTool(store)
	out, err := tool.Call(ctx, `{"id":"`+id+`","reason":"user asked"}`)
	if err != nil {
		t.Fatalf("ForgetMemoryTool.Call() error = %v", err)
	}
	if !strings.Contains(out, `"status":"deleted"`) {
		t.Fatalf("expected deleted status, got: %s", out)
	}

	results, err := store.Search(ctx, MemoryQuery{Tags: []string{"test"}, Limit: 5})
	if err != nil {
		t.Fatalf("Search() error = %v", err)
	}
	if len(results) != 0 {
		t.Fatalf("expected no results after forget, got %d", len(results))
	}
}

func TestVerifyRebuildsCorruptedIndex(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	store := NewLongTermMemoryStore(filepath.Join(root, "long_term"))

	if _, err := store.AddMemory(ctx, MemoryItem{
		ID: "mem_verify", Type: "fact", Priority: 50,
		Tags: []string{"test"}, Title: "fact", Content: "content",
		EvidenceExcerpts: []string{"e"},
	}); err != nil {
		t.Fatalf("AddMemory() error = %v", err)
	}

	os.Remove(store.indexPath())

	lm := NewLifecycleManager(root)
	report, err := lm.Verify(ctx)
	if err != nil {
		t.Fatalf("Verify() error = %v", err)
	}
	if !report.IndexRebuilt {
		t.Fatalf("expected index to be rebuilt")
	}

	results, err := store.Search(ctx, MemoryQuery{Tags: []string{"test"}, Limit: 5})
	if err != nil {
		t.Fatalf("Search() after verify error = %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 result after index rebuild, got %d", len(results))
	}
}

func TestMemoryExtractionConfigFromYAML(t *testing.T) {
	dir := t.TempDir()
	memDir := filepath.Join(dir, "memory")
	os.MkdirAll(memDir, 0o755)
	os.WriteFile(filepath.Join(memDir, "extraction.yaml"), []byte(`
tag_candidates:
  - "自定义标签"
entity_suffixes:
  - "System"
hot_window_events: 30
`), 0o644)

	cfg := LoadMemoryExtractionConfig(dir)
	if len(cfg.TagCandidates) != 1 || cfg.TagCandidates[0] != "自定义标签" {
		t.Fatalf("expected custom tag candidates, got %v", cfg.TagCandidates)
	}
	if len(cfg.EntitySuffixes) != 1 || cfg.EntitySuffixes[0] != "System" {
		t.Fatalf("expected custom entity suffixes, got %v", cfg.EntitySuffixes)
	}
	if cfg.HotWindowEvents != 30 {
		t.Fatalf("expected hot_window_events=30, got %d", cfg.HotWindowEvents)
	}
}

func TestMaintainFilesystemMemoryOnlyArchivesNoAutoExtract(t *testing.T) {
	ctx := context.Background()
	storageDir := t.TempDir()
	manager := NewMemoryManager(storageDir)

	sessionDir := filepath.Join(storageDir, "session")
	os.MkdirAll(sessionDir, 0o755)

	var msgs []MessageRecord
	msgs = append(msgs, MessageRecord{Role: "human", Content: "记一下，以后处理蓝海报销App超过100元的提交必须先确认。"})
	msgs = append(msgs, MessageRecord{Role: "ai", Content: "已记录。"})
	for i := 0; i < 22; i++ {
		msgs = append(msgs, MessageRecord{Role: "human", Content: "填充"})
	}

	now := time.Now().UTC()
	session := NewSessionMemoryStore(sessionDir)
	for i, msg := range msgs {
		evt := sessionEventFromRecord(msg, now, i)
		if _, err := session.AppendEvent(ctx, evt); err != nil {
			t.Fatalf("AppendEvent() error = %v", err)
		}
	}

	if err := manager.maintainFilesystemMemory(ctx); err != nil {
		t.Fatalf("maintainFilesystemMemory() error = %v", err)
	}

	// Compaction should NOT auto-extract long-term memories — that's the LLM's job via save_memory tool
	longTerm := NewLongTermMemoryStore(filepath.Join(storageDir, "long_term"))
	results, _ := longTerm.Search(ctx, MemoryQuery{Limit: 20})
	if len(results) != 0 {
		t.Fatalf("expected no auto-extracted memories (LLM decides via tool), got %d", len(results))
	}

	// But chunks should exist
	chunks, err := session.RecallChunks(ctx, ChunkRecallQuery{Limit: 10})
	if err != nil {
		t.Fatalf("RecallChunks() error = %v", err)
	}
	if len(chunks) == 0 {
		t.Fatalf("expected at least one chunk after compaction")
	}
}
