package agent

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// shouldCompress is the only place where the "current model context window"
// matters at runtime. These tests pin its behaviour at three representative
// model tiers (8k / 32k / 128k) so a future refactor that breaks the
// dependency on the resolver-supplied window will fail visibly.

func TestShouldCompressUsesResolverContextWindow8k(t *testing.T) {
	cfg := DefaultMemoryExtractionConfig()
	cfg.ContextWindow = 32_000 // yaml fallback should be ignored
	cfg.CompressAtPercent = 50
	cfg.HotWindowEvents = 100 // event count must not be the trigger here

	mgr := NewMemoryManager("",
		WithExtractionConfig(cfg),
		WithContextWindowFn(func() int { return 8_000 }),
	)

	// 50% of 8k = 4000 tokens. 3999 must NOT trigger; 4000 must.
	mgr.SetLastPromptTokens(3999)
	if mgr.shouldCompress(5) {
		t.Fatalf("8k window: 3999 tokens (49.99%%) should not trigger compression")
	}
	mgr.SetLastPromptTokens(4000)
	if !mgr.shouldCompress(5) {
		t.Fatalf("8k window: 4000 tokens (50%%) should trigger compression")
	}
	// Sanity: 32k yaml fallback is NOT being read — 5000 / 32000 = 15.6%
	// would otherwise stay below threshold; instead 5000 / 8000 = 62.5%
	// which must trigger.
	mgr.SetLastPromptTokens(5000)
	if !mgr.shouldCompress(5) {
		t.Fatalf("8k window: 5000 tokens (62.5%% of 8k) should trigger; resolver value being ignored?")
	}
}

func TestShouldCompressUsesResolverContextWindow32k(t *testing.T) {
	cfg := DefaultMemoryExtractionConfig()
	cfg.ContextWindow = 8_000 // yaml fallback should be ignored
	cfg.CompressAtPercent = 50
	cfg.HotWindowEvents = 100

	mgr := NewMemoryManager("",
		WithExtractionConfig(cfg),
		WithContextWindowFn(func() int { return 32_000 }),
	)

	// 50% of 32k = 16000 tokens.
	mgr.SetLastPromptTokens(15_999)
	if mgr.shouldCompress(5) {
		t.Fatalf("32k window: 15999 tokens (49.99%%) should not trigger compression")
	}
	mgr.SetLastPromptTokens(16_000)
	if !mgr.shouldCompress(5) {
		t.Fatalf("32k window: 16000 tokens (50%%) should trigger compression")
	}
}

func TestShouldCompressUsesResolverContextWindow128k(t *testing.T) {
	cfg := DefaultMemoryExtractionConfig()
	cfg.ContextWindow = 32_000 // yaml fallback should be ignored
	cfg.CompressAtPercent = 50
	cfg.HotWindowEvents = 100

	mgr := NewMemoryManager("",
		WithExtractionConfig(cfg),
		WithContextWindowFn(func() int { return 128_000 }),
	)

	// 50% of 128k = 64000 tokens. With the old hardcoded 32k, 20000 tokens
	// (62.5% of 32k) would have triggered prematurely; against the real
	// 128k window it must NOT.
	mgr.SetLastPromptTokens(20_000)
	if mgr.shouldCompress(5) {
		t.Fatalf("128k window: 20000 tokens (15.6%%) should not trigger; old 32k hardcode would have")
	}
	mgr.SetLastPromptTokens(63_999)
	if mgr.shouldCompress(5) {
		t.Fatalf("128k window: 63999 tokens (49.99%%) should not trigger compression")
	}
	mgr.SetLastPromptTokens(64_000)
	if !mgr.shouldCompress(5) {
		t.Fatalf("128k window: 64000 tokens (50%%) should trigger compression")
	}
}

func TestShouldCompressFallsBackToYAMLWhenResolverUnknown(t *testing.T) {
	cfg := DefaultMemoryExtractionConfig()
	cfg.ContextWindow = 32_000
	cfg.CompressAtPercent = 50
	cfg.HotWindowEvents = 100

	mgr := NewMemoryManager("",
		WithExtractionConfig(cfg),
		// Resolver returns 0 → unknown model → fall back to yaml's 32k.
		WithContextWindowFn(func() int { return 0 }),
	)

	mgr.SetLastPromptTokens(15_999)
	if mgr.shouldCompress(5) {
		t.Fatalf("yaml fallback (32k): 15999 tokens should not trigger")
	}
	mgr.SetLastPromptTokens(16_000)
	if !mgr.shouldCompress(5) {
		t.Fatalf("yaml fallback (32k): 16000 tokens should trigger; fallback path broken")
	}
}

func TestShouldCompressFallsBackWhenNoResolverFnSet(t *testing.T) {
	cfg := DefaultMemoryExtractionConfig()
	cfg.ContextWindow = 10_000
	cfg.CompressAtPercent = 50
	cfg.HotWindowEvents = 100

	mgr := NewMemoryManager("", WithExtractionConfig(cfg))

	mgr.SetLastPromptTokens(4_999)
	if mgr.shouldCompress(5) {
		t.Fatalf("no resolver fn: 4999/10000 should not trigger")
	}
	mgr.SetLastPromptTokens(5_000)
	if !mgr.shouldCompress(5) {
		t.Fatalf("no resolver fn: 5000/10000 (50%%) should trigger")
	}
}

func TestMaintainFilesystemMemoryUsesResolverContextWindow(t *testing.T) {
	ctx := context.Background()
	storageDir := t.TempDir()

	cfg := DefaultMemoryExtractionConfig()
	cfg.ContextWindow = 1_000 // yaml fallback would have triggered at 600 tokens
	cfg.CompressAtPercent = 50
	cfg.HotWindowEvents = 100

	mgr := NewMemoryManager(storageDir,
		WithExtractionConfig(cfg),
		WithContextWindowFn(func() int { return 100_000 }),
	)

	sessionDir := filepath.Join(storageDir, "session")
	if err := os.MkdirAll(sessionDir, 0o755); err != nil {
		t.Fatalf("mkdir session: %v", err)
	}
	session := NewSessionMemoryStore(sessionDir)
	now := time.Now().UTC()
	for i := 0; i < 5; i++ {
		if _, err := session.AppendEvent(ctx, SessionEvent{
			EventID: fmt.Sprintf("evt_%d", i),
			Ts:      now.Format(time.RFC3339Nano),
			Type:    "user_input",
			Role:    "user",
			Content: "message",
		}); err != nil {
			t.Fatalf("AppendEvent: %v", err)
		}
	}

	// 600 tokens / 100k window = 0.6% — far below the 50% threshold.
	// Compression must NOT happen, even though the same prompt-tokens value
	// (600 / 1000 = 60%) would have tripped the old hardcoded path.
	mgr.SetLastPromptTokens(600)
	if err := mgr.maintainFilesystemMemory(ctx); err != nil {
		t.Fatalf("maintainFilesystemMemory: %v", err)
	}
	chunks, err := session.RecallChunks(ctx, ChunkRecallQuery{Limit: 10})
	if err != nil {
		t.Fatalf("RecallChunks: %v", err)
	}
	if len(chunks) != 0 {
		t.Fatalf("expected no compression with 100k resolver window; got %d chunks (resolver value being ignored?)", len(chunks))
	}
}

// TestMaintainFilesystemMemoryUpdatesLastPromptTokens verifies that
// maintainFilesystemMemory updates lastPromptTokens after compressing, so the
// maintenanceLoop does not immediately re-compress when maintenancePending is
// set during a prior round.
func TestMaintainFilesystemMemoryUpdatesLastPromptTokens(t *testing.T) {
	ctx := context.Background()
	storageDir := t.TempDir()

	cfg := DefaultMemoryExtractionConfig()
	cfg.ContextWindow = 10_000
	cfg.CompressAtPercent = 50
	cfg.HotWindowEvents = 6 // will keep ~6 events in hot window
	cfg.KeepRecentTokens = 300

	mgr := NewMemoryManager(storageDir,
		WithExtractionConfig(cfg),
		WithContextWindowFn(func() int { return 10_000 }),
	)

	sessionDir := filepath.Join(storageDir, "session")
	if err := os.MkdirAll(sessionDir, 0o755); err != nil {
		t.Fatalf("mkdir session: %v", err)
	}
	session := NewSessionMemoryStore(sessionDir)
	now := time.Now().UTC()

	// Create 15 events that will trigger compression: prompt shows 5500 tokens
	// (55% of 10k context window) which is above the 50% threshold.
	for i := 0; i < 15; i++ {
		if _, err := session.AppendEvent(ctx, SessionEvent{
			EventID: fmt.Sprintf("evt_%d", i),
			Ts:      now.Format(time.RFC3339Nano),
			Type:    "user_input",
			Role:    "user",
			Content: "message",
		}); err != nil {
			t.Fatalf("AppendEvent: %v", err)
		}
	}

	// Set a high lastPromptTokens that will trigger compression.
	mgr.SetLastPromptTokens(5500)

	// First compression round: should compress and update lastPromptTokens.
	if err := mgr.maintainFilesystemMemory(ctx); err != nil {
		t.Fatalf("maintainFilesystemMemory round 1: %v", err)
	}

	// After compression, lastPromptTokens should be updated to the estimated
	// token count of the hot window, which is much smaller than 5500. The
	// exact value depends on the kept events, but it should be well below the
	// 50% threshold (5000 tokens).
	updatedTokens := mgr.LastPromptTokens()
	if updatedTokens >= 5000 {
		t.Fatalf("after compression, lastPromptTokens should be updated to hot window estimate; got %d, want < 5000", updatedTokens)
	}

	// shouldCompress should now return false since the hot window is small.
	events, err := session.readEvents(session.eventsPath())
	if err != nil {
		t.Fatalf("readEvents after compression: %v", err)
	}
	if mgr.shouldCompress(len(events)) {
		t.Fatalf("after compression, shouldCompress should return false (lastPromptTokens=%d, event_count=%d)", updatedTokens, len(events))
	}

	// If we run maintainFilesystemMemory again (simulating maintenancePending
	// re-triggering), it should NOT compress again.
	chunksBefore, err := session.RecallChunks(ctx, ChunkRecallQuery{Limit: 100})
	if err != nil {
		t.Fatalf("RecallChunks before round 2: %v", err)
	}

	if err := mgr.maintainFilesystemMemory(ctx); err != nil {
		t.Fatalf("maintainFilesystemMemory round 2: %v", err)
	}

	chunksAfter, err := session.RecallChunks(ctx, ChunkRecallQuery{Limit: 100})
	if err != nil {
		t.Fatalf("RecallChunks after round 2: %v", err)
	}

	if len(chunksAfter) != len(chunksBefore) {
		t.Fatalf("second maintainFilesystemMemory created new chunks (spurious re-compression); before=%d, after=%d", len(chunksBefore), len(chunksAfter))
	}
}

// TestColdStartSeedsLastPromptTokensFromHotWindow verifies that loading a
// persisted session on a fresh MemoryManager (process restart) seeds
// lastPromptTokens from the estimated size of the hot window read off disk.
//
// Without this, the first shouldCompress after a restart sees
// lastPromptTokens == 0, skips the token-driven branch entirely, and falls
// back to the coarse event-count heuristic — which treats a hot window of
// large events the same as one of tiny events. Seeding restores token-driven
// compaction precision on the very first turn after a cold start.
func TestColdStartSeedsLastPromptTokensFromHotWindow(t *testing.T) {
	ctx := context.Background()
	storageDir := t.TempDir()

	cfg := DefaultMemoryExtractionConfig()
	cfg.ContextWindow = 10_000

	// Phase 1: a prior session writes events to disk, then "shuts down".
	sessionDir := filepath.Join(storageDir, "session")
	if err := os.MkdirAll(sessionDir, 0o755); err != nil {
		t.Fatalf("mkdir session: %v", err)
	}
	session := NewSessionMemoryStore(sessionDir)
	now := time.Now().UTC()
	for i := 0; i < 6; i++ {
		if _, err := session.AppendEvent(ctx, SessionEvent{
			EventID: fmt.Sprintf("evt_%d", i),
			Ts:      now.Format(time.RFC3339Nano),
			Type:    "user_input",
			Role:    "user",
			Content: tokenSizedContent(50), // ~50 tokens each
		}); err != nil {
			t.Fatalf("AppendEvent: %v", err)
		}
	}

	events, err := session.readEvents(session.eventsPath())
	if err != nil {
		t.Fatalf("readEvents: %v", err)
	}
	wantTokens := sumSessionEventTokens(events)
	if wantTokens <= 0 {
		t.Fatalf("test setup: expected positive token estimate, got %d", wantTokens)
	}

	// Phase 2: cold start — a brand new manager loads the persisted session.
	mgr := NewMemoryManager(storageDir,
		WithExtractionConfig(cfg),
		WithContextWindowFn(func() int { return 10_000 }),
	)
	if got := mgr.LastPromptTokens(); got != 0 {
		t.Fatalf("precondition: fresh manager should start at 0 tokens, got %d", got)
	}

	if _, err := mgr.Get("default", MemoryConfig{Type: "window", WindowSize: 10}); err != nil {
		t.Fatalf("Get: %v", err)
	}

	if got := mgr.LastPromptTokens(); got != wantTokens {
		t.Fatalf("cold start should seed lastPromptTokens from hot window estimate; got %d, want %d", got, wantTokens)
	}
}
