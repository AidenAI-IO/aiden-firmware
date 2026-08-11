package agent

import (
	"context"
	"sync"
	"time"
)

const reflectionIdleDelay = 2 * time.Minute

// reflectionWorker owns one resettable timer and never processes more than one
// Episode at a time. A foreground run does not wait for reflection: it stops
// the idle timer, and an in-flight reflection finishes its current Episode only.
type reflectionWorker struct {
	mu         sync.Mutex
	processor  *failureReflectionProcessor
	idleDelay  time.Duration
	timer      *time.Timer
	activeRuns int
	running    bool
	pending    bool
	stopped    bool
	idleSince  time.Time
	ctx        context.Context
	cancel     context.CancelFunc
	wg         sync.WaitGroup
}

func newReflectionWorker(processor *failureReflectionProcessor) *reflectionWorker {
	ctx, cancel := context.WithCancel(context.Background())
	return &reflectionWorker{
		processor: processor,
		idleDelay: reflectionIdleDelay,
		idleSince: time.Now(),
		ctx:       ctx,
		cancel:    cancel,
	}
}

func (w *reflectionWorker) Start() error {
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

func (w *reflectionWorker) NotifyFailure() {
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

func (w *reflectionWorker) TaskStarted() {
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
}

func (w *reflectionWorker) TaskFinished() {
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

func (w *reflectionWorker) Stop() {
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
	w.mu.Unlock()
	w.wg.Wait()
}

func (w *reflectionWorker) shouldStopBatch() bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.stopped || w.activeRuns > 0
}

func (w *reflectionWorker) scheduleLocked(delay time.Duration) {
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

func (w *reflectionWorker) stopTimerLocked() {
	if w.timer != nil {
		w.timer.Stop()
		w.timer = nil
	}
}

func (w *reflectionWorker) runBatch() {
	w.mu.Lock()
	if w.stopped || w.activeRuns > 0 || w.running {
		w.mu.Unlock()
		return
	}
	w.timer = nil
	w.running = true
	w.pending = false
	w.wg.Add(1)
	w.mu.Unlock()

	result, err := w.processor.ProcessBatch(w.ctx, reflectionBatchLimit, w.shouldStopBatch)
	w.processor.logBatchError(err)

	w.mu.Lock()
	w.running = false
	if err != nil {
		w.pending = true
	}
	if result.HasPending {
		w.pending = true
	}
	if !w.stopped && w.activeRuns == 0 {
		switch {
		case w.pending:
			w.scheduleLocked(w.idleDelay)
		case !result.NextRunAt.IsZero():
			w.pending = true
			w.scheduleLocked(w.delayFor(result.NextRunAt))
		}
	}
	w.mu.Unlock()
	w.wg.Done()
}

func (w *reflectionWorker) delayFor(due time.Time) time.Duration {
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
