package ble

import "testing"

func TestEventStoreKeepsCursorAndReportsTruncation(t *testing.T) {
	store := NewEventStore(2)
	store.Append(NotificationEvent{NotificationUID: 1, Event: "added"})
	store.Append(NotificationEvent{NotificationUID: 2, Event: "added"})
	store.Append(NotificationEvent{NotificationUID: 3, Event: "removed"})

	page := store.Page(0, 10)
	if page.Truncated {
		t.Fatal("initial snapshot must not be marked truncated")
	}
	if len(page.Events) != 2 || page.Events[0].ID != "2" || page.Events[1].ID != "3" {
		t.Fatalf("unexpected retained events: %#v", page.Events)
	}
	if page.OldestID != "2" || page.LastID != "3" {
		t.Fatalf("unexpected cursors: oldest=%s last=%s", page.OldestID, page.LastID)
	}

	page = store.Page(0, 1)
	if len(page.Events) != 1 || page.Events[0].ID != "2" {
		t.Fatalf("limit was not applied: %#v", page.Events)
	}
	page = store.Page(1, 10)
	if page.Truncated {
		t.Fatal("cursor immediately before oldest event is not truncated")
	}
	page = store.Page(0, 10)
	if page.Truncated {
		t.Fatal("zero cursor requests current retained history")
	}
}

func TestEventStoreReportsGapForOldCursor(t *testing.T) {
	store := NewEventStore(2)
	for uid := uint32(1); uid <= 4; uid++ {
		store.Append(NotificationEvent{NotificationUID: uid, Event: "added"})
	}
	page := store.Page(1, 10)
	if !page.Truncated || page.OldestID != "3" {
		t.Fatalf("expected cursor gap, got %#v", page)
	}
}

func TestEventStoreRequiresCursorResetAcrossGenerations(t *testing.T) {
	oldStore := NewEventStore(2)
	oldStore.Append(NotificationEvent{NotificationUID: 1, Event: "added"})
	oldGeneration := oldStore.Generation()

	newStore := NewEventStore(2)
	newStore.Append(NotificationEvent{NotificationUID: 2, Event: "added"})
	page := newStore.PageForGeneration(1, 10, oldGeneration)
	if !page.ResetRequired || len(page.Events) != 0 {
		t.Fatalf("stale generation was not rejected: %#v", page)
	}
	if page.Generation == "" || page.Generation == oldGeneration {
		t.Fatalf("restart generation was not replaced: old=%q page=%#v", oldGeneration, page)
	}

	page = newStore.PageForGeneration(0, 10, page.Generation)
	if page.ResetRequired || len(page.Events) != 1 || page.Events[0].NotificationUID != 2 {
		t.Fatalf("reset cursor did not return current generation: %#v", page)
	}
}
