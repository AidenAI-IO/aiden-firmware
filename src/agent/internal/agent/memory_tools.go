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

func NewRecallSessionChunksTool(store *SessionMemoryStore) *RecallSessionChunksTool {
	return &RecallSessionChunksTool{store: store}
}

func (t *RecallSessionChunksTool) Name() string { return "recall_session_chunks" }

func (t *RecallSessionChunksTool) Description() string {
	return strings.Join([]string{
		"Recall compressed short-term session chunks from filesystem memory.",
		"Use when the user refers to recent or current session history that is no longer in the visible conversation, such as '刚才', '前面', '上次这个任务', or needs evidence from compressed session context.",
		"Do not use for stable long-term preferences, rules, or procedures; use recall_memory for those.",
		`Action Input must be a JSON object string, for example: {"tags":["验证码"],"app_name":"某政务App","limit":3}`,
		"Fields: tags filters chunk tags; entities filters named apps/accounts/objects; app_name filters the current app; limit defaults to 3.",
		"Returns JSON: {\"results\":[{\"chunk_id\":\"...\",\"summary\":\"...\",\"evidence\":[session events]}]}.",
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
		`Action Input must be a JSON object string, for example: {"tags":["验证码"],"entities":["某政务App"],"types":["preference"],"limit":5}`,
		"Fields: tags and entities filter relevance; types can include preference, rule, procedure, fact, failure_lesson; limit defaults to 5.",
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
