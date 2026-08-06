package messages

import (
	"encoding/json"

	"github.com/tmc/langchaingo/llms"
)

type MessageRole string

const (
	MessageRoleUser       MessageRole = "user"
	MessageRoleAssistant  MessageRole = "assistant"
	MessageRoleToolCall   MessageRole = "tool_call"
	MessageRoleToolResult MessageRole = "tool_result"
	MessageRoleState      MessageRole = "state"
	MessageRoleSystem     MessageRole = "system"
	MessageRoleNotice     MessageRole = "notice"
)

func (r MessageRole) ToStandardRole() llms.ChatMessageType {
	switch r {
	case MessageRoleUser:
		return llms.ChatMessageTypeHuman
	case MessageRoleAssistant:
		return llms.ChatMessageTypeAI
	case MessageRoleToolCall:
		return llms.ChatMessageTypeAI
	case MessageRoleToolResult:
		return llms.ChatMessageTypeTool
	case MessageRoleState:
		return llms.ChatMessageTypeHuman
	case MessageRoleSystem:
		return llms.ChatMessageTypeSystem
	case MessageRoleNotice:
		return llms.ChatMessageTypeHuman
	default:
		return llms.ChatMessageTypeHuman
	}
}

type Message struct {
	Role                   MessageRole             `json:"role"`
	Content                string                  `json:"content"`
	ToolCalls              []ToolCall              `json:"tool_calls,omitempty"`
	ToolResults            []ToolResult            `json:"tool_results,omitempty"`
	RecoverableToolResults []RecoverableToolResult `json:"recoverable_tool_results,omitempty"`
	Attachments            []Attachment            `json:"attachments,omitempty"`
}

func (msg Message) Clone() Message {
	cloned := msg
	if len(msg.ToolCalls) > 0 {
		cloned.ToolCalls = append([]ToolCall(nil), msg.ToolCalls...)
	}
	if len(msg.ToolResults) > 0 {
		cloned.ToolResults = append([]ToolResult(nil), msg.ToolResults...)
		for i := range cloned.ToolResults {
			if msg.ToolResults[i].Meta == nil {
				continue
			}
			meta := *msg.ToolResults[i].Meta
			cloned.ToolResults[i].Meta = &meta
		}
	}
	if len(msg.RecoverableToolResults) > 0 {
		cloned.RecoverableToolResults = append([]RecoverableToolResult(nil), msg.RecoverableToolResults...)
	}
	if len(msg.Attachments) > 0 {
		cloned.Attachments = append([]Attachment(nil), msg.Attachments...)
	}
	return cloned
}

// Attachment tracks file metadata for message attachments. Binary content is stored on disk
// and only loaded when ConvertToStandardMessageList is called.
type Attachment struct {
	MIMEType string `json:"mime_type"`
	FileSize int64  `json:"file_size"`
	FilePath string `json:"file_path"`
	Source   string `json:"source,omitempty"`
}

type ToolCall struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

type ToolResult struct {
	ToolCallID string          `json:"tool_call_id"`
	Name       string          `json:"name"`
	Content    string          `json:"content"`
	Meta       *ToolResultMeta `json:"meta,omitempty"`
}

type ToolResultMeta struct {
	ArtifactPath        string `json:"artifact_path,omitempty"`
	OriginalBytes       int64  `json:"original_bytes,omitempty"`
	OriginalChars       int    `json:"original_chars,omitempty"`
	EstimatedTokens     int    `json:"estimated_tokens,omitempty"`
	Complete            bool   `json:"complete"`
	ArtifactComplete    bool   `json:"artifact_complete"`
	Reason              string `json:"reason,omitempty"`
	Summary             string `json:"summary,omitempty"`
	ActionCompleted     bool   `json:"action_completed,omitempty"`
	ObservationComplete bool   `json:"observation_complete,omitempty"`
	ProcessingErrorCode string `json:"processing_error_code,omitempty"`
	ArtifactStoreError  string `json:"artifact_store_error,omitempty"`
}

// RecoverableToolResult carries trusted recovery metadata across compaction
// revisions. It is persisted with the context message but is not sent to the
// model separately from the bounded recovery block in Message.Content.
type RecoverableToolResult struct {
	ToolName         string `json:"tool_name"`
	ArtifactPath     string `json:"artifact_path"`
	ArtifactComplete bool   `json:"artifact_complete"`
	Summary          string `json:"summary,omitempty"`
}

func (m *ToolResultMeta) UnmarshalJSON(data []byte) error {
	if err := json.Unmarshal(data, m); err != nil {
		return err
	}
	return nil
}
