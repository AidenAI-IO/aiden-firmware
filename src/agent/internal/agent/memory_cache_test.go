package agent

import "testing"

func TestParsedMemoryCacheDoesNotAdmitColdEntryWhenFull(t *testing.T) {
	var cache parsedMemoryCache
	cache.init(2)
	signature := memoryFileSignature{ModTime: 1, Size: 1}

	cache.put("a", signature, parsedMemoryMarkdown{Title: "a"})
	cache.put("b", signature, parsedMemoryMarkdown{Title: "b"})
	cache.put("c", signature, parsedMemoryMarkdown{Title: "c"})

	if !cache.has("a") || !cache.has("b") {
		t.Fatal("expected admitted entries to remain cached")
	}
	if cache.has("c") {
		t.Fatal("expected cold entry c not to displace admitted entries")
	}
}

func TestParsedMemoryCacheUpdatesExistingEntryWhenFull(t *testing.T) {
	var cache parsedMemoryCache
	cache.init(1)
	oldSignature := memoryFileSignature{ModTime: 1, Size: 1}
	newSignature := memoryFileSignature{ModTime: 2, Size: 2}

	cache.put("a", oldSignature, parsedMemoryMarkdown{Title: "old"})
	cache.put("a", newSignature, parsedMemoryMarkdown{Title: "new"})

	parsed, hit := cache.get("a", newSignature)
	if !hit {
		t.Fatal("expected updated entry to hit")
	}
	if parsed.Title != "new" {
		t.Fatalf("cached title = %q, want %q", parsed.Title, "new")
	}
}

func TestParsedMemoryCacheEvictionAdmitsReplacement(t *testing.T) {
	var cache parsedMemoryCache
	cache.init(1)
	signature := memoryFileSignature{ModTime: 1, Size: 1}

	cache.put("a", signature, parsedMemoryMarkdown{Title: "a"})
	cache.evict("a")
	cache.put("b", signature, parsedMemoryMarkdown{Title: "b"})

	if cache.has("a") {
		t.Fatal("expected a to be evicted")
	}
	if !cache.has("b") {
		t.Fatal("expected b to be admitted after eviction")
	}
}

func TestParsedMemoryCacheClearAllowsReuse(t *testing.T) {
	var cache parsedMemoryCache
	cache.init(1)
	signature := memoryFileSignature{ModTime: 1, Size: 1}

	cache.put("a", signature, parsedMemoryMarkdown{Title: "a"})
	cache.evict()
	cache.put("b", signature, parsedMemoryMarkdown{Title: "b"})

	if _, hit := cache.get("b", signature); !hit {
		t.Fatal("expected cache to be reusable after clear")
	}
}

func TestParsedMemoryCacheNonPositiveCapacityDisablesCache(t *testing.T) {
	tests := []struct {
		name     string
		capacity int
	}{
		{name: "zero", capacity: 0},
		{name: "negative", capacity: -1},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var cache parsedMemoryCache
			cache.init(test.capacity)
			cache.put("a", memoryFileSignature{ModTime: 1, Size: 1}, parsedMemoryMarkdown{Title: "a"})

			if got := cache.len(); got != 0 {
				t.Fatalf("cache size = %d, want 0", got)
			}
		})
	}
}

func TestNewLongTermMemoryStoreParsedCacheCapacity(t *testing.T) {
	defaultStore := NewLongTermMemoryStore(t.TempDir())
	if got := defaultStore.parsedCache.capacity; got != defaultParsedMemoryCacheCapacity {
		t.Fatalf("default capacity = %d, want %d", got, defaultParsedMemoryCacheCapacity)
	}

	disabledStore := NewLongTermMemoryStore(t.TempDir(), withParsedCacheCapacity(0))
	if got := disabledStore.parsedCache.capacity; got != 0 {
		t.Fatalf("disabled capacity = %d, want 0", got)
	}
}
