package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

type RecallSessionChunksTool struct {
	store *SessionMemoryStore
}

type RecallMemoryTool struct {
	store *LongTermMemoryStore
}

type SaveMemoryTool struct {
	store *LongTermMemoryStore
}

type ForgetMemoryTool struct {
	store *LongTermMemoryStore
}

func NewRecallSessionChunksTool(store *SessionMemoryStore) *RecallSessionChunksTool {
	return &RecallSessionChunksTool{store: store}
}

func (t *RecallSessionChunksTool) Name() string { return "recall_session_chunks" }

func (t *RecallSessionChunksTool) Description() string {
	return strings.Join([]string{
		"Recall compressed session history chunks from earlier in this conversation.",
		"IMPORTANT: You MUST use this tool when the user asks about something said earlier that you cannot find in your visible conversation context.",
		"Triggers: '刚才说的', '前面提到', '之前聊的', '我最开始说', '你还记得吗', '回忆一下', '我们聊过什么', or any reference to earlier conversation content.",
		"If you cannot find the answer in your visible history, call this tool BEFORE saying you don't remember.",
		"Do not use for stable long-term preferences, rules, or procedures; use recall_memory for those.",
		`Input JSON: {"tags":["topic_keyword"],"entities":["AppName"],"limit":3}`,
		"How to choose tags:",
		"  - tags should be CONTENT/TOPIC keywords from the conversation, like '支付','登录','报销','验证码' — NOT time words like '最开始','最初','刚才','回忆'.",
		"  - When the user asks about a topic ('我们聊过登录吗'), pass that topic in tags.",
		"  - When the user asks about earliest/recent history with NO specific topic ('最开始聊了什么','回忆一下'), pass empty tags []  and rely on limit — chunks are returned newest-first.",
		"  - When unsure what topic to pass, prefer empty tags [] over guessing — empty returns recent chunks; wrong guesses return nothing.",
		"Returns JSON with matching conversation chunks and original event evidence.",
	}, " ")
}

func (t *RecallSessionChunksTool) Call(ctx context.Context, input string) (string, error) {
	if t.store == nil {
		return "", fmt.Errorf("session memory store is not configured")
	}
	var query ChunkRecallQuery
	if err := json.Unmarshal([]byte(input), &query); err != nil {
		return "", fmt.Errorf("decode recall_session_chunks input: %w", err)
	}
	results, err := t.store.RecallChunks(ctx, query)
	if err != nil {
		return "", err
	}
	return encodeToolJSON(map[string]any{"results": results})
}

func NewRecallMemoryTool(store *LongTermMemoryStore) *RecallMemoryTool {
	return &RecallMemoryTool{store: store}
}

func (t *RecallMemoryTool) Name() string { return "recall_memory" }

func (t *RecallMemoryTool) Description() string {
	return strings.Join([]string{
		"Recall long-term filesystem memories.",
		"Use when the user asks about remembered preferences, rules, procedures, facts, failure lessons, or says '以后', '下次', '按我的习惯', '你记得吗'.",
		"Do not use for raw recent session details or compressed conversation evidence; use recall_session_chunks for those.",
		`Input JSON: {"tags":["验证码","登录"],"entities":["某政务App"],"types":["preference"],"limit":5}`,
		"How to choose filters:",
		"  - tags: TOPIC/DOMAIN keywords related to the memory content (e.g., '验证码','支付','报销'). Leave empty [] to match all.",
		"  - entities: Specific named things (apps, accounts, services, people). Leave empty [] to match all.",
		"  - types: Memory categories — preference (user likes/dislikes), rule (must/must-not), procedure (how-to steps), fact (stable info), profile (user background). Leave empty [] to match all.",
		"  - limit: Max results to return (default 5).",
		"Returns JSON: {\"results\":[{\"id\":\"...\",\"type\":\"preference\",\"title\":\"...\",\"content\":\"...\",\"summary\":\"...\"}]}.",
	}, " ")
}

func (t *RecallMemoryTool) Call(ctx context.Context, input string) (string, error) {
	if t.store == nil {
		return "", fmt.Errorf("long-term memory store is not configured")
	}
	var query MemoryQuery
	if err := json.Unmarshal([]byte(input), &query); err != nil {
		return "", fmt.Errorf("decode recall_memory input: %w", err)
	}
	results, err := t.store.Search(ctx, query)
	if err != nil {
		return "", err
	}
	return encodeToolJSON(map[string]any{"results": results})
}

func encodeToolJSON(value any) (string, error) {
	data, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

func NewSaveMemoryTool(store *LongTermMemoryStore) *SaveMemoryTool {
	return &SaveMemoryTool{store: store}
}

func (t *SaveMemoryTool) Name() string { return "save_memory" }

func (t *SaveMemoryTool) Description() string {
	return strings.Join([]string{
		"Save a long-term memory for future recall.",
		"Use when user explicitly asks to remember something: '记一下', '你要记住', '以后要', '下次记得'.",
		"Also use when you observe a stable user preference, rule, or procedure worth persisting.",
		`Input JSON: {"type":"preference","title":"short title","content":"what to remember","tags":["tag1"],"entities":["AppName"],"evidence":["exact user quote"],"priority":80}`,
		"How to choose fields:",
		"  - type: preference (user likes/dislikes), rule (must/must-not), procedure (how-to steps), fact (stable info), profile (user role/background).",
		"  - tags: TOPIC/DOMAIN keywords for future search (e.g., '验证码','支付','报销'). NOT time words or vague terms.",
		"  - entities: Specific named things mentioned (apps, accounts, services, people).",
		"  - evidence: Original user quotes or context that led to this memory. Helps verify relevance later.",
		"  - priority: 0-100, higher = more important. Use 80+ for user-stated rules/preferences, 60+ for inferred patterns, 40+ for observations.",
		"Returns the saved memory ID or indicates the memory was deduplicated.",
	}, " ")
}

func (t *SaveMemoryTool) Call(ctx context.Context, input string) (string, error) {
	if t.store == nil {
		return "", fmt.Errorf("long-term memory store is not configured")
	}
	var req struct {
		Type     string   `json:"type"`
		Title    string   `json:"title"`
		Content  string   `json:"content"`
		Tags     []string `json:"tags"`
		Entities []string `json:"entities"`
		Evidence []string `json:"evidence"`
		Priority int      `json:"priority"`
	}
	if err := json.Unmarshal([]byte(input), &req); err != nil {
		return "", fmt.Errorf("decode save_memory input: %w", err)
	}
	if strings.TrimSpace(req.Content) == "" {
		return "", fmt.Errorf("content is required")
	}
	if len(req.Evidence) == 0 {
		req.Evidence = []string{req.Content}
	}
	if req.Priority <= 0 {
		req.Priority = 80
	}
	item := MemoryItem{
		Type:             req.Type,
		Priority:         req.Priority,
		Confidence:       0.9,
		Title:            req.Title,
		Content:          req.Content,
		Tags:             req.Tags,
		Entities:         req.Entities,
		EvidenceExcerpts: req.Evidence,
	}
	action, existingID, err := t.store.DecideAction(ctx, item)
	if err != nil {
		return "", err
	}
	var id string
	switch action {
	case "ignore":
		return encodeToolJSON(map[string]string{"status": "ignored", "reason": "duplicate of " + existingID})
	case "supersede":
		id, err = t.store.SupersedeMemory(ctx, existingID, item)
	default:
		id, err = t.store.AddMemory(ctx, item)
	}
	if err != nil {
		return "", err
	}
	_ = t.store.RegenerateProfileMD(ctx)
	return encodeToolJSON(map[string]string{"status": "saved", "id": id})
}

func NewForgetMemoryTool(store *LongTermMemoryStore) *ForgetMemoryTool {
	return &ForgetMemoryTool{store: store}
}

func (t *ForgetMemoryTool) Name() string { return "forget_memory" }

func (t *ForgetMemoryTool) Description() string {
	return strings.Join([]string{
		"Forget (delete) a long-term memory.",
		"Use when user says '忘掉', '删掉这条记忆', '不用记了', or asks to remove a previously saved memory.",
		"First use recall_memory to find the memory ID, then call this tool.",
		`Input JSON: {"id":"mem_xxx","reason":"user requested"}`,
		"Returns confirmation of deletion.",
	}, " ")
}

func (t *ForgetMemoryTool) Call(ctx context.Context, input string) (string, error) {
	if t.store == nil {
		return "", fmt.Errorf("long-term memory store is not configured")
	}
	var req struct {
		ID     string `json:"id"`
		Reason string `json:"reason"`
	}
	if err := json.Unmarshal([]byte(input), &req); err != nil {
		return "", fmt.Errorf("decode forget_memory input: %w", err)
	}
	if strings.TrimSpace(req.ID) == "" {
		return "", fmt.Errorf("memory id is required")
	}
	if req.Reason == "" {
		req.Reason = "user requested"
	}
	if err := t.store.Forget(ctx, req.ID, req.Reason); err != nil {
		return "", err
	}
	return encodeToolJSON(map[string]string{"status": "deleted", "id": req.ID})
}
