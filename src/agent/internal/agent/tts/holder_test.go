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

type blockingProvider struct {
	name    string
	started chan *blockingSession
}

func (p *blockingProvider) Name() string { return p.name }

func (p *blockingProvider) Capabilities() Capabilities { return Capabilities{} }

func (p *blockingProvider) BeginStream(context.Context, AudioSink) (StreamSession, error) {
	s := &blockingSession{}
	p.started <- s
	return s, nil
}

func (p *blockingProvider) Close() error { return nil }

type blockingSession struct{}

func (s *blockingSession) WriteText(string) error { return nil }
func (s *blockingSession) Flush() error           { return nil }
func (s *blockingSession) Close() error           { return nil }
func (s *blockingSession) Err() error             { return nil }

type noopSink struct{}

func (noopSink) Format() AudioFormat         { return AudioFormat{} }
func (noopSink) WritePCM([]byte) error       { return nil }
func (noopSink) Drain(context.Context) error { return nil }
func (noopSink) Stop() error                 { return nil }
