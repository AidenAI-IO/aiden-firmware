package main

import (
	"context"
	"encoding/binary"
	"os"
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
	onUtterance        func()
	utterances         [][]int16
	prepareTurnInput   func([]int16) (agent.TurnInput, error)
	runTurn            func(context.Context) (agent.RunResult, error)
	speak              func(context.Context) error
	spoken             []string
	repeatEmpty        bool
	readDelay          time.Duration
	ops                []string
	starts             int
	stops              int
	resets             int
	recordingActive    bool
}

func (d *fakeAudioDialog) StartRecording() error {
	if d.recordingActive {
		return nil
	}
	d.ops = append(d.ops, "start")
	d.starts++
	d.recordingActive = true
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
	return chunk, nil
}

func (d *fakeAudioDialog) ProcessVADFrame(samples []int16) []int16 {
	copied := append([]int16(nil), samples...)
	d.frames = append(d.frames, copied)
	if d.vad != nil {
		return d.vad.Process(samples)
	}
	if len(d.utterancesToReturn) > 0 {
		utterance := d.utterancesToReturn[0]
		d.utterancesToReturn = d.utterancesToReturn[1:]
		return utterance
	}
	if d.utterance != nil {
		utterance := d.utterance
		d.utterance = nil
		return utterance
	}
	return nil
}

func (d *fakeAudioDialog) ProcessUtterance(ctx context.Context, utterance []int16, runtime *agent.Runtime) error {
	d.utterances = append(d.utterances, append([]int16(nil), utterance...))
	if d.onUtterance != nil {
		d.onUtterance()
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

func (d *fakeAudioDialog) ResetVAD() {
	d.ops = append(d.ops, "reset")
	d.resets++
	if d.vad != nil {
		d.vad.Reset()
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
	callback func()
	started  bool
	stopped  bool
}

func (w *fakeWakeupWatcher) Start() error {
	w.started = true
	w.callback()
	return nil
}

func (w *fakeWakeupWatcher) Stop() {
	w.stopped = true
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
	recording := true

	processAudioLoop(dialog, nil, &recording, context.Background())

	if processedWhileRecording {
		t.Fatal("processed utterance while recording was still active")
	}
	if dialog.stops != 1 {
		t.Fatalf("dialog stops = %d, want 1 before utterance processing", dialog.stops)
	}
	if recording {
		t.Fatal("recording flag still true after utterance")
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
	watcher := &fakeWakeupWatcher{}
	var watcherPin int

	done := make(chan struct{})
	go func() {
		runWakeupMode(agent.Config{}, dialog, nil, sigChan, func(pin int, callback func()) (wakeupWatcher, error) {
			watcherPin = pin
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
	if watcherPin != 33 {
		t.Fatalf("watcher pin = %d, want 33", watcherPin)
	}
	if dialog.starts != 1 || dialog.stops == 0 || dialog.resets != 1 {
		t.Fatalf("dialog lifecycle starts=%d stops=%d resets=%d", dialog.starts, dialog.stops, dialog.resets)
	}
	if len(dialog.frames) != 1 || dialog.frames[0][0] != 300 || dialog.frames[0][1] != 400 {
		t.Fatalf("unexpected VAD frames: %#v", dialog.frames)
	}
}

func TestRunWakeupModeLegacyAcknowledgesWakeupBeforeRecording(t *testing.T) {
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
	watcher := &fakeWakeupWatcher{}

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

	if len(dialog.ops) < 3 {
		t.Fatalf("dialog ops = %#v, want acknowledgement before recording", dialog.ops)
	}
	if dialog.ops[0] != "speak:"+wakeupAckText || dialog.ops[1] != "start" || dialog.ops[2] != "reset" {
		t.Fatalf("dialog ops = %#v, want speak acknowledgement before start/reset", dialog.ops)
	}
}

func TestRunWakeupModeStopsRecordingAfterSpeechThenBiasedSilence(t *testing.T) {
	sigChan := make(chan os.Signal, 1)
	vad := agent.NewAudioVAD(1000, 50, 90, 60, false)
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
	watcher := &fakeWakeupWatcher{}

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

func TestRunWakeupModeDetectsLowEnergySpeechAfterNoiseFloorLearning(t *testing.T) {
	sigChan := make(chan os.Signal, 1)
	vad := agent.NewAudioVAD(1000, 500, 90, 60, false)
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
	watcher := &fakeWakeupWatcher{}

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
		t.Fatal("runWakeupMode did not stop after low-energy VAD utterance")
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
	watcher := &fakeWakeupWatcher{}
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
		runWakeupMode(agent.Config{VoiceSessionEnabled: &voiceSessionDisabled}, dialog, nil, sigChan, func(pin int, callback func()) (wakeupWatcher, error) {
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

func TestRunWakeupModeAudioWakeupKeepsLegacySingleTurnBehavior(t *testing.T) {
	sigChan := make(chan os.Signal, 1)
	watcher := &fakeWakeupWatcher{}
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
	watcher := &fakeWakeupWatcher{}
	var dialog *fakeAudioDialog
	dialog = &fakeAudioDialog{
		frameSamples: 2,
		chunks: []*agent.AudioChunkResult{
			{PCM: pcm16BytesFromSamples(100, 200)},
		},
		utterancesToReturn: [][]int16{
			{100, 200},
			{300, 400},
		},
		repeatEmpty: true,
		readDelay:   time.Millisecond,
		speak: func(ctx context.Context) error {
			if len(dialog.spoken) == 2 {
				go func() {
					time.Sleep(20 * time.Millisecond)
					dialog.chunks = append(dialog.chunks, &agent.AudioChunkResult{PCM: pcm16BytesFromSamples(300, 400)})
				}()
			}
			if len(dialog.spoken) == 3 {
				sigChan <- syscall.SIGTERM
			}
			return nil
		},
	}

	done := make(chan struct{})
	go func() {
		runWakeupMode(agent.Config{InputMode: "stt", VoiceFollowupTimeoutMs: 100}, dialog, nil, sigChan, func(pin int, callback func()) (wakeupWatcher, error) {
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
	if len(dialog.spoken) != 3 {
		t.Fatalf("spoken replies = %d, want wakeup acknowledgement plus 2 replies", len(dialog.spoken))
	}
	if dialog.spoken[0] != wakeupAckText {
		t.Fatalf("first spoken text = %q, want wakeup acknowledgement", dialog.spoken[0])
	}
}

func TestRunWakeupModeVoiceSessionAcknowledgesWakeupBeforeRecording(t *testing.T) {
	sigChan := make(chan os.Signal, 1)
	watcher := &fakeWakeupWatcher{}
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
			if len(dialog.spoken) == 2 {
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
		runWakeupMode(agent.Config{InputMode: "stt", VoiceMaxTurns: 1}, dialog, nil, sigChan, func(pin int, callback func()) (wakeupWatcher, error) {
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

	if len(dialog.ops) < 3 {
		t.Fatalf("dialog ops = %#v, want acknowledgement before recording", dialog.ops)
	}
	if dialog.ops[0] != "speak:"+wakeupAckText || dialog.ops[1] != "start" || dialog.ops[2] != "reset" {
		t.Fatalf("dialog ops = %#v, want speak acknowledgement before start/reset", dialog.ops)
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

func TestRunVoiceSessionClosesWhenAgentRequestsSleep(t *testing.T) {
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
			return agent.RunResult{Output: "我先休眠，等下次唤醒。", SleepRequested: true}, nil
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
		t.Fatalf("spoken = %#v, want no final reply after sleep request", dialog.spoken)
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
	if result.interrupted || result.sleepRequested || !result.exit {
		t.Fatalf("runVoiceTurn = interrupted:%v sleep:%v exit:%v, want false false true", result.interrupted, result.sleepRequested, result.exit)
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
	if result.interrupted || result.sleepRequested || !result.exit {
		t.Fatalf("runVoiceTurn = interrupted:%v sleep:%v exit:%v, want false false true", result.interrupted, result.sleepRequested, result.exit)
	}
	select {
	case <-ctxCanceled:
	default:
		t.Fatal("Speak goroutine was not finished before signal return")
	}
}

func TestCaptureUtteranceTimeoutDiscardsSilenceWhenVADNeverDetectedSpeech(t *testing.T) {
	vad := agent.NewAudioVAD(1000, 500, 90, 60, false)
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
	vad := agent.NewAudioVAD(1000, 500, 90, 60, false)
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
