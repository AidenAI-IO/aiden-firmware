package agent

import (
	"context"
	"path/filepath"
	"strings"
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
	processor := NewNotificationMemoryProcessor(ctxStore, root, nil, nil)
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
	processor := NewNotificationMemoryProcessor(ctxStore, filepath.Join(root, "memory"), nil, nil)
	var _ MemoryProcessor = processor
	worker := newNotificationMemoryWorker(processor)
	if worker == nil || worker.MemoryWorker == nil {
		t.Fatal("notification worker was not created")
	}
	worker.Stop()
}

func TestNotificationMemoryProcessorUsesMergeEngineForAdd(t *testing.T) {
	root := t.TempDir()
	ctxStore, err := NewNotificationContext(root, func(context.Context, string, string, int) (ble.EventPage, error) {
		return ble.EventPage{Generation: "g", Events: []ble.NotificationEvent{{ID: "1", Source: "android", SourceID: "n1", NotificationUID: 1, SourceEventID: "evt-1", AppIdentifier: "com.delivery", Title: "包裹更新", Message: "明天送达", Event: "added", ReceivedAt: "2026-08-21T00:00:00Z"}}}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	llm := &episodeMemoryScriptedModel{responses: []string{`{"actions":[{"action":"add","scope":"temporary","type":"fact","title":"包裹更新","content":"包裹明天送达","expires_at":"2026-08-28T00:00:00Z","tags":["notification","com.delivery"]}]}`}}
	processor := NewNotificationMemoryProcessor(ctxStore, root, nil, llm)
	processor.now = func() time.Time { return time.Date(2026, 8, 21, 1, 0, 0, 0, time.UTC) }
	if _, err := processor.ProcessBatch(context.Background(), 20, nil); err != nil {
		t.Fatal(err)
	}
	results, err := processor.temporary.Search(context.Background(), MemoryQuery{Tags: []string{"com.delivery"}, Limit: 5})
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 || results[0].Content != "包裹明天送达" {
		t.Fatalf("temporary results=%#v", results)
	}
	if got := ctxStore.State().MemoryCursor; got != "1" {
		t.Fatalf("MemoryCursor=%q, want 1", got)
	}
}

func TestNotificationMemoryProcessorRejectsInvalidProposalWithoutAdvancingCursor(t *testing.T) {
	root := t.TempDir()
	ctxStore, err := NewNotificationContext(root, func(context.Context, string, string, int) (ble.EventPage, error) {
		return ble.EventPage{Generation: "g", Events: []ble.NotificationEvent{{ID: "1", Source: "android", SourceID: "n1", NotificationUID: 1, SourceEventID: "evt-1", AppIdentifier: "com.mail", Title: "会议", Message: "明天开会", Event: "added", ReceivedAt: "2026-08-21T00:00:00Z"}}}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	processor := NewNotificationMemoryProcessor(ctxStore, root, nil, &episodeMemoryScriptedModel{responses: []string{`{"actions":[{"action":"not-a-real-action","scope":"temporary"}]}`}})
	if _, err := processor.ProcessBatch(context.Background(), 20, nil); err == nil || !strings.Contains(err.Error(), "unsupported action") {
		t.Fatalf("ProcessBatch() error=%v, want unsupported action", err)
	}
	if got := ctxStore.State().MemoryCursor; got != "" {
		t.Fatalf("MemoryCursor=%q advanced after invalid proposal", got)
	}
}

func TestNotificationMemoryProcessorUpdatesWithRevisionAndCanPromote(t *testing.T) {
	root := t.TempDir()
	temporary := NewLongTermMemoryStore(filepath.Join(root, "temporary"))
	longTerm := NewLongTermMemoryStore(filepath.Join(root, "long_term"))
	if _, err := temporary.AddMemory(context.Background(), MemoryItem{ID: "tmp_existing", Type: "fact", TimeScope: "temporary", Title: "包裹", Content: "包裹今天送达", Tags: []string{"notification", "com.delivery"}, EvidenceExcerpts: []string{"旧通知"}}); err != nil {
		t.Fatal(err)
	}
	ctxStore, err := NewNotificationContext(root, func(context.Context, string, string, int) (ble.EventPage, error) {
		return ble.EventPage{Generation: "g", Events: []ble.NotificationEvent{{ID: "1", Source: "android", SourceID: "n1", NotificationUID: 1, SourceEventID: "evt-1", AppIdentifier: "com.delivery", Title: "包裹更新", Message: "包裹改为明天送达", Event: "modified", ReceivedAt: "2026-08-21T00:00:00Z"}}}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	responses := []string{`{"actions":[{"action":"update","scope":"temporary","memory_id":"tmp_existing","memory_revision":1,"type":"fact","title":"包裹","content":"包裹改为明天送达","expires_at":"2026-08-28T00:00:00Z"}]}`}
	processor := NewNotificationMemoryProcessor(ctxStore, root, longTerm, &episodeMemoryScriptedModel{responses: responses})
	if _, err := processor.ProcessBatch(context.Background(), 20, nil); err != nil {
		t.Fatal(err)
	}
	updated, err := temporary.Search(context.Background(), MemoryQuery{Tags: []string{"com.delivery"}, Limit: 5})
	if err != nil || len(updated) != 1 || updated[0].Revision != 2 || updated[0].Content != "包裹改为明天送达" {
		t.Fatalf("updated=%#v err=%v", updated, err)
	}

	// Exercise the same processor seam for a temporary -> long-term promotion.
	ctxStore2, err := NewNotificationContext(filepath.Join(root, "promote"), func(context.Context, string, string, int) (ble.EventPage, error) {
		return ble.EventPage{Generation: "g", Events: []ble.NotificationEvent{{ID: "1", Source: "android", SourceID: "n2", NotificationUID: 2, SourceEventID: "evt-2", AppIdentifier: "com.delivery", Title: "稳定规则", Message: "以后统一寄到公司", Event: "added", ReceivedAt: "2026-08-21T00:00:00Z"}}}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := temporary.ApplyMemoryIntent(context.Background(), MemoryIntent{Item: MemoryItem{ID: "tmp_notification_1", Type: "fact", TimeScope: "temporary", Title: "稳定规则", Content: "以后统一寄到公司", Tags: []string{"notification", "com.delivery"}, EvidenceExcerpts: []string{"通知"}}}); err != nil {
		t.Fatal(err)
	}
	processor2 := NewNotificationMemoryProcessor(ctxStore2, root, longTerm, &episodeMemoryScriptedModel{responses: []string{`{"actions":[{"action":"promote","scope":"long_term","memory_id":"tmp_notification_1","memory_revision":1}]}`}})
	if _, err := processor2.ProcessBatch(context.Background(), 20, nil); err != nil {
		t.Fatal(err)
	}
	if promoted, err := longTerm.Search(context.Background(), MemoryQuery{Tags: []string{"com.delivery"}, Limit: 5}); err != nil || len(promoted) == 0 || !strings.Contains(promoted[0].Content, "公司") {
		t.Fatalf("promoted=%#v err=%v", promoted, err)
	}
	if active, err := temporary.Search(context.Background(), MemoryQuery{Tags: []string{"com.delivery"}, Limit: 10}); err != nil {
		t.Fatal(err)
	} else {
		for _, item := range active {
			if item.ID == "tmp_notification_1" {
				t.Fatalf("promoted temporary memory still active: %#v", item)
			}
		}
	}
}
