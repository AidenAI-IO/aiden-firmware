package agent

import (
	"context"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	gopty "github.com/aymanbagabas/go-pty"
)

const (
	shellSessionIdleTimeout = 30 * time.Minute
)

type shellSession struct {
	id           string
	command      string
	workdir      string
	usePTY       bool
	cmd          *exec.Cmd
	ptyCmd       *gopty.Cmd
	stdin        io.WriteCloser
	pty          gopty.Pty
	output       *shellRingBuffer
	done         chan struct{}
	cancel       context.CancelFunc
	startedAt    time.Time
	lastActivity time.Time
	exitErr      error
	exitCode     *int

	mu     sync.Mutex
	closed bool
}

type shellRingBuffer struct {
	mu        sync.Mutex
	buf       []byte
	readPos   int
	maxBytes  int
	truncated bool
}

type shellSessionManager struct {
	mu          sync.Mutex
	sessions    map[string]*shellSession
	nextID      uint64
	cleanupOnce sync.Once
}

var globalShellSessionManager = &shellSessionManager{
	sessions: map[string]*shellSession{},
}

func newShellRingBuffer(maxBytes int) *shellRingBuffer {
	return &shellRingBuffer{
		buf:      make([]byte, 0, maxBytes),
		maxBytes: maxBytes,
	}
}

func (b *shellRingBuffer) write(chunk []byte) {
	b.mu.Lock()
	defer b.mu.Unlock()

	b.buf = append(b.buf, chunk...)
	if len(b.buf) <= b.maxBytes {
		return
	}

	overflow := len(b.buf) - b.maxBytes
	b.buf = append([]byte{}, b.buf[overflow:]...)
	b.truncated = true
	if b.readPos >= overflow {
		b.readPos -= overflow
	} else {
		b.readPos = 0
	}
}

func (b *shellRingBuffer) readUnread(limit int) (string, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()

	if b.readPos >= len(b.buf) {
		return "", false
	}
	end := len(b.buf)
	truncated := false
	if limit > 0 && b.readPos+limit < end {
		end = b.readPos + limit
		truncated = true
	}
	text := string(b.buf[b.readPos:end])
	b.readPos = end
	return text, truncated || b.truncated
}

func (b *shellRingBuffer) hasUnread() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.readPos < len(b.buf)
}

func (s *shellSession) capture(r io.Reader) {
	buffer := make([]byte, 4096)
	for {
		n, err := r.Read(buffer)
		if n > 0 {
			s.output.write(buffer[:n])
		}
		if err != nil {
			return
		}
	}
}

func (s *shellSession) wait() {
	defer close(s.done)
	var exitErr error
	var exitCode *int
	if s.usePTY && s.ptyCmd != nil {
		exitErr = s.ptyCmd.Wait()
		if s.ptyCmd.ProcessState != nil {
			code := s.ptyCmd.ProcessState.ExitCode()
			exitCode = &code
		}
		s.setExitState(exitErr, exitCode)
		s.closeInput()
		return
	}

	exitErr = s.cmd.Wait()
	if s.cmd.ProcessState != nil {
		code := s.cmd.ProcessState.ExitCode()
		exitCode = &code
	}
	s.setExitState(exitErr, exitCode)
	s.closeInput()
}

func (s *shellSession) setExitState(exitErr error, exitCode *int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.exitErr = exitErr
	s.exitCode = exitCode
}

func (s *shellSession) isRunning() bool {
	select {
	case <-s.done:
		return false
	default:
		return true
	}
}

func (s *shellSession) processID() int {
	if s.usePTY && s.ptyCmd != nil && s.ptyCmd.Process != nil {
		return s.ptyCmd.Process.Pid
	}
	if s.cmd == nil || s.cmd.Process == nil {
		return 0
	}
	return s.cmd.Process.Pid
}

func (s *shellSession) exitCodeValue() interface{} {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.exitCode == nil {
		return nil
	}
	return *s.exitCode
}

func (s *shellSession) exitErrorText() interface{} {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.exitErr == nil {
		return nil
	}
	return s.exitErr.Error()
}

func (s *shellSession) stop() {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	s.closed = true

	if s.cancel != nil {
		s.cancel()
	}
	s.closeInputLocked()
	done := s.done
	var process *os.Process
	if s.usePTY && s.ptyCmd != nil {
		process = s.ptyCmd.Process
	} else if s.cmd != nil {
		process = s.cmd.Process
	}
	s.mu.Unlock()

	if process == nil {
		return
	}
	if err := process.Signal(os.Interrupt); err != nil {
		if killErr := shellKillProcessGroup(process); killErr != nil {
			log.Printf("shell: kill after failed interrupt: %v", killErr)
		}
		return
	}
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		if killErr := shellKillProcessGroup(process); killErr != nil {
			log.Printf("shell: kill after interrupt timeout: %v", killErr)
		}
	}
}

func (s *shellSession) closeInput() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closeInputLocked()
}

func (s *shellSession) closeInputLocked() {
	if s.pty != nil {
		_ = s.pty.Close()
		s.pty = nil
		s.stdin = nil
		return
	}
	if s.stdin != nil {
		_ = s.stdin.Close()
		s.stdin = nil
	}
}

func (s *shellSession) writeString(input string) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.closed || s.stdin == nil {
		return 0, fmt.Errorf("shell session is no longer running")
	}
	return io.WriteString(s.stdin, input)
}

func (m *shellSessionManager) nextSessionID() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.nextID++
	id := m.nextID
	return "shell-" + strconv.FormatUint(id, 10)
}

func (m *shellSessionManager) put(session *shellSession) {
	m.ensureCleanup()
	m.mu.Lock()
	defer m.mu.Unlock()
	session.lastActivity = time.Now()
	m.sessions[session.id] = session
}

func (m *shellSessionManager) get(id string) *shellSession {
	m.mu.Lock()
	defer m.mu.Unlock()
	session := m.sessions[id]
	if session != nil {
		session.lastActivity = time.Now()
	}
	return session
}

func (m *shellSessionManager) delete(id string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.sessions, id)
}

func (m *shellSessionManager) ensureCleanup() {
	m.cleanupOnce.Do(func() {
		go m.cleanupLoop()
	})
}

func (m *shellSessionManager) cleanupLoop() {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for range ticker.C {
		m.evictIdle()
	}
}

func (m *shellSessionManager) evictIdle() {
	m.mu.Lock()
	var expired []*shellSession
	for _, session := range m.sessions {
		if time.Since(session.lastActivity) > shellSessionIdleTimeout {
			expired = append(expired, session)
		}
	}
	for _, session := range expired {
		delete(m.sessions, session.id)
	}
	m.mu.Unlock()

	for _, session := range expired {
		session.stop()
	}
}

func shellPlatformShell() string {
	if runtime.GOOS == "windows" {
		return "cmd.exe"
	}
	return "sh"
}

func shellPlatformShellArg() string {
	if runtime.GOOS == "windows" {
		return "/C"
	}
	return "-c"
}

func shellCommandEnv(usePTY bool, execution shellExecutionConfig) []string {
	env := os.Environ()
	env = shellApplyProxyEnv(env, execution.proxy)
	// PYTHONUSERBASE and pip flags are configured globally by /etc/profile.d/aiden-python.sh
	// and inherited from the parent environment. TMPDIR is injected command-scoped
	// to use /userdata/tmp for agent shell commands, avoiding the small /tmp tmpfs (73 MB)
	// while preventing storage wear from a global TMPDIR override.
	if execution.temporaryDirectory != "" {
		env = shellEnsureEnv(env, "TMPDIR", execution.temporaryDirectory)
	}
	if usePTY {
		env = shellEnsureEnv(env, "TERM", "dumb")
		env = shellEnsureEnv(env, "NO_COLOR", "1")
		env = shellEnsureEnv(env, "CLICOLOR", "0")
		env = shellEnsureEnv(env, "COLORTERM", "")
		env = shellEnsureEnv(env, "TERM_PROGRAM", "")
		env = shellEnsureEnv(env, "TERMINAL_EMULATOR", "")
		env = shellEnsureEnv(env, "PAGER", "cat")
	}
	return env
}

func shellApplyProxyEnv(env []string, proxy ProxyConfig) []string {
	proxy = proxy.WithDefaults()
	if !proxy.HasProxyURL() {
		return env
	}

	for _, key := range []string{
		"http_proxy", "https_proxy", "all_proxy", "no_proxy",
		"HTTP_PROXY", "HTTPS_PROXY", "ALL_PROXY", "NO_PROXY",
	} {
		env = shellRemoveEnv(env, key)
	}

	if value := strings.TrimSpace(proxy.HTTPProxy); value != "" {
		env = shellEnsureEnv(env, "http_proxy", value)
		env = shellEnsureEnv(env, "HTTP_PROXY", value)
	}
	if value := strings.TrimSpace(proxy.HTTPSProxy); value != "" {
		env = shellEnsureEnv(env, "https_proxy", value)
		env = shellEnsureEnv(env, "HTTPS_PROXY", value)
	}
	if value := strings.TrimSpace(proxy.AllProxy); value != "" {
		env = shellEnsureEnv(env, "all_proxy", value)
		env = shellEnsureEnv(env, "ALL_PROXY", value)
	}
	if value := strings.TrimSpace(proxy.NoProxy); value != "" {
		env = shellEnsureEnv(env, "no_proxy", value)
		env = shellEnsureEnv(env, "NO_PROXY", value)
	}
	return env
}

func shellRemoveEnv(env []string, key string) []string {
	prefix := key + "="
	filtered := env[:0]
	for _, entry := range env {
		if !strings.HasPrefix(entry, prefix) {
			filtered = append(filtered, entry)
		}
	}
	return filtered
}

func shellEnsureEnv(env []string, key string, value string) []string {
	prefix := key + "="
	for i, entry := range env {
		if strings.HasPrefix(entry, prefix) {
			env[i] = prefix + value
			return env
		}
	}
	return append(env, prefix+value)
}

func shellKeySequence(key string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(key)) {
	case "enter", "return":
		return "\n", nil
	case "tab":
		return "\t", nil
	case "backspace":
		return "\b", nil
	case "escape", "esc":
		return "\x1b", nil
	case "space":
		return " ", nil
	case "ctrl+c":
		return "\x03", nil
	case "ctrl+d":
		return "\x04", nil
	case "ctrl+z":
		return "\x1a", nil
	case "up":
		return "\x1b[A", nil
	case "down":
		return "\x1b[B", nil
	case "right":
		return "\x1b[C", nil
	case "left":
		return "\x1b[D", nil
	default:
		return "", fmt.Errorf("unsupported key sequence: %s", key)
	}
}
