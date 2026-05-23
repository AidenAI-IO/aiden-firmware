package agent

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestNewMinimaxTTSDefaults(t *testing.T) {
	tts := NewMinimaxTTS("key", "", "", 0)

	if tts.voiceID != "male-qn-qingse" {
		t.Fatalf("voiceID = %q, want default", tts.voiceID)
	}
	if tts.emotion != "happy" {
		t.Fatalf("emotion = %q, want happy", tts.emotion)
	}
	if tts.speed != 1.0 {
		t.Fatalf("speed = %v, want 1.0", tts.speed)
	}
}

func TestMinimaxPlaybackFormatConstants(t *testing.T) {
	if minimaxTTSSampleRate != 16000 {
		t.Fatalf("sample rate = %d, want 16000", minimaxTTSSampleRate)
	}
	if minimaxTTSChannels != 1 {
		t.Fatalf("channels = %d, want 1", minimaxTTSChannels)
	}
	if minimaxTTSBitWidth != 16 {
		t.Fatalf("bit width = %d, want 16", minimaxTTSBitWidth)
	}
}

func TestMinimaxTTSCancelStopsPlaybackSession(t *testing.T) {
	audioServer := newTestAudioService(t)
	readStarted := make(chan struct{})
	transport := ttsRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Body: &blockingReadCloser{
				ctx:         req.Context(),
				readStarted: readStarted,
			},
			Header: make(http.Header),
		}, nil
	})
	tts := NewMinimaxTTS("key", "", "", 0, &http.Client{Transport: transport})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)

	go func() {
		done <- tts.TextToSpeechStream(ctx, "hello", NewAudioServiceClient(audioServer.socketPath))
	}()

	select {
	case <-readStarted:
	case <-time.After(time.Second):
		t.Fatal("TTS request body was not read")
	}
	cancel()

	select {
	case err := <-done:
		if err == nil || err != context.Canceled {
			t.Fatalf("TextToSpeechStream() error = %v, want context.Canceled", err)
		}
	case <-time.After(time.Second):
		t.Fatal("TextToSpeechStream did not return after cancel")
	}

	if audioServer.countOp("start_playback") != 1 {
		t.Fatalf("start_playback count = %d, want 1", audioServer.countOp("start_playback"))
	}
	if audioServer.countOp("stop_playback") != 1 {
		t.Fatalf("stop_playback count = %d, want 1", audioServer.countOp("stop_playback"))
	}
}

func TestStopPlaybackIgnoringEndedIgnoresMissingSession(t *testing.T) {
	audioServer := newTestAudioService(t)
	audioServer.stopStatus = "SESSION_NOT_FOUND"

	if err := stopPlaybackIgnoringEnded(NewAudioServiceClient(audioServer.socketPath), 77); err != nil {
		t.Fatalf("stopPlaybackIgnoringEnded() error = %v", err)
	}
}

func TestMinimaxTTSNaturalCompletionDoesNotStopPlayback(t *testing.T) {
	audioServer := newTestAudioService(t)
	audioServer.healthPlaybackSessions = []uint32{0}
	transport := ttsRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Body:       io.NopCloser(strings.NewReader(`{"data":{"audio":"0000"}}`)),
			Header:     make(http.Header),
		}, nil
	})
	tts := NewMinimaxTTS("key", "", "", 0, &http.Client{Transport: transport})

	if err := tts.TextToSpeechStream(context.Background(), "hello", NewAudioServiceClient(audioServer.socketPath)); err != nil {
		t.Fatalf("TextToSpeechStream() error = %v", err)
	}
	if audioServer.countOp("stop_playback") != 0 {
		t.Fatalf("stop_playback count = %d, want 0 on natural completion", audioServer.countOp("stop_playback"))
	}
	if audioServer.countOp("write_play_chunk") == 0 {
		t.Fatal("expected final playback chunks on natural completion")
	}
	if audioServer.countOp("health") == 0 {
		t.Fatal("expected health polling to wait for playback drain")
	}
}

func TestMinimaxTTSWaitsForPlaybackDrainBeforeReturning(t *testing.T) {
	audioServer := newTestAudioService(t)
	audioServer.healthPlaybackSessions = []uint32{1, 1, 0}
	transport := ttsRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Body:       io.NopCloser(strings.NewReader(`{"data":{"audio":"0000"}}`)),
			Header:     make(http.Header),
		}, nil
	})
	tts := NewMinimaxTTS("key", "", "", 0, &http.Client{Transport: transport})

	startedAt := time.Now()
	if err := tts.TextToSpeechStream(context.Background(), "hello", NewAudioServiceClient(audioServer.socketPath)); err != nil {
		t.Fatalf("TextToSpeechStream() error = %v", err)
	}
	if audioServer.countOp("health") < 3 {
		t.Fatalf("health count = %d, want at least 3", audioServer.countOp("health"))
	}
	if elapsed := time.Since(startedAt); elapsed < 2*playbackDrainPollInterval {
		t.Fatalf("TextToSpeechStream returned before playback drain polling elapsed: %s", elapsed)
	}
}

func TestMinimaxTTSDoneAudioIgnoresCallerDeadlineDuringPlaybackDrain(t *testing.T) {
	audioServer := newTestAudioService(t)
	audioServer.healthPlaybackSessions = []uint32{1, 1, 1, 1, 1, 0}
	transport := ttsRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Body:       io.NopCloser(strings.NewReader(`{"data":{"audio":"0000"}}`)),
			Header:     make(http.Header),
		}, nil
	})
	tts := NewMinimaxTTS("key", "", "", 0, &http.Client{Transport: transport})
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	if err := tts.TextToSpeechStream(ctx, "hello", NewAudioServiceClient(audioServer.socketPath)); err != nil {
		t.Fatalf("TextToSpeechStream() error = %v", err)
	}
	if ctx.Err() == nil {
		t.Fatal("caller deadline did not expire during playback drain")
	}
	if audioServer.countOp("health") < 6 {
		t.Fatalf("health count = %d, want drain polling to continue after caller deadline", audioServer.countOp("health"))
	}
}

func TestMinimaxTTSRetriesTransientPlaybackDrainHealthFailure(t *testing.T) {
	audioServer := newTestAudioService(t)
	audioServer.healthConnectionDrops = 1
	audioServer.healthPlaybackSessions = []uint32{1, 0}
	transport := ttsRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Body:       io.NopCloser(strings.NewReader(`{"data":{"audio":"0000"}}`)),
			Header:     make(http.Header),
		}, nil
	})
	tts := NewMinimaxTTS("key", "", "", 0, &http.Client{Transport: transport})

	if err := tts.TextToSpeechStream(context.Background(), "hello", NewAudioServiceClient(audioServer.socketPath)); err != nil {
		t.Fatalf("TextToSpeechStream() error = %v", err)
	}
	if audioServer.countOp("health") < 3 {
		t.Fatalf("health count = %d, want retry without consuming dropped health state", audioServer.countOp("health"))
	}
}

type ttsRoundTripFunc func(*http.Request) (*http.Response, error)

func (f ttsRoundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

type blockingReadCloser struct {
	ctx         context.Context
	readStarted chan struct{}
	once        sync.Once
}

func (b *blockingReadCloser) Read(_ []byte) (int, error) {
	b.once.Do(func() {
		close(b.readStarted)
	})
	<-b.ctx.Done()
	return 0, b.ctx.Err()
}

func (b *blockingReadCloser) Close() error {
	return nil
}

type testAudioService struct {
	socketPath               string
	listener                 net.Listener
	mu                       sync.Mutex
	ops                      []string
	stopStatus               string
	healthConnectionDrops    int
	healthPlaybackSessions   []uint32
	lastHealthPlaybackStatus uint32
}

func newTestAudioService(t *testing.T) *testAudioService {
	t.Helper()
	tempDir, err := os.MkdirTemp("", "aiden-tts-")
	if err != nil {
		t.Fatalf("create temp dir: %v", err)
	}
	t.Cleanup(func() {
		os.RemoveAll(tempDir)
	})
	socketPath := filepath.Join(tempDir, "audio.sock")
	listener, err := net.Listen("unix", socketPath)
	if err != nil {
		t.Fatalf("listen unix socket: %v", err)
	}
	s := &testAudioService{socketPath: socketPath, listener: listener, stopStatus: "OK"}
	t.Cleanup(func() {
		listener.Close()
	})
	go s.serve()
	return s
}

func (s *testAudioService) serve() {
	for {
		conn, err := s.listener.Accept()
		if err != nil {
			return
		}
		go s.handleConn(conn)
	}
}

func (s *testAudioService) handleConn(conn net.Conn) {
	defer conn.Close()
	msg, err := readUdsMessage(conn)
	if err != nil {
		return
	}
	var req audioRequest
	if err := json.Unmarshal([]byte(msg.HeaderJSON), &req); err != nil {
		return
	}
	s.mu.Lock()
	s.ops = append(s.ops, req.Op)
	stopStatus := s.stopStatus
	dropConnection := false
	var playbackSessions uint32
	if req.Op == "health" {
		if s.healthConnectionDrops > 0 {
			s.healthConnectionDrops--
			dropConnection = true
		} else {
			playbackSessions = s.nextHealthPlaybackSessionsLocked()
		}
	}
	s.mu.Unlock()
	if dropConnection {
		return
	}

	status := "OK"
	extra := map[string]interface{}{}
	if req.Op == "start_playback" {
		extra["session_id"] = uint64(77)
	}
	if req.Op == "start_recording" {
		extra["session_id"] = uint64(77)
	}
	if req.Op == "stop_playback" {
		status = stopStatus
	}
	if req.Op == "health" {
		extra["playback_sessions"] = playbackSessions
		extra["playback_active"] = playbackSessions > 0
		extra["record_sessions"] = uint32(0)
		extra["recording_active"] = false
	}
	resp := map[string]interface{}{"status": status}
	for k, v := range extra {
		resp[k] = v
	}
	data, _ := json.Marshal(resp)
	_ = writeUdsMessage(conn, udsMessage{HeaderJSON: string(data)})
}

func (s *testAudioService) nextHealthPlaybackSessionsLocked() uint32 {
	if len(s.healthPlaybackSessions) > 0 {
		value := s.healthPlaybackSessions[0]
		s.healthPlaybackSessions = s.healthPlaybackSessions[1:]
		s.lastHealthPlaybackStatus = value
		return value
	}
	return s.lastHealthPlaybackStatus
}

func (s *testAudioService) countOp(op string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	count := 0
	for _, got := range s.ops {
		if got == op {
			count++
		}
	}
	return count
}
