package main

import (
	"context"
	"errors"
	"os"
	"syscall"
	"testing"
	"time"

	"aiden-agent/internal/agent"
	"aiden-agent/internal/agent/rtclient"
)

type fakeRealtimePlaybackAudio struct {
	nextSessionID uint64
	starts        int
	writes        []fakeRealtimePlaybackWrite
	stops         []uint64
}

func (f *fakeRealtimePlaybackAudio) WaitForPlaybackDrain(context.Context) error { return nil }

type fakeRealtimePlaybackWrite struct {
	sessionID uint64
	data      []byte
	final     bool
}

func (f *fakeRealtimePlaybackAudio) StartPlayback(agent.AudioFormat) (*agent.PlaybackStartResult, error) {
	f.starts++
	f.nextSessionID++
	return &agent.PlaybackStartResult{SessionID: f.nextSessionID}, nil
}

func (f *fakeRealtimePlaybackAudio) WritePlayChunk(sessionID uint64, data []byte, final bool) error {
	f.writes = append(f.writes, fakeRealtimePlaybackWrite{
		sessionID: sessionID,
		data:      append([]byte(nil), data...),
		final:     final,
	})
	return nil
}

func (f *fakeRealtimePlaybackAudio) StopPlayback(sessionID uint64) error {
	f.stops = append(f.stops, sessionID)
	return nil
}

func TestRealtimePlaybackStaysOpenAcrossResponsesAndInterrupts(t *testing.T) {
	audio := &fakeRealtimePlaybackAudio{}
	playback := realtimePlaybackState{}
	format := agent.AudioFormat{SampleRate: 24000, Channels: 1, BitWidth: 16}

	if err := playback.open(audio, format); err != nil {
		t.Fatal(err)
	}
	if err := playback.append(audio, format, []byte{1, 2}); err != nil {
		t.Fatal(err)
	}
	if err := playback.beginResponse(audio, format); err != nil {
		t.Fatal(err)
	}
	if audio.starts != 1 {
		t.Fatalf("playback starts = %d, want one persistent session", audio.starts)
	}

	if err := playback.interrupt(audio, format); err != nil {
		t.Fatal(err)
	}

	if len(audio.stops) != 1 || audio.stops[0] != 1 {
		t.Fatalf("stopped sessions = %v, want [1]", audio.stops)
	}
	if err := playback.append(audio, format, []byte{3, 4}); err != nil {
		t.Fatal(err)
	}
	if audio.starts != 2 || len(audio.writes) != 1 {
		t.Fatalf("after stale delta: starts=%d writes=%d, want starts=2 writes=1", audio.starts, len(audio.writes))
	}

	if err := playback.beginResponse(audio, format); err != nil {
		t.Fatal(err)
	}
	if err := playback.append(audio, format, []byte{5, 6}); err != nil {
		t.Fatal(err)
	}
	if audio.starts != 2 {
		t.Fatalf("playback starts = %d, want one restart after interruption", audio.starts)
	}
	for _, write := range audio.writes {
		if write.final {
			t.Fatalf("persistent realtime playback sent final write: %#v", write)
		}
	}
}

func TestRealtimeLocalPlaybackFinalizesEachResponse(t *testing.T) {
	audio := &fakeRealtimePlaybackAudio{}
	playback := realtimePlaybackState{finalizeResponses: true}
	format := agent.AudioFormat{SampleRate: 16000, Channels: 1, BitWidth: 16}

	if err := playback.open(audio, format); err != nil {
		t.Fatal(err)
	}
	if err := playback.append(audio, format, []byte{1, 2}); err != nil {
		t.Fatal(err)
	}
	if err := playback.finishResponse(audio); err != nil {
		t.Fatal(err)
	}
	if len(audio.writes) != 2 || !audio.writes[1].final {
		t.Fatalf("writes = %#v, want PCM followed by a final write", audio.writes)
	}
	if err := playback.beginResponse(audio, format); err != nil {
		t.Fatal(err)
	}
	if len(audio.stops) != 1 || audio.starts != 2 {
		t.Fatalf("after next response: starts=%d stops=%v, want starts=2 stops=[1]", audio.starts, audio.stops)
	}
}

func TestRealtimePersistentPlaybackCanFinalizeNotification(t *testing.T) {
	audio := &fakeRealtimePlaybackAudio{}
	playback := realtimePlaybackState{}
	format := agent.AudioFormat{SampleRate: 16000, Channels: 1, BitWidth: 16}

	if err := playback.open(audio, format); err != nil {
		t.Fatal(err)
	}
	if err := playback.append(audio, format, []byte{1, 2}); err != nil {
		t.Fatal(err)
	}
	if err := playback.finishResponse(audio, true); err != nil {
		t.Fatal(err)
	}
	if len(audio.writes) != 2 || !audio.writes[1].final {
		t.Fatalf("writes = %#v, want PCM followed by a final write", audio.writes)
	}
	if !playback.finalized {
		t.Fatal("notification finalization did not mark playback finalized")
	}
	playback.markDrained()
	if playback.session != nil || playback.finalized {
		t.Fatalf("drained playback state = %+v, want no active session", playback)
	}
}

func TestSuppressedRealtimeNotificationResponseBindsAfterResponseCreated(t *testing.T) {
	responses := make(map[string]struct{})
	pending := false
	markSuppressedRealtimeNotificationResponse("", &pending, responses)
	if !pending || len(responses) != 0 {
		t.Fatalf("pending suppression = %t, responses = %v", pending, responses)
	}
	if !bindSuppressedRealtimeNotificationResponse("resp-1", &pending, responses) {
		t.Fatal("response.created did not bind pending suppression")
	}
	if pending || !hasSuppressedNotificationResponse(responses, "resp-1") {
		t.Fatalf("bound suppression = pending:%t responses:%v", pending, responses)
	}
}

func TestSuppressedRealtimeNotificationResponseMarksKnownResponse(t *testing.T) {
	responses := make(map[string]struct{})
	pending := false
	markSuppressedRealtimeNotificationResponse("resp-1", &pending, responses)
	if pending || !hasSuppressedNotificationResponse(responses, "resp-1") {
		t.Fatalf("known suppression = pending:%t responses:%v", pending, responses)
	}
}

func TestRealtimePlaybackOutputFormatUsesConfiguredDeviceFormat(t *testing.T) {
	format := realtimePlaybackOutputFormat(agent.Config{Audio: agent.AudioConfig{
		SampleRate: 16000,
		Channels:   1,
		BitWidth:   16,
	}})
	if format.SampleRate != 16000 || format.Channels != 1 || format.BitWidth != 16 {
		t.Fatalf("format = %+v, want pcm/16000/mono/16", format)
	}
}

func TestDeliverPendingVoiceNotificationConfirmsSuccessfulFallback(t *testing.T) {
	runtime := agent.NewRuntimeWithDeps(agent.DefaultConfig(), nil, nil, nil, agent.NewSkillIndex())
	if err := runtime.VoiceNotificationSink().Publish(context.Background(), agent.VoiceNotificationEvent{
		Code: "storage", Severity: agent.SeverityWarning, State: agent.VoiceNotificationActive, DedupeKey: "storage:device",
	}); err != nil {
		t.Fatal(err)
	}
	var spoken string
	deliverPendingVoiceNotification(context.Background(), runtime, func(_ context.Context, text string) error {
		spoken = text
		return nil
	})
	if spoken == "" {
		t.Fatal("fallback speaker was not called")
	}
	if next := runtime.PrepareVoiceNotification(context.Background()); next.DeliveryToken != "" {
		t.Fatalf("successful fallback left notification pending: %#v", next)
	}
}

func TestDeliverPendingVoiceNotificationRetriesCanceledFallback(t *testing.T) {
	runtime := agent.NewRuntimeWithDeps(agent.DefaultConfig(), nil, nil, nil, agent.NewSkillIndex())
	if err := runtime.VoiceNotificationSink().Publish(context.Background(), agent.VoiceNotificationEvent{
		Code: "storage", Severity: agent.SeverityWarning, State: agent.VoiceNotificationActive, DedupeKey: "storage:device",
	}); err != nil {
		t.Fatal(err)
	}
	deliverPendingVoiceNotification(context.Background(), runtime, func(context.Context, string) error {
		return context.Canceled
	})
	if next := runtime.PrepareVoiceNotification(context.Background()); next.DeliveryToken == "" {
		t.Fatal("canceled fallback did not leave notification pending")
	}
}

type fakeRealtimeEventSource struct {
	events chan rtclient.Event
	errs   chan error
	done   chan struct{}
}

type blockingRealtimeTool struct {
	started chan struct{}
	release chan struct{}
}

func (t *blockingRealtimeTool) Name() string        { return realtimeRecallTool }
func (t *blockingRealtimeTool) Description() string { return "blocking recall" }
func (t *blockingRealtimeTool) Call(ctx context.Context, _ string) (string, error) {
	close(t.started)
	select {
	case <-t.release:
		return `{"status":"ok"}`, nil
	case <-ctx.Done():
		return "", ctx.Err()
	}
}

func (f *fakeRealtimeEventSource) Events() <-chan rtclient.Event { return f.events }
func (f *fakeRealtimeEventSource) Errors() <-chan error          { return f.errs }
func (f *fakeRealtimeEventSource) Done() <-chan struct{}         { return f.done }

func TestWaitForRealtimeEventIgnoresEarlierEvents(t *testing.T) {
	source := &fakeRealtimeEventSource{
		events: make(chan rtclient.Event, 2),
		errs:   make(chan error),
		done:   make(chan struct{}),
	}
	source.events <- rtclient.Event{Type: "session.created"}
	source.events <- rtclient.Event{Type: "session.updated"}

	if err := waitForRealtimeEvent(context.Background(), source, nil, "session.updated", time.Second); err != nil {
		t.Fatal(err)
	}
}

func TestWaitForRealtimeEventTimesOut(t *testing.T) {
	source := &fakeRealtimeEventSource{
		events: make(chan rtclient.Event),
		errs:   make(chan error),
		done:   make(chan struct{}),
	}

	if err := waitForRealtimeEvent(context.Background(), source, nil, "session.updated", time.Millisecond); err == nil {
		t.Fatal("expected timeout")
	}
}

func TestWaitForRealtimeEventPropagatesShutdown(t *testing.T) {
	source := &fakeRealtimeEventSource{
		events: make(chan rtclient.Event),
		errs:   make(chan error),
		done:   make(chan struct{}),
	}
	signals := make(chan os.Signal, 1)
	signals <- syscall.SIGTERM
	if err := waitForRealtimeEvent(context.Background(), source, signals, "session.updated", time.Second); !errors.Is(err, errRealtimeShutdown) {
		t.Fatalf("waitForRealtimeEvent() error = %v, want errRealtimeShutdown", err)
	}
}

func TestStartRealtimeToolCallDoesNotBlockEventLoop(t *testing.T) {
	tool := &blockingRealtimeTool{started: make(chan struct{}), release: make(chan struct{})}
	results := make(chan realtimeToolResult, 1)
	call := rtclient.FunctionCallEvent{ResponseID: "resp-1", CallID: "call-1", Name: realtimeRecallTool, Arguments: `{}`}

	startRealtimeToolCall(context.Background(), realtimeVoiceToolExecutor{recall: tool}, call, results)
	select {
	case <-tool.started:
	case <-time.After(time.Second):
		t.Fatal("tool call did not start")
	}
	select {
	case <-results:
		t.Fatal("tool call completed before release")
	default:
	}
	close(tool.release)
	select {
	case result := <-results:
		if result.call.CallID != call.CallID || result.output != `{"status":"ok"}` {
			t.Fatalf("tool result = %+v", result)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for asynchronous tool result")
	}
}

func TestRealtimeToolTrackerWaitsForAllResultsAfterResponseDone(t *testing.T) {
	tracker := newRealtimeToolTracker()
	tracker.start("resp-1")
	tracker.start("resp-1")
	if hasTools, continueNow := tracker.done("resp-1"); !hasTools || continueNow {
		t.Fatalf("done() = (%t, %t), want tools with deferred continuation", hasTools, continueNow)
	}
	if tracker.complete("resp-1") {
		t.Fatal("first tool result continued before all results completed")
	}
	if !tracker.complete("resp-1") {
		t.Fatal("last tool result did not release deferred continuation")
	}
}

func TestRealtimeToolTrackerContinuesWhenResultsPrecedeResponseDone(t *testing.T) {
	tracker := newRealtimeToolTracker()
	tracker.start("resp-1")
	if tracker.complete("resp-1") {
		t.Fatal("tool result continued before response.done")
	}
	if hasTools, continueNow := tracker.done("resp-1"); !hasTools || !continueNow {
		t.Fatalf("done() = (%t, %t), want immediate continuation", hasTools, continueNow)
	}
}

func TestRealtimeChatBridgeInactiveRequestQueuesAndWakesSession(t *testing.T) {
	wakeup := make(chan struct{}, 1)
	bridge := newRealtimeChatBridge(func() { wakeup <- struct{}{} })
	events, err := bridge.Handle(context.Background(), agent.RealtimeChatRequest{RequestID: "req-1", Message: "hello"})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-wakeup:
	case <-time.After(time.Second):
		t.Fatal("inactive request did not activate realtime session")
	}
	select {
	case command := <-bridge.commands:
		if command.request.Message != "hello" || command.request.RequestID != "req-1" {
			t.Fatalf("queued request = %+v", command.request)
		}
		if !sendRealtimeChatEvent(command, agent.RealtimeChatEvent{Type: agent.RealtimeChatEventDone, Response: "hi"}) {
			t.Fatal("failed to deliver bridge response")
		}
		close(command.events)
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for queued request")
	}
	select {
	case event := <-events:
		if event.Type != agent.RealtimeChatEventDone || event.Response != "hi" {
			t.Fatalf("event = %+v", event)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for bridge response")
	}
}

func TestRealtimeChatBridgeActiveRequestDoesNotWakeSessionAgain(t *testing.T) {
	wakeups := 0
	bridge := newRealtimeChatBridge(func() { wakeups++ })

	bridge.activate()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	events, err := bridge.Handle(ctx, agent.RealtimeChatRequest{RequestID: "req-1", Message: "hello"})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case command := <-bridge.commands:
		if command.request.Message != "hello" || command.request.RequestID != "req-1" {
			t.Fatalf("queued request = %+v", command.request)
		}
		if !sendRealtimeChatEvent(command, agent.RealtimeChatEvent{Type: agent.RealtimeChatEventDone, Response: "hi"}) {
			t.Fatal("failed to deliver bridge response")
		}
		close(command.events)
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for queued request")
	}
	select {
	case event := <-events:
		if event.Type != agent.RealtimeChatEventDone || event.Response != "hi" {
			t.Fatalf("event = %+v", event)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for bridge response")
	}
	bridge.deactivate()
	if wakeups != 0 {
		t.Fatalf("active request triggered %d extra wakeups", wakeups)
	}
}
