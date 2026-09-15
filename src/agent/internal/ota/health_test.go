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

func TestWriteHealthMarkerIfPendingRejectsRootSlotMismatch(t *testing.T) {
	dir := t.TempDir()
	pendingPath := filepath.Join(dir, "pending_boot.json")
	markerPath := filepath.Join(dir, "health.ok")
	pending := PendingBoot{TargetSlot: "b", TargetVersion: "v2", TargetBuildTime: "2026-05-21T12:00:00Z", Nonce: "nonce-1"}
	if err := WritePendingBoot(pendingPath, pending); err != nil {
		t.Fatalf("WritePendingBoot() error = %v", err)
	}

	wrote, err := writeHealthMarkerIfPending(
		pendingPath,
		markerPath,
		func() (Slot, bool, error) { return SlotB, true, nil },
		func() (Slot, bool, error) { return SlotA, true, nil },
		func() string { return "boot-1" },
	)
	if err == nil || !strings.Contains(err.Error(), "running rootfs slot a does not match pending target b") {
		t.Fatalf("writeHealthMarkerIfPending() error = %v, want rootfs mismatch", err)
	}
	if wrote {
		t.Fatal("writeHealthMarkerIfPending() wrote marker despite rootfs mismatch")
	}
	if _, err := os.Stat(markerPath); !os.IsNotExist(err) {
		t.Fatalf("health marker exists after rootfs mismatch: %v", err)
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

func TestUpdaterMarksHealthOnlyForMatchingPublicTransactionSlotAndBoot(t *testing.T) {
	env := newUpdaterTestEnv(t)
	pending := PendingBoot{
		TargetSlot:      "b",
		TargetVersion:   env.version,
		TargetBuildTime: env.buildTime,
		Nonce:           "0123456789abcdef",
	}
	env.state.Phase = "pending-reboot"
	env.state.TargetSlot = SlotB
	env.state.TargetVersion = pending.TargetVersion
	env.state.TargetBuildTime = pending.TargetBuildTime
	env.state.PendingBootNonce = pending.Nonce
	env.saveState(t)
	if err := WritePendingBoot(filepath.Join(env.stateDir, "pending_boot.json"), pending); err != nil {
		t.Fatalf("WritePendingBoot() error = %v", err)
	}
	updater := env.updater()
	updater.currentSlot = func() (Slot, bool, error) { return SlotB, true, nil }
	updater.currentRootSlot = func() (Slot, bool, error) { return SlotB, true, nil }
	updater.bootID = func() string { return "boot-transaction-1" }
	wrote, err := updater.MarkHealthIfPending()
	if err != nil {
		t.Fatalf("MarkHealthIfPending() error = %v", err)
	}
	if !wrote {
		t.Fatal("MarkHealthIfPending() did not write a marker")
	}
	if err := ValidateHealthMarker(filepath.Join(env.stateDir, "health.ok"), pending, "boot-transaction-1"); err != nil {
		t.Fatalf("ValidateHealthMarker() error = %v", err)
	}
}

func TestDebianHealthMarkerRejectsMismatchedPersonalizationTransaction(t *testing.T) {
	env := newUpdaterTestEnv(t)
	pending := PendingBoot{
		TargetSlot:      "b",
		TargetVersion:   env.version,
		TargetBuildTime: env.buildTime,
		Nonce:           "0123456789abcdef",
	}
	env.state.Phase = "pending-reboot"
	env.state.TargetSlot = SlotB
	env.state.TargetVersion = pending.TargetVersion
	env.state.TargetBuildTime = pending.TargetBuildTime
	env.state.PendingBootNonce = pending.Nonce
	env.saveState(t)
	if err := WritePendingBoot(filepath.Join(env.stateDir, "pending_boot.json"), pending); err != nil {
		t.Fatalf("WritePendingBoot() error = %v", err)
	}
	if err := DeleteStaleHealthMarker(filepath.Join(env.stateDir, "health.ok")); err != nil {
		t.Fatalf("DeleteStaleHealthMarker() error = %v", err)
	}
	machineIDPath := filepath.Join(t.TempDir(), "machine-id")
	runtimeMachineIDPath := filepath.Join(t.TempDir(), "machine-id")
	for _, path := range []string{machineIDPath, runtimeMachineIDPath} {
		if err := os.WriteFile(path, []byte("0123456789abcdef0123456789abcdef\n"), 0o444); err != nil {
			t.Fatalf("WriteFile(machine-id) error = %v", err)
		}
	}
	sidecarPath := filepath.Join(t.TempDir(), "personalization-v1.json")
	if err := SavePersonalizationSidecar(sidecarPath, PersonalizationSidecar{
		TransactionID:   "fedcba9876543210",
		TargetVersion:   pending.TargetVersion,
		TargetBuildTime: pending.TargetBuildTime,
		Slots: map[string]RootFSPersonalization{
			"b": {
				ArtifactSHA256:           testHashA,
				PersonalizationSchema:    PersonalizationSchemaVersion,
				EffectivePartitionSHA256: testHashB,
				HashedBytes:              4096,
			},
		},
	}); err != nil {
		t.Fatalf("SavePersonalizationSidecar() error = %v", err)
	}
	env.config.DebianMode = true
	env.config.MachineIDPath = machineIDPath
	env.config.RuntimeMachineIDPath = runtimeMachineIDPath
	env.config.PersonalizationPath = sidecarPath
	updater := env.updater()
	updater.currentSlot = func() (Slot, bool, error) { return SlotB, true, nil }
	updater.currentRootSlot = func() (Slot, bool, error) { return SlotB, true, nil }
	updater.bootID = func() string { return "boot-transaction-1" }
	wrote, err := updater.MarkHealthIfPending()
	if err == nil || !strings.Contains(err.Error(), "personalization transaction") {
		t.Fatalf("MarkHealthIfPending() error = %v, want transaction mismatch", err)
	}
	if wrote {
		t.Fatal("MarkHealthIfPending() wrote marker for mismatched sidecar")
	}
	if _, err := os.Stat(filepath.Join(env.stateDir, "health.ok")); !os.IsNotExist(err) {
		t.Fatalf("health marker exists after transaction mismatch: %v", err)
	}
}
