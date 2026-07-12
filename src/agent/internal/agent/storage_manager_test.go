package agent

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type fakeStorageOps struct {
	present    bool
	dev        string
	prepareErr error
	healthy    bool
	free       int64
	total      int64
	spaceErr   error
	formatErr  error

	mounted    bool
	prepares   int
	unmounts   int
	lazyUnmnts int
	formats    int
}

func (f *fakeStorageOps) CardDevice() (string, bool) {
	if !f.present {
		return "", false
	}
	dev := f.dev
	if dev == "" {
		dev = "/dev/mmcblk2p1"
	}
	return dev, true
}

func (f *fakeStorageOps) Prepare(dev, mountPoint string) error {
	f.prepares++
	if f.prepareErr != nil {
		return f.prepareErr
	}
	f.mounted = true
	return nil
}

func (f *fakeStorageOps) Unmount(mountPoint string, lazy bool) error {
	f.unmounts++
	if lazy {
		f.lazyUnmnts++
	}
	f.mounted = false
	return nil
}

func (f *fakeStorageOps) IsMounted(mountPoint string) bool { return f.mounted }
func (f *fakeStorageOps) Healthy(mountPoint string) bool   { return f.healthy }

func (f *fakeStorageOps) SpaceInfo(path string) (int64, int64, error) {
	if f.spaceErr != nil {
		return 0, 0, f.spaceErr
	}
	return f.free, f.total, nil
}

func (f *fakeStorageOps) Format(dev string) error {
	f.formats++
	if f.formatErr != nil {
		return f.formatErr
	}
	return nil
}

func newTestStorageManager(t *testing.T, ops *fakeStorageOps) *StorageManager {
	t.Helper()
	dir := t.TempDir()
	cfg := StorageConfig{MountPoint: filepath.Join(dir, "sdcard")}
	m := newStorageManagerWithOps(cfg, ops, filepath.Join(dir, "storage_mode.json"), filepath.Join(dir, "storage.state"), filepath.Join(dir, "emmc"), nil)
	return m
}

// tickN runs n poll cycles (debounce needs two to accept a presence change).
func tickN(m *StorageManager, n int) {
	for i := 0; i < n; i++ {
		m.tick()
	}
}

func TestDeriveEffectiveMode(t *testing.T) {
	tests := []struct {
		name      string
		preferred StorageMode
		mounted   bool
		want      StorageMode
	}{
		{"auto without card", StorageModeAuto, false, StorageModeEMMCOnly},
		{"auto with card", StorageModeAuto, true, StorageModeDual},
		{"emmc without card", StorageModeEMMCOnly, false, StorageModeEMMCOnly},
		{"emmc with card", StorageModeEMMCOnly, true, StorageModeEMMCOnly},
		{"dual without card", StorageModeDual, false, StorageModeEMMCOnly},
		{"dual with card", StorageModeDual, true, StorageModeDual},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := deriveEffectiveMode(tt.preferred, tt.mounted); got != tt.want {
				t.Fatalf("deriveEffectiveMode(%d, %t) = %d, want %d", tt.preferred, tt.mounted, got, tt.want)
			}
		})
	}
}

func TestStorageManagerMountsOnInsertWithDebounce(t *testing.T) {
	ops := &fakeStorageOps{free: 1 << 30, total: 1 << 31, healthy: true}
	m := newTestStorageManager(t, ops)

	tickN(m, 2)
	if m.EffectiveMode() != StorageModeEMMCOnly {
		t.Fatalf("effective mode without card = %d, want eMMC-only", m.EffectiveMode())
	}

	ops.present = true
	m.tick()
	if got := m.Status(); got.Card.Present {
		t.Fatal("presence accepted after a single observation; debounce missing")
	}
	m.tick()
	status := m.Status()
	if !status.Card.Present || !status.Card.Mounted {
		t.Fatalf("card not mounted after debounced insert: %+v", status.Card)
	}
	if status.EffectiveMode != StorageModeDual {
		t.Fatalf("effective mode = %d, want dual", status.EffectiveMode)
	}
}

func TestStorageManagerRemovalFallsBackToEMMCOnly(t *testing.T) {
	ops := &fakeStorageOps{present: true, free: 1 << 30, total: 1 << 31, healthy: true}
	m := newTestStorageManager(t, ops)
	events := m.Subscribe()
	tickN(m, 2)
	if m.EffectiveMode() != StorageModeDual {
		t.Fatalf("setup: effective mode = %d, want dual", m.EffectiveMode())
	}
	<-events // insertion event

	ops.present = false
	tickN(m, 2)
	if m.EffectiveMode() != StorageModeEMMCOnly {
		t.Fatalf("effective mode after removal = %d, want eMMC-only", m.EffectiveMode())
	}
	if ops.lazyUnmnts != 1 {
		t.Fatalf("lazy unmounts = %d, want 1 (forced removal path)", ops.lazyUnmnts)
	}
	select {
	case ev := <-events:
		if ev.EffectiveMode != StorageModeEMMCOnly {
			t.Fatalf("event mode = %d, want eMMC-only", ev.EffectiveMode)
		}
	default:
		t.Fatal("no event delivered on removal")
	}
}

func TestStorageManagerUnhealthyMountTreatedAsRemoval(t *testing.T) {
	ops := &fakeStorageOps{present: true, free: 1 << 30, total: 1 << 31, healthy: true}
	m := newTestStorageManager(t, ops)
	tickN(m, 2)

	ops.healthy = false
	m.tick()
	status := m.Status()
	if status.Card.Mounted {
		t.Fatal("card still mounted after failed health check")
	}
	if status.EffectiveMode != StorageModeEMMCOnly {
		t.Fatalf("effective mode = %d, want eMMC-only", status.EffectiveMode)
	}
}

func TestStorageManagerPreferenceEMMCOnlyLeavesCardUnmounted(t *testing.T) {
	ops := &fakeStorageOps{present: true, free: 1 << 30, total: 1 << 31, healthy: true}
	m := newTestStorageManager(t, ops)
	if err := m.SetPreferredMode(StorageModeEMMCOnly); err != nil {
		t.Fatal(err)
	}
	tickN(m, 3)
	if ops.prepares != 0 {
		t.Fatalf("prepares = %d, want 0: eMMC-only mode must not touch the card", ops.prepares)
	}

	// Switching the preference back to auto mounts the card immediately.
	if err := m.SetPreferredMode(StorageModeAuto); err != nil {
		t.Fatal(err)
	}
	if m.EffectiveMode() != StorageModeDual {
		t.Fatalf("effective mode = %d, want dual after preference change", m.EffectiveMode())
	}
}

func TestStorageManagerPreferencePersistsAndReloads(t *testing.T) {
	ops := &fakeStorageOps{}
	m := newTestStorageManager(t, ops)
	if err := m.SetPreferredMode(StorageModeDual); err != nil {
		t.Fatal(err)
	}

	reloaded := newStorageManagerWithOps(m.cfg, ops, m.prefPath, m.statePath, m.emmcRoot, nil)
	if reloaded.Status().PreferredMode != StorageModeDual {
		t.Fatalf("reloaded preference = %d, want dual", reloaded.Status().PreferredMode)
	}
}

func TestStorageManagerCorruptPreferenceFallsBackToAuto(t *testing.T) {
	ops := &fakeStorageOps{}
	m := newTestStorageManager(t, ops)
	if err := os.WriteFile(m.prefPath, []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	reloaded := newStorageManagerWithOps(m.cfg, ops, m.prefPath, m.statePath, m.emmcRoot, nil)
	if reloaded.Status().PreferredMode != StorageModeAuto {
		t.Fatalf("preference from corrupt file = %d, want auto", reloaded.Status().PreferredMode)
	}
}

func TestStorageManagerRejectsInvalidMode(t *testing.T) {
	m := newTestStorageManager(t, &fakeStorageOps{})
	if err := m.SetPreferredMode(StorageMode(7)); err == nil {
		t.Fatal("SetPreferredMode(7) succeeded, want error")
	}
}

func TestStorageManagerSafeEjectLatch(t *testing.T) {
	ops := &fakeStorageOps{present: true, free: 1 << 30, total: 1 << 31, healthy: true}
	m := newTestStorageManager(t, ops)
	tickN(m, 2)
	if err := m.SafeEject(); err != nil {
		t.Fatal(err)
	}
	if m.Status().Card.Mounted {
		t.Fatal("card still mounted after eject")
	}

	// The card stays in the slot; further ticks must not remount it.
	prepares := ops.prepares
	tickN(m, 3)
	if ops.prepares != prepares {
		t.Fatal("card remounted while eject latch active")
	}

	// Physical removal clears the latch; reinsertion mounts again.
	ops.present = false
	tickN(m, 2)
	ops.present = true
	tickN(m, 2)
	if !m.Status().Card.Mounted {
		t.Fatal("card not remounted after removal and reinsertion")
	}
}

func TestStorageManagerMountFailureRecordsReasonAndDoesNotRetryEveryTick(t *testing.T) {
	ops := &fakeStorageOps{present: true, prepareErr: errors.New("bad superblock")}
	m := newTestStorageManager(t, ops)
	tickN(m, 4)
	status := m.Status()
	if status.Card.Mounted {
		t.Fatal("unusable card reported as mounted")
	}
	if !strings.Contains(status.Card.Reason, "bad superblock") {
		t.Fatalf("reason = %q, want the gate failure", status.Card.Reason)
	}
	if status.EffectiveMode != StorageModeEMMCOnly {
		t.Fatalf("effective mode = %d, want eMMC-only", status.EffectiveMode)
	}
	if ops.prepares != 1 {
		t.Fatalf("prepares = %d, want 1: failed mounts must not retry every tick", ops.prepares)
	}
}

func TestStorageManagerResolveDirDualWritesToSD(t *testing.T) {
	ops := &fakeStorageOps{present: true, free: 1 << 30, total: 1 << 31, healthy: true}
	m := newTestStorageManager(t, ops)
	tickN(m, 2)

	dir, err := m.ResolveDir(StorageClassAudio)
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(m.cfg.MountPointOrDefault(), storageSDSubdir, "audio")
	if dir != want {
		t.Fatalf("ResolveDir = %s, want %s", dir, want)
	}
	if _, err := os.Stat(dir); err != nil {
		t.Fatalf("resolved dir not created: %v", err)
	}
}

func TestStorageManagerResolveDirEMMCOnly(t *testing.T) {
	m := newTestStorageManager(t, &fakeStorageOps{})
	tickN(m, 2)
	dir, err := m.ResolveDir(StorageClassAudio)
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(m.emmcRoot, "audio"); dir != want {
		t.Fatalf("ResolveDir = %s, want %s", dir, want)
	}
}

func TestStorageManagerResolveDirFallbackRespectsReserve(t *testing.T) {
	ops := &fakeStorageOps{present: true, free: 1 << 30, total: 1 << 31, healthy: true}
	m := newTestStorageManager(t, ops)
	tickN(m, 2)

	// Make the SD class dir un-creatable: occupy the path with a file.
	sdDir := filepath.Join(m.cfg.MountPointOrDefault(), storageSDSubdir)
	if err := os.MkdirAll(filepath.Dir(sdDir), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(sdDir, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	dir, err := m.ResolveDir(StorageClassAudio)
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(m.emmcRoot, "audio"); dir != want {
		t.Fatalf("fallback dir = %s, want %s", dir, want)
	}
	if status := m.Status(); !status.FallingBack {
		t.Fatal("falling_back flag not raised on fallback")
	}

	// With eMMC below the reserve, the fallback write is refused.
	ops.free = 1 << 20 // 1 MB, below the 256 MB default reserve
	if _, err := m.ResolveDir(StorageClassAudio); err == nil {
		t.Fatal("fallback below eMMC reserve succeeded, want error")
	}
}

func TestStorageManagerReadRootsMergesSDFirst(t *testing.T) {
	ops := &fakeStorageOps{present: true, free: 1 << 30, total: 1 << 31, healthy: true}
	m := newTestStorageManager(t, ops)
	tickN(m, 2)

	roots := m.ReadRoots(StorageClassAudio)
	if len(roots) != 2 {
		t.Fatalf("roots = %v, want SD + eMMC", roots)
	}
	if roots[0] != filepath.Join(m.cfg.MountPointOrDefault(), storageSDSubdir, "audio") {
		t.Fatalf("first root = %s, want the SD dir", roots[0])
	}

	ops.present = false
	tickN(m, 2)
	roots = m.ReadRoots(StorageClassAudio)
	if len(roots) != 1 || roots[0] != filepath.Join(m.emmcRoot, "audio") {
		t.Fatalf("roots without card = %v, want only eMMC", roots)
	}
}

func TestStorageManagerFormatRequiresConfirmation(t *testing.T) {
	ops := &fakeStorageOps{present: true, free: 1 << 30, total: 1 << 31, healthy: true}
	m := newTestStorageManager(t, ops)
	tickN(m, 2)

	if err := m.Format("yes"); err == nil {
		t.Fatal("format without the confirm token succeeded")
	}
	if ops.formats != 0 {
		t.Fatal("format executed without confirmation")
	}
	if err := m.Format(StorageFormatConfirmToken); err != nil {
		t.Fatal(err)
	}
	if ops.formats != 1 {
		t.Fatalf("formats = %d, want 1", ops.formats)
	}
	if !m.Status().Card.Mounted {
		t.Fatal("card not remounted after format")
	}
}

func TestStorageManagerStateFileMirrorsStatus(t *testing.T) {
	ops := &fakeStorageOps{present: true, free: 1 << 30, total: 1 << 31, healthy: true}
	m := newTestStorageManager(t, ops)
	tickN(m, 2)

	data, err := os.ReadFile(m.statePath)
	if err != nil {
		t.Fatal(err)
	}
	content := string(data)
	for _, want := range []string{"SD_MOUNTED=1", "EFFECTIVE_MODE=2", "SD_MOUNTPOINT=" + m.cfg.MountPointOrDefault()} {
		if !strings.Contains(content, want) {
			t.Fatalf("state file missing %q:\n%s", want, content)
		}
	}
}
