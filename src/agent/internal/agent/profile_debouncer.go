package agent

import (
	"context"
	"sync"
	"sync/atomic"
	"time"
)

type ProfileRebuilder func(ctx context.Context) error

type ProfileDebouncer struct {
	rebuild  ProfileRebuilder
	interval time.Duration
	logger   *Logger

	mu          sync.Mutex
	pending     bool
	lastRebuild time.Time
	timer       *time.Timer

	rebuildCount atomic.Int64
	skipCount    atomic.Int64
}

func NewProfileDebouncer(rebuild ProfileRebuilder, interval time.Duration, logger *Logger) *ProfileDebouncer {
	return &ProfileDebouncer{
		rebuild:  rebuild,
		interval: interval,
		logger:   logger,
	}
}

func (d *ProfileDebouncer) RequestRebuild() {
	d.mu.Lock()
	defer d.mu.Unlock()

	since := time.Since(d.lastRebuild)
	if since < d.interval && !d.lastRebuild.IsZero() {
		d.pending = true
		d.skipCount.Add(1)
		if d.logger != nil {
			d.logger.Info("[profile-debouncer] skipped rebuild, last was %v ago (pending=true)", since.Round(time.Millisecond))
		}
		d.ensureTimerLocked()
		return
	}

	d.pending = false
	d.lastRebuild = time.Now()
	if d.logger != nil {
		d.logger.Info("[profile-debouncer] triggering async rebuild")
	}
	go d.doRebuild()
}

func (d *ProfileDebouncer) ensureTimerLocked() {
	if d.timer != nil {
		return
	}
	remaining := d.interval - time.Since(d.lastRebuild)
	if remaining <= 0 {
		remaining = d.interval
	}
	d.timer = time.AfterFunc(remaining, func() {
		d.mu.Lock()
		d.timer = nil
		shouldRebuild := d.pending
		d.pending = false
		if shouldRebuild {
			d.lastRebuild = time.Now()
		}
		d.mu.Unlock()

		if shouldRebuild {
			if d.logger != nil {
				d.logger.Info("[profile-debouncer] deferred rebuild firing")
			}
			d.doRebuild()
		}
	})
}

func (d *ProfileDebouncer) doRebuild() {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := d.rebuild(ctx); err != nil {
		if d.logger != nil {
			d.logger.Error("[profile-debouncer] rebuild failed: %v", err)
		}
		return
	}
	d.rebuildCount.Add(1)
	if d.logger != nil {
		d.logger.Info("[profile-debouncer] rebuild completed (total=%d)", d.rebuildCount.Load())
	}
}

func (d *ProfileDebouncer) Flush(ctx context.Context) error {
	d.mu.Lock()
	if d.timer != nil {
		d.timer.Stop()
		d.timer = nil
	}
	shouldRebuild := d.pending
	d.pending = false
	if shouldRebuild {
		d.lastRebuild = time.Now()
	}
	d.mu.Unlock()

	if !shouldRebuild {
		return nil
	}
	if d.logger != nil {
		d.logger.Info("[profile-debouncer] flush: running pending rebuild")
	}
	if err := d.rebuild(ctx); err != nil {
		return err
	}
	d.rebuildCount.Add(1)
	return nil
}

func (d *ProfileDebouncer) RebuildCount() int64 {
	return d.rebuildCount.Load()
}

func (d *ProfileDebouncer) SkipCount() int64 {
	return d.skipCount.Load()
}
