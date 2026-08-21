package agent

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// NotificationContextCleaner removes processed raw notification shards after
// their retention window. Files containing records beyond MemoryCursor are
// always protected, including during forced cleanup.
type NotificationContextCleaner struct {
	memoryDir    string
	retentionAge time.Duration
	priority     int
	now          func() time.Time
}

func NewNotificationContextCleaner(memoryDir string, retentionDays, priority int) *NotificationContextCleaner {
	return &NotificationContextCleaner{
		memoryDir:    memoryDir,
		retentionAge: time.Duration(retentionDays) * 24 * time.Hour,
		priority:     priority,
		now:          time.Now,
	}
}

func (c *NotificationContextCleaner) Name() string {
	if c.retentionAge > 0 {
		return fmt.Sprintf("notification_context_%dd", int(c.retentionAge.Hours()/24))
	}
	return "notification_context_processed"
}

func (c *NotificationContextCleaner) Priority() int { return c.priority }

func (c *NotificationContextCleaner) EstimateReclaimable(ctx context.Context) (uint64, error) {
	if c == nil || strings.TrimSpace(c.memoryDir) == "" {
		return 0, nil
	}
	contextStore, err := NewNotificationContext(c.memoryDir, nil)
	if err != nil {
		return 0, err
	}
	return contextStore.EstimateCleanupProcessedBefore(ctx, c.retentionAge, c.now())
}

func (c *NotificationContextCleaner) Clean(ctx context.Context) (uint64, error) {
	return c.clean(ctx, false)
}

func (c *NotificationContextCleaner) ForceClean(ctx context.Context) (uint64, error) {
	return c.clean(ctx, true)
}

func (c *NotificationContextCleaner) clean(ctx context.Context, force bool) (uint64, error) {
	if c == nil || strings.TrimSpace(c.memoryDir) == "" {
		return 0, nil
	}
	contextStore, err := NewNotificationContext(c.memoryDir, nil)
	if err != nil {
		return 0, err
	}
	_ = force
	return contextStore.CleanupProcessedBefore(ctx, c.retentionAge, c.now())
}
