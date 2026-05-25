package ota

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestStateAtomicWriteReadAndFactoryInitialization(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	state := NewFactoryState("factory-v1", "2026-05-21T10:00:00Z", uniformFactoryPartitionHashes("factory-hash"))
	state.ActiveSlot = SlotA
	if err := SaveState(path, state); err != nil {
		t.Fatalf("SaveState() error = %v", err)
	}
	loaded, err := LoadState(path)
	if err != nil {
		t.Fatalf("LoadState() error = %v", err)
	}
	for _, slot := range []Slot{SlotA, SlotB} {
		for _, part := range []string{"boot", "oem", "rootfs"} {
			name, err := slotName(slot)
			if err != nil {
				t.Fatalf("slotName(%v) error = %v", slot, err)
			}
			p := loaded.Slots[name].Partitions[part]
			if p.Version != "factory-v1" || p.Hash != "factory-hash" {
				t.Fatalf("slot %v part %s = %+v", slot, part, p)
			}
		}
	}
	if _, err := os.Stat(path + ".tmp"); !os.IsNotExist(err) {
		t.Fatalf("temporary state file remains: %v", err)
	}
}

func TestStateSaveReturnsDirectoryFsyncErrors(t *testing.T) {
	want := errors.New("fsync failed")
	original := syncDir
	syncDir = func(*os.File) error { return want }
	t.Cleanup(func() { syncDir = original })

	path := filepath.Join(t.TempDir(), "state.json")
	err := SaveState(path, NewFactoryState("factory", "2026-05-21T10:00:00Z", uniformFactoryPartitionHashes(testHashA)))
	if !errors.Is(err, want) {
		t.Fatalf("SaveState() error = %v, want %v", err, want)
	}
}

func TestStateRejectDowngradeByEqualVersionOrOlderBuildTime(t *testing.T) {
	state := NewFactoryState("20260521-100000-old", time.Date(2026, 5, 21, 10, 0, 0, 0, time.UTC).Format(time.RFC3339), uniformFactoryPartitionHashes(testHashA))
	if err := state.RejectDowngrade(Manifest{Version: state.LastCommittedVersion, BuildTime: state.LastCommittedBuildTime}); err != nil {
		t.Fatalf("exact committed manifest should be allowed for no-update path: %v", err)
	}
	if err := state.RejectDowngrade(Manifest{Version: state.LastCommittedVersion, BuildTime: "2026-05-21T11:00:00Z"}); err == nil || !strings.Contains(err.Error(), "same version") {
		t.Fatalf("equal version error = %v", err)
	}
	if err := state.RejectDowngrade(Manifest{Version: "20260521-090000-new", BuildTime: "2026-05-21T10:00:00Z"}); err == nil || !strings.Contains(err.Error(), "build_time") {
		t.Fatalf("older build_time error = %v", err)
	}
	if err := state.RejectDowngrade(Manifest{Version: "20260521-110000-new", BuildTime: "2026-05-21T11:00:00Z"}); err != nil {
		t.Fatalf("RejectDowngrade() error = %v", err)
	}
}

func TestStateSelectiveUpdateRequiresOmittedTargetPartitions(t *testing.T) {
	state := NewFactoryState("factory", "2026-05-21T10:00:00Z", uniformFactoryPartitionHashes(testHashA))
	manifest := Manifest{
		Version:   "20260521-120000-new",
		BuildTime: "2026-05-21T12:00:00Z",
		Parts: []ManifestPart{
			{Name: "boot", AssetA: &ManifestAsset{Name: "boot_a.img", Size: 1, SHA256: testHashB}, AssetB: &ManifestAsset{Name: "boot_b.img", Size: 1, SHA256: testHashB}, RequiresPartitions: []string{"oem=factory:" + testHashA, "rootfs=factory:" + testHashA}},
		},
	}
	if err := state.ValidateSelectiveUpdate(manifest, SlotB); err != nil {
		t.Fatalf("ValidateSelectiveUpdate() error = %v", err)
	}

	manifest.Parts[0].RequiresPartitions = []string{"oem=factory:" + testHashA}
	if err := state.ValidateSelectiveUpdate(manifest, SlotB); err == nil || !strings.Contains(err.Error(), "rootfs") {
		t.Fatalf("missing requirement error = %v", err)
	}

	manifest.Parts[0].RequiresPartitions = []string{"oem=factory:" + testHashA, "rootfs=other:" + testHashA}
	if err := state.ValidateSelectiveUpdate(manifest, SlotB); err == nil || !strings.Contains(err.Error(), "rootfs") {
		t.Fatalf("stale requirement error = %v", err)
	}
}

func TestStateCommitUpdateRecordsTargetSlotAndCommittedVersion(t *testing.T) {
	state := NewFactoryState("factory", "2026-05-21T10:00:00Z", uniformFactoryPartitionHashes(testHashA))
	manifest := Manifest{Version: "20260521-120000-new", BuildTime: "2026-05-21T12:00:00Z"}
	assets := map[string]ManifestAsset{"boot": {SHA256: testHashB}, "oem": {SHA256: testHashC}}
	if err := state.CommitUpdate(manifest, SlotB, assets); err != nil {
		t.Fatalf("CommitUpdate() error = %v", err)
	}

	if state.LastCommittedVersion != manifest.Version || state.LastCommittedBuildTime != manifest.BuildTime {
		t.Fatalf("committed = %q/%q", state.LastCommittedVersion, state.LastCommittedBuildTime)
	}
	if got := state.Slots["b"].Partitions["boot"]; got.Version != manifest.Version || got.Hash != testHashB {
		t.Fatalf("boot partition = %+v", got)
	}
	if got := state.Slots["b"].Partitions["rootfs"]; got.Version != "factory" || got.Hash != testHashA {
		t.Fatalf("omitted rootfs changed = %+v", got)
	}
}

func TestStateRejectsInvalidTargetSlot(t *testing.T) {
	state := NewFactoryState("factory", "2026-05-21T10:00:00Z", uniformFactoryPartitionHashes(testHashA))
	manifest := Manifest{Version: "v2", BuildTime: "2026-05-21T12:00:00Z", Parts: []ManifestPart{{Name: "boot"}}}

	if _, err := slotName(Slot(99)); err == nil || !strings.Contains(err.Error(), "invalid slot") {
		t.Fatalf("slotName invalid error = %v", err)
	}
	if err := state.ValidateSelectiveUpdate(manifest, Slot(99)); err == nil || !strings.Contains(err.Error(), "invalid slot") {
		t.Fatalf("ValidateSelectiveUpdate invalid slot error = %v", err)
	}
	if err := state.CommitUpdate(manifest, Slot(99), map[string]ManifestAsset{"boot": {SHA256: testHashB}}); err == nil || !strings.Contains(err.Error(), "invalid slot") {
		t.Fatalf("CommitUpdate invalid slot error = %v", err)
	}
}
