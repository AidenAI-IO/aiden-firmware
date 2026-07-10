package agent

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"
)

func BenchmarkLongTermMemorySearchCacheHit(b *testing.B) {
	const count = 100
	store := benchmarkLongTermMemoryStore(b, count, count)
	ctx := context.Background()
	if _, err := store.Search(ctx, MemoryQuery{Limit: 10}); err != nil {
		b.Fatalf("warm Search error: %v", err)
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := store.Search(ctx, MemoryQuery{Limit: 10}); err != nil {
			b.Fatalf("Search error: %v", err)
		}
	}
}

func BenchmarkLongTermMemorySearchOverCapacity(b *testing.B) {
	const count = 100
	store := benchmarkLongTermMemoryStore(b, count, 64)
	ctx := context.Background()
	if _, err := store.Search(ctx, MemoryQuery{Limit: 10}); err != nil {
		b.Fatalf("warm Search error: %v", err)
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := store.Search(ctx, MemoryQuery{Limit: 10}); err != nil {
			b.Fatalf("Search error: %v", err)
		}
	}
}

func benchmarkLongTermMemoryStore(b *testing.B, count int, capacity int) *LongTermMemoryStore {
	b.Helper()
	ctx := context.Background()
	store := NewLongTermMemoryStore(filepath.Join(b.TempDir(), "long_term"), withParsedCacheCapacity(capacity))
	for i := 0; i < count; i++ {
		if _, err := store.AddMemory(ctx, MemoryItem{
			ID:               fmt.Sprintf("mem_bench_%03d", i),
			Type:             "fact",
			Priority:         50,
			Confidence:       0.8,
			Tags:             []string{fmt.Sprintf("tag_%d", i%10)},
			Title:            fmt.Sprintf("memory %03d", i),
			Content:          fmt.Sprintf("content body %03d", i),
			EvidenceExcerpts: []string{fmt.Sprintf("evidence %03d", i)},
		}); err != nil {
			b.Fatalf("AddMemory error: %v", err)
		}
	}
	return store
}
