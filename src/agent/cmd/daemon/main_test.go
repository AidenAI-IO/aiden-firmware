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
	repeatEmpty        bool
	readDelay          time.Duration
	starts             int
	stops              int
	resets             int
}

func (d *fakeAudioDialog) StartRecording() error {
	d.starts++
	if len(d.chunkBatches) > 0 {
		d.chunks = d.chunkBatches[0]
		d.chunkBatches = d.chunkBatches[1:]
	}
	return nil
}

func (d *fakeAudioDialog) StopRecording() error {
	d.stops++
	return nil
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

func (d *fakeAudioDialog) FlushVAD() []int16 { return nil }

func (d *fakeAudioDialog) ResetVAD() {
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
		runWakeupMode(agent.Config{}, dialog, nil, sigChan, func(pin int, callback func()) (wakeupWatcher, error) {
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
