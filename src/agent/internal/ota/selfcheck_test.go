package ota

import (
	"context"
	"testing"
	"time"
)

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
