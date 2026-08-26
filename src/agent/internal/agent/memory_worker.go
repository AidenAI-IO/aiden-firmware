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
	ProcessBatch(context.Context, func() bool) (MemoryBatchResult, error)
	logBatchError(error)
}

// MemoryWorker owns one resettable timer and never processes more than one
// batch at a time. A foreground run does not wait for consolidation: it stops
// the idle timer and cancels an in-flight background model call.
type MemoryWorker struct {
	mu          sync.Mutex
	processors  []MemoryProcessor
	idleDelay   time.Duration
	timer       *time.Timer
	activeRuns  int
	running     bool
	pending     bool
	stopped     bool
	started     bool
	idleSince   time.Time
	ctx         context.Context
	cancel      context.CancelFunc
	batchCancel context.CancelFunc
	current     MemoryProcessor
	currentStop context.CancelFunc
	currentDone chan struct{}
	wg          sync.WaitGroup
}

func newMemoryWorker(processor MemoryProcessor, idleDelay time.Duration) *MemoryWorker {
	return newMemoryWorkerWithProcessors([]MemoryProcessor{processor}, idleDelay)
}

func newMemoryWorkerWithProcessors(processors []MemoryProcessor, idleDelay time.Duration) *MemoryWorker {
	if idleDelay < 0 {
		idleDelay = 0
	}
	ctx, cancel := context.WithCancel(context.Background())
	return &MemoryWorker{
		processors: append([]MemoryProcessor(nil), processors...),
		idleDelay:  idleDelay,
		idleSince:  time.Now(),
		ctx:        ctx,
		cancel:     cancel,
	}
}

// AddProcessor attaches a scenario processor to the shared idle worker. The
// processor owns its batch size and persistence semantics; the worker only
// runs processors serially during one idle maintenance pass.
func (w *MemoryWorker) AddProcessor(processor MemoryProcessor) error {
	if w == nil || processor == nil {
		return nil
	}
	w.mu.Lock()
	if w.stopped {
		w.mu.Unlock()
		return errMemoryWorkerStopped
	}
	for _, registered := range w.processors {
		if registered == processor {
			w.mu.Unlock()
			return nil
		}
	}
	if !w.started {
		w.processors = append(w.processors, processor)
		w.mu.Unlock()
		return nil
	}
	w.mu.Unlock()

	if err := processor.Initialize(); err != nil {
		return err
	}
	next, err := processor.NextRunAt(w.ctx)
	if err != nil {
		return err
	}

	w.mu.Lock()
	defer w.mu.Unlock()
	if w.stopped {
		return errMemoryWorkerStopped
	}
	for _, registered := range w.processors {
		if registered == processor {
			return nil
		}
	}
	w.processors = append(w.processors, processor)
	if !next.IsZero() {
		w.pending = true
		if w.activeRuns == 0 && !w.running {
			w.scheduleLocked(w.delayFor(next))
		}
	}
	return nil
}

func (w *MemoryWorker) RemoveProcessor(processor MemoryProcessor) {
	if w == nil || processor == nil {
		return
	}
	w.mu.Lock()
	removed := false
	for index, registered := range w.processors {
		if registered != processor {
			continue
		}
		w.processors = append(w.processors[:index], w.processors[index+1:]...)
		removed = true
		break
	}
	waitForProcessor := removed && w.current == processor
	var processorDone <-chan struct{}
	if waitForProcessor && w.currentStop != nil {
		w.currentStop()
		processorDone = w.currentDone
	}
	w.mu.Unlock()
	if processorDone != nil {
		<-processorDone
	}
}

func (w *MemoryWorker) ProcessorCount() int {
	if w == nil {
		return 0
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	return len(w.processors)
}

func (w *MemoryWorker) Start() error {
	if w == nil {
		return nil
	}
	w.mu.Lock()
	if w.stopped {
		w.mu.Unlock()
		return errMemoryWorkerStopped
	}
	if w.started {
		w.mu.Unlock()
		return nil
	}
	processors := append([]MemoryProcessor(nil), w.processors...)
	w.mu.Unlock()
	if len(processors) == 0 {
		return nil
	}
	var next time.Time
	for _, processor := range processors {
		if err := processor.Initialize(); err != nil {
			return err
		}
		due, err := processor.NextRunAt(w.ctx)
		if err != nil {
			return err
		}
		if !due.IsZero() && (next.IsZero() || due.Before(next)) {
			next = due
		}
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.stopped {
		return errMemoryWorkerStopped
	}
	w.started = true
	if next.IsZero() {
		return nil
	}
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
		w.scheduleLocked(w.delayFor(time.Now()))
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
	if w.stopped || w.activeRuns > 0 || w.running || len(w.processors) == 0 {
		return
	}
	// A foreground turn ending is the shared wake-up for all maintenance
	// scenarios. Processors decide whether they actually have work; the worker
	// only provides the common idle window.
	w.pending = true
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
	if w == nil {
		return MemoryBatchResult{}, nil
	}
	w.mu.Lock()
	if len(w.processors) == 0 {
		w.mu.Unlock()
		return MemoryBatchResult{}, nil
	}
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

// ProcessProcessorNow runs one scenario processor immediately. It is used by
// explicit per-scenario APIs while normal maintenance runs all processors in
// order through ProcessNow.
func (w *MemoryWorker) ProcessProcessorNow(ctx context.Context, processor MemoryProcessor) (MemoryBatchResult, error) {
	if w == nil || processor == nil {
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
	found := false
	for _, candidate := range w.processors {
		if candidate == processor {
			found = true
			break
		}
	}
	if !found {
		w.mu.Unlock()
		return MemoryBatchResult{}, fmt.Errorf("memory processor is not registered")
	}
	w.stopTimerLocked()
	batchCtx, batchCancel := w.startBatchLocked(ctx)
	w.mu.Unlock()
	return w.executeProcessors(batchCtx, batchCancel, []MemoryProcessor{processor})
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
	return w.executeProcessors(ctx, cancel, nil)
}

func (w *MemoryWorker) executeProcessors(ctx context.Context, cancel context.CancelFunc, selected []MemoryProcessor) (result MemoryBatchResult, err error) {
	defer w.wg.Done()
	defer cancel()
	defer func() {
		if recovered := recover(); recovered != nil {
			w.finishBatch(result, fmt.Errorf("memory batch panic: %v", recovered))
			panic(recovered)
		}
	}()
	processors := selected
	if processors == nil {
		w.mu.Lock()
		processors = append([]MemoryProcessor(nil), w.processors...)
		w.mu.Unlock()
	}
	for _, processor := range processors {
		w.mu.Lock()
		registered := w.processorRegisteredLocked(processor)
		if registered {
			w.current = processor
		}
		processorCtx, processorCancel := context.WithCancel(ctx)
		if registered {
			w.currentStop = processorCancel
			w.currentDone = make(chan struct{})
		}
		w.mu.Unlock()
		if !registered {
			processorCancel()
			continue
		}
		if w.shouldStopBatch() {
			processorCancel()
			w.mu.Lock()
			if w.current == processor {
				w.current = nil
				w.currentStop = nil
				close(w.currentDone)
				w.currentDone = nil
			}
			w.mu.Unlock()
			result.HasPending = true
			break
		}
		batchResult, batchErr := processor.ProcessBatch(processorCtx, w.shouldStopBatch)
		processorCancel()
		w.mu.Lock()
		if w.current == processor {
			w.current = nil
			w.currentStop = nil
			close(w.currentDone)
			w.currentDone = nil
		}
		w.mu.Unlock()
		processor.logBatchError(batchErr)
		result.HasPending = result.HasPending || batchResult.HasPending
		if !batchResult.NextRunAt.IsZero() && (result.NextRunAt.IsZero() || batchResult.NextRunAt.Before(result.NextRunAt)) {
			result.NextRunAt = batchResult.NextRunAt
		}
		if batchErr != nil && err == nil {
			err = batchErr
		}
		if ctx.Err() != nil {
			break
		}
	}
	w.finishBatch(result, err)
	return result, err
}

func (w *MemoryWorker) finishBatch(result MemoryBatchResult, err error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.batchCancel = nil
	w.current = nil
	w.currentStop = nil
	if w.currentDone != nil {
		close(w.currentDone)
		w.currentDone = nil
	}
	w.running = false
	if result.HasPending {
		w.pending = true
	}
	continuePending := result.HasPending || w.pending
	if w.stopped || w.activeRuns > 0 {
		return
	}
	switch {
	case err != nil:
		w.scheduleLocked(w.idleDelay)
	case continuePending:
		// The worker has already passed the idle window. Continue bounded
		// batches without another five-minute wait while the Agent remains
		// idle; a foreground task still cancels the next/in-flight batch.
		w.scheduleLocked(w.delayFor(time.Now()))
	case !result.NextRunAt.IsZero():
		w.pending = true
		w.scheduleLocked(w.delayFor(result.NextRunAt))
	}
}

func (w *MemoryWorker) processorRegisteredLocked(processor MemoryProcessor) bool {
	for _, registered := range w.processors {
		if registered == processor {
			return true
		}
	}
	return false
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
