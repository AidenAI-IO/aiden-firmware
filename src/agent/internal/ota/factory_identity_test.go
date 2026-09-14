package ota

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const (
	factoryMachineIDA = "0123456789abcdef0123456789abcdef"
	factoryMachineIDB = "fedcba9876543210fedcba9876543210"
)

type factoryIdentityTestEnv struct {
	updater        *Updater
	activePath     string
	inactivePath   string
	persistentPath string
	runtimePath    string
	markerPath     string
	bootID         *string
}

func TestProvisionFactoryIdentityCreatesPersistentIDAndPersonalizesInactiveSlot(t *testing.T) {
	env := newFactoryIdentityTestEnv(t, factoryMachineIDA, "", "", factoryMachineIDA)

	result, err := env.updater.ProvisionFactoryIdentity()
	if err != nil {
		t.Fatalf("ProvisionFactoryIdentity() error = %v", err)
	}
	if result.MachineID != factoryMachineIDA || result.ActiveSlot != "a" || result.InactiveSlot != "b" {
		t.Fatalf("ProvisionFactoryIdentity() = %+v", result)
	}
	if !result.PersistentCreated || !result.InactivePersonalized || result.RebootRequired {
		t.Fatalf("ProvisionFactoryIdentity() flags = %+v", result)
	}
	assertMachineIDFile(t, env.persistentPath, factoryMachineIDA)
	assertExt4MachineID(t, env.activePath, factoryMachineIDA)
	assertExt4MachineID(t, env.inactivePath, factoryMachineIDA)
	marker, err := LoadFactoryIdentityMarker(env.markerPath)
	if err != nil {
		t.Fatalf("LoadFactoryIdentityMarker() error = %v", err)
	}
	if marker.MachineID != factoryMachineIDA || !marker.SlotsProvisioned || marker.RebootBootID != "" {
		t.Fatalf("factory identity marker = %+v", marker)
	}

	// Once factory provisioning is recorded, rollback boots must not inspect a
	// partially written or otherwise unusable inactive OTA slot.
	if err := os.WriteFile(env.inactivePath, []byte("interrupted inactive OTA write"), 0o600); err != nil {
		t.Fatalf("WriteFile(corrupt inactive) error = %v", err)
	}
	result, err = env.updater.ProvisionFactoryIdentity()
	if err != nil {
		t.Fatalf("ProvisionFactoryIdentity() with recorded factory marker error = %v", err)
	}
	if result.InactivePersonalized || result.RebootRequired {
		t.Fatalf("ProvisionFactoryIdentity() after marker = %+v", result)
	}
}

func TestProvisionFactoryIdentityStagesPersistentIDAndRequiresOneReboot(t *testing.T) {
	env := newFactoryIdentityTestEnv(t, factoryMachineIDA, "", factoryMachineIDB, factoryMachineIDA)
	env.updater.runCommand = func(name string, args ...string) (commandOutput, error) {
		if name == env.updater.config.MachineIDSetupPath {
			_, err := ensureExt4MachineID(
				env.activePath,
				factoryMachineIDB,
				DefaultDebugfsPath,
				DefaultE2fsckPath,
				runPersonalizationCommand,
			)
			return commandOutput{}, err
		}
		return runPersonalizationCommand(name, args...)
	}

	result, err := env.updater.ProvisionFactoryIdentity()
	if err != nil {
		t.Fatalf("ProvisionFactoryIdentity() error = %v", err)
	}
	if result.PersistentCreated || !result.InactivePersonalized || !result.RebootRequired {
		t.Fatalf("ProvisionFactoryIdentity() = %+v", result)
	}
	assertMachineIDFile(t, env.runtimePath, factoryMachineIDB)
	assertExt4MachineID(t, env.activePath, factoryMachineIDB)
	assertExt4MachineID(t, env.inactivePath, factoryMachineIDB)
	marker, err := LoadFactoryIdentityMarker(env.markerPath)
	if err != nil {
		t.Fatalf("LoadFactoryIdentityMarker() error = %v", err)
	}
	if marker.RebootBootID != *env.bootID {
		t.Fatalf("marker reboot_boot_id = %q, want %q", marker.RebootBootID, *env.bootID)
	}

	result, err = env.updater.ProvisionFactoryIdentity()
	if err != nil {
		t.Fatalf("ProvisionFactoryIdentity() same boot error = %v", err)
	}
	if !result.RebootRequired || result.InactivePersonalized {
		t.Fatalf("ProvisionFactoryIdentity() same boot = %+v", result)
	}

	*env.bootID = "22222222-2222-2222-2222-222222222222"
	result, err = env.updater.ProvisionFactoryIdentity()
	if err != nil {
		t.Fatalf("ProvisionFactoryIdentity() after reboot error = %v", err)
	}
	if result.RebootRequired || result.InactivePersonalized {
		t.Fatalf("ProvisionFactoryIdentity() after reboot = %+v", result)
	}
	marker, err = LoadFactoryIdentityMarker(env.markerPath)
	if err != nil {
		t.Fatalf("LoadFactoryIdentityMarker(after reboot) error = %v", err)
	}
	if marker.RebootBootID != "" {
		t.Fatalf("marker reboot_boot_id after reboot = %q", marker.RebootBootID)
	}
}

func TestProvisionFactoryIdentityRejectsSlotDisagreementBeforeWriting(t *testing.T) {
	env := newFactoryIdentityTestEnv(t, factoryMachineIDA, "", "", factoryMachineIDA)
	env.updater.currentRootSlot = func() (Slot, bool, error) { return SlotB, true, nil }

	_, err := env.updater.ProvisionFactoryIdentity()
	if err == nil || !strings.Contains(err.Error(), "does not match rootfs slot") {
		t.Fatalf("ProvisionFactoryIdentity() error = %v, want slot mismatch", err)
	}
	if _, statErr := os.Stat(env.persistentPath); !os.IsNotExist(statErr) {
		t.Fatalf("persistent machine-id was written before slot validation: %v", statErr)
	}
}

func TestFactoryIdentityMarkerRejectsTampering(t *testing.T) {
	path := filepath.Join(t.TempDir(), "factory-identity-v1.json")
	marker := FactoryIdentityMarker{
		MachineID:        factoryMachineIDA,
		SlotsProvisioned: true,
	}
	if err := SaveFactoryIdentityMarker(path, marker); err != nil {
		t.Fatalf("SaveFactoryIdentityMarker() error = %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile(marker) error = %v", err)
	}
	data = []byte(strings.Replace(string(data), factoryMachineIDA, factoryMachineIDB, 1))
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("WriteFile(tampered marker) error = %v", err)
	}
	if _, err := LoadFactoryIdentityMarker(path); err == nil || !strings.Contains(err.Error(), "checksum") {
		t.Fatalf("LoadFactoryIdentityMarker(tampered) error = %v, want checksum failure", err)
	}
}

func newFactoryIdentityTestEnv(t *testing.T, activeMachineID string, inactiveMachineID string, persistentMachineID string, runtimeMachineID string) factoryIdentityTestEnv {
	t.Helper()
	dir := t.TempDir()
	storageDir := filepath.Join(dir, "ota")
	blockDir := filepath.Join(dir, "blocks")
	if err := os.MkdirAll(storageDir, 0o755); err != nil {
		t.Fatalf("MkdirAll(storage) error = %v", err)
	}
	if err := os.MkdirAll(blockDir, 0o755); err != nil {
		t.Fatalf("MkdirAll(blocks) error = %v", err)
	}
	storageDevice := filepath.Join(dir, "ota-device")
	if err := os.WriteFile(storageDevice, nil, 0o600); err != nil {
		t.Fatalf("WriteFile(storage device) error = %v", err)
	}
	mountInfoPath := filepath.Join(dir, "mountinfo")
	mountInfo := "36 25 179:12 / " + storageDir + " rw,relatime - ext4 " + storageDevice + " rw\n"
	if err := os.WriteFile(mountInfoPath, []byte(mountInfo), 0o600); err != nil {
		t.Fatalf("WriteFile(mountinfo) error = %v", err)
	}

	activePath := filepath.Join(blockDir, "rootfs_a")
	inactivePath := filepath.Join(blockDir, "rootfs_b")
	copyTestFile(t, newGenericExt4RootFS(t, machineIDContents(activeMachineID)), activePath)
	copyTestFile(t, newGenericExt4RootFS(t, machineIDContents(inactiveMachineID)), inactivePath)
	runtimePath := filepath.Join(dir, "runtime-machine-id")
	// The production process runs as root and can update mode-0444
	// /etc/machine-id. Keep the test fixture owner-writable so the host test can
	// exercise the same in-place path without elevated privileges.
	if err := os.WriteFile(runtimePath, []byte(machineIDContents(runtimeMachineID)), 0o644); err != nil {
		t.Fatalf("WriteFile(runtime machine-id) error = %v", err)
	}
	persistentPath := filepath.Join(dir, "userdata", "system", "machine-id")
	if persistentMachineID != "" {
		if err := os.MkdirAll(filepath.Dir(persistentPath), 0o755); err != nil {
			t.Fatalf("MkdirAll(persistent machine-id) error = %v", err)
		}
		if err := os.WriteFile(persistentPath, []byte(machineIDContents(persistentMachineID)), 0o444); err != nil {
			t.Fatalf("WriteFile(persistent machine-id) error = %v", err)
		}
	}
	markerPath := filepath.Join(dir, "userdata", "debian", "ota", "factory-identity-v1.json")
	bootID := "11111111-1111-1111-1111-111111111111"
	config := UpdaterConfig{
		StateDir:             storageDir,
		DownloadDir:          filepath.Join(storageDir, "downloads"),
		UpdateLockPath:       filepath.Join(storageDir, DefaultOTAUpdateLockName),
		StorageMountPoint:    storageDir,
		StorageDevicePath:    storageDevice,
		StorageFilesystem:    "ext4",
		MountInfoPath:        mountInfoPath,
		BlockDir:             blockDir,
		DebianMode:           true,
		MachineIDPath:        persistentPath,
		RuntimeMachineIDPath: runtimePath,
		FactoryIdentityPath:  markerPath,
		MachineIDSetupPath:   "test-systemd-machine-id-setup",
	}
	updater, err := NewUpdater(config, nil)
	if err != nil {
		t.Fatalf("NewUpdater() error = %v", err)
	}
	updater.currentSlot = func() (Slot, bool, error) { return SlotA, true, nil }
	updater.currentRootSlot = func() (Slot, bool, error) { return SlotA, true, nil }
	updater.bootID = func() string { return bootID }
	return factoryIdentityTestEnv{
		updater:        updater,
		activePath:     activePath,
		inactivePath:   inactivePath,
		persistentPath: persistentPath,
		runtimePath:    runtimePath,
		markerPath:     markerPath,
		bootID:         &bootID,
	}
}

func machineIDContents(machineID string) string {
	if machineID == "" {
		return ""
	}
	return machineID + "\n"
}

func copyTestFile(t *testing.T, source string, destination string) {
	t.Helper()
	data, err := os.ReadFile(source)
	if err != nil {
		t.Fatalf("ReadFile(%s) error = %v", source, err)
	}
	if err := os.WriteFile(destination, data, 0o600); err != nil {
		t.Fatalf("WriteFile(%s) error = %v", destination, err)
	}
}

func assertMachineIDFile(t *testing.T, path string, want string) {
	t.Helper()
	got, err := readPersistentMachineID(path)
	if err != nil {
		t.Fatalf("readPersistentMachineID(%s) error = %v", path, err)
	}
	if got != want {
		t.Fatalf("machine-id %s = %q, want %q", path, got, want)
	}
}

func assertExt4MachineID(t *testing.T, path string, want string) {
	t.Helper()
	got, err := readExt4MachineID(path, DefaultDebugfsPath, runPersonalizationCommand)
	if err != nil {
		t.Fatalf("readExt4MachineID(%s) error = %v", path, err)
	}
	if got != want {
		t.Fatalf("ext4 machine-id %s = %q, want %q", path, got, want)
	}
}
