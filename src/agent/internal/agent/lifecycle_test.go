package agent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
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

func TestVerifyMarksExpiredLongTermMemory(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	store := NewLongTermMemoryStore(filepath.Join(root, "long_term"))

	if _, err := store.AddMemory(ctx, MemoryItem{
		ID:               "mem_expired_verify",
		Type:             "procedure",
		Status:           "active",
		Priority:         80,
		Tags:             []string{"登录"},
		Title:            "过期流程",
		Content:          "旧流程。",
		EvidenceExcerpts: []string{"旧证据"},
		ExpiresAt:        "2000-01-01T00:00:00Z",
	}); err != nil {
		t.Fatalf("AddMemory() error = %v", err)
	}

	lm := NewLifecycleManager(root)
	report, err := lm.Verify(ctx)
	if err != nil {
		t.Fatalf("Verify() error = %v", err)
	}
	if report.ExpiredMemories != 1 {
		t.Fatalf("expected one expired memory, got %#v", report)
	}

	parsed, err := readMemoryMarkdown(store.memoryPath("mem_expired_verify"))
	if err != nil {
		t.Fatalf("read expired memory: %v", err)
	}
	if parsed.Item.Status != "expired" {
		t.Fatalf("expected status expired, got %s", parsed.Item.Status)
	}
	indexData, err := os.ReadFile(store.indexPath())
	if err != nil {
		t.Fatalf("read index: %v", err)
	}
	if !strings.Contains(string(indexData), "status: expired") {
		t.Fatalf("expected rebuilt index to include expired status:\n%s", indexData)
	}
	results, err := store.Search(ctx, MemoryQuery{Tags: []string{"登录"}, Limit: 10})
	if err != nil {
		t.Fatalf("Search() error = %v", err)
	}
	if len(results) != 0 {
		t.Fatalf("expected expired memory excluded from search, got %#v", results)
	}
}

func TestVerifyPrunesOldUnreferencedEpisodeTrace(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "lifecycle"), 0o755); err != nil {
		t.Fatalf("mkdir lifecycle: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "lifecycle", "retention.yaml"), []byte("event_compact_after: 1s\n"), 0o644); err != nil {
		t.Fatalf("write retention: %v", err)
	}

	episodes := NewTaskEpisodeStore(filepath.Join(root, "episodes"))
	for _, episode := range []TaskEpisode{
		{
			ID:        "ep_old_unreferenced",
			Status:    "active",
			StartedAt: "2000-01-01T00:00:00Z",
			EndedAt:   "2000-01-01T00:00:10Z",
			UserGoal:  "打开微信App",
			Outcome:   TaskEpisodeOutcome{Success: true, FinalState: "已打开"},
			Events:    []TaskEpisodeEvent{{EventID: "evt_old", Type: runEventToolCall, ToolName: "touch_gesture"}},
		},
		{
			ID:        "ep_old_referenced",
			Status:    "active",
			StartedAt: "2000-01-01T00:01:00Z",
			EndedAt:   "2000-01-01T00:01:10Z",
			UserGoal:  "打开通讯录",
			Outcome:   TaskEpisodeOutcome{Success: true, FinalState: "已打开"},
			Events:    []TaskEpisodeEvent{{EventID: "evt_ref", Type: runEventToolCall, ToolName: "touch_gesture"}},
		},
	} {
		if _, err := episodes.AddEpisode(ctx, episode); err != nil {
			t.Fatalf("AddEpisode(%s): %v", episode.ID, err)
		}
	}

	oldDir := filepath.Join(root, "episodes", "2000", "ep_old_unreferenced")
	refDir := filepath.Join(root, "episodes", "2000", "ep_old_referenced")
	if err := os.WriteFile(filepath.Join(oldDir, "artifacts", "step_001.jpg"), []byte("image"), 0o644); err != nil {
		t.Fatalf("write old artifact: %v", err)
	}
	if err := os.WriteFile(filepath.Join(refDir, "artifacts", "step_001.jpg"), []byte("image"), 0o644); err != nil {
		t.Fatalf("write referenced artifact: %v", err)
	}

	store := NewLongTermMemoryStore(filepath.Join(root, "long_term"))
	if _, err := store.AddMemory(ctx, MemoryItem{
		ID:               "mem_refs_episode",
		Type:             "procedure",
		Status:           "active",
		Title:            "引用 episode 的流程",
		Content:          "通讯录打开流程仍需保留 trace。",
		EvidenceExcerpts: []string{"episode trace"},
		SourceRefs:       []MemorySourceRef{{Type: "episode", ID: "ep_old_referenced"}},
	}); err != nil {
		t.Fatalf("AddMemory() error = %v", err)
	}

	lm := NewLifecycleManager(root)
	report, err := lm.Verify(ctx)
	if err != nil {
		t.Fatalf("Verify() error = %v", err)
	}
	if report.PrunedEpisodeTraces != 1 {
		t.Fatalf("expected one pruned episode trace, got %#v", report)
	}
	if _, err := os.Stat(filepath.Join(oldDir, "episode.yaml")); err != nil {
		t.Fatalf("expected unreferenced episode metadata to remain: %v", err)
	}
	if _, err := os.Stat(filepath.Join(oldDir, "events.jsonl")); !os.IsNotExist(err) {
		t.Fatalf("expected unreferenced events to be pruned, err=%v", err)
	}
	if _, err := os.Stat(filepath.Join(oldDir, "artifacts")); !os.IsNotExist(err) {
		t.Fatalf("expected unreferenced artifacts to be pruned, err=%v", err)
	}
	if _, err := os.Stat(filepath.Join(refDir, "events.jsonl")); err != nil {
		t.Fatalf("expected referenced events to remain: %v", err)
	}
	if _, err := os.Stat(filepath.Join(refDir, "artifacts", "step_001.jpg")); err != nil {
		t.Fatalf("expected referenced artifact to remain: %v", err)
	}

	index, err := episodes.loadIndex()
	if err != nil {
		t.Fatalf("load episode index: %v", err)
	}
	var sawPrunedMetadata, sawReferencedEvents bool
	for _, entry := range index.Episodes {
		switch entry.ID {
		case "ep_old_unreferenced":
			sawPrunedMetadata = true
			if entry.EventsFile != "" {
				t.Fatalf("expected pruned episode events_file empty, got %q", entry.EventsFile)
			}
		case "ep_old_referenced":
			if entry.EventsFile != "" {
				sawReferencedEvents = true
			}
		}
	}
	if !sawPrunedMetadata || !sawReferencedEvents {
		t.Fatalf("unexpected episode index after prune: %#v", index.Episodes)
	}
}

func TestMemoryExtractionConfigDefaults(t *testing.T) {
	cfg := DefaultMemoryExtractionConfig()
	if cfg.ContextWindow != 32000 {
		t.Fatalf("expected default ContextWindow=32000, got %d", cfg.ContextWindow)
	}
	if got := cfg.EpisodeMemoryIdleDelayOrDefault(); got != 5*time.Minute {
		t.Fatalf("expected default Episode Memory idle delay=5m, got %s", got)
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
context_window: 8000
episode_memory_idle_delay_seconds: 7
`), 0o644)

	cfg := LoadMemoryExtractionConfig(dir)
	if len(cfg.TagCandidates) != 1 || cfg.TagCandidates[0] != "自定义标签" {
		t.Fatalf("expected custom tag candidates, got %v", cfg.TagCandidates)
	}
	if len(cfg.EntitySuffixes) != 1 || cfg.EntitySuffixes[0] != "System" {
		t.Fatalf("expected custom entity suffixes, got %v", cfg.EntitySuffixes)
	}
	if cfg.ContextWindow != 8000 {
		t.Fatalf("expected context_window=8000, got %d", cfg.ContextWindow)
	}
	if got := cfg.EpisodeMemoryIdleDelayOrDefault(); got != 7*time.Second {
		t.Fatalf("expected episode_memory_idle_delay_seconds=7, got %s", got)
	}
}

// Configs written by builds that carried session-compaction knobs must still
// load: the removed keys are ignored rather than failing the whole file.
func TestMemoryExtractionConfigIgnoresRemovedSessionCompactionKeys(t *testing.T) {
	dir := t.TempDir()
	memDir := filepath.Join(dir, "memory")
	os.MkdirAll(memDir, 0o755)
	os.WriteFile(filepath.Join(memDir, "extraction.yaml"), []byte(`
hot_window_events: 20
count_compress_after_events: 75
compress_at_percent: 40
reserve_tokens: 4096
keep_recent_tokens: 9000
summary_max_chunks: 5
session_boundary_enabled: false
session_boundary_short_gap_seconds: 30
episode_memory_idle_delay_seconds: 11
`), 0o644)

	cfg := LoadMemoryExtractionConfig(dir)
	if got := cfg.EpisodeMemoryIdleDelayOrDefault(); got != 11*time.Second {
		t.Fatalf("live key should still apply alongside removed keys, got %s", got)
	}
	if cfg.ContextWindow != 32000 {
		t.Fatalf("expected default ContextWindow when unset, got %d", cfg.ContextWindow)
	}
}
