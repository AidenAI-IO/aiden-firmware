package tts

import (
	"context"
	"sync"
)

// ProviderHolder holds a swappable TTSProvider behind a stable interface.
// Business code holds *ProviderHolder and calls BeginStream as usual.
//
// Safety guarantee: Swap waits for all in-flight sessions (created from the
// old provider) to finish before returning the old provider. This ensures
// the caller can safely Close the old provider without interrupting playback.
type ProviderHolder struct {
	mu      sync.RWMutex
	current TTSProvider

	// activeWG tracks in-flight sessions. Each BeginStream increments it;
	// the wrapped session's Close decrements it.
	activeWG *sync.WaitGroup
}

// NewProviderHolder wraps an initial provider.
func NewProviderHolder(initial TTSProvider) *ProviderHolder {
	return &ProviderHolder{current: initial, activeWG: &sync.WaitGroup{}}
}

// Swap replaces the current provider with next. It blocks until all sessions
// created from the OLD provider have been closed, then returns the old
// provider for the caller to Close.
func (h *ProviderHolder) Swap(next TTSProvider) TTSProvider {
	h.mu.Lock()
	old := h.current
	oldWG := h.activeWG
	h.current = next
	// Replace the WaitGroup for new sessions going forward.
	// We keep a reference to the old one to wait on.
	h.activeWG = &sync.WaitGroup{}
	h.mu.Unlock()

	// Wait for all in-flight sessions from the old provider to finish.
	oldWG.Wait()
	return old
}

func (h *ProviderHolder) Name() string {
	h.mu.RLock()
	defer h.mu.RUnlock()
	if h.current == nil {
		return ""
	}
	return h.current.Name()
}

func (h *ProviderHolder) Capabilities() Capabilities {
	h.mu.RLock()
	defer h.mu.RUnlock()
	if h.current == nil {
		return Capabilities{}
	}
	return h.current.Capabilities()
}

func (h *ProviderHolder) BeginStream(ctx context.Context, sink AudioSink) (StreamSession, error) {
	h.mu.RLock()
	p := h.current
	if p == nil {
		h.mu.RUnlock()
		return nil, ErrProviderNotFound
	}
	// Track this session in the current generation's WaitGroup.
	wg := h.activeWG
	wg.Add(1)
	h.mu.RUnlock()

	session, err := p.BeginStream(ctx, sink)
	if err != nil {
		wg.Done()
		return nil, err
	}

	return &trackedSession{
		StreamSession: session,
		done:          wg,
	}, nil
}

func (h *ProviderHolder) Close() error {
	h.mu.Lock()
	old := h.current
	oldWG := h.activeWG
	if old == nil {
		h.mu.Unlock()
		return nil
	}
	h.current = nil
	h.activeWG = &sync.WaitGroup{}
	h.mu.Unlock()

	oldWG.Wait()
	return old.Close()
}

// Compile-time check.
var _ TTSProvider = (*ProviderHolder)(nil)

// trackedSession wraps a StreamSession and decrements the WaitGroup on Close.
type trackedSession struct {
	StreamSession
	done      *sync.WaitGroup
	closeOnce sync.Once
	err       error
}

func (t *trackedSession) Close() error {
	t.closeOnce.Do(func() {
		defer t.done.Done()
		t.err = t.StreamSession.Close()
	})
	return t.err
}

func (t *trackedSession) Abort() error {
	t.closeOnce.Do(func() {
		defer t.done.Done()
		if aborter, ok := t.StreamSession.(interface{ Abort() error }); ok {
			t.err = aborter.Abort()
		} else {
			t.err = t.StreamSession.Close()
		}
	})
	return t.err
}
