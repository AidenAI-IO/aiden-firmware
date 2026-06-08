package tts

import (
	"context"
	"sync/atomic"
	"testing"
	"time"
)

type countingBackend struct {
	activeOthers int32
	startCalls   int32
	finalCalls   int32
}

func (b *countingBackend) StartPlayback(format AudioFormat) (uint64, error) {
	atomic.AddInt32(&b.startCalls, 1)
	return 1, nil
}

func (b *countingBackend) WritePlayChunk(sessionID uint64, data []byte, isFinal bool) error {
	if isFinal {
		atomic.AddInt32(&b.finalCalls, 1)
	}
	return nil
}

func (b *countingBackend) StopPlayback(sessionID uint64) error { return nil }

func TestAudioServiceSinkDrainWaitsForOwnPCMSOnly(t *testing.T) {
	backend := &countingBackend{activeOthers: 1}
	sink := NewAudioServiceSink(backend, AudioFormat{SampleRate: 16000, Channels: 1, BitWidth: 16})

	pcm := make([]byte, 16000*2) // 1 second mono s16le
	if err := sink.WritePCM(pcm); err != nil {
		t.Fatalf("WritePCM() error = %v", err)
	}

	started := time.Now()
	if err := sink.Drain(context.Background()); err != nil {
		t.Fatalf("Drain() error = %v", err)
	}
	elapsed := time.Since(started)
	if elapsed < 900*time.Millisecond {
		t.Fatalf("Drain() returned too early: %s", elapsed)
	}
	if elapsed > 2*time.Second {
		t.Fatalf("Drain() took too long: %s", elapsed)
	}
	if got := atomic.LoadInt32(&backend.finalCalls); got != 1 {
		t.Fatalf("finalCalls = %d, want 1", got)
	}
}

func TestAudioServiceSinkDrainRespectsContextCancel(t *testing.T) {
	backend := &countingBackend{}
	sink := NewAudioServiceSink(backend, AudioFormat{SampleRate: 16000, Channels: 1, BitWidth: 16})
	if err := sink.WritePCM(make([]byte, 16000*4)); err != nil {
		t.Fatalf("WritePCM() error = %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if err := sink.Drain(ctx); err == nil {
		t.Fatal("Drain() error = nil, want context deadline exceeded")
	}
}

func TestEstimatedPlaybackDrainDuration(t *testing.T) {
	got := EstimatedPlaybackDrainDuration(AudioFormat{SampleRate: 16000, Channels: 1, BitWidth: 16}, 32000)
	want := time.Second + playbackDrainGrace + playbackDrainMargin
	if got != want {
		t.Fatalf("EstimatedPlaybackDrainDuration() = %s, want %s", got, want)
	}
}
