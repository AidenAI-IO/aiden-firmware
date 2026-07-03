package tts

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func withDrainWait(t *testing.T, fn func(context.Context, time.Duration) error) {
	t.Helper()
	original := waitForEstimatedDrain
	waitForEstimatedDrain = fn
	t.Cleanup(func() { waitForEstimatedDrain = original })
}

func withDrainNow(t *testing.T, fn func() time.Time) {
	t.Helper()
	original := playbackDrainNow
	playbackDrainNow = fn
	t.Cleanup(func() { playbackDrainNow = original })
}

func skipDrainWait(t *testing.T) {
	t.Helper()
	withDrainWait(t, func(context.Context, time.Duration) error { return nil })
}

func TestAudioServiceSinkPrebuffersBeforeStartingPlayback(t *testing.T) {
	format := AudioFormat{SampleRate: 16000, Channels: 1, BitWidth: 16}
	backend := &recordingAudioBackend{}
	sink := NewAudioServiceSink(backend, format)

	if err := sink.WritePCM(make([]byte, pcmBytesForDuration(format, 200*time.Millisecond))); err != nil {
		t.Fatalf("WritePCM() first chunk error = %v", err)
	}
	if backend.starts != 0 {
		t.Fatalf("playback starts after first small chunk = %d, want 0", backend.starts)
	}

	if err := sink.WritePCM(make([]byte, pcmBytesForDuration(format, 300*time.Millisecond))); err != nil {
		t.Fatalf("WritePCM() second chunk error = %v", err)
	}
	if backend.starts != 1 {
		t.Fatalf("playback starts after prebuffer = %d, want 1", backend.starts)
	}
	if got, want := backend.totalWrittenBytes(), pcmBytesForDuration(format, 500*time.Millisecond); got != want {
		t.Fatalf("prebuffered bytes written = %d, want %d", got, want)
	}
}

func TestAudioServiceSinkDrainFlushesShortBufferedPlayback(t *testing.T) {
	skipDrainWait(t)

	format := AudioFormat{SampleRate: 16000, Channels: 1, BitWidth: 16}
	backend := &recordingAudioBackend{}
	sink := NewAudioServiceSink(backend, format)

	wantBytes := pcmBytesForDuration(format, 120*time.Millisecond)
	if err := sink.WritePCM(make([]byte, wantBytes)); err != nil {
		t.Fatalf("WritePCM() error = %v", err)
	}
	if backend.starts != 0 {
		t.Fatalf("playback starts before drain = %d, want 0", backend.starts)
	}
	if err := sink.Drain(context.Background()); err != nil {
		t.Fatalf("Drain() error = %v", err)
	}
	if backend.starts != 1 {
		t.Fatalf("playback starts after drain = %d, want 1", backend.starts)
	}
	if got, want := backend.totalWrittenBytes(), wantBytes+pcmBytesForDuration(format, playbackTailSilenceDuration); got != want {
		t.Fatalf("drained bytes written = %d, want %d", got, want)
	}
	if !backend.finalReceived {
		t.Fatal("Drain() did not send final playback chunk")
	}
}

func TestAudioServiceSinkWritesTailSilenceBeforeFinal(t *testing.T) {
	skipDrainWait(t)

	format := AudioFormat{SampleRate: 16000, Channels: 1, BitWidth: 16}
	backend := &recordingAudioBackend{}
	sink := NewAudioServiceSink(backend, format)

	speechBytes := pcmBytesForDuration(format, 600*time.Millisecond)
	if err := sink.WritePCM(make([]byte, speechBytes)); err != nil {
		t.Fatalf("WritePCM() error = %v", err)
	}
	if err := sink.Drain(context.Background()); err != nil {
		t.Fatalf("Drain() error = %v", err)
	}

	tail := backend.dataAfterOffset(speechBytes)
	if got, want := len(tail), pcmBytesForDuration(format, playbackTailSilenceDuration); got != want {
		t.Fatalf("tail silence bytes = %d, want %d", got, want)
	}
	for i, b := range tail {
		if b != 0 {
			t.Fatalf("tail silence byte %d = %d, want 0", i, b)
		}
	}
}

func TestAudioServiceSinkFlushPendingRetainsBufferAfterStartFailure(t *testing.T) {
	format := AudioFormat{SampleRate: 16000, Channels: 1, BitWidth: 16}
	backend := &recordingAudioBackend{startErr: errors.New("temporary start failure")}
	sink := NewAudioServiceSink(backend, format)
	wantBytes := pcmBytesForDuration(format, 120*time.Millisecond)
	sink.pending = make([]byte, wantBytes)

	if err := sink.flushPending(); err == nil {
		t.Fatal("flushPending() error = nil, want start failure")
	}
	backend.startErr = nil

	if err := sink.flushPending(); err != nil {
		t.Fatalf("flushPending() retry error = %v", err)
	}
	if got := backend.totalWrittenBytes(); got != wantBytes {
		t.Fatalf("retry wrote bytes = %d, want %d", got, wantBytes)
	}
}

func TestAudioServiceSinkFlushPendingRetainsBufferAfterWriteFailure(t *testing.T) {
	format := AudioFormat{SampleRate: 16000, Channels: 1, BitWidth: 16}
	backend := &recordingAudioBackend{writeErr: errors.New("temporary write failure")}
	sink := NewAudioServiceSink(backend, format)
	wantBytes := pcmBytesForDuration(format, 120*time.Millisecond)
	sink.pending = make([]byte, wantBytes)

	if err := sink.flushPending(); err == nil {
		t.Fatal("flushPending() error = nil, want write failure")
	}
	backend.writeErr = nil

	if err := sink.flushPending(); err != nil {
		t.Fatalf("flushPending() retry error = %v", err)
	}
	if got := backend.totalWrittenBytes(); got != wantBytes {
		t.Fatalf("retry wrote bytes = %d, want %d", got, wantBytes)
	}
}

func TestAudioServiceSinkStopDropsBufferedPlayback(t *testing.T) {
	format := AudioFormat{SampleRate: 16000, Channels: 1, BitWidth: 16}
	backend := &recordingAudioBackend{}
	sink := NewAudioServiceSink(backend, format)

	if err := sink.WritePCM(make([]byte, pcmBytesForDuration(format, 100*time.Millisecond))); err != nil {
		t.Fatalf("WritePCM() error = %v", err)
	}
	if err := sink.Stop(); err != nil {
		t.Fatalf("Stop() error = %v", err)
	}
	if err := sink.WritePCM(make([]byte, pcmBytesForDuration(format, 500*time.Millisecond))); err != ErrSessionClosed {
		t.Fatalf("WritePCM() after Stop() error = %v, want ErrSessionClosed", err)
	}
	if backend.starts != 0 {
		t.Fatalf("playback starts after stopped prebuffer = %d, want 0", backend.starts)
	}
}

func TestAudioServiceSinkStopCanRetryAfterBackendFailure(t *testing.T) {
	format := AudioFormat{SampleRate: 16000, Channels: 1, BitWidth: 16}
	backend := &recordingAudioBackend{stopErr: errors.New("temporary stop failure")}
	sink := NewAudioServiceSink(backend, format)

	if err := sink.writePCMChunks([]byte{1}); err != nil {
		t.Fatalf("writePCMChunks() error = %v", err)
	}
	if err := sink.Stop(); err == nil {
		t.Fatal("Stop() error = nil, want stop failure")
	}
	backend.stopErr = nil

	if err := sink.Stop(); err != nil {
		t.Fatalf("Stop() retry error = %v", err)
	}
	if backend.stops != 2 {
		t.Fatalf("StopPlayback() calls = %d, want 2", backend.stops)
	}
	if err := sink.WritePCM([]byte{1}); err != ErrSessionClosed {
		t.Fatalf("WritePCM() after successful Stop() error = %v, want ErrSessionClosed", err)
	}
}

func TestAudioServiceSinkDrainWaitsForOwnPCMSOnly(t *testing.T) {
	backend := &countingBackend{activeOthers: 1}
	format := AudioFormat{SampleRate: 16000, Channels: 1, BitWidth: 16}
	sink := NewAudioServiceSink(backend, format)
	var (
		mu       sync.Mutex
		waitSeen time.Duration
	)
	withDrainWait(t, func(_ context.Context, wait time.Duration) error {
		mu.Lock()
		waitSeen = wait
		mu.Unlock()
		return nil
	})

	pcm := make([]byte, pcmBytesForDuration(format, time.Second))
	if err := sink.WritePCM(pcm); err != nil {
		t.Fatalf("WritePCM() error = %v", err)
	}

	if err := sink.Drain(context.Background()); err != nil {
		t.Fatalf("Drain() error = %v", err)
	}
	mu.Lock()
	gotWait := waitSeen
	mu.Unlock()
	if gotWait < 1900*time.Millisecond {
		t.Fatalf("Drain() wait = %s, want at least 1900ms", gotWait)
	}
	if gotWait > 3*time.Second {
		t.Fatalf("Drain() wait = %s, want no more than 3s", gotWait)
	}
	if got := atomic.LoadInt32(&backend.finalCalls); got != 1 {
		t.Fatalf("finalCalls = %d, want 1", got)
	}
}

func TestAudioServiceSinkDrainSubtractsElapsedPlayback(t *testing.T) {
	backend := &countingBackend{}
	format := AudioFormat{SampleRate: 16000, Channels: 1, BitWidth: 16}
	sink := NewAudioServiceSink(backend, format)
	startedAt := time.Unix(100, 0)
	now := startedAt
	withDrainNow(t, func() time.Time { return now })

	var waitSeen time.Duration
	withDrainWait(t, func(_ context.Context, wait time.Duration) error {
		waitSeen = wait
		return nil
	})

	if err := sink.WritePCM(make([]byte, pcmBytesForDuration(format, time.Second))); err != nil {
		t.Fatalf("WritePCM() error = %v", err)
	}
	now = startedAt.Add(1500 * time.Millisecond)

	if err := sink.Drain(context.Background()); err != nil {
		t.Fatalf("Drain() error = %v", err)
	}

	want := 1216 * time.Millisecond
	if waitSeen != want {
		t.Fatalf("Drain() wait = %s, want %s", waitSeen, want)
	}
	if waitSeen >= EstimatedPlaybackDrainDuration(format, pcmBytesForDuration(format, 2*time.Second)) {
		t.Fatalf("Drain() waited as if no streamed playback had elapsed: %s", waitSeen)
	}
}

func TestAudioServiceSinkDrainRespectsContextCancel(t *testing.T) {
	backend := &countingBackend{}
	sink := NewAudioServiceSink(backend, AudioFormat{SampleRate: 16000, Channels: 1, BitWidth: 16})
	if err := sink.WritePCM(make([]byte, 16000*4)); err != nil {
		t.Fatalf("WritePCM() error = %v", err)
	}

	withDrainWait(t, func(context.Context, time.Duration) error {
		return context.DeadlineExceeded
	})
	ctx := context.Background()
	if err := sink.Drain(ctx); err == nil {
		t.Fatal("Drain() error = nil, want context deadline exceeded")
	}
}

func TestEstimatedPlaybackDrainDuration(t *testing.T) {
	got := EstimatedPlaybackDrainDuration(AudioFormat{SampleRate: 16000, Channels: 1, BitWidth: 16}, 32000)
	want := time.Second + 812*time.Millisecond
	if got != want {
		t.Fatalf("EstimatedPlaybackDrainDuration() = %s, want %s", got, want)
	}
}

func pcmBytesForDuration(format AudioFormat, d time.Duration) int {
	bytesPerSample := format.BitWidth / 8
	return int((int64(format.SampleRate) * int64(format.Channels) * int64(bytesPerSample) * int64(d)) / int64(time.Second))
}

type recordingAudioBackend struct {
	starts        int
	stops         int
	writes        [][]byte
	writeIsFinal  []bool
	finalReceived bool
	startErr      error
	writeErr      error
	stopErr       error
}

func (b *recordingAudioBackend) StartPlayback(AudioFormat) (uint64, error) {
	b.starts++
	if b.startErr != nil {
		return 0, b.startErr
	}
	return 77, nil
}

func (b *recordingAudioBackend) WritePlayChunk(_ uint64, data []byte, isFinal bool) error {
	if b.writeErr != nil {
		return b.writeErr
	}
	if len(data) > 0 {
		chunk := make([]byte, len(data))
		copy(chunk, data)
		b.writes = append(b.writes, chunk)
	}
	b.writeIsFinal = append(b.writeIsFinal, isFinal)
	if isFinal {
		b.finalReceived = true
	}
	return nil
}

func (b *recordingAudioBackend) StopPlayback(uint64) error {
	b.stops++
	return b.stopErr
}

func (b *recordingAudioBackend) totalWrittenBytes() int {
	total := 0
	for _, write := range b.writes {
		total += len(write)
	}
	return total
}

func (b *recordingAudioBackend) dataAfterOffset(offset int) []byte {
	all := make([]byte, 0, b.totalWrittenBytes())
	for _, write := range b.writes {
		all = append(all, write...)
	}
	if offset >= len(all) {
		return nil
	}
	return all[offset:]
}

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
