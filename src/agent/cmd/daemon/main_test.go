package main

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"log"
	"os"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"aiden-agent/internal/agent"
)

type fakeAudioDialog struct {
	chunks               []*agent.AudioChunkResult
	chunkBatches         [][]*agent.AudioChunkResult
	frameSamples         int
	frames               [][]int16
	vad                  *agent.AudioVAD
	utterance            []int16
	utterancesToReturn   [][]int16
	onStart              func(*fakeAudioDialog)
	onUtterance          func()
	processUtterance     func(context.Context, []int16, *agent.Runtime) error
	utterances           [][]int16
	prepareTurnInput     func([]int16) (agent.TurnInput, error)
	runTurn              func(context.Context) (agent.RunResult, error)
	runVoiceTurn         func(context.Context, agent.TurnInput, []int16) (agent.RunResult, error)
	beginSteerInterrupt  func() bool
	resumeSteerInterrupt func() bool
	waitForVoiceRunIdle  func(context.Context) bool
	queueSteer           func(agent.TurnInput) bool
	interruptOutput      func()
	speak                func(context.Context) error
	persistVoiceTurn     func(agent.TurnInput, agent.RunResult, []int16)
	persistedTurns       []persistedVoiceTurn
	queuedSteers         []agent.TurnInput
	steerInterrupts      int
	steerResumes         int
	vadErr               error
	onRead               func(*fakeAudioDialog, *agent.AudioChunkResult)
	spoken               []string
	voiceTurnContexts    []agent.VoiceTurnContext
	repeatEmpty          bool
	readDelay            time.Duration
	ops                  []string
	starts               int
	stops                int
	resets               int
	recordingActive      bool
}

type persistedVoiceTurn struct {
	input     agent.TurnInput
	result    agent.RunResult
	utterance []int16
}

func withVoiceSteerListenTimeout(t *testing.T, timeout time.Duration) {
	t.Helper()
	old := voiceSteerListenTimeout
	voiceSteerListenTimeout = timeout
	t.Cleanup(func() {
		voiceSteerListenTimeout = old
	})
}

func (d *fakeAudioDialog) StartRecording() error {
	if d.recordingActive {
		return nil
	}
	d.ops = append(d.ops, "start")
	d.starts++
	d.recordingActive = true
	if d.onStart != nil {
		d.onStart(d)
	}
	if len(d.chunkBatches) > 0 {
		d.chunks = d.chunkBatches[0]
		d.chunkBatches = d.chunkBatches[1:]
	}
	return nil
}

func (d *fakeAudioDialog) StopRecording() error {
	d.ops = append(d.ops, "stop")
	d.stops++
	d.recordingActive = false
	return nil
}

func (d *fakeAudioDialog) RecordingActive() bool {
	return d.recordingActive
}

func (d *fakeAudioDialog) ReadRecordChunk(timeoutMs uint32) (*agent.AudioChunkResult, error) {
	if d.readDelay > 0 {
		time.Sleep(d.readDelay)
	}
	if len(d.chunks) == 0 {
		if d.repeatEmpty {
			return &agent.AudioChunkResult{}, nil
		}
		return &agent.AudioChunkResult{EndOfStream: true}, nil
	}
	chunk := d.chunks[0]
	d.chunks = d.chunks[1:]
	if d.onRead != nil {
		d.onRead(d, chunk)
	}
	return chunk, nil
}

func (d *fakeAudioDialog) ProcessVADFrame(samples []int16) ([]int16, error) {
	copied := append([]int16(nil), samples...)
	d.frames = append(d.frames, copied)
	if d.vadErr != nil {
		return nil, d.vadErr
	}
	if d.vad != nil {
		return d.vad.Process(samples)
	}
	if len(d.utterancesToReturn) > 0 {
		utterance := d.utterancesToReturn[0]
		d.utterancesToReturn = d.utterancesToReturn[1:]
		return utterance, nil
	}
	if d.utterance != nil {
		utterance := d.utterance
		d.utterance = nil
		return utterance, nil
	}
	return nil, nil
}

func (d *fakeAudioDialog) ProcessUtterance(ctx context.Context, utterance []int16, runtime *agent.Runtime) error {
	d.utterances = append(d.utterances, append([]int16(nil), utterance...))
	if d.onUtterance != nil {
		d.onUtterance()
	}
	if d.processUtterance != nil {
		return d.processUtterance(ctx, utterance, runtime)
	}
	return nil
}

func (d *fakeAudioDialog) PrepareTurnInput(utterance []int16) (agent.TurnInput, error) {
	d.utterances = append(d.utterances, append([]int16(nil), utterance...))
	if d.onUtterance != nil {
		d.onUtterance()
	}
	if d.prepareTurnInput != nil {
		return d.prepareTurnInput(utterance)
	}
	return agent.TurnInput{InputText: "voice"}, nil
}

func (d *fakeAudioDialog) RunAgentTurn(ctx context.Context, input agent.TurnInput, runtime *agent.Runtime) (agent.RunResult, error) {
	if d.runTurn != nil {
		return d.runTurn(ctx)
	}
	return agent.RunResult{Output: "reply"}, nil
}

func (d *fakeAudioDialog) RunVoiceTurn(ctx context.Context, input agent.TurnInput, utterance []int16, runtime *agent.Runtime) (agent.RunResult, error) {
	return d.RunVoiceTurnWithContext(ctx, input, utterance, runtime, agent.VoiceTurnContext{})
}

func (d *fakeAudioDialog) RunVoiceTurnWithContext(ctx context.Context, input agent.TurnInput, utterance []int16, runtime *agent.Runtime, turnContext agent.VoiceTurnContext) (agent.RunResult, error) {
	d.voiceTurnContexts = append(d.voiceTurnContexts, turnContext)
	if d.runVoiceTurn != nil {
		return d.runVoiceTurn(ctx, input, utterance)
	}
	if d.persistVoiceTurn != nil {
		d.persistVoiceTurn(input, agent.RunResult{}, utterance)
	}
	result, err := d.RunAgentTurn(ctx, input, runtime)
	if err != nil {
		return result, err
	}
	d.persistedTurns = append(d.persistedTurns, persistedVoiceTurn{
		input:     input,
		result:    result,
		utterance: append([]int16(nil), utterance...),
	})
	if d.persistVoiceTurn != nil {
		d.persistVoiceTurn(agent.TurnInput{}, result, nil)
	}
	return result, nil
}

func (d *fakeAudioDialog) QueueSteer(input agent.TurnInput) bool {
	ok := true
	if d.queueSteer != nil {
		ok = d.queueSteer(input)
	}
	if ok {
		d.queuedSteers = append(d.queuedSteers, input)
	}
	return ok
}

func (d *fakeAudioDialog) BeginSteerInterrupt() bool {
	d.ops = append(d.ops, "begin_steer_interrupt")
	d.steerInterrupts++
	if d.beginSteerInterrupt != nil {
		return d.beginSteerInterrupt()
	}
	return true
}

func (d *fakeAudioDialog) ResumeSteerInterrupt() bool {
	d.ops = append(d.ops, "resume_steer_interrupt")
	d.steerResumes++
	if d.resumeSteerInterrupt != nil {
		return d.resumeSteerInterrupt()
	}
	return true
}

func (d *fakeAudioDialog) WaitForVoiceRunIdle(ctx context.Context) bool {
	if d.waitForVoiceRunIdle != nil {
		return d.waitForVoiceRunIdle(ctx)
	}
	return true
}

func (d *fakeAudioDialog) InterruptOutput() {
	d.ops = append(d.ops, "interrupt_output")
	if d.interruptOutput != nil {
		d.interruptOutput()
	}
}

func (d *fakeAudioDialog) PersistVoiceTurn(input agent.TurnInput, result agent.RunResult, utterance []int16) {
	d.persistedTurns = append(d.persistedTurns, persistedVoiceTurn{
		input:     input,
		result:    result,
		utterance: append([]int16(nil), utterance...),
	})
	if d.persistVoiceTurn != nil {
		d.persistVoiceTurn(input, result, utterance)
	}
}

func (d *fakeAudioDialog) Speak(ctx context.Context, text string, interrupt <-chan struct{}) error {
	d.ops = append(d.ops, "speak:"+text)
	d.spoken = append(d.spoken, text)
	if d.speak != nil {
		return d.speak(ctx)
	}
	return nil
}

func (d *fakeAudioDialog) FlushVAD() []int16 {
	if d.vad != nil {
		return d.vad.Flush()
	}
	return nil
}

func (d *fakeAudioDialog) FinishManualUtterance(pending []int16) []int16 {
	frameSamples := d.VADFrameSamples()
	consumed := 0
	for consumed+frameSamples <= len(pending) {
		if _, err := d.ProcessVADFrame(pending[consumed : consumed+frameSamples]); err != nil {
			break
		}
		consumed += frameSamples
	}
	tail := pending[consumed:]
	utterance := d.FlushVAD()
	if len(tail) == 0 {
		return utterance
	}
	if len(utterance) == 0 {
		return append([]int16(nil), tail...)
	}
	return append(utterance, tail...)
}

func (d *fakeAudioDialog) ResetVAD() {
	d.ops = append(d.ops, "reset")
	d.resets++
	if d.vad != nil {
		_ = d.vad.Reset()
	}
}

func (d *fakeAudioDialog) VADFrameSamples() int {
	if d.vad != nil {
		return d.vad.FrameSamples()
	}
	return d.frameSamples
}

func (d *fakeAudioDialog) VADDebugState() agent.VADDebugState {
	if d.vad != nil {
		return d.vad.DebugState()
	}
	return agent.VADDebugState{}
}

func opsHasPrefix(ops, prefix []string) bool {
	if len(ops) < len(prefix) {
		return false
	}
	for i, want := range prefix {
		if ops[i] != want {
			return false
		}
	}
	return true
}

func countDialogOp(ops []string, op string) int {
	count := 0
	for _, got := range ops {
		if got == op {
			count++
		}
	}
	return count
}

func opOccursAfter(ops []string, op, after string, afterOccurrence int) bool {
	afterIndex := nthDialogOpIndex(ops, after, afterOccurrence)
	if afterIndex < 0 {
		return false
	}
	nextAfterIndex := nthDialogOpIndex(ops, after, afterOccurrence+1)
	if nextAfterIndex < 0 {
		nextAfterIndex = len(ops)
	}
	for _, got := range ops[afterIndex+1 : nextAfterIndex] {
		if got == op {
			return true
		}
	}
	return false
}

func opOccursBefore(ops []string, op, before string) bool {
	for _, got := range ops {
		if got == op {
			return true
		}
		if got == before {
			return false
		}
	}
	return false
}

func nthDialogOpIndex(ops []string, op string, occurrence int) int {
	if occurrence <= 0 {
		return -1
	}
	seen := 0
	for i, got := range ops {
		if got != op {
			continue
		}
		seen++
		if seen == occurrence {
			return i
		}
	}
	return -1
}

type fakeWakeupWatcher struct {
	callback     func()
	fireOnStart  bool
	firedOnStart bool
	started      bool
	stopped      bool
	startCount   int
	stopCount    int
}

func withWakeupDebounceClock(t *testing.T, start time.Time) func(time.Duration) {
	t.Helper()
	var mu sync.Mutex
	current := start
	originalNow := wakeupDebounceNow
	wakeupDebounceNow = func() time.Time {
		mu.Lock()
		defer mu.Unlock()
		return current
	}
	t.Cleanup(func() {
		wakeupDebounceNow = originalNow
	})
	return func(delta time.Duration) {
		mu.Lock()
		current = current.Add(delta)
		mu.Unlock()
	}
}

func newTestAudioVAD(t *testing.T, alwaysBuffer bool, probabilities []float64) *agent.AudioVAD {
	t.Helper()
	vad, err := agent.NewAudioVADWithScorer(agent.AudioVADConfig{
		SampleRate:      16000,
		SilenceMs:       90,
		MinSpeechMs:     60,
		AlwaysBuffer:    alwaysBuffer,
		SpeechThreshold: 0.5,
	}, &testVADScorer{probabilities: probabilities})
	if err != nil {
		t.Fatalf("NewAudioVADWithScorer() error = %v", err)
	}
	return vad
}

type testVADScorer struct {
	probabilities []float64
	index         int
}

func (s *testVADScorer) Score(samples []int16) (float64, error) {
	if len(s.probabilities) == 0 {
		return 0, nil
	}
	if s.index >= len(s.probabilities) {
		return s.probabilities[len(s.probabilities)-1], nil
	}
	probability := s.probabilities[s.index]
	s.index++
	return probability, nil
}

func (s *testVADScorer) Reset() error {
	s.index = 0
	return nil
}

func (s *testVADScorer) Close() error {
	return nil
}

func (w *fakeWakeupWatcher) Start() error {
	w.started = true
	w.startCount++
	if w.fireOnStart && !w.firedOnStart {
		w.firedOnStart = true
		w.callback()
	}
	return nil
}

func (w *fakeWakeupWatcher) Stop() {
	w.stopped = true
	w.stopCount++
}

func TestStartWakeupWatchersRegistersGPIO32And33Callbacks(t *testing.T) {
	advanceWakeup := withWakeupDebounceClock(t, time.Unix(0, 0))
	watchersByPin := map[int]*fakeWakeupWatcher{}
	eventCount := 0

	watchers, err := startWakeupWatchers(func(pin int, callback func()) (wakeupWatcher, error) {
		watcher := &fakeWakeupWatcher{callback: callback}
		watchersByPin[pin] = watcher
		return watcher, nil
	}, func() {
		eventCount++
	})
	if err != nil {
		t.Fatalf("startWakeupWatchers() error = %v", err)
	}
	defer stopWakeupWatchers(watchers)

	for _, pin := range []int{33, 32} {
		watcher := watchersByPin[pin]
		if watcher == nil {
			t.Fatalf("missing watcher for GPIO %d", pin)
		}
		if !watcher.started {
			t.Fatalf("GPIO %d watcher was not started", pin)
		}
		watcher.callback()
		advanceWakeup(wakeupDebounceInterval + time.Millisecond)
	}

	if eventCount != 2 {
		t.Fatalf("event count = %d, want one wakeup event per GPIO callback", eventCount)
	}
}

func TestStartWakeupWatchersDebouncesImmediateGPIOBurst(t *testing.T) {
	withWakeupDebounceClock(t, time.Unix(0, 0))
	watchersByPin := map[int]*fakeWakeupWatcher{}
	eventCount := 0

	watchers, err := startWakeupWatchers(func(pin int, callback func()) (wakeupWatcher, error) {
		watcher := &fakeWakeupWatcher{callback: callback}
		watchersByPin[pin] = watcher
		return watcher, nil
	}, func() {
		eventCount++
	})
	if err != nil {
		t.Fatalf("startWakeupWatchers() error = %v", err)
	}
	defer stopWakeupWatchers(watchers)

	watchersByPin[33].callback()
	watchersByPin[32].callback()
	watchersByPin[33].callback()

	if eventCount != 1 {
		t.Fatalf("event count = %d, want one wakeup event after immediate GPIO burst", eventCount)
	}
}

func TestShouldRunConsoleAudioLoopSkipsManualModeWithoutInteractiveStdin(t *testing.T) {
	tests := []struct {
		name        string
		cfg         agent.Config
		interactive bool
		want        bool
	}{
		{
			name:        "stt manual without terminal",
			cfg:         agent.Config{InputMode: "stt", TriggerMode: "manual"},
			interactive: false,
			want:        false,
		},
		{
			name:        "audio default manual without terminal",
			cfg:         agent.Config{InputMode: "audio"},
			interactive: false,
			want:        false,
		},
		{
			name:        "stt manual with terminal",
			cfg:         agent.Config{InputMode: "stt", TriggerMode: "manual"},
			interactive: true,
			want:        true,
		},
		{
			name:        "stt wakeup without terminal",
			cfg:         agent.Config{InputMode: "stt", TriggerMode: "wakeup"},
			interactive: false,
			want:        true,
		},
		{
			name:        "text mode never runs audio loop",
			cfg:         agent.Config{InputMode: "text", TriggerMode: "manual"},
			interactive: true,
			want:        false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := shouldRunConsoleAudioLoop(tt.cfg, tt.interactive); got != tt.want {
				t.Fatalf("shouldRunConsoleAudioLoop() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestProcessAudioUntilUtteranceUsesConfiguredFrameSizeAndCarriesOddPCMByte(t *testing.T) {
	dialog := &fakeAudioDialog{
		frameSamples: 2,
		chunks: []*agent.AudioChunkResult{
			{PCM: pcm16Bytes(100)[:1]},
			{PCM: pcm16BytesFromSamples(100, 200)[1:]},
		},
		utterance: []int16{100, 200},
	}

	if exit := processAudioUntilUtterance(dialog, nil, context.Background(), make(chan os.Signal, 1)); exit {
		t.Fatal("processAudioUntilUtterance() returned exit=true")
	}
	if len(dialog.frames) != 1 {
		t.Fatalf("expected one VAD frame, got %d", len(dialog.frames))
	}
	if got := dialog.frames[0]; len(got) != 2 || got[0] != 100 || got[1] != 200 {
		t.Fatalf("unexpected VAD frame: %#v", got)
	}
	if dialog.stops != 1 {
		t.Fatalf("dialog stops = %d, want 1 before utterance processing", dialog.stops)
	}
}

func TestProcessAudioUntilUtteranceStopsAfterListenTimeout(t *testing.T) {
	dialog := &fakeAudioDialog{
		frameSamples: 2,
		repeatEmpty:  true,
	}

	if exit := processAudioUntilUtteranceWithTimeout(dialog, nil, context.Background(), make(chan os.Signal, 1), time.Millisecond); exit {
		t.Fatal("processAudioUntilUtteranceWithTimeout() returned exit=true")
	}
	if len(dialog.frames) != 0 {
		t.Fatalf("expected no VAD frames for empty chunks, got %d", len(dialog.frames))
	}
}

func TestProcessAudioUntilUtteranceSendsBufferedAudioAfterVADTimeout(t *testing.T) {
	dialog := &fakeAudioDialog{
		frameSamples: 2,
		chunks: []*agent.AudioChunkResult{
			{PCM: pcm16BytesFromSamples(100, 200, 300, 400)},
		},
		repeatEmpty: true,
		readDelay:   time.Millisecond,
	}

	if exit := processAudioUntilUtteranceWithTimeout(dialog, nil, context.Background(), make(chan os.Signal, 1), time.Millisecond); exit {
		t.Fatal("processAudioUntilUtteranceWithTimeout() returned exit=true")
	}
	if len(dialog.utterances) != 1 {
		t.Fatalf("utterances = %d, want 1", len(dialog.utterances))
	}
	if got := dialog.utterances[0]; len(got) != 4 || got[0] != 100 || got[1] != 200 || got[2] != 300 || got[3] != 400 {
		t.Fatalf("unexpected fallback utterance: %#v", got)
	}
	if dialog.stops != 1 {
		t.Fatalf("dialog stops = %d, want 1 before timeout utterance processing", dialog.stops)
	}
}

func TestDrainWakeupEventsWhileListeningLogsDrainedCount(t *testing.T) {
	wakeupEvents := make(chan struct{}, 3)
	wakeupEvents <- struct{}{}
	wakeupEvents <- struct{}{}
	wakeupEvents <- struct{}{}

	var logBuf bytes.Buffer
	originalOutput := log.Writer()
	originalFlags := log.Flags()
	log.SetOutput(&logBuf)
	log.SetFlags(0)
	t.Cleanup(func() {
		log.SetOutput(originalOutput)
		log.SetFlags(originalFlags)
	})

	drainWakeupEventsWhileListening(wakeupEvents)

	if got := logBuf.String(); !strings.Contains(got, "3 duplicate wakeup(s) received while listening, ignoring") {
		t.Fatalf("drain log = %q, want drained count", got)
	}
}

func TestFormatVADDebugLogOmitsEmptyLastError(t *testing.T) {
	got := formatVADDebugLog(agent.VADDebugState{
		Probability:   0.1234,
		Threshold:     0.5,
		Speaking:      false,
		SpeechFrames:  0,
		SilenceFrames: 0,
	})

	if strings.Contains(got, "last_error=") {
		t.Fatalf("log = %q, want last_error omitted when empty", got)
	}
	want := "[vad] probability=0.123 threshold=0.500 speaking=false speech_frames=0 silence_frames=0"
	if got != want {
		t.Fatalf("log = %q, want %q", got, want)
	}
}

func TestFormatVADDebugLogIncludesLastErrorWhenPresent(t *testing.T) {
	got := formatVADDebugLog(agent.VADDebugState{
		Probability:   0.5,
		Threshold:     0.5,
		Speaking:      true,
		SpeechFrames:  3,
		SilenceFrames: 1,
		LastError:     "model failed",
	})

	want := `[vad] probability=0.500 threshold=0.500 speaking=true speech_frames=3 silence_frames=1 last_error="model failed"`
	if got != want {
		t.Fatalf("log = %q, want %q", got, want)
	}
}

func TestSignalWakeupEventCoalescesPendingEvents(t *testing.T) {
	wakeupEvents := make(chan struct{}, 1)

	signalWakeupEvent(wakeupEvents)
	signalWakeupEvent(wakeupEvents)
	signalWakeupEvent(wakeupEvents)

	if got := len(wakeupEvents); got != 1 {
		t.Fatalf("pending wakeup events = %d, want 1", got)
	}
}

func TestSignalVoiceWakeupEventCoalescesPendingEvents(t *testing.T) {
	events := make(chan voiceEvent, 1)

	signalVoiceWakeupEvent(events)
	signalVoiceWakeupEvent(events)
	signalVoiceWakeupEvent(events)

	if got := len(events); got != 1 {
		t.Fatalf("pending voice events = %d, want 1", got)
	}
	if got := <-events; got != voiceEventWakeup {
		t.Fatalf("voice event = %v, want wakeup", got)
	}
}


func TestProcessAudioLoopStopsRecordingBeforeProcessingUtterance(t *testing.T) {
	dialog := &fakeAudioDialog{
		frameSamples:    2,
		recordingActive: true,
		chunks: []*agent.AudioChunkResult{
			{PCM: pcm16BytesFromSamples(100, 200)},
		},
		utterance: []int16{100, 200},
	}
	processedWhileRecording := false
	dialog.onUtterance = func() {
		if dialog.recordingActive {
			processedWhileRecording = true
		}
	}

	processAudioLoop(dialog, nil, context.Background(), nil)

	if processedWhileRecording {
		t.Fatal("processed utterance while recording was still active")
	}
	if dialog.stops != 1 {
		t.Fatalf("dialog stops = %d, want 1 before utterance processing", dialog.stops)
	}
}

func TestProcessAudioLoopFlushesManualTailOnStop(t *testing.T) {
	vad := newTestAudioVAD(t, true, []float64{0.1, 0.1, 0.1})
	dialog := &fakeAudioDialog{
		vad:             vad,
		recordingActive: true,
		chunks: []*agent.AudioChunkResult{
			{PCM: pcm16BytesFromSamples(1, 2, 3)},
		},
		repeatEmpty: true,
		readDelay:   5 * time.Millisecond,
	}
	manualStop := make(chan manualStopResult, 1)
	ctx, cancel := context.WithCancel(context.Background())
	go processAudioLoop(dialog, nil, ctx, manualStop)

	time.Sleep(20 * time.Millisecond)
	cancel()

	result := <-manualStop
	if result.vadHandled {
		t.Fatal("expected manual stop flush, got vadHandled")
	}
	if len(result.utterance) != 3 {
		t.Fatalf("utterance = %#v, want 3 tail samples preserved", result.utterance)
	}
}

func TestProcessAudioLoopStopsRecordingOnVADError(t *testing.T) {
	dialog := &fakeAudioDialog{
		frameSamples:    2,
		recordingActive: true,
		chunks: []*agent.AudioChunkResult{
			{PCM: pcm16BytesFromSamples(100, 200)},
		},
		vadErr: errors.New("vad failed"),
	}

	processAudioLoop(dialog, nil, context.Background(), nil)

	if dialog.recordingActive {
		t.Fatal("dialog recording still active after VAD error")
	}
	if dialog.stops != 1 {
		t.Fatalf("dialog stops = %d, want 1 after VAD error", dialog.stops)
	}
}

func TestRunWakeupModeProcessesTriggeredAudioAndStopsOnSignal(t *testing.T) {
	sigChan := make(chan os.Signal, 1)
	dialog := &fakeAudioDialog{
		frameSamples: 2,
		chunks: []*agent.AudioChunkResult{
			{PCM: pcm16BytesFromSamples(300, 400)},
		},
		utterance: []int16{300, 400},
		onUtterance: func() {
			sigChan <- syscall.SIGTERM
		},
	}
	watcher := &fakeWakeupWatcher{fireOnStart: true}
	var watcherPins []int

	done := make(chan struct{})
	go func() {
		runWakeupMode(agent.Config{}, dialog, nil, sigChan, func(pin int, callback func()) (wakeupWatcher, error) {
			watcherPins = append(watcherPins, pin)
			watcher.callback = callback
			return watcher, nil
		})
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("runWakeupMode did not stop after signal")
	}

	if !watcher.started || !watcher.stopped {
		t.Fatalf("watcher lifecycle started=%v stopped=%v", watcher.started, watcher.stopped)
	}
	if len(watcherPins) != 2 || watcherPins[0] != 33 || watcherPins[1] != 32 {
		t.Fatalf("watcher pins = %#v, want [33 32]", watcherPins)
	}
	if dialog.starts != 1 || dialog.stops == 0 || dialog.resets != 1 {
		t.Fatalf("dialog lifecycle starts=%d stops=%d resets=%d", dialog.starts, dialog.stops, dialog.resets)
	}
	if len(dialog.frames) != 1 || dialog.frames[0][0] != 300 || dialog.frames[0][1] != 400 {
		t.Fatalf("unexpected VAD frames: %#v", dialog.frames)
	}
}

func TestRunWakeupModeLegacyStartsRecordingWithoutWakeupAck(t *testing.T) {
	sigChan := make(chan os.Signal, 1)
	dialog := &fakeAudioDialog{
		frameSamples: 2,
		chunks: []*agent.AudioChunkResult{
			{PCM: pcm16BytesFromSamples(300, 400)},
		},
		utterance: []int16{300, 400},
		onUtterance: func() {
			sigChan <- syscall.SIGTERM
		},
	}
	watcher := &fakeWakeupWatcher{fireOnStart: true}

	done := make(chan struct{})
	go func() {
		runWakeupMode(agent.Config{}, dialog, nil, sigChan, func(pin int, callback func()) (wakeupWatcher, error) {
			watcher.callback = callback
			return watcher, nil
		})
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("runWakeupMode did not stop after signal")
	}

	if len(dialog.spoken) != 0 {
		t.Fatalf("spoken texts = %#v, want no wakeup acknowledgement", dialog.spoken)
	}
	wantPrefix := []string{"interrupt_output", "start", "reset"}
	if !opsHasPrefix(dialog.ops, wantPrefix) {
		t.Fatalf("dialog ops = %#v, want prefix %#v without wakeup acknowledgement", dialog.ops, wantPrefix)
	}
}

func TestRunWakeupModeStopsRecordingAfterSpeechThenBiasedSilence(t *testing.T) {
	sigChan := make(chan os.Signal, 1)
	vad := newTestAudioVAD(t, false, []float64{0.9, 0.9, 0.1, 0.1, 0.1})
	samples := append([]int16{}, alternatingSamples(vad.FrameSamples()*2, 700, 1000)...)
	samples = append(samples, constantSamples(vad.FrameSamples()*3, 700)...)

	dialog := &fakeAudioDialog{
		vad: vad,
		chunks: []*agent.AudioChunkResult{
			{PCM: pcm16BytesFromSamples(samples...)},
		},
		onUtterance: func() {
			sigChan <- syscall.SIGTERM
		},
	}
	watcher := &fakeWakeupWatcher{fireOnStart: true}

	done := make(chan struct{})
	go func() {
		runWakeupMode(agent.Config{}, dialog, nil, sigChan, func(pin int, callback func()) (wakeupWatcher, error) {
			watcher.callback = callback
			return watcher, nil
		})
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("runWakeupMode did not stop after VAD utterance")
	}

	if dialog.starts != 1 {
		t.Fatalf("dialog starts = %d, want 1", dialog.starts)
	}
	if dialog.stops == 0 {
		t.Fatal("expected wakeup mode to stop recording after VAD utterance")
	}
	if len(dialog.utterances) != 1 {
		t.Fatalf("utterances = %d, want 1", len(dialog.utterances))
	}
	if len(dialog.frames) != 5 {
		t.Fatalf("VAD frames = %d, want 5", len(dialog.frames))
	}
	if got, want := len(dialog.utterances[0]), len(samples); got != want {
		t.Fatalf("utterance samples = %d, want %d", got, want)
	}
	if !watcher.started || !watcher.stopped {
		t.Fatalf("watcher lifecycle started=%v stopped=%v", watcher.started, watcher.stopped)
	}
}

func TestRunWakeupModeBuffersSpeechDetectedByRKNNAfterInitialNonSpeech(t *testing.T) {
	sigChan := make(chan os.Signal, 1)
	vad := newTestAudioVAD(t, false, append(append(make([]float64, 10), 0.9, 0.9), 0.1, 0.1, 0.1))
	noise := alternatingSamples(vad.FrameSamples()*10, 700, 35)
	speech := alternatingSamples(vad.FrameSamples()*2, 700, 160)
	silence := constantSamples(vad.FrameSamples()*3, 700)
	samples := append([]int16{}, noise...)
	samples = append(samples, speech...)
	samples = append(samples, silence...)

	dialog := &fakeAudioDialog{
		vad: vad,
		chunks: []*agent.AudioChunkResult{
			{PCM: pcm16BytesFromSamples(samples...)},
		},
		onUtterance: func() {
			sigChan <- syscall.SIGTERM
		},
	}
	watcher := &fakeWakeupWatcher{fireOnStart: true}

	done := make(chan struct{})
	go func() {
		runWakeupMode(agent.Config{}, dialog, nil, sigChan, func(pin int, callback func()) (wakeupWatcher, error) {
			watcher.callback = callback
			return watcher, nil
		})
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("runWakeupMode did not stop after RKNN-detected utterance")
	}

	if dialog.starts != 1 || dialog.stops == 0 {
		t.Fatalf("dialog lifecycle starts=%d stops=%d", dialog.starts, dialog.stops)
	}
	if len(dialog.utterances) != 1 {
		t.Fatalf("utterances = %d, want 1", len(dialog.utterances))
	}
	if got, want := len(dialog.utterances[0]), len(speech)+len(silence); got != want {
		t.Fatalf("utterance samples = %d, want %d", got, want)
	}
	if !watcher.started || !watcher.stopped {
		t.Fatalf("watcher lifecycle started=%v stopped=%v", watcher.started, watcher.stopped)
	}
}

func TestRunWakeupModeStartsNewRecordingForNextWakeup(t *testing.T) {
	advanceWakeup := withWakeupDebounceClock(t, time.Unix(0, 0))
	sigChan := make(chan os.Signal, 1)
	watcher := &fakeWakeupWatcher{fireOnStart: true}
	voiceSessionDisabled := false
	var dialog *fakeAudioDialog
	dialog = &fakeAudioDialog{
		frameSamples: 2,
		chunkBatches: [][]*agent.AudioChunkResult{
			{{PCM: pcm16BytesFromSamples(100, 200)}},
			{{PCM: pcm16BytesFromSamples(300, 400)}},
		},
		utterancesToReturn: [][]int16{
			{100, 200},
			{300, 400},
		},
		onUtterance: func() {
			if len(dialog.utterances) == 2 {
				sigChan <- syscall.SIGTERM
			} else if watcher.callback != nil {
				go func() {
					time.Sleep(10 * time.Millisecond)
					advanceWakeup(wakeupDebounceInterval + time.Millisecond)
					watcher.callback()
				}()
			}
		},
	}

	done := make(chan struct{})
	go func() {
		runWakeupMode(agent.Config{VoiceFollowupEnabled: &voiceSessionDisabled}, dialog, nil, sigChan, func(pin int, callback func()) (wakeupWatcher, error) {
			watcher.callback = callback
			return watcher, nil
		})
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("runWakeupMode did not process two wakeups")
	}

	if dialog.starts != 2 {
		t.Fatalf("dialog starts = %d, want 2", dialog.starts)
	}
	if dialog.stops < 2 {
		t.Fatalf("dialog stops = %d, want at least 2", dialog.stops)
	}
	if len(dialog.utterances) != 2 {
		t.Fatalf("utterances = %d, want 2", len(dialog.utterances))
	}
}

func TestRunWakeupModeSTTWakeupQueuesCorrectionWhileRunActive(t *testing.T) {
	advanceWakeup := withWakeupDebounceClock(t, time.Unix(0, 0))
	sigChan := make(chan os.Signal, 1)
	watcher := &fakeWakeupWatcher{fireOnStart: true}
	firstRunStarted := make(chan struct{})
	releaseFirstRun := make(chan struct{})
	queuedSteer := make(chan struct{})
	voiceSessionDisabled := false
	runInputs := make([]string, 0, 1)
	var dialog *fakeAudioDialog
	dialog = &fakeAudioDialog{
		frameSamples: 2,
		chunkBatches: [][]*agent.AudioChunkResult{
			{{PCM: pcm16BytesFromSamples(100, 200)}},
			{{PCM: pcm16BytesFromSamples(300, 400)}},
		},
		utterancesToReturn: [][]int16{
			{100, 200},
			{300, 400},
		},
		prepareTurnInput: func(utterance []int16) (agent.TurnInput, error) {
			if len(utterance) > 0 && utterance[0] == 300 {
				return agent.TurnInput{InputText: "use the shorter search term", Transcript: "use the shorter search term"}, nil
			}
			return agent.TurnInput{InputText: "open the group and send money", Transcript: "open the group and send money"}, nil
		},
		runVoiceTurn: func(ctx context.Context, input agent.TurnInput, utterance []int16) (agent.RunResult, error) {
			runInputs = append(runInputs, input.InputText)
			switch input.InputText {
			case "open the group and send money":
				close(firstRunStarted)
				<-releaseFirstRun
				return agent.RunResult{Output: "revised reply"}, nil
			case "use the shorter search term":
				t.Fatal("correction input should be queued as steer while the original run is active")
				return agent.RunResult{}, nil
			default:
				t.Errorf("unexpected run input = %#v", input)
				return agent.RunResult{}, nil
			}
		},
		queueSteer: func(input agent.TurnInput) bool {
			if input.InputText != "use the shorter search term" {
				t.Fatalf("queued steer input = %#v, want captured correction", input)
			}
			close(queuedSteer)
			close(releaseFirstRun)
			return true
		},
		speak: func(ctx context.Context) error {
			if len(dialog.spoken) == 1 {
				go func() {
					time.Sleep(10 * time.Millisecond)
					sigChan <- syscall.SIGTERM
				}()
			}
			return nil
		},
	}

	done := make(chan struct{})
	go func() {
		runWakeupMode(agent.Config{InputMode: "stt", VoiceFollowupEnabled: &voiceSessionDisabled}, dialog, nil, sigChan, func(pin int, callback func()) (wakeupWatcher, error) {
			watcher.callback = callback
			return watcher, nil
		})
		close(done)
	}()

	select {
	case <-firstRunStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("STT wakeup mode did not start the first voice run")
	}
	advanceWakeup(wakeupDebounceInterval + time.Millisecond)
	watcher.callback()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("runWakeupMode did not stop after queued STT wakeup correction")
	}
	select {
	case <-queuedSteer:
	default:
		t.Fatal("wakeup correction was not queued as steer")
	}
	if len(runInputs) != 1 || runInputs[0] != "open the group and send money" {
		t.Fatalf("run inputs = %#v, want only the original request while correction is queued", runInputs)
	}
	if len(dialog.voiceTurnContexts) != 1 {
		t.Fatalf("voice turn contexts = %#v, want one active voice turn", dialog.voiceTurnContexts)
	}
	if dialog.voiceTurnContexts[0] != (agent.VoiceTurnContext{}) {
		t.Fatalf("first voice turn context = %#v, want ordinary new task", dialog.voiceTurnContexts[0])
	}
	if got := countDialogOp(dialog.ops, "interrupt_output"); got < 2 {
		t.Fatalf("interrupt_output count = %d, want initial wakeup and interrupting wakeup; ops=%#v", got, dialog.ops)
	}
	if !opOccursAfter(dialog.ops, "interrupt_output", "start", 1) {
		t.Fatalf("dialog ops = %#v, want wakeup interrupt output before second recording start", dialog.ops)
	}
}

func TestRunWakeupModeAudioWakeupKeepsLegacySingleTurnBehavior(t *testing.T) {
	sigChan := make(chan os.Signal, 1)
	watcher := &fakeWakeupWatcher{fireOnStart: true}
	dialog := &fakeAudioDialog{
		frameSamples: 2,
		chunks: []*agent.AudioChunkResult{
			{PCM: pcm16BytesFromSamples(100, 200)},
		},
		utterance: []int16{100, 200},
		onUtterance: func() {
			go func() {
				time.Sleep(10 * time.Millisecond)
				sigChan <- syscall.SIGTERM
			}()
		},
	}

	done := make(chan struct{})
	go func() {
		runWakeupMode(agent.Config{InputMode: "audio"}, dialog, nil, sigChan, func(pin int, callback func()) (wakeupWatcher, error) {
			watcher.callback = callback
			return watcher, nil
		})
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("runWakeupMode did not stop after signal")
	}

	if dialog.starts != 1 {
		t.Fatalf("dialog starts = %d, want 1 without STT voice session", dialog.starts)
	}
	if len(dialog.utterances) != 1 {
		t.Fatalf("utterances = %d, want 1", len(dialog.utterances))
	}
}

func TestRunWakeupModeVoiceSessionProcessesFollowupWithoutSecondWakeup(t *testing.T) {
	sigChan := make(chan os.Signal, 1)
	watcher := &fakeWakeupWatcher{fireOnStart: true}
	var dialog *fakeAudioDialog
	dialog = &fakeAudioDialog{
		frameSamples: 2,
		chunks: []*agent.AudioChunkResult{
			{PCM: pcm16BytesFromSamples(100, 200)},
			{PCM: pcm16BytesFromSamples(300, 400)},
		},
		utterancesToReturn: [][]int16{
			{100, 200},
			{300, 400},
		},
		repeatEmpty: true,
		readDelay:   time.Millisecond,
		speak: func(ctx context.Context) error {
			if len(dialog.spoken) == 2 {
				sigChan <- syscall.SIGTERM
			}
			return nil
		},
	}

	done := make(chan struct{})
	go func() {
		runWakeupMode(agent.Config{InputMode: "stt", VoiceFollowupEnabled: boolPtr(true), VoiceFollowupTimeoutMs: 100}, dialog, nil, sigChan, func(pin int, callback func()) (wakeupWatcher, error) {
			watcher.callback = callback
			return watcher, nil
		})
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("runWakeupMode did not process follow-up utterance")
	}

	if dialog.starts != 2 {
		t.Fatalf("dialog starts = %d, want one recording per voice turn", dialog.starts)
	}
	if len(dialog.utterances) != 2 {
		t.Fatalf("utterances = %d, want 2", len(dialog.utterances))
	}
	if len(dialog.spoken) != 2 {
		t.Fatalf("spoken replies = %d, want 2 agent replies without wakeup acknowledgement", len(dialog.spoken))
	}
	if dialog.spoken[0] != "reply" || dialog.spoken[1] != "reply" {
		t.Fatalf("spoken texts = %#v, want only agent replies", dialog.spoken)
	}
}

func TestRunWakeupModeVoiceSessionStartsRecordingWithoutWakeupAck(t *testing.T) {
	sigChan := make(chan os.Signal, 1)
	watcher := &fakeWakeupWatcher{fireOnStart: true}
	var dialog *fakeAudioDialog
	dialog = &fakeAudioDialog{
		frameSamples: 2,
		chunkBatches: [][]*agent.AudioChunkResult{
			{{PCM: pcm16BytesFromSamples(100, 200)}},
		},
		utterancesToReturn: [][]int16{
			{100, 200},
		},
		speak: func(ctx context.Context) error {
			if len(dialog.spoken) == 1 {
				go func() {
					time.Sleep(10 * time.Millisecond)
					sigChan <- syscall.SIGTERM
				}()
			}
			return nil
		},
	}

	done := make(chan struct{})
	go func() {
		runWakeupMode(agent.Config{InputMode: "stt", VoiceFollowupEnabled: boolPtr(true), VoiceMaxTurns: 1}, dialog, nil, sigChan, func(pin int, callback func()) (wakeupWatcher, error) {
			watcher.callback = callback
			return watcher, nil
		})
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("runWakeupMode did not stop after signal")
	}

	wantPrefix := []string{"interrupt_output", "start", "reset"}
	if !opsHasPrefix(dialog.ops, wantPrefix) {
		t.Fatalf("dialog ops = %#v, want prefix %#v without wakeup acknowledgement", dialog.ops, wantPrefix)
	}
	if len(dialog.spoken) != 1 || dialog.spoken[0] != "reply" {
		t.Fatalf("spoken texts = %#v, want only the agent reply", dialog.spoken)
	}
}

func TestRunVoiceSessionClosesAfterFollowupTimeout(t *testing.T) {
	sigChan := make(chan os.Signal, 1)
	dialog := &fakeAudioDialog{
		frameSamples: 2,
		chunkBatches: [][]*agent.AudioChunkResult{
			{{PCM: pcm16BytesFromSamples(100, 200)}},
			{},
		},
		utterancesToReturn: [][]int16{
			{100, 200},
		},
		repeatEmpty: true,
		readDelay:   time.Millisecond,
	}

	exit := runVoiceSession(agent.Config{VoiceFollowupTimeoutMs: 5}, dialog, nil, sigChan, make(chan voiceEvent, 1))
	if exit {
		t.Fatal("runVoiceSession returned exit=true")
	}
	if dialog.starts != 2 {
		t.Fatalf("dialog starts = %d, want one recording for initial turn and one follow-up listen", dialog.starts)
	}
	if len(dialog.utterances) != 1 {
		t.Fatalf("utterances = %d, want 1", len(dialog.utterances))
	}
}

func TestRunVoiceSessionPersistsCompletedTurn(t *testing.T) {
	sigChan := make(chan os.Signal, 1)
	dialog := &fakeAudioDialog{
		frameSamples: 2,
		chunkBatches: [][]*agent.AudioChunkResult{
			{{PCM: pcm16BytesFromSamples(100, 200)}},
			{},
		},
		utterancesToReturn: [][]int16{
			{100, 200},
		},
		repeatEmpty: true,
		readDelay:   time.Millisecond,
		prepareTurnInput: func(utterance []int16) (agent.TurnInput, error) {
			return agent.TurnInput{InputText: "现在几点了？", Transcript: "现在几点了？"}, nil
		},
		runTurn: func(ctx context.Context) (agent.RunResult, error) {
			return agent.RunResult{EpisodeID: "ep_voice", Output: "现在是凌晨 1 点 42 分。"}, nil
		},
	}

	exit := runVoiceSession(agent.Config{VoiceFollowupTimeoutMs: 5}, dialog, nil, sigChan, make(chan voiceEvent, 1))
	if exit {
		t.Fatal("runVoiceSession returned exit=true")
	}
	if len(dialog.persistedTurns) != 1 {
		t.Fatalf("persisted turns = %d, want 1", len(dialog.persistedTurns))
	}
	turn := dialog.persistedTurns[0]
	if turn.input.Transcript != "现在几点了？" {
		t.Fatalf("persisted transcript = %q, want voice transcript", turn.input.Transcript)
	}
	if turn.result.EpisodeID != "ep_voice" || turn.result.Output != "现在是凌晨 1 点 42 分。" {
		t.Fatalf("persisted result = %#v, want completed voice result", turn.result)
	}
	if len(turn.utterance) != 2 || turn.utterance[0] != 100 || turn.utterance[1] != 200 {
		t.Fatalf("persisted utterance = %#v, want captured samples", turn.utterance)
	}
}

func TestRunVoiceSessionClosesWhenAgentRequestsWaitForWakeup(t *testing.T) {
	sigChan := make(chan os.Signal, 1)
	dialog := &fakeAudioDialog{
		frameSamples: 2,
		chunkBatches: [][]*agent.AudioChunkResult{
			{{PCM: pcm16BytesFromSamples(100, 200)}},
		},
		utterancesToReturn: [][]int16{
			{100, 200},
		},
		runTurn: func(ctx context.Context) (agent.RunResult, error) {
			return agent.RunResult{Output: "我先待命，等下次唤醒。", WaitForWakeupRequested: true}, nil
		},
	}

	exit := runVoiceSession(agent.Config{VoiceFollowupTimeoutMs: 100}, dialog, nil, sigChan, make(chan voiceEvent, 1))
	if exit {
		t.Fatal("runVoiceSession returned exit=true")
	}
	if dialog.starts != 1 {
		t.Fatalf("dialog starts = %d, want one recording for the single voice turn", dialog.starts)
	}
	if len(dialog.spoken) != 0 {
		t.Fatalf("spoken = %#v, want no final reply after wait-for-wakeup request", dialog.spoken)
	}
}

func TestRunVoiceSessionDoesNotRecordWhileSpeaking(t *testing.T) {
	sigChan := make(chan os.Signal, 1)
	dialog := &fakeAudioDialog{
		frameSamples: 2,
		chunkBatches: [][]*agent.AudioChunkResult{
			{{PCM: pcm16BytesFromSamples(100, 200)}},
			{},
		},
		utterancesToReturn: [][]int16{
			{100, 200},
		},
		repeatEmpty: true,
		readDelay:   time.Millisecond,
	}
	spokeWhileRecording := false
	dialog.speak = func(ctx context.Context) error {
		if dialog.recordingActive {
			spokeWhileRecording = true
		}
		return nil
	}

	exit := runVoiceSession(agent.Config{VoiceFollowupTimeoutMs: 5}, dialog, nil, sigChan, make(chan voiceEvent, 1))
	if exit {
		t.Fatal("runVoiceSession returned exit=true")
	}
	if spokeWhileRecording {
		t.Fatal("dialog was recording while speaking")
	}
	if dialog.starts != 2 {
		t.Fatalf("dialog starts = %d, want recording once before speech and once after speech", dialog.starts)
	}
	wantOps := []string{"start", "reset", "stop", "speak:reply", "start", "reset", "stop"}
	if len(dialog.ops) != len(wantOps) {
		t.Fatalf("dialog ops = %#v, want %#v", dialog.ops, wantOps)
	}
	for i, want := range wantOps {
		if dialog.ops[i] != want {
			t.Fatalf("dialog ops = %#v, want %#v", dialog.ops, wantOps)
		}
	}
}

func TestRunVoiceTurnSignalWaitsForThinkingGoroutine(t *testing.T) {
	sigChan := make(chan os.Signal, 1)
	events := make(chan voiceEvent, 1)
	runStarted := make(chan struct{})
	ctxCanceled := make(chan struct{})
	dialog := &fakeAudioDialog{
		runTurn: func(ctx context.Context) (agent.RunResult, error) {
			close(runStarted)
			<-ctx.Done()
			close(ctxCanceled)
			return agent.RunResult{}, ctx.Err()
		},
	}

	go func() {
		<-runStarted
		sigChan <- syscall.SIGTERM
	}()

	result := runVoiceTurn(agent.Config{}, dialog, nil, []int16{1}, sigChan, events)
	if result.interrupted || result.waitForWakeupRequested || !result.exit {
		t.Fatalf("runVoiceTurn = interrupted:%v wait:%v exit:%v, want false false true", result.interrupted, result.waitForWakeupRequested, result.exit)
	}
	select {
	case <-ctxCanceled:
	default:
		t.Fatal("RunVoiceTurn goroutine was not finished before signal return")
	}
}

func TestRunVoiceTurnPersistsUserBeforeRunEvents(t *testing.T) {
	sigChan := make(chan os.Signal, 1)
	events := make(chan voiceEvent, 1)
	historyOrder := make([]string, 0, 3)
	dialog := &fakeAudioDialog{
		runTurn: func(ctx context.Context) (agent.RunResult, error) {
			historyOrder = append(historyOrder, "role_output")
			return agent.RunResult{EpisodeID: "ep-voice", Output: "reply"}, nil
		},
		persistVoiceTurn: func(input agent.TurnInput, result agent.RunResult, utterance []int16) {
			if input.InputText != "" || input.Transcript != "" {
				historyOrder = append(historyOrder, "user")
			}
			if result.Output != "" {
				historyOrder = append(historyOrder, "assistant")
			}
		},
	}

	result := runVoiceTurnWithInput(
		agent.Config{},
		dialog,
		nil,
		agent.TurnInput{InputText: "voice command"},
		[]int16{1, 2, 3},
		sigChan,
		events,
	)

	if result.interrupted || result.waitForWakeupRequested || result.exit {
		t.Fatalf("runVoiceTurnWithInput = interrupted:%v wait:%v exit:%v, want false false false", result.interrupted, result.waitForWakeupRequested, result.exit)
	}
	want := []string{"user", "role_output", "assistant"}
	if len(historyOrder) != len(want) {
		t.Fatalf("history order = %#v, want %#v", historyOrder, want)
	}
	for i := range want {
		if historyOrder[i] != want[i] {
			t.Fatalf("history order = %#v, want %#v", historyOrder, want)
		}
	}
}

func TestRunVoiceTurnSignalWaitsForSpeakingGoroutine(t *testing.T) {
	sigChan := make(chan os.Signal, 1)
	events := make(chan voiceEvent, 1)
	speakStarted := make(chan struct{})
	ctxCanceled := make(chan struct{})
	dialog := &fakeAudioDialog{
		runTurn: func(ctx context.Context) (agent.RunResult, error) {
			return agent.RunResult{Output: "reply"}, nil
		},
		speak: func(ctx context.Context) error {
			close(speakStarted)
			<-ctx.Done()
			close(ctxCanceled)
			return ctx.Err()
		},
	}

	go func() {
		<-speakStarted
		sigChan <- syscall.SIGTERM
	}()

	result := runVoiceTurn(agent.Config{}, dialog, nil, []int16{1}, sigChan, events)
	if result.interrupted || result.waitForWakeupRequested || !result.exit {
		t.Fatalf("runVoiceTurn = interrupted:%v wait:%v exit:%v, want false false true", result.interrupted, result.waitForWakeupRequested, result.exit)
	}
	select {
	case <-ctxCanceled:
	default:
		t.Fatal("Speak goroutine was not finished before signal return")
	}
}

func TestCaptureUtteranceTimeoutDiscardsSilenceWhenVADNeverDetectedSpeech(t *testing.T) {
	vad := newTestAudioVAD(t, false, []float64{0.1, 0.1, 0.1, 0.1})
	dialog := &fakeAudioDialog{
		vad: vad,
		chunks: []*agent.AudioChunkResult{
			{PCM: pcm16BytesFromSamples(constantSamples(vad.FrameSamples()*4, 20)...)},
		},
		repeatEmpty: true,
		readDelay:   time.Millisecond,
	}

	utterance, exit := captureUtteranceWithTimeout(dialog, make(chan os.Signal, 1), make(chan voiceEvent, 1), time.Millisecond)
	if exit {
		t.Fatal("captureUtteranceWithTimeout returned exit=true")
	}
	if len(utterance) != 0 {
		t.Fatalf("utterance samples = %d, want 0 for pure silence timeout", len(utterance))
	}
}

func TestCaptureUtteranceTimeoutReturnsBufferedSpeechWhenVADStarted(t *testing.T) {
	vad := newTestAudioVAD(t, false, []float64{0.9, 0.9, 0.9})
	speech := alternatingSamples(vad.FrameSamples()*3, 700, 1000)
	dialog := &fakeAudioDialog{
		vad: vad,
		chunks: []*agent.AudioChunkResult{
			{PCM: pcm16BytesFromSamples(speech...)},
		},
		repeatEmpty: true,
		readDelay:   time.Millisecond,
	}

	utterance, exit := captureUtteranceWithTimeout(dialog, make(chan os.Signal, 1), make(chan voiceEvent, 1), time.Millisecond)
	if exit {
		t.Fatal("captureUtteranceWithTimeout returned exit=true")
	}
	if len(utterance) == 0 {
		t.Fatal("expected buffered speech after timeout")
	}
}

func TestListenOneUtteranceIgnoresWakeupWhileAlreadyListening(t *testing.T) {
	sigChan := make(chan os.Signal, 1)
	events := make(chan voiceEvent, 1)
	firstRead := true
	dialog := &fakeAudioDialog{
		frameSamples: 2,
		chunkBatches: [][]*agent.AudioChunkResult{
			{{PCM: pcm16BytesFromSamples(100, 200)}},
		},
		utterancesToReturn: [][]int16{
			{100, 200},
		},
		onRead: func(d *fakeAudioDialog, chunk *agent.AudioChunkResult) {
			if firstRead {
				firstRead = false
				events <- voiceEventWakeup
			}
		},
	}

	utterance, exit := listenOneUtterance(dialog, sigChan, events, time.Second)
	if exit {
		t.Fatal("listenOneUtterance returned exit=true")
	}
	if got, want := utterance, []int16{100, 200}; len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("utterance = %#v, want original recording samples %#v", got, want)
	}
	if len(dialog.frames) != 1 || dialog.frames[0][0] != 100 || dialog.frames[0][1] != 200 {
		t.Fatalf("VAD frames = %#v, want original recording samples", dialog.frames)
	}
	wantOps := []string{"start", "reset", "stop"}
	if len(dialog.ops) != len(wantOps) {
		t.Fatalf("dialog ops = %#v, want %#v", dialog.ops, wantOps)
	}
	for i, want := range wantOps {
		if dialog.ops[i] != want {
			t.Fatalf("dialog ops = %#v, want %#v", dialog.ops, wantOps)
		}
	}
}

func TestRunVoiceSessionDrainsBurstWakeupsWhileAlreadyListening(t *testing.T) {
	sigChan := make(chan os.Signal, 1)
	events := make(chan voiceEvent, 8)
	firstRead := true
	turnStarted := make(chan struct{})
	allowTurnResult := make(chan struct{})
	dialog := &fakeAudioDialog{
		frameSamples: 2,
		chunkBatches: [][]*agent.AudioChunkResult{
			{{PCM: pcm16BytesFromSamples(100, 200)}},
			{},
		},
		utterancesToReturn: [][]int16{
			{100, 200},
		},
		repeatEmpty: true,
		readDelay:   time.Millisecond,
		onRead: func(d *fakeAudioDialog, chunk *agent.AudioChunkResult) {
			if firstRead {
				firstRead = false
				for i := 0; i < 4; i++ {
					events <- voiceEventWakeup
				}
			}
		},
		runTurn: func(ctx context.Context) (agent.RunResult, error) {
			close(turnStarted)
			select {
			case <-ctx.Done():
				return agent.RunResult{}, ctx.Err()
			case <-allowTurnResult:
				return agent.RunResult{Output: "reply"}, nil
			}
		},
	}
	go func() {
		<-turnStarted
		time.Sleep(10 * time.Millisecond)
		close(allowTurnResult)
	}()

	exit := runVoiceSession(agent.Config{VoiceFollowupTimeoutMs: 5}, dialog, nil, sigChan, events)
	if exit {
		t.Fatal("runVoiceSession returned exit=true")
	}
	if len(dialog.spoken) != 1 || dialog.spoken[0] != "reply" {
		t.Fatalf("spoken texts = %#v, want reply after draining listen-time wakeups", dialog.spoken)
	}
}

func TestRunVoiceSessionWakeupFallsBackToNextTurnWhenSteerQueueUnavailable(t *testing.T) {
	sigChan := make(chan os.Signal, 1)
	events := make(chan voiceEvent, 2)
	originalRunStarted := make(chan struct{})
	releaseOriginalRun := make(chan struct{})
	originalRunReturned := make(chan struct{})
	queueSteerCalls := 0
	runInputs := make([]string, 0, 2)
	dialog := &fakeAudioDialog{
		frameSamples: 2,
		chunkBatches: [][]*agent.AudioChunkResult{
			{{PCM: pcm16BytesFromSamples(100, 200)}},
			{{PCM: pcm16BytesFromSamples(300, 400)}},
			{},
		},
		utterancesToReturn: [][]int16{
			{100, 200},
			{300, 400},
		},
		repeatEmpty: true,
		readDelay:   time.Millisecond,
		onStart: func(d *fakeAudioDialog) {
			if d.starts == 2 {
				close(releaseOriginalRun)
			}
		},
		prepareTurnInput: func(utterance []int16) (agent.TurnInput, error) {
			if len(utterance) > 0 && utterance[0] == 300 {
				return agent.TurnInput{InputText: "change course", Transcript: "change course"}, nil
			}
			return agent.TurnInput{InputText: "original request", Transcript: "original request"}, nil
		},
		runVoiceTurn: func(ctx context.Context, input agent.TurnInput, utterance []int16) (agent.RunResult, error) {
			runInputs = append(runInputs, input.InputText)
			switch input.InputText {
			case "original request":
				close(originalRunStarted)
				<-releaseOriginalRun
				close(originalRunReturned)
				return agent.RunResult{Output: "stale reply"}, nil
			case "change course":
				return agent.RunResult{Output: "revised reply"}, nil
			default:
				t.Errorf("unexpected run input = %#v", input)
				return agent.RunResult{}, nil
			}
		},
		queueSteer: func(input agent.TurnInput) bool {
			queueSteerCalls++
			select {
			case <-originalRunReturned:
			case <-time.After(time.Second):
				t.Fatal("original run did not return before QueueSteer")
			}
			return false
		},
	}

	go func() {
		<-originalRunStarted
		events <- voiceEventWakeup
	}()

	exit := runVoiceSession(agent.Config{VoiceFollowupTimeoutMs: 5, VoiceMaxTurns: 1}, dialog, nil, sigChan, events)
	if exit {
		t.Fatal("runVoiceSession returned exit=true")
	}
	if queueSteerCalls != 1 {
		t.Fatalf("QueueSteer calls = %d, want one steer queue attempt before next-turn fallback", queueSteerCalls)
	}
	if len(runInputs) != 2 || runInputs[0] != "original request" || runInputs[1] != "change course" {
		t.Fatalf("run inputs = %#v, want original request followed by captured steer", runInputs)
	}
	if len(dialog.spoken) != 1 || dialog.spoken[0] != "revised reply" {
		t.Fatalf("spoken texts = %#v, want only revised reply", dialog.spoken)
	}
	if dialog.starts != 2 {
		t.Fatalf("dialog starts = %d, want original listen plus steer listen", dialog.starts)
	}
}

func TestRunVoiceTurnWakeupQueuesSteerWhileOriginalRunActive(t *testing.T) {
	sigChan := make(chan os.Signal, 1)
	events := make(chan voiceEvent, 1)
	runStarted := make(chan struct{})
	releaseRun := make(chan struct{})
	queuedSteer := make(chan struct{})
	var releaseOnce sync.Once
	release := func() {
		releaseOnce.Do(func() {
			close(releaseRun)
		})
	}
	defer release()

	runInputs := make([]string, 0, 1)
	dialog := &fakeAudioDialog{
		frameSamples: 2,
		chunkBatches: [][]*agent.AudioChunkResult{
			{{PCM: pcm16BytesFromSamples(500, 600)}},
		},
		utterancesToReturn: [][]int16{
			{500, 600},
		},
		repeatEmpty: true,
		readDelay:   time.Millisecond,
		prepareTurnInput: func(utterance []int16) (agent.TurnInput, error) {
			if len(utterance) > 0 && utterance[0] == 500 {
				return agent.TurnInput{InputText: "改查深圳。", Transcript: "改查深圳。"}, nil
			}
			return agent.TurnInput{InputText: "广州。", Transcript: "广州。"}, nil
		},
		runVoiceTurn: func(ctx context.Context, input agent.TurnInput, utterance []int16) (agent.RunResult, error) {
			runInputs = append(runInputs, input.InputText)
			if input.InputText != "广州。" {
				t.Fatalf("run input = %q, want only the original request", input.InputText)
			}
			close(runStarted)
			<-releaseRun
			return agent.RunResult{Output: "steered reply"}, nil
		},
		queueSteer: func(input agent.TurnInput) bool {
			if input.InputText != "改查深圳。" {
				t.Fatalf("queued steer input = %#v, want captured correction", input)
			}
			close(queuedSteer)
			release()
			return true
		},
	}

	go func() {
		<-runStarted
		events <- voiceEventWakeup
	}()

	result := runVoiceTurnWithInputContext(
		agent.Config{VoiceFollowupTimeoutMs: 5},
		dialog,
		nil,
		agent.TurnInput{InputText: "广州。", Transcript: "广州。"},
		[]int16{100, 200},
		sigChan,
		events,
		agent.VoiceTurnContext{},
		true,
	)
	if result.exit || result.interrupted || result.waitForWakeupRequested || result.nextTurn != nil {
		t.Fatalf("runVoiceTurnWithInputContext result = %#v, want completed original run after queued steer", result)
	}
	select {
	case <-queuedSteer:
	default:
		t.Fatal("wakeup correction was not queued as steer")
	}
	if len(runInputs) != 1 || runInputs[0] != "广州。" {
		t.Fatalf("run inputs = %#v, want only original request", runInputs)
	}
	if len(dialog.queuedSteers) != 1 || dialog.queuedSteers[0].InputText != "改查深圳。" {
		t.Fatalf("queued steers = %#v, want captured correction", dialog.queuedSteers)
	}
	if len(dialog.spoken) != 1 || dialog.spoken[0] != "steered reply" {
		t.Fatalf("spoken texts = %#v, want steered reply from original run", dialog.spoken)
	}
}

func TestRunVoiceTurnWakeupBeginsSteerInterruptBeforeRecording(t *testing.T) {
	sigChan := make(chan os.Signal, 1)
	events := make(chan voiceEvent, 1)
	runStarted := make(chan struct{})
	releaseRun := make(chan struct{})
	queuedSteer := make(chan struct{})
	dialog := &fakeAudioDialog{
		frameSamples: 2,
		chunkBatches: [][]*agent.AudioChunkResult{
			{{PCM: pcm16BytesFromSamples(500, 600)}},
		},
		utterancesToReturn: [][]int16{
			{500, 600},
		},
		repeatEmpty: true,
		readDelay:   time.Millisecond,
		prepareTurnInput: func(utterance []int16) (agent.TurnInput, error) {
			if len(utterance) > 0 && utterance[0] == 500 {
				return agent.TurnInput{InputText: "改查深圳。", Transcript: "改查深圳。"}, nil
			}
			return agent.TurnInput{InputText: "广州。", Transcript: "广州。"}, nil
		},
		runVoiceTurn: func(ctx context.Context, input agent.TurnInput, utterance []int16) (agent.RunResult, error) {
			close(runStarted)
			<-releaseRun
			return agent.RunResult{Output: "steered reply"}, nil
		},
		queueSteer: func(input agent.TurnInput) bool {
			close(queuedSteer)
			close(releaseRun)
			return true
		},
	}

	go func() {
		<-runStarted
		events <- voiceEventWakeup
	}()

	result := runVoiceTurnWithInputContext(
		agent.Config{VoiceFollowupTimeoutMs: 5},
		dialog,
		nil,
		agent.TurnInput{InputText: "广州。", Transcript: "广州。"},
		[]int16{100, 200},
		sigChan,
		events,
		agent.VoiceTurnContext{},
		true,
	)
	if result.exit || result.interrupted || result.waitForWakeupRequested || result.nextTurn != nil {
		t.Fatalf("runVoiceTurnWithInputContext result = %#v, want completed original run after queued steer", result)
	}
	select {
	case <-queuedSteer:
	default:
		t.Fatal("wakeup correction was not queued as steer")
	}
	if dialog.steerInterrupts != 1 {
		t.Fatalf("steer interrupts = %d, want 1", dialog.steerInterrupts)
	}
	if !opOccursBefore(dialog.ops, "begin_steer_interrupt", "start") {
		t.Fatalf("dialog ops = %#v, want interrupt marker before steer recording starts", dialog.ops)
	}
}

func TestRunVoiceTurnWakeupEmptySteerResumesOriginalRun(t *testing.T) {
	withVoiceSteerListenTimeout(t, 5*time.Millisecond)

	sigChan := make(chan os.Signal, 1)
	events := make(chan voiceEvent, 1)
	runStarted := make(chan struct{})
	resumed := make(chan struct{})
	dialog := &fakeAudioDialog{
		frameSamples: 2,
		chunkBatches: [][]*agent.AudioChunkResult{
			{},
		},
		repeatEmpty: true,
		readDelay:   time.Millisecond,
		runVoiceTurn: func(ctx context.Context, input agent.TurnInput, utterance []int16) (agent.RunResult, error) {
			close(runStarted)
			select {
			case <-ctx.Done():
				return agent.RunResult{}, ctx.Err()
			case <-resumed:
			}
			return agent.RunResult{Output: "original reply"}, nil
		},
		resumeSteerInterrupt: func() bool {
			close(resumed)
			return true
		},
	}

	go func() {
		<-runStarted
		events <- voiceEventWakeup
	}()

	result := runVoiceTurnWithInputContext(
		agent.Config{VoiceFollowupTimeoutMs: 5},
		dialog,
		nil,
		agent.TurnInput{InputText: "广州。", Transcript: "广州。"},
		[]int16{100, 200},
		sigChan,
		events,
		agent.VoiceTurnContext{},
		true,
	)
	if result.exit || result.interrupted || result.waitForWakeupRequested || result.nextTurn != nil {
		t.Fatalf("runVoiceTurnWithInputContext result = %#v, want original run to resume after empty steer", result)
	}
	if dialog.steerInterrupts != 1 {
		t.Fatalf("steer interrupts = %d, want 1", dialog.steerInterrupts)
	}
	if dialog.steerResumes != 1 {
		t.Fatalf("steer resumes = %d, want 1", dialog.steerResumes)
	}
	if len(dialog.queuedSteers) != 0 {
		t.Fatalf("queued steers = %#v, want none for empty steer", dialog.queuedSteers)
	}
	if len(dialog.spoken) != 1 || dialog.spoken[0] != "original reply" {
		t.Fatalf("spoken texts = %#v, want original reply", dialog.spoken)
	}
}

func TestVoiceSteerListenTimeoutIgnoresFollowupTimeout(t *testing.T) {
	cfg := agent.Config{VoiceFollowupTimeoutMs: 5}
	if got := voiceSteerListenTimeoutForConfig(cfg); got != 45*time.Second {
		t.Fatalf("voiceSteerListenTimeoutForConfig() = %s, want 45s", got)
	}
}

func TestCaptureVoiceSteerInterruptsOutputBeforeRecording(t *testing.T) {
	sigChan := make(chan os.Signal, 1)
	events := make(chan voiceEvent, 1)
	dialog := &fakeAudioDialog{
		frameSamples: 2,
		chunkBatches: [][]*agent.AudioChunkResult{
			{{PCM: pcm16BytesFromSamples(300, 400)}},
		},
		utterancesToReturn: [][]int16{
			{300, 400},
		},
		prepareTurnInput: func(utterance []int16) (agent.TurnInput, error) {
			return agent.TurnInput{InputText: "change course", Transcript: "change course"}, nil
		},
	}

	nextTurn, exit := captureVoiceSteer(agent.Config{VoiceFollowupTimeoutMs: 50}, dialog, sigChan, events)
	if exit {
		t.Fatal("captureVoiceSteer returned exit=true")
	}

	wantOps := []string{"interrupt_output", "start", "reset", "stop"}
	if len(dialog.ops) != len(wantOps) {
		t.Fatalf("dialog ops = %#v, want %#v", dialog.ops, wantOps)
	}
	for i, want := range wantOps {
		if dialog.ops[i] != want {
			t.Fatalf("dialog ops = %#v, want %#v", dialog.ops, wantOps)
		}
	}
	if nextTurn == nil || nextTurn.input.InputText != "change course" {
		t.Fatalf("next turn = %#v, want change course", nextTurn)
	}
	if len(nextTurn.utterance) != 2 || nextTurn.utterance[0] != 300 || nextTurn.utterance[1] != 400 {
		t.Fatalf("next turn utterance = %#v, want captured samples", nextTurn.utterance)
	}
	if len(dialog.queuedSteers) != 0 {
		t.Fatalf("queued steers = %#v, want daemon wakeup interrupt to bypass QueueSteer", dialog.queuedSteers)
	}
}

func TestRunVoiceTurnWakeupReturnsCapturedSteerAsNextTurnWhenQueueSteerFails(t *testing.T) {
	sigChan := make(chan os.Signal, 1)
	events := make(chan voiceEvent, 1)
	runStarted := make(chan struct{})
	dialog := &fakeAudioDialog{
		frameSamples: 2,
		chunkBatches: [][]*agent.AudioChunkResult{
			{{PCM: pcm16BytesFromSamples(500, 600)}},
		},
		utterancesToReturn: [][]int16{
			{500, 600},
		},
		repeatEmpty: true,
		readDelay:   time.Millisecond,
		prepareTurnInput: func(utterance []int16) (agent.TurnInput, error) {
			if len(utterance) > 0 && utterance[0] == 500 {
				return agent.TurnInput{InputText: "改查深圳。", Transcript: "改查深圳。"}, nil
			}
			return agent.TurnInput{InputText: "广州。", Transcript: "广州。"}, nil
		},
		runTurn: func(ctx context.Context) (agent.RunResult, error) {
			close(runStarted)
			<-ctx.Done()
			return agent.RunResult{}, ctx.Err()
		},
		queueSteer: func(input agent.TurnInput) bool {
			if input.InputText != "改查深圳。" {
				t.Fatalf("queued steer input = %#v, want captured correction", input)
			}
			return false
		},
	}

	go func() {
		<-runStarted
		events <- voiceEventWakeup
	}()

	result := runVoiceTurn(agent.Config{VoiceFollowupTimeoutMs: 5}, dialog, nil, []int16{100, 200}, sigChan, events)
	if !result.interrupted || result.exit || result.waitForWakeupRequested {
		t.Fatalf("runVoiceTurn = interrupted:%v exit:%v wait:%v, want true false false", result.interrupted, result.exit, result.waitForWakeupRequested)
	}
	if result.nextTurn == nil || result.nextTurn.input.InputText != "改查深圳。" {
		t.Fatalf("next turn = %#v, want Shenzhen steer", result.nextTurn)
	}
	if len(result.nextTurn.utterance) != 2 || result.nextTurn.utterance[0] != 500 || result.nextTurn.utterance[1] != 600 {
		t.Fatalf("next turn utterance = %#v, want captured samples", result.nextTurn.utterance)
	}
	if len(dialog.spoken) != 0 {
		t.Fatalf("spoken texts = %#v, want no stale reply from interrupted turn", dialog.spoken)
	}
}

func TestRunVoiceTurnWakeupWaitsForVoiceRunToGoIdleBeforeNextTurn(t *testing.T) {
	oldTimeout := voiceTurnCancelWaitTimeout
	voiceTurnCancelWaitTimeout = 20 * time.Millisecond
	t.Cleanup(func() {
		voiceTurnCancelWaitTimeout = oldTimeout
	})

	sigChan := make(chan os.Signal, 1)
	events := make(chan voiceEvent, 1)
	runStarted := make(chan struct{})
	releaseRun := make(chan struct{})
	runReturned := make(chan struct{})
	idleWaitStarted := make(chan struct{})
	allowIdle := make(chan struct{})
	var idleWaitCalls int

	dialog := &fakeAudioDialog{
		frameSamples:       2,
		chunkBatches:       [][]*agent.AudioChunkResult{{{PCM: pcm16BytesFromSamples(700, 800)}}},
		utterancesToReturn: [][]int16{{700, 800}},
		repeatEmpty:        true,
		readDelay:          time.Millisecond,
		prepareTurnInput: func(utterance []int16) (agent.TurnInput, error) {
			if len(utterance) > 0 && utterance[0] == 700 {
				return agent.TurnInput{InputText: "next turn", Transcript: "next turn"}, nil
			}
			return agent.TurnInput{InputText: "original request", Transcript: "original request"}, nil
		},
		runVoiceTurn: func(ctx context.Context, input agent.TurnInput, utterance []int16) (agent.RunResult, error) {
			if input.InputText != "original request" {
				t.Fatalf("run input = %q, want only original request", input.InputText)
			}
			close(runStarted)
			<-ctx.Done()
			<-releaseRun
			close(runReturned)
			return agent.RunResult{}, ctx.Err()
		},
		queueSteer: func(input agent.TurnInput) bool {
			if input.InputText != "next turn" {
				t.Fatalf("queued steer input = %#v, want captured next turn", input)
			}
			return false
		},
		waitForVoiceRunIdle: func(ctx context.Context) bool {
			idleWaitCalls++
			close(idleWaitStarted)
			select {
			case <-allowIdle:
				select {
				case <-runReturned:
					return true
				case <-ctx.Done():
					return false
				}
			case <-ctx.Done():
				return false
			}
		},
	}

	go func() {
		<-runStarted
		events <- voiceEventWakeup
	}()

	resultCh := make(chan voiceTurnResult, 1)
	go func() {
		resultCh <- runVoiceTurnWithInputContext(
			agent.Config{VoiceFollowupTimeoutMs: 5},
			dialog,
			nil,
			agent.TurnInput{InputText: "original request", Transcript: "original request"},
			[]int16{100, 200},
			sigChan,
			events,
			agent.VoiceTurnContext{},
			true,
		)
	}()

	select {
	case <-idleWaitStarted:
	case <-time.After(time.Second):
		t.Fatal("did not wait for voice run to go idle")
	}

	select {
	case result := <-resultCh:
		t.Fatalf("runVoiceTurnWithInputContext returned early: %#v", result)
	case <-time.After(5 * time.Millisecond):
	}

	close(releaseRun)
	close(allowIdle)

	var result voiceTurnResult
	select {
	case result = <-resultCh:
	case <-time.After(time.Second):
		t.Fatal("runVoiceTurnWithInputContext did not return")
	}

	if !result.interrupted || result.exit || result.waitForWakeupRequested || result.nextTurn == nil {
		t.Fatalf("runVoiceTurnWithInputContext result = %#v, want interrupted next turn", result)
	}
	if result.nextTurn.input.InputText != "next turn" {
		t.Fatalf("next turn input = %#v, want captured next turn", result.nextTurn.input)
	}
	if idleWaitCalls != 1 {
		t.Fatalf("WaitForVoiceRunIdle calls = %d, want 1", idleWaitCalls)
	}
}

func TestRunVoiceSessionWakeupInterruptsSpeaking(t *testing.T) {
	sigChan := make(chan os.Signal, 1)
	events := make(chan voiceEvent, 2)
	speakStarted := make(chan struct{})
	ctxCanceled := make(chan struct{})
	speakCalls := 0
	dialog := &fakeAudioDialog{
		frameSamples: 2,
		chunkBatches: [][]*agent.AudioChunkResult{
			{{PCM: pcm16BytesFromSamples(100, 200)}},
			{},
		},
		utterancesToReturn: [][]int16{
			{100, 200},
		},
		repeatEmpty: true,
		readDelay:   time.Millisecond,
		speak: func(ctx context.Context) error {
			speakCalls++
			if speakCalls > 1 {
				return nil
			}
			close(speakStarted)
			<-ctx.Done()
			close(ctxCanceled)
			return ctx.Err()
		},
	}

	go func() {
		<-speakStarted
		events <- voiceEventWakeup
	}()

	exit := runVoiceSession(agent.Config{VoiceFollowupTimeoutMs: 5}, dialog, nil, sigChan, events)
	if exit {
		t.Fatal("runVoiceSession returned exit=true")
	}
	select {
	case <-ctxCanceled:
	default:
		t.Fatal("speaking context was not canceled")
	}
	if len(dialog.spoken) != 1 {
		t.Fatalf("spoken texts = %d, want only interrupted reply without wakeup acknowledgement", len(dialog.spoken))
	}
	if dialog.starts != 2 {
		t.Fatalf("dialog starts = %d, want recording restarted immediately after wakeup interrupt", dialog.starts)
	}
}

func TestRunVoiceSessionSpeakingInterruptMarksNextUtteranceCorrection(t *testing.T) {
	sigChan := make(chan os.Signal, 1)
	events := make(chan voiceEvent, 1)
	speakStarted := make(chan struct{})
	ctxCanceled := make(chan struct{})
	speakCalls := 0
	dialog := &fakeAudioDialog{
		frameSamples: 2,
		chunkBatches: [][]*agent.AudioChunkResult{
			{{PCM: pcm16BytesFromSamples(100, 200)}},
			{{PCM: pcm16BytesFromSamples(300, 400)}},
		},
		utterancesToReturn: [][]int16{
			{100, 200},
			{300, 400},
		},
		prepareTurnInput: func(utterance []int16) (agent.TurnInput, error) {
			if len(utterance) > 0 && utterance[0] == 300 {
				return agent.TurnInput{InputText: "改成明天。", Transcript: "改成明天。"}, nil
			}
			return agent.TurnInput{InputText: "订今天的票。", Transcript: "订今天的票。"}, nil
		},
		speak: func(ctx context.Context) error {
			speakCalls++
			if speakCalls > 1 {
				return nil
			}
			close(speakStarted)
			<-ctx.Done()
			close(ctxCanceled)
			return ctx.Err()
		},
	}

	go func() {
		<-speakStarted
		events <- voiceEventWakeup
	}()

	exit := runVoiceSession(agent.Config{VoiceFollowupTimeoutMs: 50, VoiceMaxTurns: 1}, dialog, nil, sigChan, events)
	if exit {
		t.Fatal("runVoiceSession returned exit=true")
	}
	select {
	case <-ctxCanceled:
	default:
		t.Fatal("speaking context was not canceled")
	}
	if len(dialog.voiceTurnContexts) != 2 {
		t.Fatalf("voice turn contexts = %#v, want original turn and correction follow-up", dialog.voiceTurnContexts)
	}
	if dialog.voiceTurnContexts[0] != (agent.VoiceTurnContext{}) {
		t.Fatalf("first voice turn context = %#v, want ordinary new task", dialog.voiceTurnContexts[0])
	}
	if got, want := dialog.voiceTurnContexts[1], interruptedVoiceCorrectionContext(); got != want {
		t.Fatalf("second voice turn context = %#v, want %#v", got, want)
	}
}

func TestRunWakeupModeSTTSpeakingInterruptMarksNextTurnCorrection(t *testing.T) {
	advanceWakeup := withWakeupDebounceClock(t, time.Unix(0, 0))
	sigChan := make(chan os.Signal, 1)
	watcher := &fakeWakeupWatcher{fireOnStart: true}
	speakStarted := make(chan struct{})
	ctxCanceled := make(chan struct{})
	voiceSessionDisabled := false
	speakCalls := 0
	var dialog *fakeAudioDialog
	dialog = &fakeAudioDialog{
		frameSamples: 2,
		chunkBatches: [][]*agent.AudioChunkResult{
			{{PCM: pcm16BytesFromSamples(100, 200)}},
			{{PCM: pcm16BytesFromSamples(300, 400)}},
		},
		utterancesToReturn: [][]int16{
			{100, 200},
			{300, 400},
		},
		prepareTurnInput: func(utterance []int16) (agent.TurnInput, error) {
			if len(utterance) > 0 && utterance[0] == 300 {
				return agent.TurnInput{InputText: "短一点。", Transcript: "短一点。"}, nil
			}
			return agent.TurnInput{InputText: "总结这段内容。", Transcript: "总结这段内容。"}, nil
		},
		speak: func(ctx context.Context) error {
			speakCalls++
			if speakCalls > 1 {
				sigChan <- syscall.SIGTERM
				return nil
			}
			close(speakStarted)
			<-ctx.Done()
			close(ctxCanceled)
			return ctx.Err()
		},
	}

	done := make(chan struct{})
	go func() {
		runWakeupMode(agent.Config{InputMode: "stt", VoiceFollowupEnabled: &voiceSessionDisabled, VoiceFollowupTimeoutMs: 50}, dialog, nil, sigChan, func(pin int, callback func()) (wakeupWatcher, error) {
			watcher.callback = callback
			return watcher, nil
		})
		close(done)
	}()

	select {
	case <-speakStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("runWakeupMode did not start speaking")
	}
	advanceWakeup(wakeupDebounceInterval + time.Millisecond)
	watcher.callback()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("runWakeupMode did not stop after correction turn")
	}
	select {
	case <-ctxCanceled:
	default:
		t.Fatal("speaking context was not canceled")
	}
	if len(dialog.voiceTurnContexts) != 2 {
		t.Fatalf("voice turn contexts = %#v, want original turn and correction follow-up", dialog.voiceTurnContexts)
	}
	if dialog.voiceTurnContexts[0] != (agent.VoiceTurnContext{}) {
		t.Fatalf("first voice turn context = %#v, want ordinary new task", dialog.voiceTurnContexts[0])
	}
	if got, want := dialog.voiceTurnContexts[1], interruptedVoiceCorrectionContext(); got != want {
		t.Fatalf("second voice turn context = %#v, want %#v", got, want)
	}
}

func pcm16Bytes(sample int16) []byte {
	return pcm16BytesFromSamples(sample)
}

func pcm16BytesFromSamples(samples ...int16) []byte {
	out := make([]byte, len(samples)*2)
	for i, sample := range samples {
		binary.LittleEndian.PutUint16(out[i*2:], uint16(sample))
	}
	return out
}

func constantSamples(samples int, value int16) []int16 {
	out := make([]int16, samples)
	for i := range out {
		out[i] = value
	}
	return out
}

func alternatingSamples(samples int, center, amplitude int16) []int16 {
	out := make([]int16, samples)
	for i := range out {
		if i%2 == 0 {
			out[i] = center + amplitude
		} else {
			out[i] = center - amplitude
		}
	}
	return out
}
