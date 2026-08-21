package agent

import (
	"context"
	"path/filepath"
	"testing"
)

func TestApplyMemoryIntentUpdatesAndReinforcesByID(t *testing.T) {
	store := NewLongTermMemoryStore(t.TempDir())
	ctx := context.Background()
	first := MemoryItem{
		ID: "tmp_notification_42", Type: "fact", TimeScope: "temporary",
		Title: "包裹更新", Content: "包裹明天送达", Tags: []string{"notification"},
		SourceRefs:       []MemorySourceRef{{Type: "notification", ID: "42", EventIDs: []string{"e1"}}},
		EvidenceExcerpts: []string{"包裹明天送达"},
	}
	got, err := store.ApplyMemoryIntent(ctx, MemoryIntent{Item: first})
	if err != nil {
		t.Fatal(err)
	}
	if got.Operation != MemoryOperationAdd || got.ID != first.ID {
		t.Fatalf("first apply=%#v", got)
	}

	same := first
	same.SourceRefs = []MemorySourceRef{{Type: "notification", ID: "42", EventIDs: []string{"e2"}}}
	got, err = store.ApplyMemoryIntent(ctx, MemoryIntent{Item: same})
	if err != nil {
		t.Fatal(err)
	}
	if got.Operation != MemoryOperationReinforce {
		t.Fatalf("same apply=%#v, want reinforce", got)
	}

	updated := same
	updated.Content = "包裹今天送达"
	got, err = store.ApplyMemoryIntent(ctx, MemoryIntent{Item: updated})
	if err != nil {
		t.Fatal(err)
	}
	if got.Operation != MemoryOperationUpdate {
		t.Fatalf("updated apply=%#v, want update", got)
	}

	results, err := store.Search(ctx, MemoryQuery{Tags: []string{"notification"}, Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 || results[0].Content != "包裹今天送达" {
		t.Fatalf("results=%#v", results)
	}
	parsed, err := readMemoryMarkdown(results[0].FilePath)
	if err != nil {
		t.Fatal(err)
	}
	if parsed.Item.Revision != 3 || len(parsed.Item.SourceRefs[0].EventIDs) != 2 {
		t.Fatalf("merged item=%#v", parsed.Item)
	}
}

func TestApplyMemoryIntentRemovesByID(t *testing.T) {
	store := NewLongTermMemoryStore(t.TempDir())
	ctx := context.Background()
	item := MemoryItem{
		ID: "tmp_notification_7", Type: "fact", TimeScope: "temporary",
		Title: "提醒", Content: "会议即将开始", EvidenceExcerpts: []string{"会议即将开始"},
	}
	if _, err := store.ApplyMemoryIntent(ctx, MemoryIntent{Item: item}); err != nil {
		t.Fatal(err)
	}
	got, err := store.ApplyMemoryIntent(ctx, MemoryIntent{Item: MemoryItem{ID: item.ID}, Remove: true})
	if err != nil {
		t.Fatal(err)
	}
	if got.Operation != MemoryOperationRemove {
		t.Fatalf("remove apply=%#v", got)
	}
	results, err := store.Search(ctx, MemoryQuery{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 0 {
		t.Fatalf("removed memory still recalled: %#v", results)
	}
}

func TestApplyMemoryIntentRemoveChecksExpectedRevision(t *testing.T) {
	store := NewLongTermMemoryStore(filepath.Join(t.TempDir(), "long_term"))
	ctx := context.Background()
	item := MemoryItem{ID: "mem_revision_remove", Type: "fact", TimeScope: "long_term", Content: "old", EvidenceExcerpts: []string{"evidence"}}
	if _, err := store.ApplyMemoryIntent(ctx, MemoryIntent{Item: item}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ApplyMemoryIntent(ctx, MemoryIntent{Item: MemoryItem{ID: item.ID}, ExpectedRevision: 2, Remove: true}); err != errMemoryRevisionChanged {
		t.Fatalf("stale remove error=%v, want %v", err, errMemoryRevisionChanged)
	}
	if _, err := store.Search(ctx, MemoryQuery{Tags: nil, Limit: 10}); err != nil {
		t.Fatal(err)
	}
}

func TestDeviceMemoryApplyUsesCommonUpdateAndRevision(t *testing.T) {
	store := NewDeviceMemoryStore(t.TempDir())
	ctx := context.Background()
	first := DeviceMemoryItem{
		ID: "devmem_delivery", Type: "fact", Status: deviceMemoryStatusActive,
		Title: "包裹", Summary: "明天送达", Content: "包裹明天送达",
		EvidenceRefs: []MemorySourceRef{{Type: "episode", ID: "episode-1", EventIDs: []string{"event-1"}}},
	}
	got, err := store.ApplyMemoryIntent(ctx, MemoryIntent{DeviceItem: &first})
	if err != nil {
		t.Fatal(err)
	}
	if got.Operation != MemoryOperationAdd || got.ID != first.ID {
		t.Fatalf("first apply=%#v", got)
	}

	updated := first
	updated.Revision = 1
	updated.Summary = "今天送达"
	updated.Content = "包裹今天送达"
	updated.EvidenceRefs = []MemorySourceRef{{Type: "episode", ID: "episode-2", EventIDs: []string{"event-2"}}}
	got, err = store.ApplyMemoryIntent(ctx, MemoryIntent{DeviceItem: &updated, ExpectedRevision: 1})
	if err != nil {
		t.Fatal(err)
	}
	if got.Operation != MemoryOperationUpdate {
		t.Fatalf("updated apply=%#v", got)
	}

	current, found, err := store.Get(ctx, first.ID)
	if err != nil || !found {
		t.Fatalf("get current found=%v err=%v", found, err)
	}
	if current.Revision != 2 || len(current.RevisionHistory) != 1 || len(current.EvidenceRefs) != 2 {
		t.Fatalf("merged device memory=%#v", current)
	}
}
