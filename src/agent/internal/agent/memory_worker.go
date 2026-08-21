package agent

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"
)

const defaultMemoryWorkerIdleDelay = 5 * time.Minute

var (
	errMemoryWorkerBusy    = errors.New("memory worker is busy")
	errMemoryWorkerStopped = errors.New("memory worker is stopped")
)

// MemoryBatchResult describes the scheduling outcome of one scenario batch.
// A processor owns the meaning of pending work and the next due time; the
// worker only applies the scheduling policy consistently across scenarios.
type MemoryBatchResult struct {
	HasPending bool
	NextRunAt  time.Time
}

// MemoryProcessor is the scenario-specific half of a MemoryWorker. It owns
// persistence, admission, model interaction, and applying the batch result.
// The worker owns lifecycle, cancellation, foreground preemption, and timer
// scheduling.
type MemoryProcessor interface {
	Initialize() error
	NextRunAt(context.Context) (time.Time, error)
	ProcessBatch(context.Context, int, func() bool) (MemoryBatchResult, error)
	logBatchError(error)
}

// MemoryWorker owns one resettable timer and never processes more than one
// batch at a time. A foreground run does not wait for consolidation: it stops
// the idle timer and cancels an in-flight background model call.
type MemoryWorker struct {
	mu          sync.Mutex
	processor   MemoryProcessor
	batchLimit  int
	idleDelay   time.Duration
	timer       *time.Timer
	activeRuns  int
	running     bool
	pending     bool
	stopped     bool
	idleSince   time.Time
	ctx         context.Context
	cancel      context.CancelFunc
	batchCancel context.CancelFunc
	wg          sync.WaitGroup
}

func newMemoryWorker(processor MemoryProcessor, batchLimit int, idleDelay time.Duration) *MemoryWorker {
	if batchLimit <= 0 {
		batchLimit = 1
	}
	if idleDelay < 0 {
		idleDelay = 0
	}
	ctx, cancel := context.WithCancel(context.Background())
	return &MemoryWorker{
		processor:  processor,
		batchLimit: batchLimit,
		idleDelay:  idleDelay,
		idleSince: time.Now(),
		ctx:       ctx,
		cancel:    cancel,
	}
}

func (w *MemoryWorker) Start() error {
	if w == nil || w.processor == nil {
		return nil
	}
	if err := w.processor.Initialize(); err != nil {
		return err
	}
	next, err := w.processor.NextRunAt(w.ctx)
	if err != nil {
		return err
	}
	if next.IsZero() {
		return nil
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	w.pending = true
	w.scheduleLocked(w.delayFor(next))
	return nil
}

func (w *MemoryWorker) Notify() {
	if w == nil {
		return
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.stopped {
		return
	}
	w.pending = true
	if w.activeRuns == 0 && !w.running {
		w.scheduleLocked(w.idleDelay)
	}
}

func (w *MemoryWorker) TaskStarted() {
	if w == nil {
		return
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.stopped {
		return
	}
	w.activeRuns++
	w.idleSince = time.Time{}
	w.stopTimerLocked()
	if w.batchCancel != nil {
		w.batchCancel()
	}
}

func (w *MemoryWorker) TaskFinished() {
	if w == nil {
		return
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.activeRuns > 0 {
		w.activeRuns--
	}
	if w.activeRuns == 0 {
		w.idleSince = time.Now()
	}
	if w.stopped || w.activeRuns > 0 || w.running || !w.pending {
		return
	}
	w.scheduleLocked(w.idleDelay)
}

func (w *MemoryWorker) Stop() {
	if w == nil {
		return
	}
	w.mu.Lock()
	if w.stopped {
		w.mu.Unlock()
		return
	}
	w.stopped = true
	w.stopTimerLocked()
	w.cancel()
	if w.batchCancel != nil {
		w.batchCancel()
	}
	w.mu.Unlock()
	w.wg.Wait()
}

func (w *MemoryWorker) ProcessNow(ctx context.Context) (MemoryBatchResult, error) {
	if w == nil || w.processor == nil {
		return MemoryBatchResult{}, nil
	}
	w.mu.Lock()
	if w.stopped {
		w.mu.Unlock()
		return MemoryBatchResult{}, errMemoryWorkerStopped
	}
	if w.activeRuns > 0 || w.running {
		w.mu.Unlock()
		return MemoryBatchResult{}, errMemoryWorkerBusy
	}
	w.stopTimerLocked()
	batchCtx, batchCancel := w.startBatchLocked(ctx)
	w.mu.Unlock()

	return w.executeBatch(batchCtx, batchCancel)
}

func (w *MemoryWorker) startBatchLocked(parent context.Context) (context.Context, context.CancelFunc) {
	w.running = true
	w.pending = false
	batchCtx, batchCancel := context.WithCancel(parent)
	w.batchCancel = batchCancel
	w.wg.Add(1)
	return batchCtx, batchCancel
}

func (w *MemoryWorker) executeBatch(ctx context.Context, cancel context.CancelFunc) (result MemoryBatchResult, err error) {
	defer w.wg.Done()
	defer cancel()
	defer func() {
		if recovered := recover(); recovered != nil {
			w.finishBatch(result, fmt.Errorf("memory batch panic: %v", recovered))
			panic(recovered)
		}
	}()
	result, err = w.processor.ProcessBatch(ctx, w.batchLimit, w.shouldStopBatch)
	w.processor.logBatchError(err)
	w.finishBatch(result, err)
	return result, err
}

func (w *MemoryWorker) finishBatch(result MemoryBatchResult, err error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.batchCancel = nil
	w.running = false
	if err != nil || result.HasPending {
		w.pending = true
	}
	if w.stopped || w.activeRuns > 0 {
		return
	}
	switch {
	case w.pending:
		w.scheduleLocked(w.idleDelay)
	case !result.NextRunAt.IsZero():
		w.pending = true
		w.scheduleLocked(w.delayFor(result.NextRunAt))
	}
}

func (w *MemoryWorker) shouldStopBatch() bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.stopped || w.activeRuns > 0
}

func (w *MemoryWorker) scheduleLocked(delay time.Duration) {
	if w.stopped || w.activeRuns > 0 || w.running {
		return
	}
	if delay < 0 {
		delay = 0
	}
	if w.timer == nil {
		w.timer = time.AfterFunc(delay, w.runBatch)
		return
	}
	w.timer.Stop()
	w.timer.Reset(delay)
}

func (w *MemoryWorker) stopTimerLocked() {
	if w.timer != nil {
		w.timer.Stop()
		w.timer = nil
	}
}

func (w *MemoryWorker) runBatch() {
	w.mu.Lock()
	if w.stopped || w.activeRuns > 0 || w.running {
		w.mu.Unlock()
		return
	}
	w.timer = nil
	batchCtx, batchCancel := w.startBatchLocked(w.ctx)
	w.mu.Unlock()

	_, _ = w.executeBatch(batchCtx, batchCancel)
}

func (w *MemoryWorker) delayFor(due time.Time) time.Duration {
	now := time.Now()
	runAt := due
	if runAt.IsZero() || runAt.Before(now) {
		runAt = now
	}
	idleReadyAt := now.Add(w.idleDelay)
	if !w.idleSince.IsZero() {
		idleReadyAt = w.idleSince.Add(w.idleDelay)
	}
	if runAt.Before(idleReadyAt) {
		runAt = idleReadyAt
	}
	delay := time.Until(runAt)
	if delay < 0 {
		delay = 0
	}
	return delay
}
