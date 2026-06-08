package tts

import (
	"context"
	"testing"
	"time"
)

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

func pcmBytesForDuration(format AudioFormat, d time.Duration) int {
	bytesPerSample := format.BitWidth / 8
	return int((int64(format.SampleRate) * int64(format.Channels) * int64(bytesPerSample) * int64(d)) / int64(time.Second))
}

type recordingAudioBackend struct {
	starts        int
	writes        [][]byte
	writeIsFinal  []bool
	finalReceived bool
}

func (b *recordingAudioBackend) StartPlayback(AudioFormat) (uint64, error) {
	b.starts++
	return 77, nil
}

func (b *recordingAudioBackend) WritePlayChunk(_ uint64, data []byte, isFinal bool) error {
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

func (b *recordingAudioBackend) StopPlayback(uint64) error { return nil }

func (b *recordingAudioBackend) PlaybackSessionCount() (int, error) { return 0, nil }

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
