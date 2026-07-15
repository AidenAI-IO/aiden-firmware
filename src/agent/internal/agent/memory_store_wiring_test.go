package agent

import (
	"context"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func TestRuntimeMemorySubsystemsShareLongTermStore(t *testing.T) {
	configDir := t.TempDir()
	memoryDir := filepath.Join(configDir, "memory")
	store := NewLongTermMemoryStore(filepath.Join(memoryDir, "long_term"), WithLifecycleDir(filepath.Join(memoryDir, "lifecycle")))
	manager := NewMemoryManager(memoryDir, WithLongTermMemoryStore(store))
	tools := NewBuiltinToolSet(HIDConfig{}, AudioConfig{}, SearchConfig{}, ProxyConfig{})
	tools.RegisterMemoryTools(memoryDir, 0, store)

	runtime := NewRuntimeWithDeps(Config{ConfigDir: configDir}, nil, manager, tools, nil)
	plane, ok := runtime.memoryPlane.(*FilesystemMemoryPlane)
	if !ok {
		t.Fatalf("memory plane type = %T, want *FilesystemMemoryPlane", runtime.memoryPlane)
	}
	if plane.LongTerm() != store {
		t.Fatal("memory plane did not reuse the shared long-term store")
	}
	if manager.longTerm != store {
		t.Fatal("memory manager did not retain the shared long-term store")
	}

	recall, ok := tools.tools["recall_memory"].(*RecallMemoryTool)
	if !ok {
		t.Fatalf("recall_memory tool type = %T, want *RecallMemoryTool", tools.tools["recall_memory"])
	}
	if recall.store != store {
		t.Fatal("memory tools did not reuse the shared long-term store")
	}
}

func TestRuntimeMemorySubsystemsCreateSharedLongTermStoreFallback(t *testing.T) {
	configDir := t.TempDir()
	memoryDir := filepath.Join(configDir, "memory")
	manager := NewMemoryManager(memoryDir)
	tools := NewBuiltinToolSet(HIDConfig{}, AudioConfig{}, SearchConfig{}, ProxyConfig{})
	tools.RegisterMemoryTools(memoryDir, 0, nil)

	runtime := NewRuntimeWithDeps(Config{ConfigDir: configDir}, nil, manager, tools, nil)
	plane, ok := runtime.memoryPlane.(*FilesystemMemoryPlane)
	if !ok {
		t.Fatalf("memory plane type = %T, want *FilesystemMemoryPlane", runtime.memoryPlane)
	}
	store := manager.longTerm
	if store == nil {
		t.Fatal("memory manager long-term store was not initialized")
	}
	if plane.LongTerm() != store {
		t.Fatal("memory plane did not reuse the fallback long-term store")
	}

	recall, ok := tools.tools["recall_memory"].(*RecallMemoryTool)
	if !ok {
		t.Fatalf("recall_memory tool type = %T, want *RecallMemoryTool", tools.tools["recall_memory"])
	}
	if recall.store != store {
		t.Fatal("memory tools did not reuse the fallback long-term store")
	}
}

// TestRuntimeSharedStoreConcurrentAccess verifies that concurrent read access
// to the shared LongTermMemoryStore from multiple subsystems is race-free.
func TestRuntimeSharedStoreConcurrentAccess(t *testing.T) {
	configDir := t.TempDir()
	memoryDir := filepath.Join(configDir, "memory")

	store := NewLongTermMemoryStore(
		filepath.Join(memoryDir, "long_term"),
		WithLifecycleDir(filepath.Join(memoryDir, "lifecycle")),
	)

	manager := NewMemoryManager(memoryDir, WithLongTermMemoryStore(store))
	tools := NewBuiltinToolSet(HIDConfig{}, AudioConfig{}, SearchConfig{}, ProxyConfig{})
	tools.RegisterMemoryTools(memoryDir, 0, store)

	runtime := NewRuntimeWithDeps(Config{ConfigDir: configDir}, nil, manager, tools, nil)
	plane, ok := runtime.memoryPlane.(*FilesystemMemoryPlane)
	if !ok {
		t.Fatalf("memory plane type = %T, want *FilesystemMemoryPlane", runtime.memoryPlane)
	}

	ctx := context.Background()

	// Pre-populate some memories
	for i := 0; i < 10; i++ {
		_, err := store.AddMemory(ctx, MemoryItem{
			ID:               fmt.Sprintf("mem_%d", i),
			Type:             "fact",
			Priority:         50,
			Confidence:       0.8,
			Content:          fmt.Sprintf("content %d", i),
			EvidenceExcerpts: []string{"evidence"},
		})
		if err != nil {
			t.Fatalf("pre-populate AddMemory: %v", err)
		}
	}

	const workers = 10
	const opsPerWorker = 10

	var wg sync.WaitGroup
	errors := make(chan error, workers*opsPerWorker)

	// All workers perform concurrent reads via different subsystems
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for j := 0; j < opsPerWorker; j++ {
				switch id % 3 {
				case 0:
					// Read via store.Search
					_, err := store.Search(ctx, MemoryQuery{Limit: 5})
					if err != nil {
						errors <- fmt.Errorf("store.Search[%d]: %w", id, err)
					}
				case 1:
					// Read via plane.searchLongTerm
					_, err := plane.searchLongTerm(ctx, memorySearchQuery{Limit: 5})
					if err != nil {
						errors <- fmt.Errorf("plane.searchLongTerm[%d]: %w", id, err)
					}
				case 2:
					// Read via store.loadIndex
					_, err := store.loadIndex(ctx)
					if err != nil {
						errors <- fmt.Errorf("store.loadIndex[%d]: %w", id, err)
					}
				}
			}
		}(i)
	}

	wg.Wait()
	close(errors)

	var errs []error
	for err := range errors {
		errs = append(errs, err)
	}
	if len(errs) > 0 {
		t.Fatalf("concurrent read operations produced %d errors, first: %v", len(errs), errs[0])
	}
}

// TestRuntimeFallbackStoreProfileFnWiring verifies that the fallback store
// inherits profileFn and debouncer from the memory manager.
func TestRuntimeFallbackStoreProfileFnWiring(t *testing.T) {
	configDir := t.TempDir()
	memoryDir := filepath.Join(configDir, "memory")

	calls := 0
	profileFn := func(ctx context.Context, entries []ProfileEntry) string {
		calls++
		return "test profile"
	}

	debouncer := NewProfileDebouncer(func(ctx context.Context) error {
		return nil
	}, 100*time.Millisecond, nil)

	manager := NewMemoryManager(
		memoryDir,
		WithProfileFn(profileFn),
		WithMemoryProfileDebouncer(debouncer),
	)

	tools := NewBuiltinToolSet(HIDConfig{}, AudioConfig{}, SearchConfig{}, ProxyConfig{})
	tools.RegisterMemoryTools(memoryDir, 0, nil)

	runtime := NewRuntimeWithDeps(Config{ConfigDir: configDir}, nil, manager, tools, nil)

	ctx := context.Background()
	store := manager.longTerm
	if store == nil {
		t.Fatal("fallback store not initialized")
	}

	// Add a profile-relevant memory
	_, err := store.AddMemory(ctx, MemoryItem{
		ID:               "mem_profile",
		Type:             "profile",
		Priority:         90,
		Confidence:       0.9,
		Content:          "user profile",
		EvidenceExcerpts: []string{"evidence"},
	})
	if err != nil {
		t.Fatalf("AddMemory: %v", err)
	}

	if err := store.RegenerateProfileMD(ctx); err != nil {
		t.Fatalf("RegenerateProfileMD: %v", err)
	}

	if calls != 1 {
		t.Fatalf("profileFn calls = %d, want 1", calls)
	}

	plane := runtime.memoryPlane.(*FilesystemMemoryPlane)
	if plane.LongTerm().profileDebouncer != debouncer {
		t.Fatal("fallback store did not inherit profileDebouncer")
	}
}

// TestRuntimeFallbackStoreCacheIsolation verifies that the fallback store
// is a fresh instance with an empty cache, not a reused pre-initialized one.
func TestRuntimeFallbackStoreCacheIsolation(t *testing.T) {
	configDir := t.TempDir()
	memoryDir := filepath.Join(configDir, "memory")

	// Create a standalone store and populate it
	preStore := NewLongTermMemoryStore(
		filepath.Join(memoryDir, "long_term"),
		WithLifecycleDir(filepath.Join(memoryDir, "lifecycle")),
	)
	ctx := context.Background()
	_, err := preStore.AddMemory(ctx, MemoryItem{
		ID:               "mem_pre",
		Type:             "fact",
		Priority:         50,
		Confidence:       0.8,
		Content:          "pre-init",
		EvidenceExcerpts: []string{"evidence"},
	})
	if err != nil {
		t.Fatalf("pre-init AddMemory: %v", err)
	}

	// Warm the cache
	_, err = preStore.Search(ctx, MemoryQuery{Limit: 10})
	if err != nil {
		t.Fatalf("pre-init Search: %v", err)
	}

	preStore.cacheMu.Lock()
	preSize := preStore.parsedCache.len()
	preStore.cacheMu.Unlock()

	if preSize == 0 {
		t.Fatal("pre-init cache should have entries")
	}

	// Create runtime with fallback (should create NEW store)
	manager := NewMemoryManager(memoryDir)
	tools := NewBuiltinToolSet(HIDConfig{}, AudioConfig{}, SearchConfig{}, ProxyConfig{})
	tools.RegisterMemoryTools(memoryDir, 0, nil)

	runtime := NewRuntimeWithDeps(Config{ConfigDir: configDir}, nil, manager, tools, nil)

	fallbackStore := manager.longTerm
	if fallbackStore == nil {
		t.Fatal("fallback store not created")
	}

	if fallbackStore == preStore {
		t.Fatal("fallback store is same instance as pre-init store")
	}

	// Fallback cache should start empty
	fallbackStore.cacheMu.Lock()
	fallbackSize := fallbackStore.parsedCache.len()
	fallbackStore.cacheMu.Unlock()

	if fallbackSize != 0 {
		t.Fatalf("fallback cache size = %d, want 0", fallbackSize)
	}

	// But should still find the memory on disk
	results, err := fallbackStore.Search(ctx, MemoryQuery{
		Limit: 10,
	})
	if err != nil {
		t.Fatalf("fallback Search: %v", err)
	}

	if len(results) == 0 {
		t.Fatal("fallback search found 0 results, expected to find the pre-init memory")
	}
	found := false
	for _, r := range results {
		if r.ID == "mem_pre" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("fallback did not find 'mem_pre' in %d results", len(results))
	}

	plane := runtime.memoryPlane.(*FilesystemMemoryPlane)
	if plane.LongTerm() == preStore {
		t.Fatal("plane references pre-init store instead of fallback")
	}
	if plane.LongTerm() != fallbackStore {
		t.Fatal("plane does not reference fallback store")
	}
}

// TestRuntimeFallbackStoreIdempotency verifies that re-initializing runtime
// with the same manager reuses the existing store.
func TestRuntimeFallbackStoreIdempotency(t *testing.T) {
	configDir := t.TempDir()
	memoryDir := filepath.Join(configDir, "memory")

	manager := NewMemoryManager(memoryDir)
	tools := NewBuiltinToolSet(HIDConfig{}, AudioConfig{}, SearchConfig{}, ProxyConfig{})
	tools.RegisterMemoryTools(memoryDir, 0, nil)

	runtime1 := NewRuntimeWithDeps(Config{ConfigDir: configDir}, nil, manager, tools, nil)
	store1 := manager.longTerm
	if store1 == nil {
		t.Fatal("first runtime did not create fallback store")
	}

	tools2 := NewBuiltinToolSet(HIDConfig{}, AudioConfig{}, SearchConfig{}, ProxyConfig{})
	tools2.RegisterMemoryTools(memoryDir, 0, nil)
	runtime2 := NewRuntimeWithDeps(Config{ConfigDir: configDir}, nil, manager, tools2, nil)
	store2 := manager.longTerm

	if store2 != store1 {
		t.Fatal("second runtime created new store instead of reusing manager.longTerm")
	}

	plane1 := runtime1.memoryPlane.(*FilesystemMemoryPlane)
	plane2 := runtime2.memoryPlane.(*FilesystemMemoryPlane)

	if plane1.LongTerm() != store1 {
		t.Fatal("first plane does not reference store1")
	}
	if plane2.LongTerm() != store1 {
		t.Fatal("second plane does not reference store1")
	}
}

