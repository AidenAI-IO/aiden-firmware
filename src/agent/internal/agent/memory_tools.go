package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

type RecallSessionChunksTool struct {
	store    *SessionMemoryStore
	archived *ArchivedSessionStore
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

func NewRecallSessionChunksTool(store *SessionMemoryStore, archived *ArchivedSessionStore) *RecallSessionChunksTool {
	return &RecallSessionChunksTool{store: store, archived: archived}
}

func (t *RecallSessionChunksTool) Name() string { return "recall_session_chunks" }

func (t *RecallSessionChunksTool) Description() string {
	return strings.Join([]string{
		"Recall compressed session history chunks from earlier in this conversation, and optionally from previous sessions.",
		"IMPORTANT: You MUST use this tool when the user asks about something said earlier that you cannot find in your visible conversation context.",
		"The session summary in your prompt lists recent chunks; older chunks are archived but still recallable.",
		"How to recall:",
		"  - PREFERRED: pass chunk_ids to retrieve specific chunks by ID (works for both active and archived chunks).",
		"  - FALLBACK: pass tags (content/topic keywords like 'payment', 'login') to search all chunks. Use empty tags [] for recent history.",
		"  - Set include_archived=true when the user references prior conversations. Trigger words include any English or non-English equivalents of: 'yesterday', 'last time', 'earlier today', 'before', 'previously', 'recently', 'the other day', 'just now', 'a moment ago'.",
		"  - RETRY RULE: If you search the current session (include_archived=false) and find no matching chunks, you MUST immediately retry with include_archived=true before telling the user you cannot find it. Sessions rotate after ~30 minutes of inactivity, so 'just now' references may already be in an archived session.",
		"  - archived_time_range controls how far back to search archived sessions: 'last_24h' (default), 'last_7d', or 'all'. Start with last_24h; widen only if the user references older content.",
		`Input JSON: {"chunk_ids":["chunk_xxx"]} or {"tags":["topic"],"limit":3,"include_archived":true,"archived_time_range":"last_24h"}`,
		"Returns JSON with matching conversation chunks (each tagged with source=active|archived and session_id when archived) and their full original events.",
	}, " ")
}

func (t *RecallSessionChunksTool) ArgsSchema() map[string]any {
	return objectArgsSchema(map[string]any{
		"chunk_ids":           stringArrayArgSchema("Specific session chunk ids to retrieve."),
		"tags":                stringArrayArgSchema("Topic keywords to search when chunk_ids are not known."),
		"entities":            stringArrayArgSchema("Named entities to search for."),
		"limit":               minIntegerArgSchema("Maximum number of chunks to return.", 1),
		"include_archived":    boolArgSchema("Also search closed sessions stored under session_archive/. Default false."),
		"archived_time_range": stringEnumArgSchema("How far back to search archived sessions.", archivedTimeRangeLast24h, archivedTimeRangeLast7d, archivedTimeRangeAll),
	})
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
	for i := range results {
		if results[i].Source == "" {
			results[i].Source = chunkRecallSourceActive
		}
	}

	if query.IncludeArchived && t.archived != nil {
		remaining := query.Limit
		if remaining > 0 {
			remaining -= len(results)
		}
		if query.Limit <= 0 || remaining > 0 {
			archiveQuery := query
			if query.Limit > 0 {
				archiveQuery.Limit = remaining
			} else {
				// Apply the same default cap used by active recall (3) to
				// prevent unbounded archived chunk scanning.
				archiveQuery.Limit = 3
			}
			archived, err := t.archived.RecallChunks(ctx, archiveQuery)
			if err == nil {
				results = append(results, archived...)
			}
			// Archive search failures are non-fatal: a missing or partial
			// archive directory shouldn't prevent the agent from getting
			// active-session results back.
		}
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

func (t *RecallMemoryTool) ArgsSchema() map[string]any {
	return objectArgsSchema(map[string]any{
		"tags":     stringArrayArgSchema("Topic or domain keywords."),
		"entities": stringArrayArgSchema("Specific named things such as apps, accounts, services, or people."),
		"types":    stringArrayArgSchema("Memory categories such as preference, rule, procedure, fact, or profile."),
		"limit":    minIntegerArgSchema("Maximum number of memories to return.", 1),
	})
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
		"Mandatory use: when the user explicitly asks you to remember, save, record, keep in mind, or use something as a future default, call save_memory before saying it is remembered or saved.",
		"Do not claim a memory was remembered or saved until this tool returns saved or ignored as a duplicate.",
		"Also use when you observe a stable user preference, rule, or procedure worth persisting.",
		`Input JSON: {"type":"preference","title":"short title","content":"what to remember","tags":["tag1"],"entities":["AppName"],"evidence":["exact user quote"],"priority":80}`,
		"How to choose fields:",
		"  - type: preference (user likes/dislikes), rule (must/must-not), procedure (how-to steps), fact (stable info), profile (user role/background).",
		"  - Use type=profile for durable user profile facts such as name, nickname, location, home city, timezone, role, or background so they appear in the synthesized user profile.",
		"  - Use type=preference or type=rule for future defaults such as the user's default city for weather queries.",
		"  - Use type=fact only for stable facts that should be recalled but do not need to appear in the synthesized user profile.",
		"  - tags: TOPIC/DOMAIN keywords for future search (e.g., 'verification', 'payment', 'expense'). NOT time words or vague terms.",
		"  - entities: Specific named things mentioned (apps, accounts, services, people).",
		"  - evidence: Original user quotes or context that led to this memory. Helps verify relevance later.",
		"  - priority: 1-100, higher = more important. Use 80+ for user-stated rules/preferences, 60+ for inferred patterns, 40+ for observations.",
		"Returns the saved memory ID or indicates the memory was deduplicated.",
	}, " ")
}

func (t *SaveMemoryTool) ArgsSchema() map[string]any {
	return objectArgsSchema(map[string]any{
		"type":     stringEnumArgSchema("Memory category.", "preference", "rule", "procedure", "fact", "profile"),
		"title":    stringArgSchema("Short title for the memory."),
		"content":  stringArgSchema("The stable information to remember."),
		"tags":     stringArrayArgSchema("Topic or domain keywords for future search."),
		"entities": stringArrayArgSchema("Specific named things mentioned by the memory."),
		"evidence": stringArrayArgSchema("Original quotes or observations that support this memory."),
		"priority": rangedIntegerArgSchema("Importance from 1 to 100.", 1, 100),
	}, "type", "content")
}

func (t *SaveMemoryTool) Call(ctx context.Context, input string) (string, error) {
	if t.store == nil {
		return "", fmt.Errorf("long-term memory store is not configured")
	}
	req, err := decodeSaveMemoryRequest(input)
	if err != nil {
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

func (t *ForgetMemoryTool) ArgsSchema() map[string]any {
	return objectArgsSchema(map[string]any{
		"id":     stringArgSchema("Memory id to delete."),
		"reason": stringArgSchema("Reason for deleting the memory."),
	}, "id")
}

func (t *ForgetMemoryTool) Call(ctx context.Context, input string) (string, error) {
	if t.store == nil {
		return "", fmt.Errorf("long-term memory store is not configured")
	}
	req, err := decodeForgetMemoryRequest(input)
	if err != nil {
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

func (t *RecallDeviceMemoryTool) ArgsSchema() map[string]any {
	return objectArgsSchema(map[string]any{
		"terms":     stringArrayArgSchema("Free-text search terms."),
		"tags":      stringArrayArgSchema("Topic tags to match."),
		"entities":  stringArrayArgSchema("Named apps, devices, accounts, or UI concepts to match."),
		"types":     stringArrayArgSchema("Device memory categories such as procedure, failure, calibration, or conflict."),
		"device_id": stringArgSchema("Device id to filter by; omit for default search."),
		"limit":     minIntegerArgSchema("Maximum number of device memories to return.", 1),
	})
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

func (t *InspectEpisodeTool) ArgsSchema() map[string]any {
	return objectArgsSchema(map[string]any{
		"id": stringArgSchema("Stored task episode id to inspect."),
	}, "id")
}

func (t *InspectEpisodeTool) Call(ctx context.Context, input string) (string, error) {
	if t.store == nil {
		return "", fmt.Errorf("episode store is not configured")
	}
	req, err := decodeInspectEpisodeRequest(input)
	if err != nil {
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
