package agent

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

type recordingMemoryProcessor struct {
	name  string
	order *[]string
	mu    *sync.Mutex
}

func (p *recordingMemoryProcessor) Initialize() error { return nil }
func (p *recordingMemoryProcessor) NextRunAt(context.Context) (time.Time, error) {
	return time.Now(), nil
}
func (p *recordingMemoryProcessor) ProcessBatch(context.Context, func() bool) (MemoryBatchResult, error) {
	p.mu.Lock()
	*p.order = append(*p.order, p.name)
	p.mu.Unlock()
	return MemoryBatchResult{}, nil
}
func (p *recordingMemoryProcessor) logBatchError(error) {}

type pendingOnceMemoryProcessor struct {
	mu      sync.Mutex
	calls   int
	drained chan struct{}
}

func (p *pendingOnceMemoryProcessor) Initialize() error { return nil }
func (p *pendingOnceMemoryProcessor) NextRunAt(context.Context) (time.Time, error) {
	return time.Now(), nil
}
func (p *pendingOnceMemoryProcessor) ProcessBatch(context.Context, func() bool) (MemoryBatchResult, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.calls++
	if p.calls == 1 {
		return MemoryBatchResult{HasPending: true}, nil
	}
	close(p.drained)
	return MemoryBatchResult{}, nil
}
func (p *pendingOnceMemoryProcessor) logBatchError(error) {}

type pendingErrorOnceMemoryProcessor struct {
	mu      sync.Mutex
	calls   int
	drained chan struct{}
}

func (p *pendingErrorOnceMemoryProcessor) Initialize() error { return nil }
func (p *pendingErrorOnceMemoryProcessor) NextRunAt(context.Context) (time.Time, error) {
	return time.Now(), nil
}
func (p *pendingErrorOnceMemoryProcessor) ProcessBatch(context.Context, func() bool) (MemoryBatchResult, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.calls++
	if p.calls == 1 {
		return MemoryBatchResult{HasPending: true}, errors.New("temporary batch failure")
	}
	close(p.drained)
	return MemoryBatchResult{}, nil
}
func (p *pendingErrorOnceMemoryProcessor) logBatchError(error) {}

type signalingMemoryProcessor struct {
	started chan struct{}
}

func (p *signalingMemoryProcessor) Initialize() error { return nil }
func (p *signalingMemoryProcessor) NextRunAt(context.Context) (time.Time, error) {
	return time.Time{}, nil
}
func (p *signalingMemoryProcessor) ProcessBatch(ctx context.Context, _ func() bool) (MemoryBatchResult, error) {
	select {
	case p.started <- struct{}{}:
	default:
	}
	<-ctx.Done()
	return MemoryBatchResult{}, ctx.Err()
}
func (p *signalingMemoryProcessor) logBatchError(error) {}

func TestMemoryWorkerRunsRegisteredProcessorsSerially(t *testing.T) {
	var order []string
	var mu sync.Mutex
	first := &recordingMemoryProcessor{name: "episode", order: &order, mu: &mu}
	second := &recordingMemoryProcessor{name: "notification", order: &order, mu: &mu}
	worker := newMemoryWorkerWithProcessors(nil, time.Hour)
	if err := worker.AddProcessor(first); err != nil {
		t.Fatal(err)
	}
	if err := worker.AddProcessor(second); err != nil {
		t.Fatal(err)
	}
	result, err := worker.ProcessNow(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if result.HasPending {
		t.Fatal("ProcessNow() reported pending work")
	}
	mu.Lock()
	defer mu.Unlock()
	if len(order) != 2 || order[0] != "episode" || order[1] != "notification" {
		t.Fatalf("processor order = %#v, want [episode notification]", order)
	}
	worker.Stop()
}

func TestMemoryWorkerContinuesPendingBatchesWithinIdleWindow(t *testing.T) {
	processor := &pendingOnceMemoryProcessor{drained: make(chan struct{})}
	worker := newMemoryWorker(processor, time.Hour)
	worker.mu.Lock()
	worker.idleSince = time.Now().Add(-2 * time.Hour)
	worker.mu.Unlock()
	defer worker.Stop()

	result, err := worker.ProcessNow(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !result.HasPending {
		t.Fatal("first batch did not report pending work")
	}
	select {
	case <-processor.drained:
	case <-time.After(time.Second):
		t.Fatal("pending batch waited for another idle delay")
	}
	processor.mu.Lock()
	defer processor.mu.Unlock()
	if processor.calls != 2 {
		t.Fatalf("processor calls = %d, want 2", processor.calls)
	}
}

func TestMemoryWorkerBacksOffPendingBatchErrors(t *testing.T) {
	processor := &pendingErrorOnceMemoryProcessor{drained: make(chan struct{})}
	worker := newMemoryWorker(processor, 50*time.Millisecond)
	worker.mu.Lock()
	worker.idleSince = time.Now().Add(-time.Hour)
	worker.mu.Unlock()
	defer worker.Stop()

	result, err := worker.ProcessNow(context.Background())
	if err == nil || !result.HasPending {
		t.Fatalf("ProcessNow() result=%#v err=%v, want pending error", result, err)
	}
	select {
	case <-processor.drained:
		t.Fatal("pending error retried without backoff")
	case <-time.After(10 * time.Millisecond):
	}
	select {
	case <-processor.drained:
	case <-time.After(time.Second):
		t.Fatal("pending error was not retried after backoff")
	}
}

func TestMemoryWorkerNotifyRunsImmediatelyWhenAlreadyIdle(t *testing.T) {
	processor := &recordingMemoryProcessor{name: "notification", order: &[]string{}, mu: &sync.Mutex{}}
	worker := newMemoryWorker(processor, time.Hour)
	if err := worker.Start(); err != nil {
		t.Fatal(err)
	}
	defer worker.Stop()
	worker.mu.Lock()
	worker.idleSince = time.Now().Add(-2 * time.Hour)
	worker.mu.Unlock()

	worker.Notify()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		processor.mu.Lock()
		called := len(*processor.order) > 0
		processor.mu.Unlock()
		if called {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("notification waited for another idle delay after the Agent was already idle")
}

func TestMemoryWorkerRemoveProcessorCancelsAndWaitsForInFlightBatch(t *testing.T) {
	processor := &signalingMemoryProcessor{started: make(chan struct{}, 1)}
	worker := newMemoryWorker(processor, time.Hour)
	done := make(chan struct{})
	go func() {
		_, _ = worker.ProcessNow(context.Background())
		close(done)
	}()
	select {
	case <-processor.started:
	case <-time.After(time.Second):
		t.Fatal("processor did not start")
	}

	worker.RemoveProcessor(processor)
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("RemoveProcessor returned before the in-flight processor stopped")
	}
	worker.Stop()
}
