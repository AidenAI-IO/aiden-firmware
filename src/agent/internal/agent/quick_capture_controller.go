package agent

import (
	"context"
	"errors"
	"sync"
	"time"
)

const quickCaptureTimeout = 60 * time.Second

var (
	ErrQuickCaptureBusy        = errors.New("quick capture already in progress")
	ErrQuickCaptureUnavailable = errors.New("quick capture is not configured")
)

type quickCapturer interface {
	Capture(ctx context.Context) (string, error)
}

type QuickCaptureController struct {
	capturer quickCapturer
	logger   *Logger

	mu      sync.Mutex
	running bool
	spawn   func(func())
}

func NewQuickCaptureController(capturer quickCapturer, logger *Logger) *QuickCaptureController {
	return &QuickCaptureController{
		capturer: capturer,
		logger:   logger,
		spawn:    func(run func()) { go run() },
	}
}

// Trigger starts one asynchronous capture. A second trigger is rejected until
// the current vision call and memory write finish.
func (c *QuickCaptureController) Trigger() error {
	if c == nil || c.capturer == nil {
		return ErrQuickCaptureUnavailable
	}

	c.mu.Lock()
	if c.running {
		c.mu.Unlock()
		return ErrQuickCaptureBusy
	}
	c.running = true
	spawn := c.spawn
	if spawn == nil {
		spawn = func(run func()) { go run() }
	}
	c.mu.Unlock()

	spawn(c.runCapture)
	return nil
}

func (c *QuickCaptureController) runCapture() {
	ctx, cancel := context.WithTimeout(context.Background(), quickCaptureTimeout)
	defer cancel()

	memoryID, err := c.capturer.Capture(ctx)

	c.mu.Lock()
	c.running = false
	c.mu.Unlock()

	if c.logger == nil {
		return
	}
	if err != nil {
		c.logger.Warn("[quick_capture] capture failed: %v", err)
		return
	}
	c.logger.Info("[quick_capture] screen memory saved: memory_id=%s", memoryID)
}
