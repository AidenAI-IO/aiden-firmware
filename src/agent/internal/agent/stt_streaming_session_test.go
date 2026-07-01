package agent

import (
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type blockingFinalizeUploader struct {
	transcript  string
	finalizeErr error
	closeErr    error

	started   chan struct{}
	release   chan struct{}
	closed    chan struct{}
	startOnce sync.Once
	closeOnce sync.Once

	finalizeCalls int32
}

func newBlockingFinalizeUploader(transcript string) *blockingFinalizeUploader {
	return &blockingFinalizeUploader{
		transcript: transcript,
		started:    make(chan struct{}),
		release:    make(chan struct{}),
		closed:     make(chan struct{}),
	}
}

func (u *blockingFinalizeUploader) UploadPCM(_ []byte) error {
	return nil
}

func (u *blockingFinalizeUploader) Finalize() (string, error) {
	atomic.AddInt32(&u.finalizeCalls, 1)
	u.startOnce.Do(func() {
		close(u.started)
	})
	<-u.release
	if u.finalizeErr != nil {
		return "", u.finalizeErr
	}
	return u.transcript, nil
}

func (u *blockingFinalizeUploader) Close() error {
	u.closeOnce.Do(func() {
		close(u.release)
		close(u.closed)
	})
	return u.closeErr
}

func TestStreamingSTTSessionFinalizeIsSingleFlight(t *testing.T) {
	uploader := newBlockingFinalizeUploader("hello")
	session := &streamingSTTSession{uploader: uploader}

	type result struct {
		transcript string
		err        error
	}
	results := make(chan result, 2)

	go func() {
		transcript, err := session.Finalize()
		results <- result{transcript: transcript, err: err}
	}()

	select {
	case <-uploader.started:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for first finalize call")
	}

	go func() {
		transcript, err := session.Finalize()
		results <- result{transcript: transcript, err: err}
	}()

	select {
	case <-results:
		t.Fatal("Finalize() returned before the first call completed")
	case <-time.After(20 * time.Millisecond):
	}

	if err := uploader.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}

	for i := 0; i < 2; i++ {
		result := <-results
		if result.err != nil {
			t.Fatalf("Finalize() error = %v", result.err)
		}
		if result.transcript != "hello" {
			t.Fatalf("Finalize() transcript = %q, want hello", result.transcript)
		}
	}

	if got := atomic.LoadInt32(&uploader.finalizeCalls); got != 1 {
		t.Fatalf("Finalize() calls = %d, want 1", got)
	}
}

func TestStreamingSTTSessionFinalizeWithTimeoutClosesUploader(t *testing.T) {
	uploader := newBlockingFinalizeUploader("unused")
	session := &streamingSTTSession{uploader: uploader}

	transcript, err := session.FinalizeWithTimeout(20 * time.Millisecond)
	if transcript != "" {
		t.Fatalf("FinalizeWithTimeout() transcript = %q, want empty", transcript)
	}
	if !errors.Is(err, errStreamingSTTFinalizeTimeout) {
		t.Fatalf("FinalizeWithTimeout() error = %v, want timeout", err)
	}

	select {
	case <-uploader.closed:
	case <-time.After(time.Second):
		t.Fatal("expected uploader to be closed after finalize timeout")
	}
}
