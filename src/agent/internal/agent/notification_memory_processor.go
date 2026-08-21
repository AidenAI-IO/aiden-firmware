package agent

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"aiden-agent/internal/ble"
)

const (
	notificationMemoryBatchLimit = 20
	notificationMemoryTTL        = 7 * 24 * time.Hour
)

// NotificationMemoryProcessor is the notification-specific half of a
// MemoryWorker. The first implementation is intentionally deterministic: it
// filters obvious one-shot noise and stores the remaining useful notification
// as a short-lived memory. The worker seam allows model-backed extraction to
// replace this policy without changing scheduling or cursor handling.
type NotificationMemoryProcessor struct {
	context   *NotificationContext
	temporary *LongTermMemoryStore
	longTerm  *LongTermMemoryStore
	now       func() time.Time
}

var _ MemoryProcessor = (*NotificationMemoryProcessor)(nil)

func NewNotificationMemoryProcessor(notificationContext *NotificationContext, memoryDir string, longTermStore *LongTermMemoryStore) *NotificationMemoryProcessor {
	if longTermStore == nil {
		longTermStore = NewLongTermMemoryStore(filepath.Join(memoryDir, "long_term"))
	}
	return &NotificationMemoryProcessor{
		context:   notificationContext,
		temporary: NewLongTermMemoryStore(filepath.Join(memoryDir, "temporary")),
		longTerm:  longTermStore,
		now:       func() time.Time { return time.Now().UTC() },
	}
}

func (p *NotificationMemoryProcessor) Initialize() error {
	return nil
}

func (p *NotificationMemoryProcessor) NextRunAt(ctx context.Context) (time.Time, error) {
	if p == nil || p.context == nil {
		return time.Time{}, nil
	}
	return p.now(), nil
}

func (p *NotificationMemoryProcessor) ProcessBatch(ctx context.Context, limit int, shouldStop func() bool) (MemoryBatchResult, error) {
	if p == nil || p.context == nil {
		return MemoryBatchResult{}, nil
	}
	if limit <= 0 {
		limit = notificationMemoryBatchLimit
	}
	if _, err := p.context.Consume(ctx, limit); err != nil {
		return MemoryBatchResult{}, err
	}
	pending, err := p.context.ReadPending(ctx, limit)
	if err != nil {
		return MemoryBatchResult{}, err
	}
	processed := make([]NotificationRecord, 0, len(pending))
	for _, record := range pending {
		if shouldStop != nil && shouldStop() {
			break
		}
		if err := ctx.Err(); err != nil {
			return MemoryBatchResult{HasPending: true}, err
		}
		if !notificationMemoryIgnored(record.NotificationEvent) {
			if err := p.persistTemporary(ctx, record); err != nil {
				return MemoryBatchResult{HasPending: true}, err
			}
		}
		processed = append(processed, record)
	}
	if len(processed) > 0 {
		if err := p.context.CommitProcessed(ctx, processed); err != nil {
			return MemoryBatchResult{HasPending: true}, err
		}
	}
	// Keep polling while the worker is alive. The BLE service currently exposes
	// a cursor API rather than an Agent-side push callback.
	return MemoryBatchResult{HasPending: true}, nil
}

func (p *NotificationMemoryProcessor) persistTemporary(ctx context.Context, record NotificationRecord) error {
	event := record.NotificationEvent
	content := strings.TrimSpace(strings.Join([]string{strings.TrimSpace(event.Title), strings.TrimSpace(event.Message)}, "\n"))
	if content == "" {
		return nil
	}
	title := strings.TrimSpace(event.Title)
	if title == "" {
		title = strings.TrimSpace(event.AppIdentifier)
	}
	if title == "" {
		title = "通知"
	}
	now := p.now()
	item := MemoryItem{
		ID:         "tmp_notification_" + record.ContextID,
		Type:       "fact",
		TimeScope:  "temporary",
		Priority:   40,
		Confidence: 0.7,
		Title:      title,
		Content:    content,
		Tags:       notificationMemoryTags(event),
		Entities:   compactNotificationEntities(event),
		CreatedAt:  now.Format(time.RFC3339Nano),
		ExpiresAt:  now.Add(notificationMemoryTTL).Format(time.RFC3339Nano),
		SourceRefs: []MemorySourceRef{{
			Type:     "notification",
			ID:       record.ContextID,
			EventIDs: []string{event.ID},
		}},
		EvidenceExcerpts: []string{content},
	}
	if p.temporary == nil {
		return fmt.Errorf("temporary memory store is not configured")
	}
	_, err := p.temporary.AddMemory(ctx, item)
	return err
}

func (p *NotificationMemoryProcessor) logBatchError(err error) {}

func notificationMemoryIgnored(event ble.NotificationEvent) bool {
	if strings.EqualFold(strings.TrimSpace(event.Event), "removed") {
		return true
	}
	value := strings.ToLower(strings.Join([]string{event.AppIdentifier, event.Title, event.Message, event.Category}, " "))
	for _, marker := range []string{"验证码", "verification code", "one-time password", "otp", "营销", "促销", "promotion", "advertisement"} {
		if strings.Contains(value, marker) {
			return true
		}
	}
	return strings.TrimSpace(event.Title) == "" && strings.TrimSpace(event.Message) == ""
}

func notificationMemoryTags(event ble.NotificationEvent) []string {
	tags := []string{"notification"}
	if app := strings.TrimSpace(event.AppIdentifier); app != "" {
		tags = append(tags, app)
	}
	return tags
}

func compactNotificationEntities(event ble.NotificationEvent) []string {
	if value := strings.TrimSpace(event.AppIdentifier); value != "" {
		return []string{value}
	}
	return nil
}
