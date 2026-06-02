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

type RecallDeviceMemoryTool struct {
	store *DeviceMemoryStore
}

type InspectEpisodeTool struct {
	store *TaskEpisodeStore
}

func NewRecallSessionChunksTool(store *SessionMemoryStore) *RecallSessionChunksTool {
	return &RecallSessionChunksTool{store: store}
}

func (t *RecallSessionChunksTool) Name() string { return "recall_session_chunks" }

func (t *RecallSessionChunksTool) Description() string {
	return strings.Join([]string{
		"Recall compressed session history chunks from earlier in this conversation.",
		"IMPORTANT: You MUST use this tool when the user asks about something said earlier that you cannot find in your visible conversation context.",
		"The session summary in your prompt lists recent chunks; older chunks are archived but still recallable.",
		"How to recall:",
		"  - PREFERRED: pass chunk_ids to retrieve specific chunks by ID (works for both active and archived chunks).",
		"  - FALLBACK: pass tags (content/topic keywords like 'payment', 'login') to search all chunks. Use empty tags [] for recent history.",
		`Input JSON: {"chunk_ids":["chunk_xxx"]} or {"tags":["topic"],"limit":3}`,
		"Returns JSON with matching conversation chunks and their full original events.",
	}, " ")
}

func (t *RecallSessionChunksTool) Call(ctx context.Context, input string) (string, error) {
	if t.store == nil {
		return "", fmt.Errorf("session memory store is not configured")
	}
	query, err := decodeChunkRecallQuery(input)
	if err != nil {
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
		"Use when the user asks about remembered preferences, rules, procedures, facts, or failure lessons.",
		"Do not use for raw recent session details or compressed conversation evidence; use recall_session_chunks for those.",
		`Input JSON: {"tags":["verification","login"],"entities":["AppName"],"types":["preference"],"limit":5}`,
		"How to choose filters:",
		"  - tags: TOPIC/DOMAIN keywords related to the memory content (e.g., 'verification', 'payment', 'expense'). Leave empty [] to match all.",
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
	query, err := decodeMemoryQuery(input)
	if err != nil {
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
		"Use when user explicitly asks to remember something, or when you observe a stable user preference, rule, or procedure worth persisting.",
		`Input JSON: {"type":"preference","title":"short title","content":"what to remember","tags":["tag1"],"entities":["AppName"],"evidence":["exact user quote"],"priority":80}`,
		"How to choose fields:",
		"  - type: preference (user likes/dislikes), rule (must/must-not), procedure (how-to steps), fact (stable info), profile (user role/background).",
		"  - tags: TOPIC/DOMAIN keywords for future search (e.g., 'verification', 'payment', 'expense'). NOT time words or vague terms.",
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
	t.store.RequestProfileRebuild()
	return encodeToolJSON(map[string]string{"status": "saved", "id": id})
}

func NewForgetMemoryTool(store *LongTermMemoryStore) *ForgetMemoryTool {
	return &ForgetMemoryTool{store: store}
}

func (t *ForgetMemoryTool) Name() string { return "forget_memory" }

func (t *ForgetMemoryTool) Description() string {
	return strings.Join([]string{
		"Forget (delete) a long-term memory.",
		"Use when user asks to remove a previously saved memory.",
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

func NewRecallDeviceMemoryTool(store *DeviceMemoryStore) *RecallDeviceMemoryTool {
	return &RecallDeviceMemoryTool{store: store}
}

func (t *RecallDeviceMemoryTool) Name() string { return "recall_device_memory" }

func (t *RecallDeviceMemoryTool) Description() string {
	return strings.Join([]string{
		"Debug recall for device memory: device profiles, app profiles, procedures, calibration notes, failures, and conflicts.",
		"The runtime automatically retrieves relevant device memory before planning; use this tool only when inspecting memory state is explicitly useful.",
		`Input JSON: {"terms":["微信"],"tags":["登录"],"entities":["微信App"],"types":["procedure","failure"],"device_id":"default","limit":5}`,
	}, " ")
}

func (t *RecallDeviceMemoryTool) Call(ctx context.Context, input string) (string, error) {
	if t.store == nil {
		return "", fmt.Errorf("device memory store is not configured")
	}
	query, err := decodeDeviceMemoryQuery(input)
	if err != nil {
		return "", fmt.Errorf("decode recall_device_memory input: %w", err)
	}
	results, err := t.store.Search(ctx, query)
	if err != nil {
		return "", err
	}
	return encodeToolJSON(map[string]any{"results": results})
}

func NewInspectEpisodeTool(store *TaskEpisodeStore) *InspectEpisodeTool {
	return &InspectEpisodeTool{store: store}
}

func (t *InspectEpisodeTool) Name() string { return "inspect_episode" }

func (t *InspectEpisodeTool) Description() string {
	return strings.Join([]string{
		"Debug inspect a stored task episode by ID, including metadata and compact event trace.",
		"The runtime writes task episodes automatically after runs; use this only for memory debugging or explicit user requests.",
		`Input JSON: {"id":"ep_..."}`,
	}, " ")
}

func (t *InspectEpisodeTool) Call(ctx context.Context, input string) (string, error) {
	if t.store == nil {
		return "", fmt.Errorf("episode store is not configured")
	}
	var req struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal([]byte(input), &req); err != nil {
		return "", fmt.Errorf("decode inspect_episode input: %w", err)
	}
	if strings.TrimSpace(req.ID) == "" {
		return "", fmt.Errorf("episode id is required")
	}
	episode, err := t.store.Get(ctx, req.ID)
	if err != nil {
		return "", err
	}
	return encodeToolJSON(map[string]any{"episode": episode})
}
