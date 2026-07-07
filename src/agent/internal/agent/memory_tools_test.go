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

	tool := NewRecallSessionChunksTool(session, nil)
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

func TestRecallSessionChunksToolIgnoresAppNameInput(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	session := NewSessionMemoryStore(filepath.Join(root, "session"))

	if _, err := session.AppendEvent(ctx, SessionEvent{EventID: "evt_mail", Type: "user_input", Role: "user", Content: "打开邮件", AppName: "Mail"}); err != nil {
		t.Fatalf("AppendEvent() mail error = %v", err)
	}
	if _, err := session.Compress(ctx, CompressOption{ChunkID: "chunk_mail", Tags: []string{"navigation"}, Summary: "Mail navigation"}); err != nil {
		t.Fatalf("Compress() mail error = %v", err)
	}
	if err := session.replaceEvents(nil); err != nil {
		t.Fatalf("replaceEvents() after mail error = %v", err)
	}
	if _, err := session.AppendEvent(ctx, SessionEvent{EventID: "evt_calendar", Type: "user_input", Role: "user", Content: "打开日历", AppName: "Calendar"}); err != nil {
		t.Fatalf("AppendEvent() calendar error = %v", err)
	}
	if _, err := session.Compress(ctx, CompressOption{ChunkID: "chunk_calendar", Tags: []string{"navigation"}, Summary: "Calendar navigation"}); err != nil {
		t.Fatalf("Compress() calendar error = %v", err)
	}

	tool := NewRecallSessionChunksTool(session, nil)
	out, err := tool.Call(ctx, `{"app_name":"Mail","limit":10}`)
	if err != nil {
		t.Fatalf("Call() error = %v", err)
	}

	var decoded struct {
		Results []struct {
			ChunkID string `json:"chunk_id"`
		} `json:"results"`
	}
	if err := json.Unmarshal([]byte(out), &decoded); err != nil {
		t.Fatalf("decode output %q: %v", out, err)
	}
	if len(decoded.Results) != 2 {
		t.Fatalf("app_name input should not filter session chunks, got %#v", decoded.Results)
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
	tools.RegisterMemoryTools(t.TempDir(), nil, 0, nil)
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

func TestRecallToolDescriptionsGuideAgentUsage(t *testing.T) {
	sessionDescription := NewRecallSessionChunksTool(NewSessionMemoryStore(t.TempDir()), nil).Description()
	for _, want := range []string{
		"prior conversation content",
		"visible context",
		"archived chunks",
		"chunk_ids",
		"PREFERRED",
		"FALLBACK",
	} {
		if !strings.Contains(sessionDescription, want) {
			t.Fatalf("recall_session_chunks description missing %q:\n%s", want, sessionDescription)
		}
	}

	memoryDescription := NewRecallMemoryTool(NewLongTermMemoryStore(t.TempDir())).Description()
	for _, want := range []string{
		"long-term",
		"recall_session_chunks",
		`"tags":["verification"`,
		"topic/domain keywords",
		"preference",
		"rule",
		"procedure",
	} {
		if !strings.Contains(memoryDescription, want) {
			t.Fatalf("recall_memory description missing %q:\n%s", want, memoryDescription)
		}
	}

	saveDescription := NewSaveMemoryTool(NewLongTermMemoryStore(t.TempDir())).Description()
	for _, want := range []string{
		"Mandatory when the user asks to remember/save",
		"status=saved or status=ignored as a duplicate",
		"Use profile for durable user facts",
		"location, timezone",
		"synthesized user profile",
		"preference",
	} {
		if !strings.Contains(saveDescription, want) {
			t.Fatalf("save_memory description missing %q:\n%s", want, saveDescription)
		}
	}
}
