package tts

import (
	"context"
	"testing"
	"time"
)

func TestProviderHolderSwapWaitsForOldSessionClose(t *testing.T) {
	oldProvider := &blockingProvider{name: "old", started: make(chan *blockingSession, 1)}
	nextProvider := &blockingProvider{name: "next", started: make(chan *blockingSession, 1)}
	holder := NewProviderHolder(oldProvider)

	session, err := holder.BeginStream(context.Background(), noopSink{})
	if err != nil {
		t.Fatalf("BeginStream() error = %v", err)
	}
	select {
	case <-oldProvider.started:
	case <-time.After(time.Second):
		t.Fatal("old provider did not start a session")
	}

	swapped := make(chan TTSProvider, 1)
	go func() {
		swapped <- holder.Swap(nextProvider)
	}()

	select {
	case <-swapped:
		t.Fatal("Swap returned before the old session closed")
	case <-time.After(50 * time.Millisecond):
	}

	if err := session.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}

	select {
	case old := <-swapped:
		if old != oldProvider {
			t.Fatalf("Swap returned %#v, want old provider", old)
		}
	case <-time.After(time.Second):
		t.Fatal("Swap did not return after the old session closed")
	}
}

func TestProviderHolderCloseWaitsForActiveSessionClose(t *testing.T) {
	provider := &blockingProvider{name: "current", started: make(chan *blockingSession, 1), closed: make(chan struct{})}
	holder := NewProviderHolder(provider)

	session, err := holder.BeginStream(context.Background(), noopSink{})
	if err != nil {
		t.Fatalf("BeginStream() error = %v", err)
	}
	select {
	case <-provider.started:
	case <-time.After(time.Second):
		t.Fatal("provider did not start a session")
	}

	closedErr := make(chan error, 1)
	go func() {
		closedErr <- holder.Close()
	}()

	select {
	case <-provider.closed:
		t.Fatal("provider closed before active session closed")
	case <-closedErr:
		t.Fatal("holder.Close returned before active session closed")
	case <-time.After(50 * time.Millisecond):
	}
	if got := holder.Name(); got != "" {
		t.Fatalf("holder.Name() = %q while Close is waiting, want empty", got)
	}

	if err := session.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	select {
	case <-provider.closed:
	case <-time.After(time.Second):
		t.Fatal("provider did not close after active session closed")
	}
	select {
	case err := <-closedErr:
		if err != nil {
			t.Fatalf("holder.Close() error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("holder.Close did not return after active session closed")
	}
}

func TestProviderHolderTrackedSessionAbortReleasesActiveSession(t *testing.T) {
	provider := &blockingProvider{name: "current", started: make(chan *blockingSession, 1)}
	holder := NewProviderHolder(provider)

	session, err := holder.BeginStream(context.Background(), noopSink{})
	if err != nil {
		t.Fatalf("BeginStream() error = %v", err)
	}
	started := waitForBlockingSession(t, provider.started)

	swapped := make(chan TTSProvider, 1)
	go func() {
		swapped <- holder.Swap(&blockingProvider{name: "next", started: make(chan *blockingSession, 1)})
	}()

	select {
	case <-swapped:
		t.Fatal("Swap returned before the active session was aborted")
	case <-time.After(50 * time.Millisecond):
	}

	aborter, ok := session.(interface{ Abort() error })
	if !ok {
		t.Fatal("tracked session does not expose Abort")
	}
	if err := aborter.Abort(); err != nil {
		t.Fatalf("Abort() error = %v", err)
	}
	if started.abortCalls != 1 {
		t.Fatalf("underlying Abort() calls = %d, want 1", started.abortCalls)
	}
	if started.closeCalls != 0 {
		t.Fatalf("underlying Close() calls = %d, want 0", started.closeCalls)
	}

	select {
	case old := <-swapped:
		if old != provider {
			t.Fatalf("Swap returned %#v, want provider", old)
		}
	case <-time.After(time.Second):
		t.Fatal("Swap did not return after session abort")
	}
}

func waitForBlockingSession(t *testing.T, ch <-chan *blockingSession) *blockingSession {
	t.Helper()
	select {
	case session := <-ch:
		return session
	case <-time.After(time.Second):
		t.Fatal("provider did not start a session")
		return nil
	}
}

type blockingProvider struct {
	name    string
	started chan *blockingSession
	closed  chan struct{}
}

func (p *blockingProvider) Name() string { return p.name }

func (p *blockingProvider) Capabilities() Capabilities { return Capabilities{} }

func (p *blockingProvider) BeginStream(context.Context, AudioSink) (StreamSession, error) {
	s := &blockingSession{}
	p.started <- s
	return s, nil
}

func (p *blockingProvider) Close() error {
	if p.closed != nil {
		close(p.closed)
	}
	return nil
}

type blockingSession struct {
	closeCalls int
	abortCalls int
}

func (s *blockingSession) WriteText(string) error { return nil }
func (s *blockingSession) Flush() error           { return nil }
func (s *blockingSession) Close() error {
	s.closeCalls++
	return nil
}
func (s *blockingSession) Abort() error {
	s.abortCalls++
	return nil
}
func (s *blockingSession) Err() error { return nil }

type noopSink struct{}

func (noopSink) Format() AudioFormat         { return AudioFormat{} }
func (noopSink) WritePCM([]byte) error       { return nil }
func (noopSink) Drain(context.Context) error { return nil }
func (noopSink) Stop() error                 { return nil }

func TestTrackedSessionAbortAndCloseShareCloseOnce(t *testing.T) {
	provider := &blockingProvider{name: "test", started: make(chan *blockingSession, 1)}
	holder := NewProviderHolder(provider)

	session, err := holder.BeginStream(context.Background(), noopSink{})
	if err != nil {
		t.Fatalf("BeginStream() error = %v", err)
	}
	started := waitForBlockingSession(t, provider.started)

	aborter, ok := session.(interface{ Abort() error })
	if !ok {
		t.Fatal("tracked session does not expose Abort")
	}

	// Call both Abort and Close concurrently
	done := make(chan error, 2)
	go func() { done <- aborter.Abort() }()
	go func() { done <- session.Close() }()

	// Both should return without error
	for i := 0; i < 2; i++ {
		select {
		case err := <-done:
			if err != nil {
				t.Fatalf("concurrent call %d returned error: %v", i, err)
			}
		case <-time.After(time.Second):
			t.Fatal("concurrent call did not return")
		}
	}

	// Exactly one of Abort or Close should have been called on the underlying session
	totalCalls := started.abortCalls + started.closeCalls
	if totalCalls != 1 {
		t.Fatalf("underlying session got %d abort+close calls, want 1 (abort=%d, close=%d)",
			totalCalls, started.abortCalls, started.closeCalls)
	}
}
