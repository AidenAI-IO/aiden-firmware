package agent

import (
	"context"
	"io"
	"log"
	"strings"
	"sync"
	"time"
)

const (
	defaultProgressSpeechDelay       = 800 * time.Millisecond
	defaultProgressSpeechMinInterval = 3 * time.Second
	defaultProgressSpeechMaxRunes    = 40
)

type progressSpeaker struct {
	speak       func(context.Context, string) error
	delay       time.Duration
	minInterval time.Duration
	maxRunes    int
	logf        func(string, ...any)

	mu          sync.Mutex
	timer       *time.Timer
	generation  int64
	pending     string
	lastStarted time.Time
	cancelled   bool
}

type progressSpeakerConfig struct {
	Delay       time.Duration
	MinInterval time.Duration
	MaxRunes    int
	Logf        func(string, ...any)
}

func newProgressSpeaker(speak func(context.Context, string) error, cfg progressSpeakerConfig) *progressSpeaker {
	delay := cfg.Delay
	if delay <= 0 {
		delay = defaultProgressSpeechDelay
	}
	minInterval := cfg.MinInterval
	if minInterval < 0 {
		minInterval = 0
	} else if minInterval == 0 {
		minInterval = defaultProgressSpeechMinInterval
	}
	maxRunes := cfg.MaxRunes
	if maxRunes <= 0 {
		maxRunes = defaultProgressSpeechMaxRunes
	}
	logf := cfg.Logf
	if logf == nil {
		logf = log.Printf
	}
	return &progressSpeaker{
		speak:       speak,
		delay:       delay,
		minInterval: minInterval,
		maxRunes:    maxRunes,
		logf:        logf,
	}
}

func (p *progressSpeaker) Schedule(ctx context.Context, text string) {
	if p == nil || p.speak == nil {
		return
	}
	text = truncateTodoRunes(strings.Join(strings.Fields(strings.TrimSpace(text)), " "), p.maxRunes)
	if text == "" {
		return
	}
	if ctx == nil {
		ctx = context.Background()
	}

	p.mu.Lock()
	defer p.mu.Unlock()
	if p.cancelled {
		return
	}
	p.pending = text
	p.generation++
	generation := p.generation
	if p.timer != nil {
		p.timer.Stop()
	}
	wait := p.delay
	if !p.lastStarted.IsZero() {
		if remaining := p.minInterval - time.Since(p.lastStarted); remaining > wait {
			wait = remaining
		}
	}
	p.timer = time.AfterFunc(wait, func() {
		p.fire(ctx, generation)
	})
}

func (p *progressSpeaker) Cancel() {
	if p == nil {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.cancelled = true
	p.pending = ""
	p.generation++
	if p.timer != nil {
		p.timer.Stop()
		p.timer = nil
	}
}

func (p *progressSpeaker) fire(ctx context.Context, generation int64) {
	p.mu.Lock()
	if p.cancelled || generation != p.generation || strings.TrimSpace(p.pending) == "" {
		p.mu.Unlock()
		return
	}
	text := p.pending
	p.pending = ""
	p.lastStarted = time.Now()
	p.mu.Unlock()

	if err := ctx.Err(); err != nil {
		return
	}
	if err := p.speak(ctx, text); err != nil {
		p.logf("[progress_speech] TTS failed: %v", err)
	}
}

func progressSpeechTextForEvent(event RunEvent) (string, bool) {
	if event.Type != "todo_update" || event.Todo == nil {
		return "", false
	}
	item, ok := event.Todo.CurrentItem()
	if !ok || item.Status != TodoInProgress {
		return "", false
	}
	text := strings.TrimSpace(event.Content)
	if text == "" {
		text = event.Todo.CurrentSpeech()
	}
	text = truncateTodoRunes(strings.Join(strings.Fields(strings.TrimSpace(text)), " "), defaultProgressSpeechMaxRunes)
	return text, text != ""
}

type cancelOnFirstWriteWriter struct {
	inner  io.Writer
	cancel func()
	once   sync.Once
}

func newCancelOnFirstWriteWriter(inner io.Writer, cancel func()) io.Writer {
	if inner == nil || cancel == nil {
		return inner
	}
	return &cancelOnFirstWriteWriter{inner: inner, cancel: cancel}
}

func (w *cancelOnFirstWriteWriter) Write(p []byte) (int, error) {
	if len(p) > 0 {
		w.once.Do(w.cancel)
	}
	return w.inner.Write(p)
}
