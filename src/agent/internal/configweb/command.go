package configweb

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"time"
)

type commandResult struct {
	Output   []byte
	ExitCode int
	TimedOut bool
}

func runCommand(timeout time.Duration, env []string, input []byte, name string, args ...string) commandResult {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Env = env
	if cmd.Env == nil {
		cmd.Env = os.Environ()
	}
	cmd.Stdin = bytes.NewReader(input)
	output, err := cmd.CombinedOutput()
	result := commandResult{Output: output, ExitCode: 0}
	if ctx.Err() == context.DeadlineExceeded {
		result.TimedOut = true
		result.ExitCode = -1
		return result
	}
	if err == nil {
		return result
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		result.ExitCode = exitErr.ExitCode()
	} else {
		result.ExitCode = 127
		if len(output) == 0 {
			result.Output = []byte(err.Error())
		}
	}
	return result
}

func commandExists(name string) bool {
	_, err := exec.LookPath(name)
	return err == nil
}

func (s *Server) scheduleAgentRestart() {
	s.restartMu.Lock()
	defer s.restartMu.Unlock()
	s.reapRestartLocked()
	if s.sttTestActive || s.restartCommand != nil {
		s.restartDeferred = true
		s.restartReadinessPending = true
		return
	}
	s.launchRestartLocked()
}

func (s *Server) startDeferredRestartIfIdle() {
	s.restartMu.Lock()
	defer s.restartMu.Unlock()
	s.reapRestartLocked()
	if !s.sttTestActive && s.restartDeferred && s.restartCommand == nil {
		s.restartDeferred = false
		s.restartReadinessPending = true
		s.launchRestartLocked()
	}
}

func (s *Server) waitForAgentRestart(timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for {
		s.restartMu.Lock()
		s.reapRestartLocked()
		pending := s.restartCommand != nil
		s.restartMu.Unlock()
		if !pending {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("timed out waiting for agent restart")
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func (s *Server) agentRestartPending() bool {
	s.restartMu.Lock()
	defer s.restartMu.Unlock()
	s.reapRestartLocked()
	return s.restartReadinessPending || s.restartCommand != nil || s.restartDeferred
}

func (s *Server) clearAgentRestartReadiness() {
	s.restartMu.Lock()
	s.restartReadinessPending = false
	s.restartMu.Unlock()
}

func (s *Server) launchRestartLocked() {
	path := strings.TrimSpace(s.options.AgentInitScript)
	if path == "" {
		logConfigWebError("agent restart script path is empty")
		return
	}
	cmd := exec.Command(path, "restart")
	cmd.Stdin = nil
	cmd.Stdout = nil
	cmd.Stderr = nil
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		cmd = exec.Command("/bin/sh", path, "restart")
		cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
		if shellErr := cmd.Start(); shellErr != nil {
			logConfigWebError("launch agent restart: " + shellErr.Error())
			return
		}
	}
	s.restartCommand = cmd
	s.restartDone = make(chan error, 1)
	s.restartReadinessPending = true
	done := s.restartDone
	go func() { done <- cmd.Wait() }()
}

func (s *Server) reapRestartLocked() {
	if s.restartCommand == nil || s.restartDone == nil {
		return
	}
	select {
	case <-s.restartDone:
		s.restartCommand = nil
		s.restartDone = nil
	default:
	}
}

func logConfigWebError(message string) {
	fmt.Fprintf(os.Stderr, "[config_web] %s\n", message)
}

func mergeEnvironment(base []string, assignments []EnvAssignment) []string {
	values := make(map[string]string, len(base)+len(assignments))
	order := make([]string, 0, len(base)+len(assignments))
	for _, item := range base {
		key, _, ok := strings.Cut(item, "=")
		if !ok {
			continue
		}
		if _, exists := values[key]; !exists {
			order = append(order, key)
		}
		values[key] = item
	}
	for _, assignment := range assignments {
		if _, exists := values[assignment.Key]; !exists {
			order = append(order, assignment.Key)
		}
		values[assignment.Key] = assignment.Key + "=" + assignment.Value
	}
	result := make([]string, 0, len(order))
	for _, key := range order {
		result = append(result, values[key])
	}
	return result
}
