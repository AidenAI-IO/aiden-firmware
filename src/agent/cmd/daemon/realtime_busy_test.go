package main

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"aiden-agent/internal/agent"
	"aiden-agent/internal/agent/realtimevoice"
)

// Exercise the real daemon select loop, including its audio traffic and chat
// admission. This provider deliberately has no ResponseInterrupter, like Gemini.
type busyTestSession struct {
	events      chan realtimevoice.Event
	created     chan struct{}
	closed      chan struct{}
	toolResults chan struct{}
	blockCreate bool
}

func (s *busyTestSession) Info() realtimevoice.SessionInfo {
	return realtimevoice.SessionInfo{InputSampleRate: 16000, OutputSampleRate: 16000,
		Capabilities: realtimevoice.Capabilities{EmitsSpeechEvents: true}}
}
func (s *busyTestSession) Events() <-chan realtimevoice.Event      { return s.events }
func (s *busyTestSession) Errors() <-chan error                    { return nil }
func (s *busyTestSession) Done() <-chan struct{}                   { return s.closed }
func (s *busyTestSession) SendAudio(context.Context, []byte) error { return nil }
func (s *busyTestSession) SendText(context.Context, string) error  { return nil }
func (s *busyTestSession) SendToolResult(context.Context, string, string) error {
	s.toolResults <- struct{}{}
	return nil
}
func (s *busyTestSession) CreateResponse(ctx context.Context) error {
	s.created <- struct{}{}
	if s.blockCreate {
		<-ctx.Done()
		return ctx.Err()
	}
	return nil
}
func (s *busyTestSession) Close() error { close(s.closed); return nil }

type busyTestProvider struct{ session *busyTestSession }

func (p busyTestProvider) Open(context.Context, realtimevoice.SessionConfig) (realtimevoice.Session, error) {
	return p.session, nil
}

func busyTestAudio(t *testing.T) string {
	t.Helper()
	// macOS Unix socket paths cannot accommodate the usual long t.TempDir path.
	dir, err := os.MkdirTemp("/tmp", "busy-audio-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	path := filepath.Join(dir, "audio.sock")
	listener, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { listener.Close() })
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			go func() {
				defer conn.Close()
				for {
					var prefix [12]byte
					if _, err := io.ReadFull(conn, prefix[:]); err != nil {
						return
					}
					header := make([]byte, binary.LittleEndian.Uint32(prefix[:4]))
					if _, err := io.ReadFull(conn, header); err != nil {
						return
					}
					if _, err := io.CopyN(io.Discard, conn, int64(binary.LittleEndian.Uint64(prefix[4:]))); err != nil {
						return
					}
					var req struct {
						Op string `json:"op"`
					}
					if json.Unmarshal(header, &req) != nil {
						return
					}
					response := []byte(`{"status":"OK","session_id":"1"}`)
					var pcm []byte
					if req.Op == "read_record_chunk" {
						time.Sleep(5 * time.Millisecond)
						pcm = make([]byte, 320) // Continuous mic traffic must not reset the watchdog.
					}
					binary.LittleEndian.PutUint32(prefix[:4], uint32(len(response)))
					binary.LittleEndian.PutUint64(prefix[4:], uint64(len(pcm)))
					if _, err := conn.Write(append(append(prefix[:], response...), pcm...)); err != nil {
						return
					}
				}
			}()
		}
	}()
	return path
}

func startBusyTestSession(t *testing.T, timeout time.Duration, bridges ...*realtimeChatBridge) (*busyTestSession, *realtimeChatBridge, <-chan error) {
	return startBusyTestSessionWithBlockedCreate(t, timeout, false, bridges...)
}

func startBusyTestSessionWithBlockedCreate(t *testing.T, timeout time.Duration, blockCreate bool, bridges ...*realtimeChatBridge) (*busyTestSession, *realtimeChatBridge, <-chan error) {
	t.Helper()
	s := &busyTestSession{events: make(chan realtimevoice.Event, 32), created: make(chan struct{}, 32), closed: make(chan struct{}), toolResults: make(chan struct{}, 32)}
	s.blockCreate = blockCreate
	registry := realtimevoice.NewProviderRegistry()
	registry.Register("gemini", func(realtimevoice.ProviderConfig) realtimevoice.Provider { return busyTestProvider{s} })
	cfg := agent.Config{ConfigDir: t.TempDir(), VoiceModel: agent.VoiceModelConfig{Provider: "gemini"},
		Audio: agent.AudioConfig{Backend: "audio_service", Socket: busyTestAudio(t), SampleRate: 16000, Channels: 1, BitWidth: 16}}
	bridge := newRealtimeChatBridge()
	if len(bridges) > 0 {
		bridge = bridges[0]
	}
	signals := make(chan os.Signal, 1)
	done := make(chan error, 1)
	go func() { done <- runRealtimeSessionWithIdleTimeout(cfg, signals, nil, nil, registry, timeout, bridge) }()
	t.Cleanup(func() {
		signals <- os.Interrupt
		select {
		case <-s.closed:
		case <-time.After(2 * time.Second):
			t.Error("session did not close")
		}
	})
	return s, bridge, done
}

func TestRealtimeBusyCancelClosesUninterruptibleSession(t *testing.T) {
	s, bridge, done := startBusyTestSession(t, 60*time.Second)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	events, err := bridge.Handle(ctx, agent.RealtimeChatRequest{RequestID: "cancel-me", Message: "hello"})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-s.created:
	case err := <-done:
		t.Fatalf("session startup: %v", err)
	case <-time.After(2 * time.Second):
		t.Fatal("response not requested")
	}
	cancel()
	select {
	case <-s.closed:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("canceled Gemini-like request still owns the session; following requests can remain busy")
	}
	for range events {
	}
	bridge.mu.RLock()
	active := bridge.active
	bridge.mu.RUnlock()
	if active {
		t.Fatal("closed session still admits commands")
	}
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("session exit = %v, want cancellation", err)
	}
	// The next request uses a fresh provider session through the SAME bridge.
	next, _, nextDone := startBusyTestSession(t, time.Second, bridge)
	nextEvents := busyTestRequest(t, next, bridge, nextDone, "after-cancel")
	next.events <- realtimevoice.Event{Kind: realtimevoice.EventResponseDone, Status: "completed"}
	requireBusyEvent(t, nextEvents, agent.RealtimeChatEventDone)
}

func TestRealtimeBusyNoOutputTimesOutDespiteMicTraffic(t *testing.T) {
	s, bridge, done := startBusyTestSession(t, 100*time.Millisecond)
	events, err := bridge.Handle(context.Background(), agent.RealtimeChatRequest{RequestID: "stalled", Message: "hello"})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-s.created:
	case err := <-done:
		t.Fatalf("session startup: %v", err)
	case <-time.After(2 * time.Second):
		t.Fatal("response not requested")
	}
	requireBusyTimeout(t, events)
	select {
	case <-s.closed:
	case <-time.After(time.Second):
		t.Fatal("timed-out session not closed")
	}
}

func TestRealtimeBusyCancelDuringCreateClosesSession(t *testing.T) {
	s, bridge, done := startBusyTestSessionWithBlockedCreate(t, time.Second, true)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if _, err := bridge.Handle(ctx, agent.RealtimeChatRequest{RequestID: "cancel-create", Message: "hello"}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-s.created:
	case err := <-done:
		t.Fatalf("startup: %v", err)
	case <-time.After(time.Second):
		t.Fatal("create not reached")
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("exit=%v, want cancellation", err)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("partially sent response retained its session after cancellation")
	}
}

// Shared assertion used by the timeout tests below.
func requireBusyTimeout(t *testing.T, events <-chan agent.RealtimeChatEvent) {
	t.Helper()
	select {
	case event := <-events:
		if event.Type != agent.RealtimeChatEventError || !strings.Contains(event.Error, "no progress") {
			t.Fatalf("event = %+v, want explicit no-progress timeout", event)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("stalled response never timed out")
	}
}

func busyTestRequest(t *testing.T, s *busyTestSession, bridge *realtimeChatBridge, done <-chan error, id string) <-chan agent.RealtimeChatEvent {
	t.Helper()
	events, err := bridge.Handle(context.Background(), agent.RealtimeChatRequest{RequestID: id, Message: "hello"})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-s.created:
	case err := <-done:
		t.Fatalf("session exited before requesting response: %v", err)
	case <-time.After(2 * time.Second):
		t.Fatal("response not requested")
	}
	return events
}

func requireBusyEvent(t *testing.T, events <-chan agent.RealtimeChatEvent, kind string) {
	t.Helper()
	select {
	case event, ok := <-events:
		if !ok || event.Type != kind {
			t.Fatalf("event=%+v open=%t, want %s", event, ok, kind)
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("missing %s", kind)
	}
}

func TestRealtimeBusyStreamingRenewsDeadlineAndCompletionDisarmsIt(t *testing.T) {
	s, bridge, done := startBusyTestSession(t, 250*time.Millisecond)
	events := busyTestRequest(t, s, bridge, done, "streaming")
	s.events <- realtimevoice.Event{Kind: realtimevoice.EventResponseStarted, ResponseID: "r1"}
	// Total response time exceeds the timeout, but each real delta renews it.
	for range 5 {
		time.Sleep(80 * time.Millisecond)
		s.events <- realtimevoice.Event{Kind: realtimevoice.EventTranscriptDelta, Role: "assistant", TextSource: "text", Text: "word", ResponseID: "r1"}
		requireBusyEvent(t, events, agent.RealtimeChatEventDelta)
	}
	s.events <- realtimevoice.Event{Kind: realtimevoice.EventResponseDone, ResponseID: "r1", Status: "completed"}
	requireBusyEvent(t, events, agent.RealtimeChatEventDone)
	select {
	case err := <-done:
		t.Fatalf("idle conversation closed after completion: %v", err)
	case <-time.After(350 * time.Millisecond):
	}
	next := busyTestRequest(t, s, bridge, done, "next-turn")
	// Late output from the retired response must not complete the new request.
	s.events <- realtimevoice.Event{Kind: realtimevoice.EventResponseDone, ResponseID: "r1", Status: "completed"}
	s.events <- realtimevoice.Event{Kind: realtimevoice.EventResponseStarted, ResponseID: "r2"}
	s.events <- realtimevoice.Event{Kind: realtimevoice.EventResponseDone, ResponseID: "r2", Status: "completed"}
	requireBusyEvent(t, next, agent.RealtimeChatEventDone)
}

func TestRealtimeBusyNoiseDoesNotKeepStalledResponseAlive(t *testing.T) {
	s, bridge, done := startBusyTestSession(t, 150*time.Millisecond)
	events := busyTestRequest(t, s, bridge, done, "noise")
	s.events <- realtimevoice.Event{Kind: realtimevoice.EventResponseStarted, ResponseID: "current"}
	stop := make(chan struct{})
	defer close(stop)
	go func() {
		ticker := time.NewTicker(10 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-stop:
				return
			case <-s.closed:
				return
			case <-ticker.C:
				for _, event := range []realtimevoice.Event{
					{Kind: realtimevoice.EventReady},
					{Kind: realtimevoice.EventUsage},
					{Kind: realtimevoice.EventTranscriptDelta, Role: "user", Text: "mic"},
					{Kind: realtimevoice.EventAudio, ResponseID: "stale", PCM: []byte{0, 0}},
					{Kind: realtimevoice.EventTranscriptDelta, Role: "assistant", ResponseID: "current", Text: ""},
				} {
					select {
					case s.events <- event:
					case <-stop:
						return
					case <-s.closed:
						return
					}
				}
			}
		}
	}()
	requireBusyTimeout(t, events)
}

func TestRealtimeBusyAudioAndToolProgressRenewDeadline(t *testing.T) {
	s, bridge, done := startBusyTestSession(t, 300*time.Millisecond)
	events := busyTestRequest(t, s, bridge, done, "audio-tool")
	s.events <- realtimevoice.Event{Kind: realtimevoice.EventResponseStarted, ResponseID: "r"}
	time.Sleep(180 * time.Millisecond)
	s.events <- realtimevoice.Event{Kind: realtimevoice.EventAudio, ResponseID: "r", PCM: make([]byte, 320)}
	time.Sleep(180 * time.Millisecond)
	s.events <- realtimevoice.Event{Kind: realtimevoice.EventToolCall, ResponseID: "r", CallID: "tool", Name: "unknown_tool", Arguments: "{}"}
	select {
	case <-s.toolResults:
	case err := <-done:
		t.Fatalf("session exited before tool result: %v", err)
	case <-time.After(time.Second):
		t.Fatal("tool result not sent")
	}
	time.Sleep(180 * time.Millisecond)
	s.events <- realtimevoice.Event{Kind: realtimevoice.EventResponseDone, ResponseID: "r", Status: "completed"}
	requireBusyEvent(t, events, agent.RealtimeChatEventDone)
}

func TestRealtimeBusyUnansweredVoiceTurnTimesOut(t *testing.T) {
	s, bridge, done := startBusyTestSession(t, 100*time.Millisecond)
	events := busyTestRequest(t, s, bridge, done, "setup")
	s.events <- realtimevoice.Event{Kind: realtimevoice.EventResponseDone, Status: "completed"}
	requireBusyEvent(t, events, agent.RealtimeChatEventDone)
	s.events <- realtimevoice.Event{Kind: realtimevoice.EventSpeechStarted}
	s.events <- realtimevoice.Event{Kind: realtimevoice.EventSpeechStopped}
	select {
	case err := <-done:
		if err == nil || !strings.Contains(err.Error(), "no progress") {
			t.Fatalf("session exit=%v, want timeout", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("pending voice turn permanently blocks text admission")
	}
}
