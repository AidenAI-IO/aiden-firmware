package agent

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
)

var (
	localAudioRecorderLookPath       = exec.LookPath
	localAudioRecorderCommandContext = exec.CommandContext
)

const localAudioRecordChunkBytes = 4096

type localAudioRecordingBackend struct {
	mu       sync.Mutex
	nextID   uint64
	sessions map[uint64]*localAudioRecordingSession
	logger   *Logger
}

type localAudioRecordingSession struct {
	mu       sync.Mutex
	cancel   context.CancelFunc
	chunks   chan []byte
	done     chan struct{}
	stopCh   chan struct{}
	stopOnce sync.Once
	stopping bool
	err      error
}

type localAudioRecorder struct {
	name string
	args func(AudioFormat) []string
}

func newLocalAudioRecordingBackend(logger *Logger) *localAudioRecordingBackend {
	return &localAudioRecordingBackend{
		sessions: make(map[uint64]*localAudioRecordingSession),
		logger:   logger,
	}
}

func (b *localAudioRecordingBackend) StartRecording(format AudioFormat) (*RecordStartResult, error) {
	if err := validateLocalRecordingFormat(format); err != nil {
		return nil, err
	}
	recorder, err := detectLocalAudioRecorder()
	if err != nil {
		return nil, err
	}

	ctx, cancel := context.WithCancel(context.Background())
	cmd := localAudioRecorderCommandContext(ctx, recorder.name, recorder.args(format)...)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		cancel()
		return nil, fmt.Errorf("open local recorder output: %w", err)
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		cancel()
		return nil, fmt.Errorf("start local audio recorder %q: %w", recorder.name, err)
	}

	session := &localAudioRecordingSession{
		cancel: cancel,
		chunks: make(chan []byte, 32),
		done:   make(chan struct{}),
		stopCh: make(chan struct{}),
	}
	b.mu.Lock()
	b.nextID++
	id := b.nextID
	b.sessions[id] = session
	b.mu.Unlock()

	if b.logger != nil {
		b.logger.Info("Local audio recording enabled: recorder=%s", recorder.name)
	}
	go b.capture(id, session, cmd, stdout, &stderr)
	return &RecordStartResult{SessionID: id}, nil
}

func (b *localAudioRecordingBackend) capture(id uint64, session *localAudioRecordingSession, cmd *exec.Cmd, stdout io.Reader, stderr *bytes.Buffer) {
	defer close(session.done)
	defer close(session.chunks)

	buf := make([]byte, localAudioRecordChunkBytes)
	var readErr error
	for {
		n, err := stdout.Read(buf)
		if n > 0 {
			chunk := append([]byte(nil), buf[:n]...)
			if !sendLocalRecordingChunk(session, chunk) {
				readErr = err
				goto finished
			}
		}
		if err != nil {
			if err != io.EOF {
				readErr = err
			}
			break
		}
	}

finished:
	waitErr := cmd.Wait()
	session.mu.Lock()
	stopping := session.stopping
	if !stopping {
		switch {
		case readErr != nil:
			session.err = fmt.Errorf("read local audio recorder: %w", readErr)
		case waitErr != nil:
			detail := strings.TrimSpace(stderr.String())
			if detail != "" {
				session.err = fmt.Errorf("local audio recorder exited: %w: %s", waitErr, detail)
			} else {
				session.err = fmt.Errorf("local audio recorder exited: %w", waitErr)
			}
		default:
			session.err = fmt.Errorf("local audio recorder exited unexpectedly")
		}
	}
	session.mu.Unlock()
}

func sendLocalRecordingChunk(session *localAudioRecordingSession, chunk []byte) bool {
	// Prefer delivering data already returned by stdout.Read. Without this
	// first attempt, a closed stopCh can win a random select against a ready
	// buffered send and truncate the recording tail.
	select {
	case session.chunks <- chunk:
		return true
	default:
	}

	// If the consumer cannot currently accept the chunk, retain the stop case
	// so StopRecording cannot deadlock behind a full chunks buffer.
	select {
	case session.chunks <- chunk:
		return true
	case <-session.stopCh:
		return false
	}
}

func (b *localAudioRecordingBackend) ReadRecordChunk(sessionID uint64, timeoutMs uint32) (*AudioChunkResult, error) {
	b.mu.Lock()
	session := b.sessions[sessionID]
	b.mu.Unlock()
	if session == nil {
		return nil, fmt.Errorf("local recording session not found: %d", sessionID)
	}

	timer := time.NewTimer(time.Duration(timeoutMs) * time.Millisecond)
	defer timer.Stop()
	select {
	case chunk, ok := <-session.chunks:
		if ok {
			return &AudioChunkResult{PCM: chunk}, nil
		}
		b.deleteSession(sessionID, session)
		session.mu.Lock()
		err := session.err
		session.mu.Unlock()
		if err != nil {
			return nil, err
		}
		return &AudioChunkResult{EndOfStream: true}, nil
	case <-timer.C:
		return &AudioChunkResult{}, nil
	}
}

func (b *localAudioRecordingBackend) StopRecording(sessionID uint64) error {
	b.mu.Lock()
	session := b.sessions[sessionID]
	b.mu.Unlock()
	if session == nil {
		return nil
	}

	session.mu.Lock()
	session.stopping = true
	cancel := session.cancel
	session.mu.Unlock()
	session.stopOnce.Do(func() { close(session.stopCh) })
	if cancel != nil {
		cancel()
	}
	<-session.done
	b.deleteSession(sessionID, session)
	return nil
}

func (b *localAudioRecordingBackend) deleteSession(id uint64, session *localAudioRecordingSession) {
	b.mu.Lock()
	if b.sessions[id] == session {
		delete(b.sessions, id)
	}
	b.mu.Unlock()
}

func validateLocalRecordingFormat(format AudioFormat) error {
	if format.SampleRate == 0 {
		return fmt.Errorf("local recording requires sample_rate > 0")
	}
	if format.Channels == 0 {
		return fmt.Errorf("local recording requires channels > 0")
	}
	if format.BitWidth != 16 {
		return fmt.Errorf("local recording requires 16-bit PCM, got %d", format.BitWidth)
	}
	return nil
}

func detectLocalAudioRecorder() (*localAudioRecorder, error) {
	candidates := localAudioRecorderCandidates(runtime.GOOS)
	for _, candidate := range candidates {
		if _, err := localAudioRecorderLookPath(candidate.name); err == nil {
			return &candidate, nil
		}
	}
	names := make([]string, 0, len(candidates))
	for _, candidate := range candidates {
		names = append(names, candidate.name)
	}
	return nil, fmt.Errorf("no local audio recorder found (tried %s)", strings.Join(names, ", "))
}

func localAudioRecorderCandidates(goos string) []localAudioRecorder {
	rawArgs := func(format AudioFormat) []string {
		return []string{
			"-q", "-t", "raw", "-e", "signed-integer", "-L", "-b", "16",
			"-c", strconv.FormatUint(uint64(format.Channels), 10),
			"-r", strconv.FormatUint(uint64(format.SampleRate), 10), "-",
		}
	}
	switch goos {
	case "darwin":
		return []localAudioRecorder{
			{name: "rec", args: rawArgs},
			{name: "ffmpeg", args: func(format AudioFormat) []string {
				return ffmpegRecorderArgs("avfoundation", ":0", format)
			}},
		}
	case "windows":
		return nil
	default:
		return []localAudioRecorder{
			{name: "pw-record", args: func(format AudioFormat) []string {
				return []string{"--format=s16", "--rate=" + strconv.FormatUint(uint64(format.SampleRate), 10), "--channels=" + strconv.FormatUint(uint64(format.Channels), 10), "-"}
			}},
			{name: "parec", args: func(format AudioFormat) []string {
				return []string{"--raw", "--format=s16le", "--rate=" + strconv.FormatUint(uint64(format.SampleRate), 10), "--channels=" + strconv.FormatUint(uint64(format.Channels), 10)}
			}},
			{name: "arecord", args: func(format AudioFormat) []string {
				return []string{"-q", "-t", "raw", "-f", "S16_LE", "-r", strconv.FormatUint(uint64(format.SampleRate), 10), "-c", strconv.FormatUint(uint64(format.Channels), 10)}
			}},
			{name: "rec", args: rawArgs},
			{name: "ffmpeg", args: func(format AudioFormat) []string {
				return ffmpegRecorderArgs("pulse", "default", format)
			}},
		}
	}
}

func ffmpegRecorderArgs(inputFormat, input string, format AudioFormat) []string {
	return []string{
		"-hide_banner", "-loglevel", "error", "-f", inputFormat, "-i", input,
		"-ac", strconv.FormatUint(uint64(format.Channels), 10),
		"-ar", strconv.FormatUint(uint64(format.SampleRate), 10),
		"-f", "s16le", "pipe:1",
	}
}
