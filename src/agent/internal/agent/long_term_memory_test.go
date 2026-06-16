package agent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLongTermMemoryAddWritesMarkdownAndSearchableIndex(t *testing.T) {
	ctx := context.Background()
	store := NewLongTermMemoryStore(filepath.Join(t.TempDir(), "long_term"))

	id, err := store.AddMemory(ctx, MemoryItem{
		ID:               "mem_test_login",
		Type:             "preference",
		Priority:         80,
		Confidence:       0.9,
		Tags:             []string{"登录", "验证码"},
		Entities:         []string{"某政务App"},
		Title:            "某政务 App 验证码登录偏好",
		Content:          "用户下次登录某政务 App 时，应先重新获取验证码。",
		EvidenceExcerpts: []string{"用户说：记一下，这个 App 下次登录要先重新获取验证码。"},
		SourceRefs: []MemorySourceRef{
			{Type: "chunk", ID: "chunk_test", EventIDs: []string{"evt_test"}},
		},
	})
	if err != nil {
		t.Fatalf("AddMemory() error = %v", err)
	}
	if id != "mem_test_login" {
		t.Fatalf("unexpected memory id: %q", id)
	}

	data, err := os.ReadFile(filepath.Join(store.RootDir(), "memories", "mem_test_login.md"))
	if err != nil {
		t.Fatalf("read memory markdown: %v", err)
	}
	markdown := string(data)
	for _, want := range []string{
		"id: mem_test_login",
		"type: preference",
		"status: active",
		"# 某政务 App 验证码登录偏好",
		"## 证据摘录",
		"用户说：记一下",
	} {
		if !strings.Contains(markdown, want) {
			t.Fatalf("expected markdown to contain %q, got:\n%s", want, markdown)
		}
	}

	results, err := store.Search(ctx, MemoryQuery{Tags: []string{"验证码"}, Limit: 5})
	if err != nil {
		t.Fatalf("Search() error = %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 search result, got %d", len(results))
	}
	if results[0].ID != "mem_test_login" || !strings.Contains(results[0].Content, "重新获取验证码") {
		t.Fatalf("unexpected search result: %#v", results[0])
	}
}

func TestLongTermMemorySearchReadsPlainMarkdownBody(t *testing.T) {
	ctx := context.Background()
	store := NewLongTermMemoryStore(filepath.Join(t.TempDir(), "long_term"))
	memoriesDir := filepath.Join(store.RootDir(), "memories")
	if err := os.MkdirAll(memoriesDir, 0o755); err != nil {
		t.Fatalf("create memories dir: %v", err)
	}
	markdown := `---
id: personamem_campfire_storytelling
type: fact
status: active
priority: 80
confidence: 0.9
tags:
  - family
  - campfire
  - storytelling
time_scope: long_term
created_at: "2026-05-29T00:00:00Z"
updated_at: "2026-05-29T00:00:00Z"
traceability: full
---

# Campfire storytelling discomfort

The user previously said storytelling around the campfire can feel awkward.
`
	if err := os.WriteFile(filepath.Join(memoriesDir, "personamem_campfire_storytelling.md"), []byte(markdown), 0o644); err != nil {
		t.Fatalf("write plain memory markdown: %v", err)
	}

	results, err := store.Search(ctx, MemoryQuery{Tags: []string{"campfire"}, Limit: 1})
	if err != nil {
		t.Fatalf("Search() error = %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 search result, got %d", len(results))
	}
	if !strings.Contains(results[0].Content, "feel awkward") {
		t.Fatalf("expected plain markdown body as content, got %#v", results[0])
	}
	if !strings.Contains(results[0].Summary, "feel awkward") {
		t.Fatalf("expected plain markdown body summary, got %#v", results[0])
	}
}

func TestLongTermMemorySearchRanksTopicalMatchesWhenFiltersAreTooNarrow(t *testing.T) {
	ctx := context.Background()
	store := NewLongTermMemoryStore(filepath.Join(t.TempDir(), "long_term"))

	if _, err := store.AddMemory(ctx, MemoryItem{
		ID:         "personamem_running_background",
		Type:       "profile",
		Priority:   100,
		Confidence: 0.9,
		Tags:       []string{"fitness", "running", "background"},
		Entities:   []string{"user"},
		Title:      "Running background",
		Content:    "The user has a background in running for fitness.",
		EvidenceExcerpts: []string{
			"The user mentioned running as a fitness background.",
		},
	}); err != nil {
		t.Fatalf("AddMemory() running error = %v", err)
	}
	if _, err := store.AddMemory(ctx, MemoryItem{
		ID:         "personamem_soccer_community",
		Type:       "preference",
		Priority:   80,
		Confidence: 0.9,
		Tags:       []string{"sports", "soccer", "community"},
		Title:      "Soccer and community fitness",
		Content:    "The user has positive experience with soccer and values team sports for fitness, camaraderie, teamwork, motivation, and lasting friendships.",
		EvidenceExcerpts: []string{
			"The user values soccer for fitness and community.",
		},
	}); err != nil {
		t.Fatalf("AddMemory() soccer error = %v", err)
	}

	results, err := store.Search(ctx, MemoryQuery{
		Tags:     []string{"fitness", "community"},
		Entities: []string{"user"},
		Types:    []string{"profile", "preference"},
		Limit:    2,
	})
	if err != nil {
		t.Fatalf("Search() error = %v", err)
	}
	if len(results) == 0 {
		t.Fatalf("expected ranked search results")
	}
	if results[0].ID != "personamem_soccer_community" {
		t.Fatalf("expected soccer memory first, got %#v", results)
	}
}

func TestLongTermMemorySearchDoesNotReturnTypeOnlyMatchesForTopicalQuery(t *testing.T) {
	ctx := context.Background()
	store := NewLongTermMemoryStore(filepath.Join(t.TempDir(), "long_term"))

	if _, err := store.AddMemory(ctx, MemoryItem{
		ID:         "personamem_campfire_storytelling",
		Type:       "fact",
		Priority:   80,
		Confidence: 0.9,
		Tags:       []string{"family", "campfire", "storytelling"},
		Title:      "Campfire storytelling discomfort",
		Content:    "The user previously said storytelling around the campfire can feel awkward.",
		EvidenceExcerpts: []string{
			"The user said campfire storytelling can feel awkward.",
		},
	}); err != nil {
		t.Fatalf("AddMemory() error = %v", err)
	}

	results, err := store.Search(ctx, MemoryQuery{
		Tags:     []string{"travel", "activities", "blog"},
		Entities: []string{"travel blogs"},
		Types:    []string{"fact"},
		Limit:    3,
	})
	if err != nil {
		t.Fatalf("Search() error = %v", err)
	}
	if len(results) != 0 {
		t.Fatalf("expected no unrelated type-only matches, got %#v", results)
	}
}

func TestLongTermMemorySearchTreatsWhitespaceOnlyTagsAsEmptyQuery(t *testing.T) {
	ctx := context.Background()
	store := NewLongTermMemoryStore(filepath.Join(t.TempDir(), "long_term"))

	if _, err := store.AddMemory(ctx, MemoryItem{
		ID:         "mem_general_preference",
		Type:       "preference",
		Priority:   80,
		Confidence: 0.9,
		Tags:       []string{"general"},
		Title:      "General preference",
		Content:    "The user prefers concise replies.",
		EvidenceExcerpts: []string{
			"The user asked for concise replies.",
		},
	}); err != nil {
		t.Fatalf("AddMemory() error = %v", err)
	}

	results, err := store.Search(ctx, MemoryQuery{Tags: []string{" \t"}, Limit: 1})
	if err != nil {
		t.Fatalf("Search() error = %v", err)
	}
	if len(results) != 1 || results[0].ID != "mem_general_preference" {
		t.Fatalf("expected whitespace-only tags to behave like empty query, got %#v", results)
	}
}

func TestLongTermMemorySearchSkipsCorruptIndexedMarkdown(t *testing.T) {
	ctx := context.Background()
	store := NewLongTermMemoryStore(filepath.Join(t.TempDir(), "long_term"))
	if _, err := store.AddMemory(ctx, MemoryItem{
		ID:         "mem_valid",
		Type:       "preference",
		Priority:   80,
		Confidence: 0.9,
		Tags:       []string{"general"},
		Title:      "Valid memory",
		Content:    "The user prefers concise replies.",
		EvidenceExcerpts: []string{
			"The user asked for concise replies.",
		},
	}); err != nil {
		t.Fatalf("AddMemory() error = %v", err)
	}

	corruptPath := filepath.Join(store.RootDir(), "memories", "mem_corrupt.md")
	if err := os.WriteFile(corruptPath, nil, 0o644); err != nil {
		t.Fatalf("write corrupt memory: %v", err)
	}
	index, err := store.loadIndex(ctx)
	if err != nil {
		t.Fatalf("loadIndex() error = %v", err)
	}
	index.Memories = append(index.Memories, memoryIndexEntry{
		ID:         "mem_corrupt",
		File:       filepath.ToSlash(filepath.Join("memories", "mem_corrupt.md")),
		Type:       "preference",
		Status:     "active",
		Priority:   100,
		Confidence: 1,
		Tags:       []string{"general"},
	})
	if err := store.writeIndex(index); err != nil {
		t.Fatalf("writeIndex() error = %v", err)
	}

	results, err := store.Search(ctx, MemoryQuery{Tags: []string{"general"}, Limit: 5})
	if err != nil {
		t.Fatalf("Search() error = %v", err)
	}
	if len(results) != 1 || results[0].ID != "mem_valid" {
		t.Fatalf("expected valid memory only, got %#v", results)
	}
}

func TestLongTermMemoryRebuildIndexSkipsCorruptMarkdown(t *testing.T) {
	ctx := context.Background()
	store := NewLongTermMemoryStore(filepath.Join(t.TempDir(), "long_term"))
	if _, err := store.AddMemory(ctx, MemoryItem{
		ID:         "mem_valid",
		Type:       "preference",
		Priority:   80,
		Confidence: 0.9,
		Tags:       []string{"general"},
		Title:      "Valid memory",
		Content:    "The user prefers concise replies.",
		EvidenceExcerpts: []string{
			"The user asked for concise replies.",
		},
	}); err != nil {
		t.Fatalf("AddMemory() error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(store.RootDir(), "memories", "mem_corrupt.md"), nil, 0o644); err != nil {
		t.Fatalf("write corrupt memory: %v", err)
	}

	if err := store.RebuildIndex(ctx); err != nil {
		t.Fatalf("RebuildIndex() error = %v", err)
	}
	index, err := store.loadIndex(ctx)
	if err != nil {
		t.Fatalf("loadIndex() error = %v", err)
	}
	if len(index.Memories) != 1 || index.Memories[0].ID != "mem_valid" {
		t.Fatalf("expected valid memory only in rebuilt index, got %#v", index.Memories)
	}
}

func TestLongTermMemoryForgetExcludesMemoryFromSearch(t *testing.T) {
	ctx := context.Background()
	store := NewLongTermMemoryStore(filepath.Join(t.TempDir(), "long_term"))

	if _, err := store.AddMemory(ctx, MemoryItem{
		ID:               "mem_forget_me",
		Type:             "rule",
		Priority:         100,
		Confidence:       0.95,
		Tags:             []string{"支付"},
		Title:            "支付确认规则",
		Content:          "支付前必须确认。",
		EvidenceExcerpts: []string{"用户说：支付前先问我。"},
	}); err != nil {
		t.Fatalf("AddMemory() error = %v", err)
	}

	if err := store.Forget(ctx, "mem_forget_me", "user_request"); err != nil {
		t.Fatalf("Forget() error = %v", err)
	}
	forgottenMarkdown, err := os.ReadFile(filepath.Join(store.RootDir(), "memories", "mem_forget_me.md"))
	if err != nil {
		t.Fatalf("read forgotten markdown: %v", err)
	}
	if !strings.Contains(string(forgottenMarkdown), "用户说：支付前先问我。") {
		t.Fatalf("expected Forget to preserve evidence excerpt, got:\n%s", forgottenMarkdown)
	}
	results, err := store.Search(ctx, MemoryQuery{Tags: []string{"支付"}, Limit: 5})
	if err != nil {
		t.Fatalf("Search() error = %v", err)
	}
	if len(results) != 0 {
		t.Fatalf("expected deleted memory to be excluded, got %#v", results)
	}

	tombstoneData, err := os.ReadFile(filepath.Join(store.RootDir(), "..", "lifecycle", "tombstones.jsonl"))
	if err != nil {
		t.Fatalf("read tombstones: %v", err)
	}
	if !strings.Contains(string(tombstoneData), "mem_forget_me") || !strings.Contains(string(tombstoneData), "user_request") {
		t.Fatalf("expected tombstone for forgotten memory, got %s", tombstoneData)
	}
}
