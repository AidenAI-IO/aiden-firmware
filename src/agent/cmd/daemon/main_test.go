package main

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"log"
	"os"
	"strings"
	"syscall"
	"testing"
	"time"

	"aiden-agent/internal/agent"
)

type fakeAudioDialog struct {
	chunks             []*agent.AudioChunkResult
	chunkBatches       [][]*agent.AudioChunkResult
	frameSamples       int
	frames             [][]int16
	vad                *agent.AudioVAD
	utterance          []int16
	utterancesToReturn [][]int16
	onStart            func(*fakeAudioDialog)
	onUtterance        func()
	processUtterance   func(context.Context, []int16, *agent.Runtime) error
	utterances         [][]int16
	prepareTurnInput   func([]int16) (agent.TurnInput, error)
	runTurn            func(context.Context) (agent.RunResult, error)
	speak              func(context.Context) error
	persistedTurns     []persistedVoiceTurn
	vadErr             error
	onRead             func(*fakeAudioDialog, *agent.AudioChunkResult)
	spoken             []string
	repeatEmpty        bool
	readDelay          time.Duration
	ops                []string
	starts             int
	stops              int
	resets             int
	recordingActive    bool
}

type persistedVoiceTurn struct {
	input     agent.TurnInput
	result    agent.RunResult
	utterance []int16
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

func (d *fakeAudioDialog) PersistVoiceTurn(input agent.TurnInput, result agent.RunResult, utterance []int16) {
	d.persistedTurns = append(d.persistedTurns, persistedVoiceTurn{
		input:     input,
		result:    result,
		utterance: append([]int16(nil), utterance...),
	})
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

type fakeWakeupWatcher struct {
	callback     func()
	fireOnStart  bool
	firedOnStart bool
	started      bool
	stopped      bool
	startCount   int
	stopCount    int
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
	}

	if eventCount != 2 {
		t.Fatalf("event count = %d, want one wakeup event per GPIO callback", eventCount)
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
	if len(dialog.ops) < 2 {
		t.Fatalf("dialog ops = %#v, want recording start/reset", dialog.ops)
	}
	if dialog.ops[0] != "start" || dialog.ops[1] != "reset" {
		t.Fatalf("dialog ops = %#v, want start/reset without wakeup acknowledgement", dialog.ops)
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

func TestRunWakeupModeLegacyWakeupInterruptsProcessing(t *testing.T) {
	sigChan := make(chan os.Signal, 1)
	watcher := &fakeWakeupWatcher{fireOnStart: true}
	processingStarted := make(chan struct{})
	ctxCanceled := make(chan struct{})
	secondStart := make(chan struct{})
	voiceSessionDisabled := false
	var dialog *fakeAudioDialog
	dialog = &fakeAudioDialog{
		frameSamples: 2,
		chunks: []*agent.AudioChunkResult{
			{PCM: pcm16BytesFromSamples(100, 200)},
		},
		utterance: []int16{100, 200},
		onStart: func(d *fakeAudioDialog) {
			if d.starts == 2 {
				close(secondStart)
				sigChan <- syscall.SIGTERM
			}
		},
		processUtterance: func(ctx context.Context, utterance []int16, runtime *agent.Runtime) error {
			close(processingStarted)
			select {
			case <-ctx.Done():
				close(ctxCanceled)
				return ctx.Err()
			case <-time.After(150 * time.Millisecond):
				sigChan <- syscall.SIGTERM
				return errors.New("legacy wakeup did not cancel utterance processing")
			}
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
	case <-processingStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("legacy wakeup mode did not start utterance processing")
	}
	watcher.callback()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("runWakeupMode did not stop after interrupted legacy wakeup")
	}
	select {
	case <-ctxCanceled:
	default:
		t.Fatal("legacy utterance context was not canceled by wakeup")
	}
	select {
	case <-secondStart:
	default:
		t.Fatal("legacy wakeup interrupt did not trigger the next recording")
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

	if len(dialog.ops) < 2 {
		t.Fatalf("dialog ops = %#v, want recording start/reset", dialog.ops)
	}
	if dialog.ops[0] != "start" || dialog.ops[1] != "reset" {
		t.Fatalf("dialog ops = %#v, want start/reset without wakeup acknowledgement", dialog.ops)
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
		t.Fatal("RunAgentTurn goroutine was not finished before signal return")
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

func TestRunVoiceSessionWakeupInterruptsThinking(t *testing.T) {
	sigChan := make(chan os.Signal, 1)
	events := make(chan voiceEvent, 2)
	runStarted := make(chan struct{})
	ctxCanceled := make(chan struct{})
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
		runTurn: func(ctx context.Context) (agent.RunResult, error) {
			close(runStarted)
			<-ctx.Done()
			close(ctxCanceled)
			return agent.RunResult{}, ctx.Err()
		},
	}

	go func() {
		<-runStarted
		events <- voiceEventWakeup
	}()

	exit := runVoiceSession(agent.Config{VoiceFollowupTimeoutMs: 5}, dialog, nil, sigChan, events)
	if exit {
		t.Fatal("runVoiceSession returned exit=true")
	}
	select {
	case <-ctxCanceled:
	default:
		t.Fatal("thinking context was not canceled")
	}
	if len(dialog.spoken) != 0 {
		t.Fatalf("spoken texts = %#v, want no acknowledgement after wakeup interrupt", dialog.spoken)
	}
	if dialog.starts != 2 {
		t.Fatalf("dialog starts = %d, want recording restarted for follow-up listen", dialog.starts)
	}
}

func TestRunVoiceTurnPersistsCompletedResultReturnedDuringWakeupInterrupt(t *testing.T) {
	sigChan := make(chan os.Signal, 1)
	events := make(chan voiceEvent, 1)
	runStarted := make(chan struct{})
	ctxCanceled := make(chan struct{})
	dialog := &fakeAudioDialog{
		prepareTurnInput: func(utterance []int16) (agent.TurnInput, error) {
			return agent.TurnInput{InputText: "广州。", Transcript: "广州。"}, nil
		},
		runTurn: func(ctx context.Context) (agent.RunResult, error) {
			close(runStarted)
			<-ctx.Done()
			close(ctxCanceled)
			return agent.RunResult{EpisodeID: "ep_weather", Output: "广州当前天气：小雨。"}, nil
		},
	}

	go func() {
		<-runStarted
		events <- voiceEventWakeup
	}()

	result := runVoiceTurn(agent.Config{}, dialog, nil, []int16{100, 200}, sigChan, events)
	if !result.interrupted || result.exit || result.waitForWakeupRequested {
		t.Fatalf("runVoiceTurn = interrupted:%v exit:%v wait:%v, want true false false", result.interrupted, result.exit, result.waitForWakeupRequested)
	}
	select {
	case <-ctxCanceled:
	default:
		t.Fatal("thinking context was not canceled")
	}
	if len(dialog.persistedTurns) != 1 {
		t.Fatalf("persisted turns = %d, want 1", len(dialog.persistedTurns))
	}
	turn := dialog.persistedTurns[0]
	if turn.input.Transcript != "广州。" {
		t.Fatalf("persisted transcript = %q, want 广州。", turn.input.Transcript)
	}
	if turn.result.EpisodeID != "ep_weather" || turn.result.Output != "广州当前天气：小雨。" {
		t.Fatalf("persisted result = %#v, want completed weather result", turn.result)
	}
	if len(turn.utterance) != 2 || turn.utterance[0] != 100 || turn.utterance[1] != 200 {
		t.Fatalf("persisted utterance = %#v, want captured samples", turn.utterance)
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
