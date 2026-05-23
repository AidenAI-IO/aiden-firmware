package ota

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestHealthDeletesStaleMarkerAndValidatesPendingBoot(t *testing.T) {
	dir := t.TempDir()
	markerPath := filepath.Join(dir, "health.ok")
	if err := os.WriteFile(markerPath, []byte(`stale`), 0o644); err != nil {
		t.Fatalf("WriteFile(marker) error = %v", err)
	}
	if err := DeleteStaleHealthMarker(markerPath); err != nil {
		t.Fatalf("DeleteStaleHealthMarker() error = %v", err)
	}
	if _, err := os.Stat(markerPath); !os.IsNotExist(err) {
		t.Fatalf("marker still exists: %v", err)
	}

	pending := PendingBoot{TargetSlot: "b", TargetVersion: "v2", TargetBuildTime: "2026-05-21T12:00:00Z", Nonce: "nonce-1"}
	marker := HealthMarker{Slot: "b", Version: "v2", BuildTime: pending.TargetBuildTime, Nonce: "nonce-1", BootID: "boot-1"}
	encoded, _ := json.Marshal(marker)
	if err := os.WriteFile(markerPath, encoded, 0o644); err != nil {
		t.Fatalf("WriteFile(marker) error = %v", err)
	}
	if err := ValidateHealthMarker(markerPath, pending, "boot-1"); err != nil {
		t.Fatalf("ValidateHealthMarker() error = %v", err)
	}

	tests := []struct {
		name        string
		mutate      func(*HealthMarker)
		bootID      string
		wantMessage string
	}{
		{"slot", func(m *HealthMarker) { m.Slot = "a" }, "boot-1", "slot"},
		{"version", func(m *HealthMarker) { m.Version = "v1" }, "boot-1", "version"},
		{"nonce", func(m *HealthMarker) { m.Nonce = "wrong" }, "boot-1", "nonce"},
		{"boot_id", func(m *HealthMarker) {}, "boot-2", "boot_id"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			bad := marker
			tt.mutate(&bad)
			encoded, _ = json.Marshal(bad)
			if err := os.WriteFile(markerPath, encoded, 0o644); err != nil {
				t.Fatalf("WriteFile(marker) error = %v", err)
			}
			if err := ValidateHealthMarker(markerPath, pending, tt.bootID); err == nil || !strings.Contains(err.Error(), tt.wantMessage) {
				t.Fatalf("mismatch error = %v, want %q", err, tt.wantMessage)
			}
		})
	}
}

func TestHealthTimeoutCallsInjectedReboot(t *testing.T) {
	called := false
	err := WaitForHealth(context.Background(), filepath.Join(t.TempDir(), "missing.ok"), PendingBoot{TargetSlot: "b", TargetVersion: "v2", TargetBuildTime: "2026-05-21T12:00:00Z", Nonce: "n"}, "boot-1", time.Millisecond, time.Millisecond, func() error {
		called = true
		return nil
	})
	if err == nil || !strings.Contains(err.Error(), "health timeout") {
		t.Fatalf("WaitForHealth() error = %v", err)
	}
	if !called {
		t.Fatal("reboot function was not called")
	}
}

func TestWriteHealthMarkerUsesPendingBootAndCurrentBoot(t *testing.T) {
	dir := t.TempDir()
	markerPath := filepath.Join(dir, "health.ok")
	pending := PendingBoot{TargetSlot: "b", TargetVersion: "v2", TargetBuildTime: "2026-05-21T12:00:00Z", Nonce: "nonce-1"}

	if err := WriteHealthMarker(markerPath, pending, "b", "boot-1"); err != nil {
		t.Fatalf("WriteHealthMarker() error = %v", err)
	}
	if err := ValidateHealthMarker(markerPath, pending, "boot-1"); err != nil {
		t.Fatalf("ValidateHealthMarker() error = %v", err)
	}
}

func TestWriteHealthMarkerIfPendingIgnoresMissingPendingBoot(t *testing.T) {
	dir := t.TempDir()
	wrote, err := WriteHealthMarkerIfPending(filepath.Join(dir, "missing.json"), filepath.Join(dir, "health.ok"))
	if err != nil {
		t.Fatalf("WriteHealthMarkerIfPending() error = %v", err)
	}
	if wrote {
		t.Fatal("WriteHealthMarkerIfPending() wrote marker for missing pending boot")
	}
}

func TestHealthWaitRejectsNonPositiveInterval(t *testing.T) {
	for _, interval := range []time.Duration{0, -time.Millisecond} {
		called := false
		err := WaitForHealth(context.Background(), filepath.Join(t.TempDir(), "missing.ok"), PendingBoot{TargetSlot: "b", TargetVersion: "v2", TargetBuildTime: "2026-05-21T12:00:00Z", Nonce: "n"}, "boot-1", time.Millisecond, interval, func() error {
			called = true
			return nil
		})
		if err == nil || !strings.Contains(err.Error(), "interval") {
			t.Fatalf("interval %s error = %v", interval, err)
		}
		if called {
			t.Fatalf("reboot called for invalid interval %s", interval)
		}
	}
}
