package agent

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestProfileDebouncerCoalescesFastCalls(t *testing.T) {
	var callCount atomic.Int64
	rebuild := func(ctx context.Context) error {
		callCount.Add(1)
		return nil
	}

	d := NewProfileDebouncer(rebuild, 200*time.Millisecond, nil)

	for i := 0; i < 5; i++ {
		d.RequestRebuild()
	}

	time.Sleep(50 * time.Millisecond)
	immediate := callCount.Load()
	if immediate != 1 {
		t.Fatalf("expected exactly 1 immediate rebuild, got %d", immediate)
	}

	time.Sleep(300 * time.Millisecond)
	total := callCount.Load()
	if total != 2 {
		t.Fatalf("expected 2 total rebuilds (1 immediate + 1 deferred), got %d", total)
	}

	if d.SkipCount() != 4 {
		t.Fatalf("expected 4 skips, got %d", d.SkipCount())
	}
}

func TestProfileDebouncerFlushDrainsPending(t *testing.T) {
	var callCount atomic.Int64
	rebuild := func(ctx context.Context) error {
		callCount.Add(1)
		return nil
	}

	d := NewProfileDebouncer(rebuild, 10*time.Second, nil)
	d.RequestRebuild()
	d.RequestRebuild()

	time.Sleep(20 * time.Millisecond)
	if callCount.Load() != 1 {
		t.Fatalf("expected 1 immediate rebuild, got %d", callCount.Load())
	}

	if err := d.Flush(context.Background()); err != nil {
		t.Fatalf("Flush() error = %v", err)
	}

	if callCount.Load() != 2 {
		t.Fatalf("expected 2 rebuilds after flush, got %d", callCount.Load())
	}
}

func TestProfileDebouncerFlushNoopWhenNothingPending(t *testing.T) {
	var callCount atomic.Int64
	rebuild := func(ctx context.Context) error {
		callCount.Add(1)
		return nil
	}

	d := NewProfileDebouncer(rebuild, time.Second, nil)

	if err := d.Flush(context.Background()); err != nil {
		t.Fatalf("Flush() error = %v", err)
	}

	if callCount.Load() != 0 {
		t.Fatalf("expected 0 rebuilds for noop flush, got %d", callCount.Load())
	}
}

func TestProfileDebouncerFlushPreservesPendingWhenWaitIdleFails(t *testing.T) {
	startedFirst := make(chan struct{})
	releaseFirst := make(chan struct{})
	var releaseOnce sync.Once
	release := func() {
		releaseOnce.Do(func() {
			close(releaseFirst)
		})
	}
	defer release()

	var callCount atomic.Int64
	rebuild := func(ctx context.Context) error {
		call := callCount.Add(1)
		if call == 1 {
			close(startedFirst)
			<-releaseFirst
		}
		return nil
	}

	d := NewProfileDebouncer(rebuild, time.Hour, nil)
	d.RequestRebuild()

	select {
	case <-startedFirst:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for first rebuild to start")
	}

	d.RequestRebuild()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	err := d.Flush(ctx)
	cancel()
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Flush() error = %v, want deadline exceeded", err)
	}
	if got := callCount.Load(); got != 1 {
		t.Fatalf("rebuild count after failed flush = %d, want 1", got)
	}

	release()
	if err := d.Flush(context.Background()); err != nil {
		t.Fatalf("second Flush() error = %v", err)
	}
	if got := callCount.Load(); got != 2 {
		t.Fatalf("rebuild count after retry flush = %d, want 2", got)
	}
}

func TestProfileDebouncerAllowsRebuildAfterIntervalExpires(t *testing.T) {
	var callCount atomic.Int64
	rebuild := func(ctx context.Context) error {
		callCount.Add(1)
		return nil
	}

	d := NewProfileDebouncer(rebuild, 100*time.Millisecond, nil)

	d.RequestRebuild()
	time.Sleep(20 * time.Millisecond)
	if callCount.Load() != 1 {
		t.Fatalf("expected 1 rebuild, got %d", callCount.Load())
	}

	time.Sleep(150 * time.Millisecond)

	d.RequestRebuild()
	time.Sleep(20 * time.Millisecond)
	if callCount.Load() != 2 {
		t.Fatalf("expected 2 rebuilds after interval, got %d", callCount.Load())
	}
}

func TestProfileDebouncerFiveConsecutiveSavesDoNotTriggerFiveRebuilds(t *testing.T) {
	var callCount atomic.Int64
	rebuild := func(ctx context.Context) error {
		callCount.Add(1)
		return nil
	}

	d := NewProfileDebouncer(rebuild, 60*time.Second, nil)

	for i := 0; i < 5; i++ {
		d.RequestRebuild()
	}

	time.Sleep(50 * time.Millisecond)
	if callCount.Load() > 1 {
		t.Fatalf("5 consecutive save_memory calls triggered %d rebuilds (expected <= 1)", callCount.Load())
	}

	if err := d.Flush(context.Background()); err != nil {
		t.Fatalf("Flush() error = %v", err)
	}

	final := callCount.Load()
	if final > 2 {
		t.Fatalf("expected at most 2 total rebuilds, got %d", final)
	}
	if final < 2 {
		t.Fatalf("expected 2 total rebuilds (1 immediate + 1 flush), got %d", final)
	}
}
