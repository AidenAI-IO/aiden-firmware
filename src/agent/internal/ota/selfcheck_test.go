package ota

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func TestSelfCheckTimeoutDoesNotPreventLaterProbes(t *testing.T) {
	dir := t.TempDir()
	slow := filepath.Join(dir, "slow")
	fast := filepath.Join(dir, "fast")
	if err := os.WriteFile(slow, []byte("#!/bin/sh\nexec sleep 30\n"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(fast, []byte("#!/bin/sh\nprintf '%s\\n' '{\"connected\":true}'\n"), 0700); err != nil {
		t.Fatal(err)
	}
	// macOS charges a one-time cost for the first execution of a newly created
	// file. Pay it here so the probe budget below measures probe isolation
	// rather than the platform's first-exec latency.
	warmExecutable(t, fast)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	report := RunSelfCheck(ctx, SelfCheckConfig{
		CommandTimeout: 200 * time.Millisecond,
		FrameCLI:       slow, AudioCLI: fast, Curl: fast,
		BLESocketPath: filepath.Join(dir, "missing.sock"),
	})
	if report.Items["frame_service"].Status != "fail" {
		t.Fatalf("slow required probe = %+v", report.Items["frame_service"])
	}
	for _, name := range []string{"audio_service", "agent_http", "phone_bridge"} {
		if report.Items[name].Status != "pass" {
			t.Fatalf("probe after timeout %s = %+v", name, report.Items[name])
		}
	}
	if ctx.Err() != nil {
		t.Fatalf("probe did not respect its own timeout: %v", ctx.Err())
	}
}

// warmExecutable executes a freshly written script once so later runs of the
// same path are not charged the platform's first-exec cost.
func warmExecutable(t *testing.T, path string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if out, err := exec.CommandContext(ctx, path).CombinedOutput(); err != nil {
		t.Fatalf("warm %s error = %v output=%s", path, err, out)
	}
}

func TestSelfCheckCommandsHonorParentCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := runSelfCheckCommand(ctx, time.Hour, "sh", "-c", "exit 0"); err == nil {
		t.Fatal("command ran despite parent cancellation")
	}
}

func TestSelfCheckReportCountsRequiredFailures(t *testing.T) {
	report := RunSelfCheck(context.Background(), SelfCheckConfig{
		CommandTimeout: 10 * time.Millisecond,
		RequiredUDC:    true,
	})
	if report.Items == nil || report.FinishedAt.IsZero() {
		t.Fatalf("invalid report: %+v", report)
	}
	if report.Passed+report.Warnings+report.Failures != len(report.Items) {
		t.Fatalf("counts do not match items: %+v", report)
	}
	if _, ok := report.Items["agent_http"]; !ok {
		t.Fatal("agent_http check missing")
	}
}

func TestSelfCheckTreatsAgentFailureAsWarningButRequiresConfigWeb(t *testing.T) {
	dir := t.TempDir()
	agentFailureCurl := filepath.Join(dir, "curl-agent-failure")
	const agentFailureScript = `#!/bin/sh
case "$*" in
  *127.0.0.1:8080/health) exit 1 ;;
  *phone-bridge/status) printf '%s\n' '{"connected":false}' ;;
esac
exit 0
`
	if err := os.WriteFile(agentFailureCurl, []byte(agentFailureScript), 0700); err != nil {
		t.Fatal(err)
	}

	report := RunSelfCheck(context.Background(), SelfCheckConfig{
		CommandTimeout: 100 * time.Millisecond,
		Curl:           agentFailureCurl,
		BLESocketPath:  filepath.Join(dir, "missing.sock"),
	})
	if got := report.Items["agent_http"].Status; got != "warn" {
		t.Fatalf("agent_http status = %q, want warn", got)
	}
	if got := report.Items["config_web"].Status; got != "pass" {
		t.Fatalf("config_web status after Agent failure = %q, want pass", got)
	}

	configFailureCurl := filepath.Join(dir, "curl-config-failure")
	if err := os.WriteFile(configFailureCurl, []byte("#!/bin/sh\nexit 1\n"), 0700); err != nil {
		t.Fatal(err)
	}
	report = RunSelfCheck(context.Background(), SelfCheckConfig{
		CommandTimeout: 100 * time.Millisecond,
		Curl:           configFailureCurl,
		BLESocketPath:  filepath.Join(dir, "missing.sock"),
	})
	if got := report.Items["config_web"].Status; got != "fail" {
		t.Fatalf("config_web status = %q, want fail", got)
	}
}

func TestSafeSnapshotNameRemovesPathSeparators(t *testing.T) {
	if got := safeSnapshotName("v1/../../release"); got != "v1_.._.._release" {
		t.Fatalf("safeSnapshotName() = %q", got)
	}
}

func TestClassifyPhoneBridgeStatusTreatsDisconnectedAsWarning(t *testing.T) {
	status, detail := classifyPhoneBridgeStatus(`{"connected":false}`, false)
	if status != "warn" || detail != "Phone Bridge is not connected" {
		t.Fatalf("classifyPhoneBridgeStatus() = %q, %q", status, detail)
	}
}

func TestClassifyPhoneBridgeStatusTreatsRequiredDisconnectedAsFailure(t *testing.T) {
	status, _ := classifyPhoneBridgeStatus(`{"connected":false}`, true)
	if status != "fail" {
		t.Fatalf("classifyPhoneBridgeStatus(required) = %q, want fail", status)
	}
}
