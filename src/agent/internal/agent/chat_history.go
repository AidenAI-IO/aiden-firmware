package agent

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const maxChatHistoryContentRunes = 8000

type ChatHistoryStore struct {
	mu           sync.Mutex
	rootDir      string
	onNewMessage func(Message) // callback invoked after successful Append
}

func NewChatHistoryStore(rootDir string) *ChatHistoryStore {
	if strings.TrimSpace(rootDir) == "" {
		return nil
	}
	return &ChatHistoryStore{rootDir: rootDir}
}

// SetOnNewMessage registers a callback invoked after each successful Append.
// The callback runs synchronously on the appending goroutine; keep it fast.
func (s *ChatHistoryStore) SetOnNewMessage(callback func(Message)) {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.onNewMessage = callback
	s.mu.Unlock()
}

func (s *ChatHistoryStore) Append(ctx context.Context, message Message) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}
	if s == nil || s.rootDir == "" {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	if message.Timestamp.IsZero() {
		message.Timestamp = time.Now()
	}
	message = compactMessageForChatHistory(message)
	if err := os.MkdirAll(s.rootDir, 0o755); err != nil {
		return fmt.Errorf("create chat history directory: %w", err)
	}
	file, err := os.OpenFile(s.eventsPath(), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return fmt.Errorf("open chat history events: %w", err)
	}
	defer file.Close()
	if err := json.NewEncoder(file).Encode(message); err != nil {
		return fmt.Errorf("append chat history event: %w", err)
	}
	// Notify callback after successful persistence
	if s.onNewMessage != nil {
		s.onNewMessage(message)
	}
	return nil
}

func (s *ChatHistoryStore) Load(ctx context.Context) ([]Message, error) {
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	default:
	}
	if s == nil || s.rootDir == "" {
		return nil, nil
	}
	file, err := os.Open(s.eventsPath())
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("open chat history events: %w", err)
	}
	defer file.Close()

	var messages []Message
	validData := make([]byte, 0)
	repairedTruncatedTail := false
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 0), 8<<20)
	for scanner.Scan() {
		line := bytes.Trim(scanner.Bytes(), "\x00 \t\r\n")
		if len(line) == 0 {
			continue
		}
		var message Message
		if err := json.Unmarshal(line, &message); err != nil {
			if isTruncatedJSONLineError(err) {
				repairedTruncatedTail = true
				break
			}
			return nil, fmt.Errorf("decode chat history event: %w", err)
		}
		validData = append(validData, line...)
		validData = append(validData, '\n')
		messages = append(messages, message)
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("scan chat history events: %w", err)
	}
	if repairedTruncatedTail {
		_ = writeFileAtomic(s.eventsPath(), validData, 0o644)
	}
	return messages, nil
}

func (s *ChatHistoryStore) Clear() error {
	if s == nil || s.rootDir == "" {
		return nil
	}
	if err := os.RemoveAll(s.rootDir); err != nil {
		return fmt.Errorf("remove chat history: %w", err)
	}
	return nil
}

func (s *ChatHistoryStore) eventsPath() string {
	return filepath.Join(s.rootDir, "events.jsonl")
}

func compactMessageForChatHistory(message Message) Message {
	if message.Type == "tool_result" {
		message.Content = compactToolResultForChatHistory(message.Content)
	}
	message.Attachments = compactAttachmentsForChatHistory(message.Attachments)
	message.Content = truncateChatHistoryRunes(message.Content, maxChatHistoryContentRunes)
	message.ToolInput = truncateChatHistoryRunes(message.ToolInput, maxChatHistoryContentRunes)
	message.Description = truncateChatHistoryRunes(message.Description, maxChatHistoryContentRunes)
	return message
}

func compactAttachmentsForChatHistory(attachments []MessageAttachment) []MessageAttachment {
	if len(attachments) == 0 {
		return nil
	}
	out := make([]MessageAttachment, 0, len(attachments))
	for _, attachment := range attachments {
		attachment.Data = ""
		out = append(out, attachment)
	}
	return out
}

func compactToolResultForChatHistory(content string) string {
	return stripScreenshotData(content)
}

// stripScreenshotData removes the base64 image payload from a screenshot tool
// result while preserving its metadata (width/height/format/size and any
// action_output). It is the single source of truth for screenshot-data
// scrubbing, used by chat_history persistence, the legacy session snapshot, and
// the session events.jsonl path (sessionEventFromRecord +
// SessionMemoryStore.AppendEvent) so a base64 JPEG never lands in persistent
// memory stores and can never inflate the hot-window token estimate. Returns
// content unchanged when it is not a screenshot result (or has no Data field),
// so non-screenshot strings pass through untouched.
func stripScreenshotData(content string) string {
	var result postActionScreenshotResult
	if err := json.Unmarshal([]byte(content), &result); err != nil || result.Data == "" {
		return content
	}
	format := strings.TrimSpace(result.Format)
	if format == "" {
		format = "jpeg"
	}
	compact := map[string]interface{}{
		"width":  result.Width,
		"height": result.Height,
		"format": format,
		"size":   result.Size,
	}
	if strings.TrimSpace(result.ActionOutput) != "" {
		compact["action_output"] = strings.TrimSpace(result.ActionOutput)
	}
	data, err := json.Marshal(compact)
	if err != nil {
		return content
	}
	return string(data)
}

func truncateChatHistoryRunes(value string, maxRunes int) string {
	if maxRunes <= 0 {
		return value
	}
	runes := []rune(value)
	if len(runes) <= maxRunes {
		return value
	}
	return string(runes[:maxRunes]) + fmt.Sprintf("\n...[truncated %d chars]", len(runes)-maxRunes)
}
