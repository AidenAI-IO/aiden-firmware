package agent

import (
	"context"
	"encoding/json"
	"runtime"
	"strings"
	"testing"
	"time"
)

func skipOnWindows(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("test relies on POSIX sh")
	}
}

func TestShellToolForegroundEcho(t *testing.T) {
	skipOnWindows(t)
	tool := &ShellTool{}

	out, err := tool.Call(context.Background(), `{"command":"echo hello"}`)
	if err != nil {
		t.Fatalf("Call returned error: %v", err)
	}
	if strings.TrimRight(out, "\n") != "hello" {
		t.Fatalf("Call output = %q, want %q", out, "hello\n")
	}
}

func TestShellToolForegroundWorkdir(t *testing.T) {
	skipOnWindows(t)
	dir := t.TempDir()
	tool := &ShellTool{}

	payload := map[string]interface{}{
		"command": "pwd",
		"workdir": dir,
	}
	bs, _ := json.Marshal(payload)

	out, err := tool.Call(context.Background(), string(bs))
	if err != nil {
		t.Fatalf("Call returned error: %v", err)
	}

	got := strings.TrimRight(out, "\n")
	// macOS resolves /var to /private/var; accept both.
	if got != dir && got != "/private"+dir {
		t.Fatalf("Call output = %q, want %q", got, dir)
	}
}

func TestShellToolForegroundFailureSurfacesStderr(t *testing.T) {
	skipOnWindows(t)
	tool := &ShellTool{}

	out, err := tool.Call(context.Background(), `{"command":"sh -c 'echo boom 1>&2; exit 7'"}`)
	if err != nil {
		t.Fatalf("Call returned error: %v", err)
	}
	if !strings.HasPrefix(out, "error: Error: ") {
		t.Fatalf("expected error prefix, got %q", out)
	}
	if !strings.Contains(out, "Stderr:\nboom") {
		t.Fatalf("expected stderr in output, got %q", out)
	}
}

func TestShellToolForegroundTimeout(t *testing.T) {
	skipOnWindows(t)
	tool := &ShellTool{}

	out, err := tool.Call(context.Background(), `{"command":"sleep 2","timeout":0.2}`)
	if err != nil {
		t.Fatalf("Call returned error: %v", err)
	}
	if !strings.Contains(out, "timed out") {
		t.Fatalf("expected timeout message, got %q", out)
	}
}

func TestShellToolMissingCommand(t *testing.T) {
	tool := &ShellTool{}

	out, err := tool.Call(context.Background(), `{}`)
	if err != nil {
		t.Fatalf("Call returned error: %v", err)
	}
	if !strings.Contains(out, "Missing required parameter: command") {
		t.Fatalf("expected missing command error, got %q", out)
	}
}

func TestShellToolBackgroundLifecycle(t *testing.T) {
	skipOnWindows(t)
	tool := &ShellTool{}

	startOut, err := tool.Call(context.Background(), `{"action":"start","command":"sh -c 'for i in 1 2 3; do echo line-$i; done; sleep 5'"}`)
	if err != nil {
		t.Fatalf("start returned error: %v", err)
	}

	var started struct {
		SessionID string `json:"session_id"`
		Running   bool   `json:"running"`
		PID       int    `json:"pid"`
	}
	if err := json.Unmarshal([]byte(startOut), &started); err != nil {
		t.Fatalf("start output = %q, unmarshal err = %v", startOut, err)
	}
	if started.SessionID == "" {
		t.Fatalf("start output missing session_id: %q", startOut)
	}
	if !started.Running {
		t.Fatalf("expected running=true, got %q", startOut)
	}

	defer func() {
		stopArgs, _ := json.Marshal(map[string]interface{}{
			"action":     "stop",
			"session_id": started.SessionID,
		})
		_, _ = tool.Call(context.Background(), string(stopArgs))
	}()

	// Poll a few times until we see all three lines or run out of attempts.
	var combined string
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		pollArgs, _ := json.Marshal(map[string]interface{}{
			"action":     "poll",
			"session_id": started.SessionID,
		})
		pollOut, err := tool.Call(context.Background(), string(pollArgs))
		if err != nil {
			t.Fatalf("poll returned error: %v", err)
		}

		var poll struct {
			SessionID string `json:"session_id"`
			Running   bool   `json:"running"`
			Output    string `json:"output"`
		}
		if err := json.Unmarshal([]byte(pollOut), &poll); err != nil {
			t.Fatalf("poll output = %q, unmarshal err = %v", pollOut, err)
		}
		if poll.SessionID != started.SessionID {
			t.Fatalf("session_id mismatch: got %q want %q", poll.SessionID, started.SessionID)
		}
		combined += poll.Output
		if strings.Contains(combined, "line-1") &&
			strings.Contains(combined, "line-2") &&
			strings.Contains(combined, "line-3") {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}

	if !strings.Contains(combined, "line-1") ||
		!strings.Contains(combined, "line-2") ||
		!strings.Contains(combined, "line-3") {
		t.Fatalf("expected all three lines, got %q", combined)
	}

	stopArgs, _ := json.Marshal(map[string]interface{}{
		"action":     "stop",
		"session_id": started.SessionID,
	})
	stopOut, err := tool.Call(context.Background(), string(stopArgs))
	if err != nil {
		t.Fatalf("stop returned error: %v", err)
	}

	var stopped struct {
		Stopped bool `json:"stopped"`
	}
	if err := json.Unmarshal([]byte(stopOut), &stopped); err != nil {
		t.Fatalf("stop output = %q, unmarshal err = %v", stopOut, err)
	}
	if !stopped.Stopped {
		t.Fatalf("stop output not marked stopped: %q", stopOut)
	}

	// Polling a stopped session should report it gone.
	pollArgs, _ := json.Marshal(map[string]interface{}{
		"action":     "poll",
		"session_id": started.SessionID,
	})
	missingOut, err := tool.Call(context.Background(), string(pollArgs))
	if err != nil {
		t.Fatalf("post-stop poll returned error: %v", err)
	}
	if !strings.Contains(missingOut, "shell session not found") {
		t.Fatalf("expected session-not-found error, got %q", missingOut)
	}
}

func TestShellToolBackgroundSubmitInput(t *testing.T) {
	skipOnWindows(t)
	tool := &ShellTool{}

	startOut, err := tool.Call(context.Background(), `{"action":"start","command":"cat"}`)
	if err != nil {
		t.Fatalf("start returned error: %v", err)
	}
	var started struct {
		SessionID string `json:"session_id"`
	}
	if err := json.Unmarshal([]byte(startOut), &started); err != nil {
		t.Fatalf("start unmarshal err: %v", err)
	}
	defer func() {
		stopArgs, _ := json.Marshal(map[string]interface{}{
			"action":     "stop",
			"session_id": started.SessionID,
		})
		_, _ = tool.Call(context.Background(), string(stopArgs))
	}()

	submitArgs, _ := json.Marshal(map[string]interface{}{
		"action":     "submit",
		"session_id": started.SessionID,
		"input":      "ping",
	})
	if _, err := tool.Call(context.Background(), string(submitArgs)); err != nil {
		t.Fatalf("submit returned error: %v", err)
	}

	pollArgs, _ := json.Marshal(map[string]interface{}{
		"action":     "poll",
		"session_id": started.SessionID,
	})
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		pollOut, err := tool.Call(context.Background(), string(pollArgs))
		if err != nil {
			t.Fatalf("poll returned error: %v", err)
		}
		var poll struct {
			Output string `json:"output"`
		}
		if err := json.Unmarshal([]byte(pollOut), &poll); err != nil {
			t.Fatalf("poll unmarshal err: %v", err)
		}
		if strings.Contains(poll.Output, "ping") {
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("did not see %q echoed back from cat session", "ping")
}

func TestShellToolUnknownAction(t *testing.T) {
	tool := &ShellTool{}

	out, err := tool.Call(context.Background(), `{"action":"bogus"}`)
	if err != nil {
		t.Fatalf("Call returned error: %v", err)
	}
	if out != "error: invalid action: bogus" {
		t.Fatalf("expected invalid-action error, got %q", out)
	}
}

func TestShellRingBufferTruncates(t *testing.T) {
	rb := newShellRingBuffer(8)
	rb.write([]byte("abcdefgh"))
	rb.write([]byte("ij"))

	out, truncated := rb.readUnread(100)
	if out != "cdefghij" {
		t.Fatalf("readUnread = %q, want %q", out, "cdefghij")
	}
	if !truncated {
		t.Fatalf("expected truncated=true after overflow")
	}
}

func TestShellRingBufferHasUnread(t *testing.T) {
	rb := newShellRingBuffer(16)
	if rb.hasUnread() {
		t.Fatalf("hasUnread() = true on empty buffer")
	}
	rb.write([]byte("abcdef"))
	if !rb.hasUnread() {
		t.Fatalf("hasUnread() = false after write")
	}
	if _, _ = rb.readUnread(3); !rb.hasUnread() {
		t.Fatalf("hasUnread() = false with bytes still pending")
	}
	if _, _ = rb.readUnread(100); rb.hasUnread() {
		t.Fatalf("hasUnread() = true after full drain")
	}
}

func TestShellToolBackgroundPollDrainsBeforeDelete(t *testing.T) {
	skipOnWindows(t)
	tool := &ShellTool{}

	// Produce > shellDefaultPollBytes of output and exit, so the first
	// bounded poll cannot drain everything.
	startOut, err := tool.Call(context.Background(), `{"action":"start","command":"sh -c 'printf %20000s X'"}`)
	if err != nil {
		t.Fatalf("start returned error: %v", err)
	}
	var started struct {
		SessionID string `json:"session_id"`
	}
	if err := json.Unmarshal([]byte(startOut), &started); err != nil {
		t.Fatalf("start unmarshal err: %v", err)
	}

	pollArgs, _ := json.Marshal(map[string]interface{}{
		"action":     "poll",
		"session_id": started.SessionID,
	})

	// Wait for the process to exit, then poll once. Output should be capped
	// to the default poll size; session must still be reachable for follow-up
	// polls because there is more buffered data.
	deadline := time.Now().Add(2 * time.Second)
	var pollOut string
	for time.Now().Before(deadline) {
		pollOut, err = tool.Call(context.Background(), string(pollArgs))
		if err != nil {
			t.Fatalf("poll returned error: %v", err)
		}
		var poll struct {
			Running bool `json:"running"`
		}
		if err := json.Unmarshal([]byte(pollOut), &poll); err != nil {
			t.Fatalf("poll unmarshal err: %v", err)
		}
		if !poll.Running {
			break
		}
		time.Sleep(25 * time.Millisecond)
	}

	// Drain remaining output across follow-up polls. The first poll already
	// happened in the wait loop above. We accumulate until the session is
	// gone, then assert the full payload was delivered.
	totalBytes := 0
	firstPollSawTruncation := false
	for i := 0; i < 10; i++ {
		if strings.Contains(pollOut, "shell session not found") {
			break
		}
		var poll struct {
			SessionID       string `json:"session_id"`
			Running         bool   `json:"running"`
			Output          string `json:"output"`
			OutputTruncated bool   `json:"output_truncated"`
		}
		if err := json.Unmarshal([]byte(pollOut), &poll); err != nil {
			t.Fatalf("poll unmarshal err: %v (raw=%q)", err, pollOut)
		}
		if i == 0 && poll.OutputTruncated {
			firstPollSawTruncation = true
		}
		totalBytes += len(poll.Output)
		pollOut, err = tool.Call(context.Background(), string(pollArgs))
		if err != nil {
			t.Fatalf("poll returned error: %v", err)
		}
	}

	if !firstPollSawTruncation {
		t.Fatalf("expected first post-exit poll to be truncated; output may have fit in one poll")
	}
	if totalBytes < 20000 {
		t.Fatalf("totalBytes = %d, want >= 20000 (drain lost data)", totalBytes)
	}
	if !strings.Contains(pollOut, "shell session not found") {
		t.Fatalf("expected session-not-found after drain, got %q", pollOut)
	}
}

func TestShellKeySequence(t *testing.T) {
	cases := map[string]string{
		"enter":  "\n",
		"tab":    "\t",
		"ctrl+c": "\x03",
		"up":     "\x1b[A",
	}
	for key, want := range cases {
		got, err := shellKeySequence(key)
		if err != nil {
			t.Fatalf("shellKeySequence(%q) error: %v", key, err)
		}
		if got != want {
			t.Fatalf("shellKeySequence(%q) = %q, want %q", key, got, want)
		}
	}
	if _, err := shellKeySequence("nope"); err == nil {
		t.Fatalf("expected error for unknown key")
	}
}
