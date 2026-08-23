package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"aiden-agent/internal/ble"
)

func TestNotificationContextConsumesPersistsAndCommits(t *testing.T) {
	root := t.TempDir()
	page := ble.EventPage{
		Generation: "gen-a",
		Events: []ble.NotificationEvent{{
			ID: "1", Source: "ios_ancs", SourceEventID: "evt-1", DeviceID: "phone",
			Event: "added", AppIdentifier: "com.example", Title: "  Meeting  ", Message: "hello",
			ReceivedAt: "2026-08-21T01:02:03Z",
		}},
		OldestID: "1", LastID: "1",
	}
	reader := func(context.Context, string, string, int) (ble.EventPage, error) {
		return page, nil
	}
	c, err := NewNotificationContext(root, reader)
	if err != nil {
		t.Fatal(err)
	}
	events, err := c.Consume(context.Background(), 10)
	if err != nil || len(events) != 1 {
		t.Fatalf("Consume() events=%#v err=%v", events, err)
	}
	if events[0].Title != "Meeting" || c.State().SourceCursor != "1" {
		t.Fatalf("unexpected normalized state: event=%#v state=%#v", events[0], c.State())
	}
	pending, err := c.ReadPending(context.Background(), 10)
	if err != nil || len(pending) != 1 {
		t.Fatalf("ReadPending() events=%#v err=%v", pending, err)
	}
	if err := c.CommitProcessed(context.Background(), pending); err != nil {
		t.Fatal(err)
	}
	if got := c.State().MemoryCursor; got != "1" {
		t.Fatalf("MemoryCursor=%q, want 1", got)
	}
	data, err := os.ReadFile(filepath.Join(root, "notifications", "events", "2026-08-21.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	var stored NotificationRecord
	if err := json.Unmarshal(data[:len(data)-1], &stored); err != nil {
		t.Fatal(err)
	}
	if stored.Title != "Meeting" {
		t.Fatalf("stored event was not sanitized: %#v", stored)
	}
}

func TestNotificationContextDeduplicatesAcrossRestart(t *testing.T) {
	root := t.TempDir()
	reader := func(context.Context, string, string, int) (ble.EventPage, error) {
		return ble.EventPage{Generation: "g", Events: []ble.NotificationEvent{{
			ID: "1", Source: "android", SourceEventID: "same", DeviceID: "phone", Event: "added",
			ReceivedAt: "2026-08-21T00:00:00Z",
		}}}, nil
	}
	first, err := NewNotificationContext(root, reader)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := first.Consume(context.Background(), 10); err != nil || len(got) != 1 {
		t.Fatalf("first Consume() got=%#v err=%v", got, err)
	}
	second, err := NewNotificationContext(root, reader)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := second.Consume(context.Background(), 10); err != nil || len(got) != 0 {
		t.Fatalf("duplicate Consume() got=%#v err=%v", got, err)
	}
	pending, err := second.ReadPending(context.Background(), 10)
	if err != nil || len(pending) != 1 {
		t.Fatalf("dedupe removed persisted event: pending=%#v err=%v", pending, err)
	}
}

func TestNotificationContextUsesMonotonicContextCursorAcrossBLEGenerationReset(t *testing.T) {
	root := t.TempDir()
	phase := 0
	reader := func(_ context.Context, since, generation string, _ int) (ble.EventPage, error) {
		phase++
		switch phase {
		case 1:
			return ble.EventPage{Generation: "g1", Events: []ble.NotificationEvent{{
				ID: "1", Source: "android", SourceEventID: "old", Event: "added", ReceivedAt: "2026-08-20T00:00:00Z",
			}}}, nil
		case 2:
			if since != "1" || generation != "g1" {
				t.Fatalf("unexpected second request since=%q generation=%q", since, generation)
			}
			return ble.EventPage{Generation: "g2", ResetRequired: true, OldestID: "1"}, nil
		default:
			if since != "0" || generation != "g2" {
				t.Fatalf("unexpected reset request since=%q generation=%q", since, generation)
			}
			return ble.EventPage{Generation: "g2", Events: []ble.NotificationEvent{{
				ID: "1", Source: "android", SourceEventID: "new", Event: "added", ReceivedAt: "2026-08-21T00:00:00Z",
			}}}, nil
		}
	}
	c, err := NewNotificationContext(root, reader)
	if err != nil {
		t.Fatal(err)
	}
	first, err := c.Consume(context.Background(), 10)
	if err != nil || len(first) != 1 || first[0].ContextID != "1" {
		t.Fatalf("first consume=%#v err=%v", first, err)
	}
	second, err := c.Consume(context.Background(), 10)
	if err != nil || len(second) != 1 || second[0].ID != "1" || second[0].ContextID != "2" {
		t.Fatalf("second consume=%#v err=%v", second, err)
	}
	if err := c.CommitProcessed(context.Background(), first); err != nil {
		t.Fatal(err)
	}
	if err := c.CommitProcessed(context.Background(), second[:1]); err != nil {
		t.Fatal(err)
	}
	if got := c.State().MemoryCursor; got != "2" {
		t.Fatalf("MemoryCursor=%q, want 2", got)
	}
}

func TestNotificationContextRecordsRingGapAndResetsGeneration(t *testing.T) {
	root := t.TempDir()
	calls := 0
	reader := func(_ context.Context, since, generation string, _ int) (ble.EventPage, error) {
		calls++
		if calls == 1 {
			return ble.EventPage{Generation: "new", ResetRequired: true, OldestID: "4"}, nil
		}
		if since != "0" || generation != "new" {
			t.Fatalf("reset request since=%q generation=%q", since, generation)
		}
		return ble.EventPage{Generation: "new", Truncated: true, OldestID: "4", LastID: "4", Events: []ble.NotificationEvent{{
			ID: "4", Source: "ios_ancs", SourceEventID: "evt-4", DeviceID: "phone", Event: "added",
			ReceivedAt: "2026-08-21T00:00:00Z",
		}}}, nil
	}
	c, err := NewNotificationContext(root, reader)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Consume(context.Background(), 10); err != nil {
		t.Fatal(err)
	}
	state := c.State()
	if state.Generation != "new" || state.SourceCursor != "4" || state.GapCount != 2 {
		t.Fatalf("unexpected gap state: %#v", state)
	}
}

func TestNotificationContextPersistsGenerationResetBeforeRetry(t *testing.T) {
	root := t.TempDir()
	calls := 0
	reader := func(_ context.Context, since, generation string, _ int) (ble.EventPage, error) {
		calls++
		if calls == 1 {
			if since != "0" || generation != "" {
				t.Fatalf("initial request since=%q generation=%q", since, generation)
			}
			return ble.EventPage{Generation: "new", ResetRequired: true, OldestID: "9"}, nil
		}
		if since != "0" || generation != "new" {
			t.Fatalf("reset request since=%q generation=%q", since, generation)
		}
		return ble.EventPage{}, context.Canceled
	}
	c, err := NewNotificationContext(root, reader)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Consume(context.Background(), 10); err != context.Canceled {
		t.Fatalf("Consume() err=%v, want context canceled", err)
	}
	state := c.State()
	if state.Generation != "new" || state.SourceCursor != "0" {
		t.Fatalf("reset state=%#v, want generation new and source cursor 0", state)
	}
	reloaded, err := NewNotificationContext(root, reader)
	if err != nil {
		t.Fatal(err)
	}
	if state := reloaded.State(); state.Generation != "new" || state.SourceCursor != "0" {
		t.Fatalf("persisted reset state=%#v, want generation new and source cursor 0", state)
	}
}

func TestNotificationContextCommitRejectsNonContiguousBatch(t *testing.T) {
	c, err := NewNotificationContext(t.TempDir(), func(context.Context, string, string, int) (ble.EventPage, error) {
		return ble.EventPage{}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	err = c.CommitProcessed(context.Background(), []NotificationRecord{{ContextID: "2"}})
	if err == nil {
		t.Fatal("CommitProcessed accepted a non-contiguous cursor")
	}
}

func TestNotificationContextQueryReadsPersistedRecordsWithoutAdvancingCursors(t *testing.T) {
	root := t.TempDir()
	reader := func(context.Context, string, string, int) (ble.EventPage, error) {
		return ble.EventPage{Generation: "g", Events: []ble.NotificationEvent{
			{ID: "1", Source: "android", SourceEventID: "one", AppIdentifier: "com.chat", Title: "Old", Message: "keep", ReceivedAt: "2026-08-20T00:00:00Z"},
			{ID: "2", Source: "android", SourceEventID: "two", AppIdentifier: "com.mail", Title: "New", Message: "meeting moved", ReceivedAt: "2026-08-21T00:00:00Z"},
		}, LastID: "2", OldestID: "1"}, nil
	}
	c, err := NewNotificationContext(root, reader)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Consume(context.Background(), 10); err != nil {
		t.Fatal(err)
	}
	before := c.State()
	results, err := c.Query(context.Background(), NotificationQuery{Date: "2026-08-21", Text: "moved", Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 || results[0].ID != "2" {
		t.Fatalf("Query() results=%#v, want event 2", results)
	}
	after := c.State()
	if after.SourceCursor != before.SourceCursor || after.MemoryCursor != before.MemoryCursor {
		t.Fatalf("Query advanced cursors: before=%#v after=%#v", before, after)
	}
}

func TestNotificationContextQueryLatestAppliesLimitAfterFiltering(t *testing.T) {
	root := t.TempDir()
	reader := func(context.Context, string, string, int) (ble.EventPage, error) {
		return ble.EventPage{Events: []ble.NotificationEvent{
			{ID: "1", AppIdentifier: "com.example", ReceivedAt: "2026-08-20T00:00:00Z"},
			{ID: "2", AppIdentifier: "com.example", ReceivedAt: "2026-08-21T00:00:00Z"},
			{ID: "3", AppIdentifier: "com.example", ReceivedAt: "2026-08-21T01:00:00Z"},
		}}, nil
	}
	c, err := NewNotificationContext(root, reader)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Consume(context.Background(), 10); err != nil {
		t.Fatal(err)
	}
	results, err := c.Query(context.Background(), NotificationQuery{AppIdentifier: "COM.EXAMPLE", Limit: 2})
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 2 || results[0].ID != "2" || results[1].ID != "3" {
		t.Fatalf("latest results=%#v, want [2 3]", results)
	}
}

func TestNotificationContextSanitizeTruncatesUTF8ByRunes(t *testing.T) {
	value := truncateNotificationText("你好世界", 3)
	if value != "你好世" {
		t.Fatalf("truncateNotificationText()=%q, want 你好世", value)
	}
}

func TestNotificationContextRepairsIncompleteTailRecord(t *testing.T) {
	root := t.TempDir()
	eventsDir := filepath.Join(root, "notifications", "events")
	if err := os.MkdirAll(eventsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	record := NotificationRecord{
		NotificationEvent: ble.NotificationEvent{ID: "1", Source: "ios", SourceEventID: "evt-1", Event: "added", ReceivedAt: "2026-08-21T00:00:00Z"},
		ContextID:         "1",
		Generation:        "g",
	}
	line, err := json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(eventsDir, "2026-08-21.jsonl")
	partial := append(append(append([]byte{}, line...), '\n'), []byte(`{"context_id":"2"`)...)
	if err := os.WriteFile(path, partial, 0o644); err != nil {
		t.Fatal(err)
	}
	c, err := NewNotificationContext(root, func(context.Context, string, string, int) (ble.EventPage, error) {
		return ble.EventPage{}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	pending, err := c.ReadPending(context.Background(), 10)
	if err != nil || len(pending) != 1 || pending[0].ContextID != "1" {
		t.Fatalf("ReadPending()=%#v err=%v, want the complete record only", pending, err)
	}
	repaired, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(repaired, append(line, '\n')) {
		t.Fatalf("repaired JSONL=%q, want complete first record with newline", repaired)
	}
}

func TestNotificationContextRepairsCompleteTailNewline(t *testing.T) {
	root := t.TempDir()
	eventsDir := filepath.Join(root, "notifications", "events")
	if err := os.MkdirAll(eventsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	record := NotificationRecord{
		NotificationEvent: ble.NotificationEvent{ID: "1", Source: "ios", SourceEventID: "evt-1", Event: "added", ReceivedAt: "2026-08-21T00:00:00Z"},
		ContextID:         "1",
		Generation:        "g",
	}
	line, err := json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(eventsDir, "2026-08-21.jsonl")
	if err := os.WriteFile(path, line, 0o644); err != nil {
		t.Fatal(err)
	}
	c, err := NewNotificationContext(root, func(context.Context, string, string, int) (ble.EventPage, error) {
		return ble.EventPage{}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.ReadPending(context.Background(), 10); err != nil {
		t.Fatal(err)
	}
	repaired, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(repaired, append(line, '\n')) {
		t.Fatalf("repaired JSONL=%q, want complete record with newline", repaired)
	}
}

func TestNotificationContextReadsRecordLargerThanScannerLimit(t *testing.T) {
	root := t.TempDir()
	eventsDir := filepath.Join(root, "notifications", "events")
	if err := os.MkdirAll(eventsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	record := NotificationRecord{
		NotificationEvent: ble.NotificationEvent{ID: "1", Source: "ios", SourceEventID: "large", Event: "added", Message: strings.Repeat("x", 256*1024), ReceivedAt: "2026-08-21T00:00:00Z"},
		ContextID:         "1",
		Generation:        "g",
	}
	line, err := json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(eventsDir, "2026-08-21.jsonl")
	if err := os.WriteFile(path, append(line, '\n'), 0o644); err != nil {
		t.Fatal(err)
	}
	c, err := NewNotificationContext(root, func(context.Context, string, string, int) (ble.EventPage, error) {
		return ble.EventPage{}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	results, err := c.Query(context.Background(), NotificationQuery{Limit: 10})
	if err != nil || len(results) != 1 || len(results[0].Message) != 256*1024 {
		t.Fatalf("Query() len=%d message=%d err=%v, want one 256 KiB record", len(results), func() int {
			if len(results) == 0 {
				return 0
			}
			return len(results[0].Message)
		}(), err)
	}
}

func TestNotificationContextBoundsFingerprintWindow(t *testing.T) {
	root := t.TempDir()
	const count = maxNotificationFingerprints + 1
	events := make([]ble.NotificationEvent, 0, count)
	for i := 1; i <= count; i++ {
		events = append(events, ble.NotificationEvent{
			ID: strconv.Itoa(i), Source: "ios", SourceEventID: "evt-" + strconv.Itoa(i), Event: "added", ReceivedAt: "2026-08-21T00:00:00Z",
		})
	}
	c, err := NewNotificationContext(root, func(context.Context, string, string, int) (ble.EventPage, error) {
		return ble.EventPage{Generation: "g", Events: events}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Consume(context.Background(), count); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(root, "notifications", "fingerprints.json"))
	if err != nil {
		t.Fatal(err)
	}
	var stored notificationFingerprintFile
	if err := json.Unmarshal(data, &stored); err != nil {
		t.Fatal(err)
	}
	if got := len(stored.Fingerprints); got != trimNotificationFingerprintsTo {
		t.Fatalf("fingerprint count=%d, want %d", got, trimNotificationFingerprintsTo)
	}
}
