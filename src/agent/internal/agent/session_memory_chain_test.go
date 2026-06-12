package agent

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
)

func TestFilesystemMemoryChainCompressRecallAndLongTermSearch(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	session := NewSessionMemoryStore(filepath.Join(root, "session"))

	firstID, err := session.AppendEvent(ctx, SessionEvent{
		Type:    "user_input",
		Role:    "user",
		Content: "帮我看看这个登录失败是什么意思",
		AppName: "某政务App",
	})
	if err != nil {
		t.Fatalf("AppendEvent() first error = %v", err)
	}
	if _, err := session.AppendEvent(ctx, SessionEvent{
		Type:    "screen_context",
		Role:    "screen",
		Content: "页面提示：验证码已过期，请重新获取。",
		AppName: "某政务App",
	}); err != nil {
		t.Fatalf("AppendEvent() screen error = %v", err)
	}
	if _, err := session.AppendEvent(ctx, SessionEvent{
		Type:    "user_input",
		Role:    "user",
		Content: "记一下，这个 App 下次登录要先重新获取验证码。",
		AppName: "某政务App",
	}); err != nil {
		t.Fatalf("AppendEvent() memory candidate error = %v", err)
	}

	chunk, err := session.Compress(ctx, CompressOption{
		Summary:  "用户在某政务 App 登录页遇到验证码过期，并要求下次先重新获取验证码。",
		Tags:     []string{"登录", "验证码"},
		Entities: []string{"某政务App"},
	})
	if err != nil {
		t.Fatalf("Compress() error = %v", err)
	}
	if !strings.HasPrefix(chunk.ID, "chunk_") || chunk.EventCount != 3 {
		t.Fatalf("unexpected chunk summary: %#v", chunk)
	}

	recalled, err := session.RecallChunks(ctx, ChunkRecallQuery{Tags: []string{"验证码"}, Entities: []string{"某政务App"}, Limit: 1})
	if err != nil {
		t.Fatalf("RecallChunks() error = %v", err)
	}
	if len(recalled) != 1 {
		t.Fatalf("expected 1 recalled chunk, got %d", len(recalled))
	}
	if recalled[0].ChunkID != chunk.ID || len(recalled[0].Evidence) != 3 {
		t.Fatalf("unexpected recalled chunk: %#v", recalled[0])
	}
	if recalled[0].Evidence[0].EventID != firstID {
		t.Fatalf("expected recalled evidence to preserve event ids, got %#v", recalled[0].Evidence[0])
	}

	longTerm := NewLongTermMemoryStore(filepath.Join(root, "long_term"))
	if _, err := longTerm.AddMemory(ctx, MemoryItem{
		ID:         "mem_login_preference",
		Type:       "preference",
		Priority:   80,
		Confidence: 0.9,
		Tags:       []string{"登录", "验证码"},
		Entities:   []string{"某政务App"},
		Title:      "某政务 App 验证码登录偏好",
		Content:    "用户下次登录某政务 App 时，应先重新获取验证码，不主动切换到密码登录。",
		EvidenceExcerpts: []string{
			"用户说：记一下，这个 App 下次登录要先重新获取验证码。",
			"屏幕显示：页面提示验证码已过期，请重新获取。",
		},
		SourceRefs: []MemorySourceRef{
			{Type: "chunk", ID: recalled[0].ChunkID, EventIDs: []string{recalled[0].Evidence[0].EventID}},
		},
	}); err != nil {
		t.Fatalf("AddMemory() error = %v", err)
	}

	memories, err := longTerm.Search(ctx, MemoryQuery{Tags: []string{"验证码"}, Entities: []string{"某政务App"}, Limit: 3})
	if err != nil {
		t.Fatalf("Search() error = %v", err)
	}
	if len(memories) != 1 {
		t.Fatalf("expected 1 long-term memory, got %d", len(memories))
	}
	if memories[0].ID != "mem_login_preference" || !strings.Contains(memories[0].Content, "重新获取验证码") {
		t.Fatalf("unexpected long-term memory result: %#v", memories[0])
	}
}
