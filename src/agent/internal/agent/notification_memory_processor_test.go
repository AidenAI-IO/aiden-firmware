package agent

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"aiden-agent/internal/ble"
)

func TestNotificationMemoryProcessorFiltersNoiseAndWritesTemporaryMemory(t *testing.T) {
	root := t.TempDir()
	reader := func(context.Context, string, string, int) (ble.EventPage, error) {
		return ble.EventPage{Generation: "g", Events: []ble.NotificationEvent{
			{ID: "1", Source: "android", SourceEventID: "otp", AppIdentifier: "com.auth", Title: "验证码", Message: "123456", Event: "added", ReceivedAt: "2026-08-21T00:00:00Z"},
			{ID: "2", Source: "android", SourceEventID: "useful", AppIdentifier: "com.delivery", Title: "包裹更新", Message: "明天送达", Event: "added", ReceivedAt: "2026-08-21T00:01:00Z"},
		}}, nil
	}
	ctxStore, err := NewNotificationContext(root, reader)
	if err != nil {
		t.Fatal(err)
	}
	processor := NewNotificationMemoryProcessor(ctxStore, root)
	processor.now = func() time.Time { return time.Date(2026, 8, 21, 1, 0, 0, 0, time.UTC) }
	_, err = processor.ProcessBatch(context.Background(), 20, nil)
	if err != nil {
		t.Fatal(err)
	}
	results, err := processor.temporary.Search(context.Background(), MemoryQuery{Tags: []string{"com.delivery"}, Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 || results[0].ID != "tmp_notification_2" {
		t.Fatalf("temporary results=%#v", results)
	}
	if got := ctxStore.State().MemoryCursor; got != "2" {
		t.Fatalf("MemoryCursor=%q, want 2", got)
	}
}

func TestNotificationMemoryProcessorImplementsWorkerIsolationSeam(t *testing.T) {
	root := t.TempDir()
	ctxStore, err := NewNotificationContext(filepath.Join(root, "memory"), func(context.Context, string, string, int) (ble.EventPage, error) {
		return ble.EventPage{}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	processor := NewNotificationMemoryProcessor(ctxStore, filepath.Join(root, "memory"))
	var _ MemoryProcessor = processor
	worker := newNotificationMemoryWorker(processor)
	if worker == nil || worker.MemoryWorker == nil {
		t.Fatal("notification worker was not created")
	}
	worker.Stop()
}
