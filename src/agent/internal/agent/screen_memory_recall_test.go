package agent

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

// addScreenMemory writes a Screen Memory the way the Quick Capture pipeline
// does, with an explicit ID so ordering is deterministic.
func addScreenMemory(t *testing.T, store *LongTermMemoryStore, id, title, content string, tags, entities []string) {
	t.Helper()
	if _, err := store.AddMemory(context.Background(), MemoryItem{
		ID:               id,
		Type:             MemoryTypeScreenSnapshot,
		Priority:         80,
		Confidence:       1.0,
		Tags:             tags,
		Entities:         entities,
		Title:            title,
		Content:          content,
		EvidenceExcerpts: []string{content},
		SourceRefs: []MemorySourceRef{
			{Type: MemorySourceTypeScreenCapture, ID: id},
		},
	}); err != nil {
		t.Fatalf("AddMemory(%s) error = %v", id, err)
	}
}

func TestScreenMemoryFoundByTypeAlone(t *testing.T) {
	// "The one I just saved" carries no topical terms, so a type-only query
	// has to work.
	store := NewLongTermMemoryStore(filepath.Join(t.TempDir(), "long_term"))
	addScreenMemory(t, store, "mem_1700000000000000000_aa",
		"微信聊天窗口",
		"微信聊天窗口，对方发来一个顺丰快递单号。\n\n关键信息：\n- SF1234567890",
		[]string{"快递", "物流"}, []string{"微信", "顺丰"})

	results, err := store.Search(context.Background(), MemoryQuery{
		Types: []string{MemoryTypeScreenSnapshot},
	})
	if err != nil {
		t.Fatalf("Search() error = %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("type-only search returned %d results, want 1", len(results))
	}
}

func TestScreenMemoryFoundByKeyTextSubstring(t *testing.T) {
	// The tracking number appears only in the stored content. If key text were
	// paraphrased away by the summary, this question would be unanswerable.
	store := NewLongTermMemoryStore(filepath.Join(t.TempDir(), "long_term"))
	addScreenMemory(t, store, "mem_1700000000000000000_aa",
		"微信聊天窗口",
		"微信聊天窗口，对方发来一个顺丰快递单号。\n\n关键信息：\n- SF1234567890",
		[]string{"快递"}, []string{"微信", "顺丰"})

	results, err := store.Search(context.Background(), MemoryQuery{
		Tags: []string{"SF1234567890"},
	})
	if err != nil {
		t.Fatalf("Search() error = %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("searching for the tracking number returned %d results, want 1", len(results))
	}
}

func TestScreenMemoryOrderedNewestFirst(t *testing.T) {
	store := NewLongTermMemoryStore(filepath.Join(t.TempDir(), "long_term"))
	// IDs carry fixed-width nanosecond timestamps; these are ascending in time.
	ids := []string{
		"mem_1700000000000000000_aa",
		"mem_1700000000000000001_bb",
		"mem_1700000000000000002_cc",
	}
	for i, id := range ids {
		addScreenMemory(t, store, id,
			fmt.Sprintf("屏幕 %d", i),
			fmt.Sprintf("第 %d 次保存的屏幕内容。", i),
			[]string{"快递"}, []string{"微信"})
	}

	results, err := store.Search(context.Background(), MemoryQuery{
		Types: []string{MemoryTypeScreenSnapshot},
	})
	if err != nil {
		t.Fatalf("Search() error = %v", err)
	}
	if len(results) != len(ids) {
		t.Fatalf("search returned %d results, want %d", len(results), len(ids))
	}
	if results[0].ID != ids[len(ids)-1] {
		t.Fatalf("first result = %q, want newest %q", results[0].ID, ids[len(ids)-1])
	}
	for i := 1; i < len(results); i++ {
		if results[i-1].ID <= results[i].ID {
			t.Fatalf("results not newest-first: %q before %q", results[i-1].ID, results[i].ID)
		}
	}
}

func TestScreenMemoryMixedTypeOrderingIsTransitiveBeforeLimit(t *testing.T) {
	store := NewLongTermMemoryStore(filepath.Join(t.TempDir(), "long_term"))
	addScreenMemory(t, store, "mem_1700000000000000002_screen_old", "Older screen", "same topic", nil, nil)
	addScreenMemory(t, store, "mem_1700000000000000004_screen_new", "Newer screen", "same topic", nil, nil)
	for _, id := range []string{"mem_1700000000000000001_fact_old", "mem_1700000000000000003_fact_new"} {
		if _, err := store.AddMemory(context.Background(), MemoryItem{
			ID:               id,
			Type:             "fact",
			Priority:         80,
			Confidence:       1.0,
			Title:            id,
			Content:          "same topic",
			EvidenceExcerpts: []string{"same topic"},
		}); err != nil {
			t.Fatalf("AddMemory(%s) error = %v", id, err)
		}
	}

	results, err := store.Search(context.Background(), MemoryQuery{Limit: 3})
	if err != nil {
		t.Fatalf("Search() error = %v", err)
	}
	want := []string{
		"mem_1700000000000000004_screen_new",
		"mem_1700000000000000002_screen_old",
		"mem_1700000000000000001_fact_old",
	}
	if len(results) != len(want) {
		t.Fatalf("Search() returned %d results, want %d", len(results), len(want))
	}
	for i := range want {
		if results[i].ID != want[i] {
			t.Fatalf("result[%d] = %q, want %q", i, results[i].ID, want[i])
		}
	}
}

func TestTwoCapturesOfSameScreenBothSurvive(t *testing.T) {
	// The data-loss guard. Had DecideAction been in the pipeline, overlapping
	// tags and entities would have marked the earlier entry "replaced" and
	// Search would no longer return it. Two presses are two moments.
	ctx := context.Background()
	store := NewLongTermMemoryStore(filepath.Join(t.TempDir(), "long_term"))

	first := "mem_1700000000000000000_aa"
	second := "mem_1700000000000000001_bb"
	addScreenMemory(t, store, first, "微信聊天窗口",
		"微信聊天窗口，快递单号 SF1111111111。",
		[]string{"快递"}, []string{"微信", "顺丰"})
	addScreenMemory(t, store, second, "微信聊天窗口",
		"微信聊天窗口，快递单号 SF2222222222。",
		[]string{"快递"}, []string{"微信", "顺丰"})

	results, err := store.Search(ctx, MemoryQuery{
		Types: []string{MemoryTypeScreenSnapshot},
		Limit: 10,
	})
	if err != nil {
		t.Fatalf("Search() error = %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("search returned %d results, want 2: the earlier capture must not be superseded", len(results))
	}

	seen := map[string]bool{}
	for _, r := range results {
		seen[r.ID] = true
		if r.Status != "active" {
			t.Fatalf("memory %q status = %q, want active", r.ID, r.Status)
		}
	}
	if !seen[first] {
		t.Fatalf("the earlier capture %q is missing from results", first)
	}
}

func TestScreenMemoryStaysOutOfProfile(t *testing.T) {
	ctx := context.Background()
	store := NewLongTermMemoryStore(filepath.Join(t.TempDir(), "long_term"))
	addScreenMemory(t, store, "mem_1700000000000000000_aa",
		"银行App余额页",
		"银行 App 余额页面，余额 12345.67 元。",
		[]string{"银行"}, []string{"某银行App"})

	if err := store.RegenerateProfileMD(ctx); err != nil {
		t.Fatalf("RegenerateProfileMD() error = %v", err)
	}
	profilePath := filepath.Join(store.RootDir(), "profile.md")
	if _, err := os.Stat(profilePath); !os.IsNotExist(err) {
		t.Fatal("profile.md was written from a screen memory; balance details must not reach the User Profile")
	}
}
