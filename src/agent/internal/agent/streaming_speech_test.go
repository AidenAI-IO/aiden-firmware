package agent

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestStreamingSpeechWriterDoesNotBlockAfterSpeakError(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	writer := newStreamingSpeechWriter(ctx, &AudioDialog{
		audioClient: NewAudioServiceClient("/tmp/audio.sock"),
		ttsClient:   &fakeTTSClient{err: errors.New("tts failed")},
	})

	done := make(chan error, 1)
	go func() {
		_, err := writer.Write([]byte(strings.Repeat("This sentence is long enough to become one spoken chunk. ", 40)))
		done <- err
	}()

	blocked := false
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Write() error = %v", err)
		}
	case <-time.After(200 * time.Millisecond):
		blocked = true
		cancel()
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Fatal("Write() remained blocked after context cancellation")
		}
	}

	if err := writer.CloseAndWait(); err == nil {
		t.Fatal("CloseAndWait() error = nil, want TTS error")
	}
	if blocked {
		t.Fatal("Write() blocked after speakLoop observed a TTS error")
	}
	if writer.Spoke() {
		t.Fatal("Spoke() = true after failed TTS playback")
	}
}
