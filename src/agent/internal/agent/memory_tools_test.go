package agent

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
)

func TestRecallSessionChunksToolReturnsMatchingEvidence(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	session := NewSessionMemoryStore(filepath.Join(root, "session"))
	if _, err := session.AppendEvent(ctx, SessionEvent{EventID: "evt_1", Type: "user_input", Role: "user", Content: "登录失败", AppName: "某政务App"}); err != nil {
		t.Fatalf("AppendEvent() error = %v", err)
	}
	if _, err := session.AppendEvent(ctx, SessionEvent{EventID: "evt_2", Type: "screen_context", Role: "screen", Content: "验证码已过期", AppName: "某政务App"}); err != nil {
		t.Fatalf("AppendEvent() error = %v", err)
	}
	if _, err := session.Compress(ctx, CompressOption{Tags: []string{"登录", "验证码"}, Entities: []string{"某政务App"}, Summary: "验证码过期登录场景"}); err != nil {
		t.Fatalf("Compress() error = %v", err)
	}

	tool := NewRecallSessionChunksTool(session)
	out, err := tool.Call(ctx, `{"tags":["验证码"],"app_name":"某政务App","limit":1}`)
	if err != nil {
		t.Fatalf("Call() error = %v", err)
	}

	var decoded struct {
		Results []struct {
			ChunkID  string         `json:"chunk_id"`
			Summary  string         `json:"summary"`
			Evidence []SessionEvent `json:"evidence"`
		} `json:"results"`
	}
	if err := json.Unmarshal([]byte(out), &decoded); err != nil {
		t.Fatalf("decode output %q: %v", out, err)
	}
	if len(decoded.Results) != 1 {
		t.Fatalf("expected 1 result, got %#v", decoded.Results)
	}
	if !strings.HasPrefix(decoded.Results[0].ChunkID, "chunk_") || len(decoded.Results[0].Evidence) != 2 {
		t.Fatalf("unexpected recall result: %#v", decoded.Results[0])
	}
	if decoded.Results[0].Evidence[1].Content != "验证码已过期" {
		t.Fatalf("expected screen evidence, got %#v", decoded.Results[0].Evidence)
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

func TestToolSetRegistersMemoryRecallTools(t *testing.T) {
	tools := NewBuiltinToolSet(HIDConfig{}, AudioConfig{}, SearchConfig{}, ProxyConfig{})
	tools.RegisterMemoryTools(t.TempDir())
	if _, ok := tools.Get("recall_session_chunks"); !ok {
		t.Fatalf("expected recall_session_chunks tool to be registered")
	}
	if _, ok := tools.Get("recall_memory"); !ok {
		t.Fatalf("expected recall_memory tool to be registered")
	}
}

func TestRecallToolDescriptionsGuideAgentUsage(t *testing.T) {
	sessionDescription := NewRecallSessionChunksTool(NewSessionMemoryStore(t.TempDir())).Description()
	for _, want := range []string{
		"MUST use this tool",
		"earlier in this conversation",
		"Do not use",
		`{"tags":["topic_keyword"]`,
		"How to choose tags",
		"CONTENT/TOPIC keywords",
		"empty tags []",
		"evidence",
	} {
		if !strings.Contains(sessionDescription, want) {
			t.Fatalf("recall_session_chunks description missing %q:\n%s", want, sessionDescription)
		}
	}

	memoryDescription := NewRecallMemoryTool(NewLongTermMemoryStore(t.TempDir())).Description()
	for _, want := range []string{
		"Use when",
		"long-term",
		"Do not use",
		`"tags":["verification"`,
		`"entities":["AppName"]`,
		"How to choose filters",
		"TOPIC/DOMAIN keywords",
		"preference",
		"rule",
		"procedure",
	} {
		if !strings.Contains(memoryDescription, want) {
			t.Fatalf("recall_memory description missing %q:\n%s", want, memoryDescription)
		}
	}
}
