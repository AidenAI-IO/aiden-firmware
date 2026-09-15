package ota

import (
	"context"
	"os"
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
