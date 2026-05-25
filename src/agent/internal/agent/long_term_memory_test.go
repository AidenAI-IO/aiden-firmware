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
