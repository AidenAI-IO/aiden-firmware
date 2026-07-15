package agent

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
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
	blank      bool

	mounted      bool
	prepares     int
	unmounts     int
	lazyUnmnts   int
	formats      int
	lastFormatFS string
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

func (f *fakeStorageOps) FormatDisk(fs string) (string, error) {
	f.formats++
	f.lastFormatFS = fs
	if f.formatErr != nil {
		return "", f.formatErr
	}
	// A freshly formatted card is no longer blank and mounts cleanly.
	f.blank = false
	f.prepareErr = nil
	return "/dev/mmcblk2p1", nil
}

func (f *fakeStorageOps) CardIsBlank() bool { return f.blank }

func newTestStorageManager(t *testing.T, ops *fakeStorageOps) *StorageManager {
	t.Helper()
	dir := t.TempDir()
	cfg := StorageConfig{MountPoint: filepath.Join(dir, "sdcard")}
	m := newStorageManagerWithOps(cfg, ops, filepath.Join(dir, "storage.state"), filepath.Join(dir, "emmc"), nil)
	return m
}

// tickN runs n poll cycles (debounce needs two to accept a presence change).
func tickN(m *StorageManager, n int) {
	for i := 0; i < n; i++ {
		m.tick()
	}
}

func TestDeriveEffectiveMode(t *testing.T) {
	if got := deriveEffectiveMode(false); got != StorageModeEMMCOnly {
		t.Fatalf("deriveEffectiveMode(false) = %d, want eMMC-only", got)
	}
	if got := deriveEffectiveMode(true); got != StorageModeDual {
		t.Fatalf("deriveEffectiveMode(true) = %d, want dual", got)
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

// waitForFormatJob polls until the async format job leaves the running state.
func waitForFormatJob(t *testing.T, m *StorageManager) StorageFormatJob {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		job := m.Status().FormatJob
		if job.Status != StorageFormatRunning && job.Status != StorageFormatIdle {
			return job
		}
		if time.Now().After(deadline) {
			t.Fatalf("format job did not finish: %+v", job)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestStorageManagerFormatRequiresConfirmation(t *testing.T) {
	ops := &fakeStorageOps{present: true, free: 1 << 30, total: 1 << 31, healthy: true}
	m := newTestStorageManager(t, ops)
	tickN(m, 2)

	if err := m.StartFormat(StorageFormatFAT32, "yes"); err == nil {
		t.Fatal("format without the confirm token succeeded")
	}
	if err := m.StartFormat("ntfs", StorageFormatConfirmToken); err == nil {
		t.Fatal("format with unsupported filesystem succeeded")
	}
	if ops.formats != 0 {
		t.Fatal("format executed without valid request")
	}

	if err := m.StartFormat("", StorageFormatConfirmToken); err != nil {
		t.Fatal(err)
	}
	job := waitForFormatJob(t, m)
	if job.Status != StorageFormatSuccess {
		t.Fatalf("job = %+v, want success", job)
	}
	if ops.lastFormatFS != StorageFormatFAT32 {
		t.Fatalf("format fs = %q, want fat32 default", ops.lastFormatFS)
	}
	if ops.formats != 1 {
		t.Fatalf("formats = %d, want 1", ops.formats)
	}
	if !m.Status().Card.Mounted {
		t.Fatal("card not remounted after format")
	}
}

func TestStorageManagerFormatExt4MountsAfterSuccess(t *testing.T) {
	ops := &fakeStorageOps{present: true, free: 1 << 30, total: 1 << 31, healthy: true}
	m := newTestStorageManager(t, ops)
	tickN(m, 2)

	if err := m.StartFormat(StorageFormatExt4, StorageFormatConfirmToken); err != nil {
		t.Fatal(err)
	}
	job := waitForFormatJob(t, m)
	if job.Status != StorageFormatSuccess {
		t.Fatalf("job = %+v, want success", job)
	}
	if ops.lastFormatFS != StorageFormatExt4 {
		t.Fatalf("format fs = %q, want ext4", ops.lastFormatFS)
	}
	if status := m.Status(); !status.Card.Mounted || status.EffectiveMode != StorageModeDual {
		t.Fatalf("status = %+v, want mounted dual storage after format", status)
	}
}

func TestStorageManagerFormatFailureReported(t *testing.T) {
	ops := &fakeStorageOps{present: true, free: 1 << 30, total: 1 << 31, healthy: true, formatErr: errors.New("card yanked")}
	m := newTestStorageManager(t, ops)
	tickN(m, 2)

	if err := m.StartFormat(StorageFormatFAT32, StorageFormatConfirmToken); err != nil {
		t.Fatal(err)
	}
	job := waitForFormatJob(t, m)
	if job.Status != StorageFormatFailed || !strings.Contains(job.Error, "card yanked") {
		t.Fatalf("job = %+v, want failure with reason", job)
	}
	if m.Status().Card.Mounted {
		t.Fatal("card mounted after failed format")
	}
}

func TestStorageManagerRejectsActionsDuringFormat(t *testing.T) {
	ops := &fakeStorageOps{present: true, free: 1 << 30, total: 1 << 31, healthy: true}
	m := newTestStorageManager(t, ops)
	tickN(m, 2)

	// Freeze the job in the running state without spawning the goroutine.
	m.mu.Lock()
	m.formatJob = StorageFormatJob{Status: StorageFormatRunning, FS: StorageFormatFAT32}
	m.mu.Unlock()

	if err := m.StartFormat(StorageFormatFAT32, StorageFormatConfirmToken); err == nil {
		t.Fatal("second format accepted while one is running")
	}
	if err := m.SafeEject(); err == nil {
		t.Fatal("eject accepted while formatting")
	}
	prepares := ops.prepares
	tickN(m, 3)
	if ops.prepares != prepares {
		t.Fatal("poll loop touched the card while formatting")
	}
}

func TestStorageManagerAutoFormatsBlankCard(t *testing.T) {
	ops := &fakeStorageOps{
		present: true, healthy: true, free: 1 << 30, total: 1 << 31,
		prepareErr: errors.New("mount: Invalid argument"), blank: true,
	}
	m := newTestStorageManager(t, ops)
	tickN(m, 2)

	job := waitForFormatJob(t, m)
	if job.Status != StorageFormatSuccess {
		t.Fatalf("auto-format job = %+v, want success", job)
	}
	if !job.Auto {
		t.Fatal("job not marked as auto-triggered")
	}
	if job.FS != StorageFormatFAT32 {
		t.Fatalf("auto-format fs = %q, want fat32", job.FS)
	}
	if ops.formats != 1 {
		t.Fatalf("formats = %d, want 1", ops.formats)
	}
	if status := m.Status(); !status.Card.Mounted || status.EffectiveMode != StorageModeDual {
		t.Fatalf("card not mounted after auto-format: %+v", status)
	}
}

func TestStorageManagerNeverAutoFormatsUnreadableCard(t *testing.T) {
	// A card that fails to mount but carries recognizable content (blank=false,
	// e.g. NTFS or an encrypted volume) must never be formatted.
	ops := &fakeStorageOps{
		present: true, healthy: true,
		prepareErr: errors.New("mount: Invalid argument"), blank: false,
	}
	m := newTestStorageManager(t, ops)
	tickN(m, 4)

	if ops.formats != 0 {
		t.Fatalf("formats = %d, want 0: unreadable card was auto-formatted", ops.formats)
	}
	status := m.Status()
	if status.Card.Mounted || status.EffectiveMode != StorageModeEMMCOnly {
		t.Fatalf("status = %+v, want rejected card on eMMC-only", status)
	}
	if !strings.Contains(status.Card.Reason, "Invalid argument") {
		t.Fatalf("reason = %q, want the mount failure", status.Card.Reason)
	}
}

func TestStorageManagerAutoFormatOncePerInsertion(t *testing.T) {
	ops := &fakeStorageOps{
		present: true, healthy: true,
		prepareErr: errors.New("no filesystem"), blank: true,
		formatErr: errors.New("card yanked"),
	}
	m := newTestStorageManager(t, ops)
	tickN(m, 2)
	job := waitForFormatJob(t, m)
	if job.Status != StorageFormatFailed {
		t.Fatalf("job = %+v, want failure", job)
	}
	tickN(m, 4)
	if ops.formats != 1 {
		t.Fatalf("formats = %d, want 1: failed auto-format must not retry in place", ops.formats)
	}

	// Removal and reinsertion earns a fresh attempt.
	ops.present = false
	tickN(m, 2)
	ops.present = true
	ops.formatErr = nil
	tickN(m, 2)
	job = waitForFormatJob(t, m)
	if job.Status != StorageFormatSuccess || ops.formats != 2 {
		t.Fatalf("job = %+v formats = %d, want success on reinsertion", job, ops.formats)
	}
}

func TestBuildMBRSector(t *testing.T) {
	const totalSectors = 62333952 // ~29.7 GiB card
	sector, err := buildMBRSector(totalSectors, 0x0C)
	if err != nil {
		t.Fatal(err)
	}
	if len(sector) != 512 {
		t.Fatalf("sector length = %d, want 512", len(sector))
	}
	if sector[510] != 0x55 || sector[511] != 0xAA {
		t.Fatal("missing MBR boot signature")
	}
	entry := sector[446:462]
	if entry[0] != 0x00 {
		t.Fatal("partition marked bootable")
	}
	if entry[4] != 0x0C {
		t.Fatalf("partition type = %#x, want 0x0C", entry[4])
	}
	start := uint32(entry[8]) | uint32(entry[9])<<8 | uint32(entry[10])<<16 | uint32(entry[11])<<24
	count := uint32(entry[12]) | uint32(entry[13])<<8 | uint32(entry[14])<<16 | uint32(entry[15])<<24
	if start != mbrPartitionStartLBA {
		t.Fatalf("partition start = %d, want %d", start, mbrPartitionStartLBA)
	}
	if uint64(count) != totalSectors-mbrPartitionStartLBA {
		t.Fatalf("partition sectors = %d, want %d", count, totalSectors-mbrPartitionStartLBA)
	}
	// Bytes 2-445 stay zero so no stale bootstrap code survives.
	for i := 0; i < 446; i++ {
		if sector[i] != 0 {
			t.Fatalf("bootstrap area byte %d = %#x, want 0", i, sector[i])
		}
	}

	if _, err := buildMBRSector(4096, 0x0C); err == nil {
		t.Fatal("tiny device accepted")
	}
	if _, err := mbrPartitionType("ntfs"); err == nil {
		t.Fatal("unsupported fs accepted for partition type")
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
