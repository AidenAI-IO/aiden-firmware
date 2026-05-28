package agent

import (
	"context"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func TestStopPlaybackIgnoringEndedIgnoresMissingSession(t *testing.T) {
	audioServer := newTestAudioService(t)
	audioServer.stopStatus = "SESSION_NOT_FOUND"

	if err := stopPlaybackIgnoringEnded(NewAudioServiceClient(audioServer.socketPath), 77); err != nil {
		t.Fatalf("stopPlaybackIgnoringEnded() error = %v", err)
	}
}

func TestWaitForPlaybackDrainReturnsAfterPlaybackEnds(t *testing.T) {
	audioServer := newTestAudioService(t)
	audioServer.healthPlaybackSessions = []uint32{1, 1, 0}

	startedAt := time.Now()
	if err := waitForPlaybackDrain(context.Background(), NewAudioServiceClient(audioServer.socketPath), playbackDrainTimeout); err != nil {
		t.Fatalf("waitForPlaybackDrain() error = %v", err)
	}
	if audioServer.countOp("health") < 3 {
		t.Fatalf("health count = %d, want at least 3", audioServer.countOp("health"))
	}
	if elapsed := time.Since(startedAt); elapsed < 2*playbackDrainPollInterval {
		t.Fatalf("waitForPlaybackDrain returned before playback drain polling elapsed: %s", elapsed)
	}
}

func TestWaitForPlaybackDrainRetriesTransientHealthFailure(t *testing.T) {
	audioServer := newTestAudioService(t)
	audioServer.healthConnectionDrops = 1
	audioServer.healthPlaybackSessions = []uint32{1, 0}

	if err := waitForPlaybackDrain(context.Background(), NewAudioServiceClient(audioServer.socketPath), playbackDrainTimeout); err != nil {
		t.Fatalf("waitForPlaybackDrain() error = %v", err)
	}
	if audioServer.countOp("health") < 3 {
		t.Fatalf("health count = %d, want retry without consuming dropped health state", audioServer.countOp("health"))
	}
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
