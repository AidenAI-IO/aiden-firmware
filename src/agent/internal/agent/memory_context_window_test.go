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
