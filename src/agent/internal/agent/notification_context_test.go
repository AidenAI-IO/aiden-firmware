package agent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"unicode/utf8"

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
	if events[0].Title != "  Meeting  " || c.State().SourceCursor != "1" {
		t.Fatalf("unexpected raw state: event=%#v state=%#v", events[0], c.State())
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
	if stored.Title != "  Meeting  " {
		t.Fatalf("stored event was not preserved as raw data: %#v", stored)
	}
}

func TestNotificationContextCapsStoredNotificationText(t *testing.T) {
	root := t.TempDir()
	message := strings.Repeat("界", maxNotificationStoredText+1)
	c, err := NewNotificationContext(root, func(context.Context, string, string, int) (ble.EventPage, error) {
		return ble.EventPage{Generation: "g", Events: []ble.NotificationEvent{{
			ID: "1", Source: "android", SourceEventID: "evt-1", Message: message,
			ReceivedAt: "2026-08-21T01:02:03Z",
		}}}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	events, err := c.Consume(context.Background(), 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || utf8.RuneCountInString(events[0].Message) != maxNotificationStoredText {
		t.Fatalf("stored message length=%d, want %d", utf8.RuneCountInString(events[0].Message), maxNotificationStoredText)
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

func TestNotificationContextInstancesShareRootLock(t *testing.T) {
	root := t.TempDir()
	first, err := NewNotificationContext(root, func(context.Context, string, string, int) (ble.EventPage, error) {
		return ble.EventPage{}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	second, err := NewNotificationContext(root, func(context.Context, string, string, int) (ble.EventPage, error) {
		return ble.EventPage{}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if first.mu != second.mu {
		t.Fatal("NotificationContext instances for one root do not share the cleaner/write lock")
	}
}

func TestNotificationContextDoesNotConsumeWhenStorageDisablesWrites(t *testing.T) {
	called := false
	c, err := NewNotificationContext(t.TempDir(), func(context.Context, string, string, int) (ble.EventPage, error) {
		called = true
		return ble.EventPage{}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	c.SetStorageWriteGate(&recordingStorageWriteGate{allow: false})
	events, err := c.Consume(context.Background(), 10)
	if err != nil || len(events) != 0 {
		t.Fatalf("Consume() events=%#v err=%v", events, err)
	}
	if called {
		t.Fatal("Consume() contacted BLE while Notification Context writes were disabled")
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

func TestNotificationContextReadsAllCompleteRecordsWhenFinalLineHasNoNewline(t *testing.T) {
	root := t.TempDir()
	c, err := NewNotificationContext(root, func(context.Context, string, string, int) (ble.EventPage, error) {
		return ble.EventPage{}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "notifications", "events", "2026-08-21.jsonl")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	first, _ := json.Marshal(NotificationRecord{ContextID: "1", NotificationEvent: ble.NotificationEvent{ReceivedAt: "2026-08-21T00:00:00Z"}})
	second, _ := json.Marshal(NotificationRecord{ContextID: "2", NotificationEvent: ble.NotificationEvent{ReceivedAt: "2026-08-21T00:01:00Z"}})
	if err := os.WriteFile(path, append(append(first, '\n'), second...), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := c.ReadPending(context.Background(), 10)
	if err != nil || len(got) != 2 || got[0].ContextID != "1" || got[1].ContextID != "2" {
		t.Fatalf("ReadPending() got=%#v err=%v", got, err)
	}
}

func TestNotificationContextReadPendingAppliesLimitAfterCursorSort(t *testing.T) {
	root := t.TempDir()
	c, err := NewNotificationContext(root, func(context.Context, string, string, int) (ble.EventPage, error) {
		return ble.EventPage{}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	for date, record := range map[string]NotificationRecord{
		"2026-08-20": {ContextID: "2", NotificationEvent: ble.NotificationEvent{ReceivedAt: "2026-08-20T00:00:00Z"}},
		"2026-08-21": {ContextID: "1", NotificationEvent: ble.NotificationEvent{ReceivedAt: "2026-08-21T00:00:00Z"}},
	} {
		data, _ := json.Marshal(record)
		path := filepath.Join(root, "notifications", "events", date+".jsonl")
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, append(data, '\n'), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	got, err := c.ReadPending(context.Background(), 1)
	if err != nil || len(got) != 1 || got[0].ContextID != "1" {
		t.Fatalf("ReadPending() got=%#v err=%v", got, err)
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

func TestNotificationFingerprintWindowIsBounded(t *testing.T) {
	fingerprints := make(map[string]string, maxNotificationFingerprints+1)
	for cursor := 1; cursor <= maxNotificationFingerprints+1; cursor++ {
		fingerprints["fingerprint-"+strconv.Itoa(cursor)] = strconv.Itoa(cursor)
	}
	pruneNotificationFingerprints(fingerprints)
	if len(fingerprints) != trimNotificationFingerprintsTo {
		t.Fatalf("fingerprints=%d, want %d", len(fingerprints), trimNotificationFingerprintsTo)
	}
	if _, exists := fingerprints["fingerprint-1"]; exists {
		t.Fatal("oldest fingerprint was not pruned")
	}
	if _, exists := fingerprints["fingerprint-"+strconv.Itoa(maxNotificationFingerprints+1)]; !exists {
		t.Fatal("newest fingerprint was pruned")
	}
}

func TestNotificationContextRecoversCursorAndDedupeFromEventLog(t *testing.T) {
	root := t.TempDir()
	seed, err := NewNotificationContext(root, func(context.Context, string, string, int) (ble.EventPage, error) {
		return ble.EventPage{}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	record := NotificationRecord{
		NotificationEvent: ble.NotificationEvent{ID: "7", Source: "android", SourceEventID: "evt-7", ReceivedAt: "2026-08-21T00:00:00Z"},
		ContextID:         "1",
		Generation:        "g",
	}
	seed.mu.Lock()
	err = seed.appendEventLocked(record)
	seed.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}

	recovered, err := NewNotificationContext(root, func(context.Context, string, string, int) (ble.EventPage, error) {
		return ble.EventPage{Generation: "g", Events: []ble.NotificationEvent{record.NotificationEvent}}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if state := recovered.State(); state.StoredCursor != "1" || state.SourceCursor != "7" || state.Generation != "g" {
		t.Fatalf("recovered state=%#v", state)
	}
	accepted, err := recovered.Consume(context.Background(), 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(accepted) != 0 {
		t.Fatalf("replayed event was appended again: %#v", accepted)
	}
	records, err := readNotificationRecordFile(filepath.Join(root, "notifications", "events", "2026-08-21.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 1 {
		t.Fatalf("event log contains %d records, want 1", len(records))
	}
}

func TestNotificationContextQueryDoesNotRepairIncompleteTailRecord(t *testing.T) {
	root := t.TempDir()
	eventsDir := filepath.Join(root, "notifications", "events")
	if err := os.MkdirAll(eventsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(eventsDir, "2026-08-21.jsonl")
	complete := `{"id":"1","context_id":"1","received_at":"2026-08-21T00:00:00Z"}` + "\n"
	if err := os.WriteFile(path, append([]byte(complete), []byte(`{"id":"2","context_id":"2"`)...), 0o644); err != nil {
		t.Fatal(err)
	}
	results, err := QueryNotificationRecords(context.Background(), root, NotificationQuery{Limit: 10})
	if err == nil || len(results) != 0 {
		t.Fatalf("read-only query results=%#v err=%v, want incomplete-record error", results, err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	want := complete + `{"id":"2","context_id":"2"`
	if string(data) != want {
		t.Fatalf("query modified file=%q, want %q", string(data), want)
	}
}
