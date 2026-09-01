package main

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"aiden-agent/internal/agent"
	"aiden-agent/internal/agent/realtimevoice"
)

type fakeRealtimePlaybackAudio struct {
	nextSessionID uint64
	starts        int
	writes        []fakeRealtimePlaybackWrite
	stops         []uint64
}

type fakeRealtimePlaybackWrite struct {
	sessionID uint64
	data      []byte
	final     bool
}

type fakeRealtimeResponseInterrupter struct {
	calls int
	last  realtimevoice.ResponseInterruption
}

func (f *fakeRealtimeResponseInterrupter) Interrupt(_ context.Context, interruption realtimevoice.ResponseInterruption) error {
	f.calls++
	f.last = interruption
	return nil
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

func TestEnsureImplicitRealtimeResponseReopensPlaybackAfterInterruption(t *testing.T) {
	audio := &fakeRealtimePlaybackAudio{}
	playback := realtimePlaybackState{}
	format := agent.AudioFormat{SampleRate: 24000, Channels: 1, BitWidth: 16}
	if err := playback.open(audio, format); err != nil {
		t.Fatal(err)
	}
	if err := playback.interrupt(audio, format); err != nil {
		t.Fatal(err)
	}
	active := false
	assistantPersisted := true
	responseText := strings.Builder{}
	responseText.WriteString("stale")
	responseTranscript := strings.Builder{}
	responseTranscript.WriteString("stale")
	if err := ensureImplicitRealtimeResponse(
		&active, &assistantPersisted, &responseText, &responseTranscript,
		&playback, audio, format,
	); err != nil {
		t.Fatal(err)
	}
	if !active || assistantPersisted || playback.suppressDeltas {
		t.Fatalf("response state = active:%t persisted:%t suppress:%t", active, assistantPersisted, playback.suppressDeltas)
	}
	if responseText.Len() != 0 || responseTranscript.Len() != 0 {
		t.Fatalf("implicit response did not reset buffers: text=%q transcript=%q", responseText.String(), responseTranscript.String())
	}
	if err := playback.append(audio, format, []byte{5, 6}); err != nil {
		t.Fatal(err)
	}
	if len(audio.writes) != 1 || string(audio.writes[0].data) != string([]byte{5, 6}) {
		t.Fatalf("writes = %#v, want reopened response audio", audio.writes)
	}
}

func TestRecordRealtimeFinalTranscriptPopulatesChatFallback(t *testing.T) {
	var responseTranscript strings.Builder
	var chatTranscript strings.Builder
	var chatText strings.Builder
	if !recordRealtimeFinalTranscript("final only", &responseTranscript, &chatText, &chatTranscript) {
		t.Fatal("final transcript was not recorded for chat fallback")
	}
	if responseTranscript.String() != "final only" || chatTranscript.String() != "final only" {
		t.Fatalf("transcripts = response:%q chat:%q", responseTranscript.String(), chatTranscript.String())
	}
}

func TestRealtimeSessionTerminationPreservesBufferedError(t *testing.T) {
	want := errors.New("transport failed")
	errs := make(chan error, 1)
	errs <- want
	close(errs)
	if got := realtimeSessionTerminationError(errs); !errors.Is(got, want) {
		t.Fatalf("termination error = %v, want %v", got, want)
	}
}

func TestInterruptRealtimeResponseSkipsIdleResponse(t *testing.T) {
	interrupter := &fakeRealtimeResponseInterrupter{}
	position := realtimevoice.ResponseInterruption{ItemID: "item_1", AudioEndMS: 250}
	if err := interruptRealtimeResponse(context.Background(), false, interrupter, position); err != nil {
		t.Fatal(err)
	}
	if interrupter.calls != 0 {
		t.Fatalf("idle response interrupts = %d, want 0", interrupter.calls)
	}
	if err := interruptRealtimeResponse(context.Background(), true, interrupter, position); err != nil {
		t.Fatal(err)
	}
	if interrupter.calls != 1 {
		t.Fatalf("active response interrupts = %d, want 1", interrupter.calls)
	}
	if interrupter.last != position {
		t.Fatalf("interrupt position = %+v, want %+v", interrupter.last, position)
	}
}

func TestRealtimeTurnStateKeepsNewTurnPendingAcrossOldResponseDone(t *testing.T) {
	state := realtimeTurnState{}
	state.responseStarted("response-old")
	state.speechStarted()
	state.speechStopped("")

	if !state.responseFinished("response-old") {
		t.Fatal("old response terminal event was not accepted")
	}
	if state.canInjectResponse() {
		t.Fatal("task response was admitted while the new user turn awaited response.started")
	}

	state.responseStarted("response-new")
	if state.inputTurnPending {
		t.Fatal("new response did not consume the pending user turn")
	}
	if !state.responseFinished("response-new") || !state.canInjectResponse() {
		t.Fatal("turn state did not become idle after the new response completed")
	}
}

func TestRealtimeTurnStateRejectsStaleResponseDone(t *testing.T) {
	state := realtimeTurnState{}
	state.responseStarted("response-new")
	if state.responseFinished("response-old") {
		t.Fatal("stale response terminal event was accepted")
	}
	if !state.responseActive || state.responseID != "response-new" {
		t.Fatalf("stale terminal event changed active response state: %+v", state)
	}
}

func TestRealtimeTurnStateDoesNotConsumeNewTurnForLateResponseCreated(t *testing.T) {
	state := realtimeTurnState{}
	state.responseRequested()
	state.speechStarted()
	state.speechStopped("")
	if state.responseStarted("response-old") {
		t.Fatal("late response.created was accepted as the current turn")
	}

	if !state.inputTurnPending {
		t.Fatalf("late response.created consumed the new user turn: %+v", state)
	}
}

func TestRealtimeTurnStateKeepsTranscriptFromInterruptedTurn(t *testing.T) {
	state := realtimeTurnState{}
	state.responseStarted("response-old")
	state.speechStarted()
	state.speechStopped("")
	state.userTranscriptObserved()

	if !state.inputTurnPending || state.inputTurnSequence != 1 {
		t.Fatalf("user transcript was lost while old response was active: %+v", state)
	}
}

func TestRealtimeTurnStateIgnoresLateTranscriptForActiveResponse(t *testing.T) {
	state := realtimeTurnState{}
	state.responseStarted("response-1")
	state.userTranscriptObserved()
	if state.inputTurnPending || state.inputTurnSequence != 0 {
		t.Fatalf("late transcript opened a new input turn: %+v", state)
	}
}

func TestRealtimeTurnStateRejectsStaleResponseOutput(t *testing.T) {
	state := realtimeTurnState{}
	state.responseStarted("response-old")
	state.speechStarted()
	state.speechStopped("")
	state.responseRequested()

	if state.acceptsResponseEvent("response-old") {
		t.Fatal("retired response output was accepted after a new request")
	}
	if !state.acceptsResponseEvent("response-new") {
		t.Fatal("current response output was rejected before response.created")
	}
}

func TestRealtimeTurnStateSuppressesCurrentResponseAfterBargeIn(t *testing.T) {
	state := realtimeTurnState{}
	state.responseStarted("response-1")
	state.speechStarted()
	if state.acceptsResponseEvent("response-1") {
		t.Fatal("response output was accepted after a new input turn started")
	}
	state.speechStopped("turn_invalid")
	if !state.acceptsResponseEvent("response-1") {
		t.Fatal("response output was not restored after turn_invalid")
	}
}

func TestRealtimeTurnStateRejectsAnonymousStaleResponseUntilNewTranscript(t *testing.T) {
	state := realtimeTurnState{}
	state.responseRequested()
	state.speechStarted()
	state.speechStopped("")
	if state.responseStarted("") {
		t.Fatal("anonymous stale response.created was accepted")
	}
	if state.acceptsResponseEvent("") || state.responseFinished("") {
		t.Fatal("anonymous stale response output was accepted")
	}

	state.userTranscriptObserved()
	if !state.responseStarted("") {
		t.Fatal("new anonymous response was rejected after its transcript arrived")
	}
}

func TestRealtimeTurnStateRejectsDuplicateResponseCreated(t *testing.T) {
	state := realtimeTurnState{}
	if !state.responseStarted("response-1") {
		t.Fatal("initial response.created was rejected")
	}
	state.speechStarted()
	state.speechStopped("")
	if state.responseStarted("response-1") {
		t.Fatal("duplicate response.created consumed the new input turn")
	}
	if !state.inputTurnPending {
		t.Fatal("duplicate response.created cleared the new input turn")
	}
}

func TestRealtimeTurnStateLocalSpeechStopDoesNotReopenConsumedTurn(t *testing.T) {
	state := realtimeTurnState{}
	state.speechStarted()
	state.responseStarted("")
	state.localSpeechStopped()
	if state.inputTurnPending || state.inputSpeechActive {
		t.Fatalf("local speech stop reopened consumed turn: %+v", state)
	}
}

func TestRealtimeTurnStateLocalSpeechStopKeepsUnansweredTurnPending(t *testing.T) {
	state := realtimeTurnState{}
	state.speechStarted()
	state.localSpeechStopped()
	if state.canInjectResponse() || !state.inputTurnPending {
		t.Fatalf("local speech stop admitted unanswered turn: %+v", state)
	}
}

func TestRealtimeTurnStateTracksTranscriptOnlyUserTurn(t *testing.T) {
	state := realtimeTurnState{}
	state.userTranscriptObserved()
	if state.canInjectResponse() {
		t.Fatal("task response was admitted while a transcript-only user turn was pending")
	}
	if !state.responseStarted("") {
		t.Fatal("current transcript-only response was rejected")
	}
	if state.inputTurnPending {
		t.Fatal("response start did not consume transcript-only user turn")
	}
}

func TestRealtimeTurnStateCompletesTranscriptOnlyTurnWithoutResponse(t *testing.T) {
	state := realtimeTurnState{}
	state.userTranscriptObserved()
	if !state.responseFinished("") || state.inputTurnPending {
		t.Fatalf("transcript-only turn did not complete cleanly: %+v", state)
	}
}

func TestRealtimeTurnStateAcceptsInterruptedResponseTerminalForCleanup(t *testing.T) {
	state := realtimeTurnState{}
	state.responseStarted("response-old")
	state.responseInterrupted()

	if !state.responseFinished("response-old") {
		t.Fatal("terminal event for the interrupted response was not accepted for cleanup")
	}
	if state.responseActive || state.responseID != "" {
		t.Fatalf("interrupted response terminal did not leave the state idle: %+v", state)
	}
}

func TestRealtimeTurnStateRejectsInterruptedResponseTerminalAfterNewRequest(t *testing.T) {
	state := realtimeTurnState{}
	state.responseStarted("response-old")
	state.responseInterrupted()
	state.responseRequested()

	if state.responseFinished("response-old") {
		t.Fatal("terminal event from the interrupted response was accepted")
	}
	if !state.responseActive {
		t.Fatalf("stale terminal event cleared the new requested response: %+v", state)
	}
}

func TestRealtimeTurnStateRejectsCompletedResponseDuplicateAfterNewRequest(t *testing.T) {
	state := realtimeTurnState{}
	state.responseStarted("response-old")
	if !state.responseFinished("response-old") {
		t.Fatal("initial response terminal event was not accepted")
	}
	state.responseRequested()

	if state.responseFinished("response-old") {
		t.Fatal("duplicate terminal event from the completed response was accepted")
	}
	if !state.responseActive {
		t.Fatalf("duplicate terminal event cleared the new requested response: %+v", state)
	}
}

func TestRealtimeTurnStateLeavesInvalidTurnIdle(t *testing.T) {
	state := realtimeTurnState{}
	state.speechStarted()
	state.speechStopped("turn_invalid")
	if !state.canInjectResponse() {
		t.Fatal("invalid speech turn left response admission blocked")
	}
}

func TestRealtimePlaybackCapsInterruptionAtEstimatedPlayedAudio(t *testing.T) {
	audio := &fakeRealtimePlaybackAudio{}
	format := agent.AudioFormat{SampleRate: 24000, Channels: 1, BitWidth: 16}
	now := time.Unix(100, 0)
	playback := realtimePlaybackState{now: func() time.Time { return now }}
	if err := playback.appendItem(audio, format, "item_1", make([]byte, 12000)); err != nil {
		t.Fatal(err)
	}
	now = now.Add(100 * time.Millisecond)
	got := playback.responseInterruption(format)
	want := (realtimevoice.ResponseInterruption{ItemID: "item_1", AudioEndMS: 100})
	if got != want {
		t.Fatalf("interruption = %+v, want %+v", got, want)
	}

	// Wall-clock playback cannot exceed the PCM duration already accepted by the
	// asynchronous audio queue.
	now = now.Add(time.Second)
	got = playback.responseInterruption(format)
	want.AudioEndMS = 250
	if got != want {
		t.Fatalf("capped interruption = %+v, want %+v", got, want)
	}
}

func TestRealtimePlaybackResumesResponseAfterInvalidTurn(t *testing.T) {
	audio := &fakeRealtimePlaybackAudio{}
	playback := realtimePlaybackState{}
	state := realtimeTurnState{}
	format := agent.AudioFormat{SampleRate: 24000, Channels: 1, BitWidth: 16}
	state.responseStarted("response-1")
	if err := playback.beginResponse(audio, format); err != nil {
		t.Fatal(err)
	}
	if err := playback.append(audio, format, []byte{1, 2}); err != nil {
		t.Fatal(err)
	}
	if err := playback.interrupt(audio, format); err != nil {
		t.Fatal(err)
	}
	state.speechStarted()
	state.speechStopped("turn_invalid")
	if err := restoreRealtimePlaybackAfterInvalidTurn(&state, &playback, audio, format); err != nil {
		t.Fatal(err)
	}
	if playback.suppressDeltas {
		t.Fatal("invalid turn left response audio suppressed")
	}
	if err := playback.append(audio, format, []byte{3, 4}); err != nil {
		t.Fatal(err)
	}
	if len(audio.writes) != 2 || string(audio.writes[1].data) != string([]byte{3, 4}) {
		t.Fatalf("writes = %#v, want resumed response audio", audio.writes)
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

func TestRealtimeProviderAudioFormatPrefersNegotiatedFormat(t *testing.T) {
	got := realtimeProviderAudioFormat(realtimevoice.AudioFormat{
		Encoding: "pcm_s16le", SampleRate: 24000, Channels: 2, BitDepth: 24,
	}, 16000, 8000)
	if got.SampleRate != 24000 || got.Channels != 2 || got.BitWidth != 24 {
		t.Fatalf("format = %+v, want negotiated pcm/24000/stereo/24", got)
	}
}

func TestRealtimeProviderAudioFormatFallsBackToLegacyRate(t *testing.T) {
	got := realtimeProviderAudioFormat(realtimevoice.AudioFormat{}, 16000, 24000)
	if got.SampleRate != 16000 || got.Channels != 1 || got.BitWidth != 16 {
		t.Fatalf("format = %+v, want legacy pcm/16000/mono/16", got)
	}
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

func TestStartRealtimeToolCallDoesNotBlockEventLoop(t *testing.T) {
	tool := &blockingRealtimeTool{started: make(chan struct{}), release: make(chan struct{})}
	results := make(chan realtimeToolResult, 1)
	call := realtimevoice.Event{Kind: realtimevoice.EventToolCall, ResponseID: "resp-1", CallID: "call-1", Name: realtimeRecallTool, Arguments: `{}`}

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

func TestRealtimeRotationSurvivesSessionErrorPath(t *testing.T) {
	// Rotation is distinguished with errors.Is in the wakeup loop, so the
	// sentinel has to reach it intact through the session error channel. A plain
	// error must stay non-rotation so real failures still drop to idle.
	rotated := fmt.Errorf("%w: gemini live ends this session in 50s", realtimevoice.ErrSessionRotated)
	errs := make(chan error, 1)
	errs <- rotated
	close(errs)
	got := realtimeSessionTerminationError(errs)
	if !errors.Is(got, realtimevoice.ErrSessionRotated) {
		t.Fatalf("rotation sentinel lost on the termination path: %v", got)
	}
	if !strings.Contains(got.Error(), "50s") {
		t.Fatalf("announced budget lost: %v", got)
	}

	plain := make(chan error, 1)
	plain <- errors.New("transport failed")
	close(plain)
	if errors.Is(realtimeSessionTerminationError(plain), realtimevoice.ErrSessionRotated) {
		t.Fatal("a plain transport error must not be treated as rotation")
	}
	if shouldFailQueuedRealtimeChat(rotated) {
		t.Fatal("queued chat must survive a scheduled session rotation")
	}
	if !shouldFailQueuedRealtimeChat(errors.New("transport failed")) {
		t.Fatal("queued chat must fail when a session ends with a real error")
	}
}

func TestFailActiveRealtimeChatReportsSessionError(t *testing.T) {
	command := realtimeChatCommand{
		ctx:    context.Background(),
		events: make(chan agent.RealtimeChatEvent, 1),
	}
	failActiveRealtimeChat(&command, errors.New("provider response failed"))
	event, ok := <-command.events
	if !ok {
		t.Fatal("active chat closed without an error event")
	}
	if event.Type != agent.RealtimeChatEventError || event.Error != "provider response failed" {
		t.Fatalf("active chat event = %+v", event)
	}
	if _, ok := <-command.events; ok {
		t.Fatal("active chat event channel remains open")
	}
}
