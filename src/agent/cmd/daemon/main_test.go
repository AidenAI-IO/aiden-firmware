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
	chunks       []*agent.AudioChunkResult
	frameSamples int
	frames       [][]int16
	utterance    []int16
	onUtterance  func()
	starts       int
	stops        int
	resets       int
}

func (d *fakeAudioDialog) StartRecording() error {
	d.starts++
	return nil
}

func (d *fakeAudioDialog) StopRecording() error {
	d.stops++
	return nil
}

func (d *fakeAudioDialog) ReadRecordChunk(timeoutMs uint32) (*agent.AudioChunkResult, error) {
	if len(d.chunks) == 0 {
		return &agent.AudioChunkResult{EndOfStream: true}, nil
	}
	chunk := d.chunks[0]
	d.chunks = d.chunks[1:]
	return chunk, nil
}

func (d *fakeAudioDialog) ProcessVADFrame(samples []int16) []int16 {
	copied := append([]int16(nil), samples...)
	d.frames = append(d.frames, copied)
	if d.utterance != nil {
		utterance := d.utterance
		d.utterance = nil
		return utterance
	}
	return nil
}

func (d *fakeAudioDialog) ProcessUtterance(ctx context.Context, utterance []int16, runtime *agent.Runtime) error {
	if d.onUtterance != nil {
		d.onUtterance()
	}
	return nil
}

func (d *fakeAudioDialog) FlushVAD() []int16 { return nil }

func (d *fakeAudioDialog) ResetVAD() { d.resets++ }

func (d *fakeAudioDialog) VADFrameSamples() int { return d.frameSamples }

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
