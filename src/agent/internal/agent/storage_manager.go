package agent

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"
)

// StorageMode reports where governed application data is written. The mode
// is derived purely from card availability — a usable card means dual
// storage, no usable card means eMMC only. There is no user preference.
// See docs/04-agent/storage-modes.md for the full design.
type StorageMode int

const (
	// StorageModeEMMCOnly is the fallback when no usable card is mounted.
	StorageModeEMMCOnly StorageMode = 1
	// StorageModeDual writes governed data to SD first, falling back to eMMC.
	StorageModeDual StorageMode = 2
)

// StorageDataClass identifies a governed application-data category.
type StorageDataClass string

const (
	StorageClassAudio    StorageDataClass = "audio"
	StorageClassLogs     StorageDataClass = "logs"
	StorageClassOTACache StorageDataClass = "ota-cache"
)

// storageSDSubdir is the dedicated subtree on the card; the card's own
// content outside it is never touched.
const storageSDSubdir = "aiden"

// StorageFormatConfirmToken must be sent by clients to confirm a destructive format.
const StorageFormatConfirmToken = "format-sd-card"

// Filesystems the format job can create. FAT32 is the default for PC/phone
// interoperability; ext4 is journaled and has on-device fsck support; exFAT
// keeps PC/phone interoperability on large cards without FAT32's 4 GB
// file-size limit.
const (
	StorageFormatFAT32 = "fat32"
	StorageFormatExt4  = "ext4"
	StorageFormatExFAT = "exfat"
)

// Format job states surfaced through Status and the state mirror.
const (
	StorageFormatIdle    = "idle"
	StorageFormatRunning = "running"
	StorageFormatSuccess = "success"
	StorageFormatFailed  = "failed"
)

// storageVolumeLabel is stamped on freshly formatted cards.
const storageVolumeLabel = "AIDEN"

// Migration tuning. Only immutable, closed files are moved: the minimum age
// guards against touching a file that is still being written.
const (
	migrationMinFileAge    = 60 * time.Second
	migrationFilePause     = 100 * time.Millisecond // throttle between files
	migrationRetryCooldown = 10 * time.Minute       // after failed/exhausted runs
	migrationPartialSuffix = ".aiden-partial"       // in-flight copy marker on SD
)

// storageStateFileName mirrors runtime state for external processes (cmd/ota).
const defaultStorageStatePath = "/run/aiden/storage.state"

// StorageCardStatus describes the SD card as last observed.
type StorageCardStatus struct {
	Present    bool   `json:"present"`
	Mounted    bool   `json:"mounted"`
	Device     string `json:"device,omitempty"`
	TotalBytes int64  `json:"total_bytes,omitempty"`
	FreeBytes  int64  `json:"free_bytes,omitempty"`
	// Reason explains why a present card is not usable (mount/validation failure).
	Reason string `json:"reason,omitempty"`
}

// StorageFormatJob tracks the asynchronous card-format task. Formatting
// rewrites the whole card: a fresh MBR with one partition plus a new
// filesystem, so it runs as a background job with polled status.
type StorageFormatJob struct {
	Status string `json:"status"` // idle | running | success | failed
	FS     string `json:"fs,omitempty"`
	Error  string `json:"error,omitempty"`
	// Auto marks a job started by blank-card detection rather than a user.
	Auto       bool      `json:"auto,omitempty"`
	StartedAt  time.Time `json:"started_at,omitempty"`
	FinishedAt time.Time `json:"finished_at,omitempty"`
}

// StorageMigrationJob tracks the background eMMC→SD migration of older
// governed data, triggered by the eMMC free-space watermarks.
type StorageMigrationJob struct {
	Status string `json:"status"` // idle | running | success | failed
	// Detail is a human-readable summary of how the last run ended.
	Detail     string    `json:"detail,omitempty"`
	Error      string    `json:"error,omitempty"`
	MovedFiles int       `json:"moved_files,omitempty"`
	MovedBytes int64     `json:"moved_bytes,omitempty"`
	StartedAt  time.Time `json:"started_at,omitempty"`
	FinishedAt time.Time `json:"finished_at,omitempty"`
}

// StorageStatus is the API-facing snapshot of the storage subsystem.
type StorageStatus struct {
	EffectiveMode StorageMode         `json:"effective_mode"`
	Card          StorageCardStatus   `json:"card"`
	MountPoint    string              `json:"mount_point"`
	FormatJob     StorageFormatJob    `json:"format_job"`
	Migration     StorageMigrationJob `json:"migration"`
}

// StorageEvent notifies subscribers that the effective mode changed.
type StorageEvent struct {
	EffectiveMode StorageMode `json:"effective_mode"`
}

// storageSysOps abstracts the platform operations so the state machine is
// testable on the host.
type storageSysOps interface {
	// CardDevice reports the block device to use and whether the card is present.
	CardDevice() (dev string, present bool)
	// Prepare runs fsck, mounts dev at mountPoint read-write, and probe-writes.
	Prepare(dev, mountPoint string) error
	// Unmount detaches mountPoint; lazy detach is used for forced removal.
	Unmount(mountPoint string, lazy bool) error
	// IsMounted reports whether mountPoint currently has a filesystem mounted.
	IsMounted(mountPoint string) bool
	// Healthy reports whether the mounted filesystem still responds.
	Healthy(mountPoint string) bool
	// SpaceInfo returns free/total bytes for the filesystem containing path.
	SpaceInfo(path string) (free, total int64, err error)
	// FormatDisk rewrites the whole card: fresh MBR with one partition plus
	// a new filesystem of the given type. Destructive. Returns the partition
	// device node to mount.
	FormatDisk(fs string) (string, error)
	// CardIsBlank reports whether the card carries no recognizable content
	// at all: no kernel-visible partitions, no blkid signature (filesystem /
	// RAID / crypto / partition table), and no MBR/GPT magic in the first
	// sectors. Anything unverifiable must be reported as NOT blank.
	CardIsBlank() bool
}

// StorageManager owns SD card mounting, mode derivation, and per-class
// directory resolution. It is the only writer of the preference file and the
// runtime state mirror.
type StorageManager struct {
	cfg          StorageConfig
	logger       *Logger
	ops          storageSysOps
	statePath    string // "" disables the state mirror
	emmcRoot     string
	pollInterval time.Duration

	mu             sync.Mutex
	card           StorageCardStatus
	lastEffective  StorageMode
	ejected        bool // safe-eject latch; cleared when the card is removed
	mountFailed    bool // avoid retrying a failing mount every tick
	presenceRaw    bool
	presenceCount  int
	subscribers    []chan StorageEvent
	stateWriteErr  bool             // log the mirror-write failure only once
	formatJob      StorageFormatJob // async format task state
	autoFormatDone bool             // one auto-format attempt per insertion

	migration        StorageMigrationJob
	migrationCancel  chan struct{} // non-nil while a migration run is active
	migrationDone    chan struct{} // closed when the migration worker exits
	migrationRetryAt time.Time     // cooldown after a failed/exhausted run

	stopOnce sync.Once
	stop     chan struct{}
}

// storageEMMCRootEnv overrides the eMMC data root. This is a test hook used
// by tests/board/test_storage_migration.sh to point the watermark migrator
// at a small loop-mounted filesystem instead of the real 3 GB /userdata;
// production deployments never set it.
const storageEMMCRootEnv = "AIDEN_STORAGE_EMMC_ROOT"

// NewStorageManager builds a manager with real platform operations.
func NewStorageManager(cfg StorageConfig, logger *Logger) *StorageManager {
	emmcRoot := "/userdata"
	if override := strings.TrimSpace(os.Getenv(storageEMMCRootEnv)); override != "" {
		emmcRoot = override
		if logger != nil {
			logger.Warn("[storage] %s=%s overrides the eMMC data root (test hook; unset it for production use)",
				storageEMMCRootEnv, override)
		}
	}
	return newStorageManagerWithOps(cfg, &realStorageOps{device: cfg.DeviceOrDefault()}, defaultStorageStatePath, emmcRoot, logger)
}

func newStorageManagerWithOps(cfg StorageConfig, ops storageSysOps, statePath, emmcRoot string, logger *Logger) *StorageManager {
	return &StorageManager{
		cfg:           cfg,
		logger:        logger,
		ops:           ops,
		statePath:     statePath,
		emmcRoot:      emmcRoot,
		pollInterval:  2 * time.Second,
		lastEffective: StorageModeEMMCOnly,
		formatJob:     StorageFormatJob{Status: StorageFormatIdle},
		migration:     StorageMigrationJob{Status: StorageFormatIdle},
		stop:          make(chan struct{}),
	}
}

// Start launches the polling loop. Safe to call once.
func (m *StorageManager) Start() {
	go func() {
		m.tick()
		ticker := time.NewTicker(m.pollInterval)
		defer ticker.Stop()
		for {
			select {
			case <-m.stop:
				return
			case <-ticker.C:
				m.tick()
			}
		}
	}()
}

// Stop terminates the polling loop.
func (m *StorageManager) Stop() {
	m.stopOnce.Do(func() { close(m.stop) })
}

// deriveEffectiveMode is the single rule from which all boot and hot-plug
// behavior follows.
func deriveEffectiveMode(cardMounted bool) StorageMode {
	if !cardMounted {
		return StorageModeEMMCOnly
	}
	return StorageModeDual
}

// EffectiveMode returns the currently derived mode.
func (m *StorageManager) EffectiveMode() StorageMode {
	m.mu.Lock()
	defer m.mu.Unlock()
	return deriveEffectiveMode(m.card.Mounted)
}

// Status returns an API-facing snapshot.
func (m *StorageManager) Status() StorageStatus {
	m.mu.Lock()
	defer m.mu.Unlock()
	return StorageStatus{
		EffectiveMode: deriveEffectiveMode(m.card.Mounted),
		Card:          m.card,
		MountPoint:    m.cfg.MountPointOrDefault(),
		FormatJob:     m.formatJob,
		Migration:     m.migration,
	}
}

// SafeEject syncs and unmounts the card so it can be pulled. The card is not
// remounted until it is physically removed and reinserted.
func (m *StorageManager) SafeEject() error {
	// Ejecting invalidates the migration target; stop the worker first.
	if err := m.cancelMigrationAndWait("eject"); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.formatJob.Status == StorageFormatRunning {
		return fmt.Errorf("cannot eject while a format job is running")
	}
	if !m.card.Mounted {
		return fmt.Errorf("no mounted card to eject")
	}
	if err := m.unmountLocked(false, ""); err != nil {
		return fmt.Errorf("cannot eject: %w (close any shell or process using %s and retry)", err, m.cfg.MountPointOrDefault())
	}
	m.ejected = true
	m.finishTransitionLocked()
	return nil
}

// StartFormat launches the asynchronous format job: fresh MBR with one
// partition plus a new filesystem. Erases the entire card, including any
// partitions not created by this device. confirm must equal
// StorageFormatConfirmToken. Progress is polled via Status().FormatJob.
// After a successful format the card is mounted and dual storage resumes.
func (m *StorageManager) StartFormat(fs, confirm string) error {
	if confirm != StorageFormatConfirmToken {
		return fmt.Errorf("format not confirmed")
	}
	if fs == "" {
		fs = StorageFormatFAT32
	}
	if fs != StorageFormatFAT32 && fs != StorageFormatExt4 && fs != StorageFormatExFAT {
		return fmt.Errorf("unsupported filesystem %q (want %s, %s or %s)", fs, StorageFormatFAT32, StorageFormatExt4, StorageFormatExFAT)
	}
	// Formatting owns the card exclusively; stop a running migration first.
	if err := m.cancelMigrationAndWait("format"); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.formatJob.Status == StorageFormatRunning {
		return fmt.Errorf("a format job is already running")
	}
	if _, present := m.ops.CardDevice(); !present {
		return fmt.Errorf("no card present")
	}
	if m.card.Mounted {
		if err := m.unmountLocked(false, ""); err != nil {
			return fmt.Errorf("cannot format: %w (close any shell or process using %s and retry)", err, m.cfg.MountPointOrDefault())
		}
	}
	m.formatJob = StorageFormatJob{Status: StorageFormatRunning, FS: fs, StartedAt: time.Now()}
	m.writeStateFileLocked()
	go m.runFormatJob(fs)
	return nil
}

// runFormatJob executes the destructive format outside the manager lock so
// status polling stays responsive; the running-job flag keeps the poll loop
// and other operations away from the card meanwhile.
func (m *StorageManager) runFormatJob(fs string) {
	_, err := m.ops.FormatDisk(fs)

	m.mu.Lock()
	defer m.mu.Unlock()
	m.formatJob.FinishedAt = time.Now()
	if err != nil {
		m.formatJob.Status = StorageFormatFailed
		m.formatJob.Error = err.Error()
		m.card.Reason = fmt.Sprintf("format failed: %v", err)
		m.logf("[storage] format (%s) failed: %v", fs, err)
		m.finishTransitionLocked()
		return
	}
	m.formatJob.Status = StorageFormatSuccess
	m.card.Reason = ""
	m.ejected = false
	m.mountFailed = false
	m.tryMountLocked()
	m.finishTransitionLocked()
}

// ----- eMMC → SD migration --------------------------------------------------

// maybeStartMigrationLocked launches the background migration when a card is
// mounted and eMMC free space fell below the start watermark. Caller holds m.mu.
func (m *StorageManager) maybeStartMigrationLocked() {
	if !m.card.Mounted ||
		m.formatJob.Status == StorageFormatRunning ||
		m.migration.Status == StorageFormatRunning {
		return
	}
	if !m.migrationRetryAt.IsZero() && time.Now().Before(m.migrationRetryAt) {
		return
	}
	startPct, stopPct := m.cfg.MigrateWatermarksOrDefault()
	freePct, err := m.emmcFreePct()
	if err != nil {
		m.warnf("[storage] migration trigger: cannot read eMMC space at %s: %v", m.emmcRoot, err)
		return
	}
	if freePct >= float64(startPct) {
		return
	}
	m.migration = StorageMigrationJob{Status: StorageFormatRunning, StartedAt: time.Now()}
	m.migrationCancel = make(chan struct{})
	m.migrationDone = make(chan struct{})
	m.writeStateFileLocked()
	m.logf("[storage] eMMC free space %.1f%% is below the %d%% watermark; migrating older data to SD until %d%%",
		freePct, startPct, stopPct)
	go m.runMigration(m.migrationCancel, m.migrationDone, stopPct)
}

func (m *StorageManager) emmcFreePct() (float64, error) {
	free, total, err := m.ops.SpaceInfo(m.emmcRoot)
	if err != nil {
		return 0, err
	}
	if total <= 0 {
		return 0, fmt.Errorf("filesystem total size reported as %d", total)
	}
	return float64(free) / float64(total) * 100, nil
}

// requestMigrationCancelLocked signals the migration worker to stop after the
// current file. Caller holds m.mu.
func (m *StorageManager) requestMigrationCancelLocked(reason string) {
	if m.migrationCancel == nil {
		return
	}
	m.logf("[storage] canceling migration: %s", reason)
	close(m.migrationCancel)
	m.migrationCancel = nil
}

// cancelMigrationAndWait stops a running migration and waits for the worker
// to exit, so callers (format, eject) get exclusive card access. Must be
// called without m.mu held.
func (m *StorageManager) cancelMigrationAndWait(reason string) error {
	m.mu.Lock()
	m.requestMigrationCancelLocked(reason)
	done := m.migrationDone
	m.mu.Unlock()
	if done == nil {
		return nil
	}
	select {
	case <-done:
		return nil
	case <-time.After(15 * time.Second):
		return fmt.Errorf("migration is still stopping; retry %s shortly", reason)
	}
}

// runMigration moves the oldest migratable files from eMMC to the SD card
// until the stop watermark is reached, no eligible files remain, or the run
// is canceled. Every failure path logs with full context.
func (m *StorageManager) runMigration(cancel <-chan struct{}, done chan<- struct{}, stopPct int) {
	defer close(done)

	movedFiles := 0
	var movedBytes int64
	finish := func(status, detail, errText string, cooldown bool) {
		m.mu.Lock()
		m.migration.Status = status
		m.migration.Detail = detail
		m.migration.Error = errText
		m.migration.MovedFiles = movedFiles
		m.migration.MovedBytes = movedBytes
		m.migration.FinishedAt = time.Now()
		m.migrationCancel = nil
		m.migrationDone = nil
		if cooldown {
			m.migrationRetryAt = time.Now().Add(migrationRetryCooldown)
		}
		m.writeStateFileLocked()
		m.mu.Unlock()
		if errText != "" {
			m.warnf("[storage] migration finished: %s (%s); moved %d files / %d bytes", detail, errText, movedFiles, movedBytes)
		} else {
			m.logf("[storage] migration finished: %s; moved %d files / %d bytes", detail, movedFiles, movedBytes)
		}
	}

	srcDir := m.emmcClassDir(StorageClassAudio)
	m.mu.Lock()
	dstDir := m.sdClassDir(m.cfg.MountPointOrDefault(), StorageClassAudio)
	m.mu.Unlock()
	if err := os.MkdirAll(dstDir, 0o755); err != nil {
		finish(StorageFormatFailed, "cannot create SD target directory "+dstDir, err.Error(), true)
		return
	}
	m.cleanStalePartials(dstDir)

	// Snapshot the candidates once, oldest first. Files created after this
	// point are by definition the newest and are never migration targets in
	// this run.
	candidates, err := m.listMigratable(srcDir)
	if err != nil {
		finish(StorageFormatFailed, "cannot list "+srcDir, err.Error(), true)
		return
	}
	if len(candidates) == 0 {
		finish(StorageFormatSuccess,
			"no migratable files (only system data or files still in use occupy eMMC); free space remains below the watermark",
			"", true)
		return
	}

	failedFiles := 0
	for _, candidate := range candidates {
		select {
		case <-cancel:
			finish(StorageFormatSuccess, "canceled", "", false)
			return
		default:
		}

		freePct, err := m.emmcFreePct()
		if err != nil {
			finish(StorageFormatFailed, "cannot read eMMC space during migration", err.Error(), true)
			return
		}
		if freePct >= float64(stopPct) {
			finish(StorageFormatSuccess, fmt.Sprintf("reached the %d%% stop watermark", stopPct), "", false)
			return
		}
		m.mu.Lock()
		mounted := m.card.Mounted
		m.mu.Unlock()
		if !mounted {
			finish(StorageFormatFailed, "card no longer mounted", "migration aborted", true)
			return
		}

		size, err := m.migrateOneFile(filepath.Join(srcDir, candidate), filepath.Join(dstDir, candidate))
		if err != nil {
			failedFiles++
			m.warnf("[storage] migrate %s -> %s failed: %v", filepath.Join(srcDir, candidate), dstDir, err)
			continue // a single bad file must not stall the whole run
		}
		movedFiles++
		movedBytes += size
		m.logf("[storage] migrated %s (%d bytes) to SD", candidate, size)
		m.mu.Lock()
		m.migration.MovedFiles = movedFiles
		m.migration.MovedBytes = movedBytes
		m.writeStateFileLocked()
		m.mu.Unlock()

		// Throttle so recordings and system IO are not starved.
		select {
		case <-cancel:
			finish(StorageFormatSuccess, "canceled", "", false)
			return
		case <-time.After(migrationFilePause):
		}
	}

	// Candidates exhausted: re-check where that left us.
	detail := "all migratable files moved; free space remains below the stop watermark"
	cooldown := true
	if freePct, err := m.emmcFreePct(); err == nil && freePct >= float64(stopPct) {
		detail = fmt.Sprintf("reached the %d%% stop watermark", stopPct)
		cooldown = false
	}
	errText := ""
	status := StorageFormatSuccess
	if failedFiles > 0 {
		status = StorageFormatFailed
		errText = fmt.Sprintf("%d files failed to migrate", failedFiles)
		cooldown = true
	}
	finish(status, detail, errText, cooldown)
}

// listMigratable returns basenames of regular files in dir older than
// migrationMinFileAge, oldest first.
func (m *StorageManager) listMigratable(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	type fileAge struct {
		name    string
		modTime time.Time
	}
	cutoff := time.Now().Add(-migrationMinFileAge)
	var files []fileAge
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			m.warnf("[storage] migration: stat %s: %v", filepath.Join(dir, entry.Name()), err)
			continue
		}
		if !info.Mode().IsRegular() || info.ModTime().After(cutoff) {
			continue
		}
		files = append(files, fileAge{name: entry.Name(), modTime: info.ModTime()})
	}
	sort.Slice(files, func(i, j int) bool { return files[i].modTime.Before(files[j].modTime) })
	names := make([]string, len(files))
	for i, f := range files {
		names[i] = f.name
	}
	return names, nil
}

// migrateOneFile is the idempotent per-file transaction:
// copy to a .aiden-partial temp name → fsync → verify size → rename →
// delete source. A crash at any point leaves either both copies (resolved by
// the destination-exists check on the next run and by eMMC-first ReadRoots)
// or the migrated file only.
func (m *StorageManager) migrateOneFile(src, dst string) (int64, error) {
	info, err := os.Stat(src)
	if err != nil {
		return 0, fmt.Errorf("stat source: %w", err)
	}
	if dstInfo, err := os.Stat(dst); err == nil {
		if dstInfo.Size() == info.Size() {
			// Left over from an interrupted run: the copy completed but the
			// source was not deleted. Just finish the transaction.
			if err := os.Remove(src); err != nil {
				return 0, fmt.Errorf("destination already migrated but removing source failed: %w", err)
			}
			m.logf("[storage] migration: %s already on SD (same size); removed eMMC copy", filepath.Base(src))
			return info.Size(), nil
		}
		return 0, fmt.Errorf("destination exists with different size (src %d, dst %d); refusing to overwrite", info.Size(), dstInfo.Size())
	}

	partial := dst + migrationPartialSuffix
	if err := copyFileSync(src, partial); err != nil {
		if rmErr := os.Remove(partial); rmErr != nil && !os.IsNotExist(rmErr) {
			m.warnf("[storage] migration: cleanup of %s failed: %v", partial, rmErr)
		}
		return 0, err
	}
	partialInfo, err := os.Stat(partial)
	if err != nil {
		return 0, fmt.Errorf("stat copied file: %w", err)
	}
	if partialInfo.Size() != info.Size() {
		if rmErr := os.Remove(partial); rmErr != nil {
			m.warnf("[storage] migration: cleanup of %s failed: %v", partial, rmErr)
		}
		return 0, fmt.Errorf("size mismatch after copy (src %d, copy %d)", info.Size(), partialInfo.Size())
	}
	// The rename is the commit point: only then does the file appear under
	// its real name on the SD card.
	if err := os.Rename(partial, dst); err != nil {
		if rmErr := os.Remove(partial); rmErr != nil {
			m.warnf("[storage] migration: cleanup of %s failed: %v", partial, rmErr)
		}
		return 0, fmt.Errorf("commit rename: %w", err)
	}
	if err := os.Remove(src); err != nil {
		return 0, fmt.Errorf("copied to SD but removing eMMC source failed: %w", err)
	}
	return info.Size(), nil
}

// cleanStalePartials removes leftover in-flight copies from interrupted runs.
func (m *StorageManager) cleanStalePartials(dir string) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if !os.IsNotExist(err) {
			m.warnf("[storage] migration: cannot scan %s for stale partial files: %v", dir, err)
		}
		return
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), migrationPartialSuffix) {
			continue
		}
		path := filepath.Join(dir, entry.Name())
		if err := os.Remove(path); err != nil {
			m.warnf("[storage] migration: cannot remove stale partial %s: %v", path, err)
		} else {
			m.logf("[storage] migration: removed stale partial %s", path)
		}
	}
}

func copyFileSync(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return fmt.Errorf("open source: %w", err)
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
	if err != nil {
		return fmt.Errorf("create copy: %w", err)
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return fmt.Errorf("copy data: %w", err)
	}
	if err := out.Sync(); err != nil {
		out.Close()
		return fmt.Errorf("sync copy: %w", err)
	}
	if err := out.Close(); err != nil {
		return fmt.Errorf("close copy: %w", err)
	}
	return nil
}

// Subscribe returns a channel that receives an event whenever the effective
// mode changes. Events are dropped rather than blocking the manager.
func (m *StorageManager) Subscribe() <-chan StorageEvent {
	ch := make(chan StorageEvent, 4)
	m.mu.Lock()
	m.subscribers = append(m.subscribers, ch)
	m.mu.Unlock()
	return ch
}

// ResolveDir returns the directory new data of the given class should be
// written to, creating it if needed. New data always lands on eMMC (fast,
// reliable, unaffected by card removal); the background migrator moves
// older files to the SD card when eMMC runs low (see runMigration).
func (m *StorageManager) ResolveDir(class StorageDataClass) (string, error) {
	dir := m.emmcClassDir(class)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("create %s: %w", dir, err)
	}
	return dir, nil
}

// ReadRoots returns every root that may hold data of the given class, eMMC
// first. Callers merge their views across all roots; during the migration
// crash window a file may briefly exist on both stores, and the eMMC copy is
// authoritative until the source is deleted, so first-hit readers resolve
// duplicates correctly.
func (m *StorageManager) ReadRoots(class StorageDataClass) []string {
	m.mu.Lock()
	mounted := m.card.Mounted
	mountPoint := m.cfg.MountPointOrDefault()
	m.mu.Unlock()

	roots := []string{m.emmcClassDir(class)}
	if mounted {
		roots = append(roots, m.sdClassDir(mountPoint, class))
	}
	return roots
}

// CleanupRoots returns the roots whose contents are subject to the
// audio-archive retention limits. With a mounted card, eMMC space is managed
// by watermark migration, so the limits apply to the SD side only (the final
// eviction tier). Without a card the limits keep the eMMC store bounded as
// before — note that this can mass-evict files that migration had allowed to
// accumulate, which the archive cleanup logs.
func (m *StorageManager) CleanupRoots(class StorageDataClass) []string {
	m.mu.Lock()
	mounted := m.card.Mounted
	mountPoint := m.cfg.MountPointOrDefault()
	m.mu.Unlock()

	if mounted {
		return []string{m.sdClassDir(mountPoint, class)}
	}
	return []string{m.emmcClassDir(class)}
}

func (m *StorageManager) sdClassDir(mountPoint string, class StorageDataClass) string {
	return filepath.Join(mountPoint, storageSDSubdir, string(class))
}

func (m *StorageManager) emmcClassDir(class StorageDataClass) string {
	switch class {
	case StorageClassAudio:
		return filepath.Join(m.emmcRoot, "audio")
	case StorageClassLogs:
		return filepath.Join(m.emmcRoot, "agent", "log")
	case StorageClassOTACache:
		return filepath.Join(m.emmcRoot, "ota", "downloads")
	default:
		return filepath.Join(m.emmcRoot, string(class))
	}
}

// tick is one poll cycle: debounce presence, then reconcile mount state.
func (m *StorageManager) tick() {
	dev, present := m.ops.CardDevice()

	m.mu.Lock()
	defer m.mu.Unlock()

	// A running format owns the card exclusively; skip reconciliation.
	if m.formatJob.Status == StorageFormatRunning {
		return
	}

	// Debounce: require two consecutive observations before accepting a
	// presence change (contact bounce on physical slots is common).
	if present != m.card.Present {
		if present == m.presenceRaw {
			m.presenceCount++
		} else {
			m.presenceRaw = present
			m.presenceCount = 1
		}
		if m.presenceCount < 2 {
			return
		}
	} else {
		m.presenceRaw = present
		m.presenceCount = 0
	}

	switch {
	case present && !m.card.Present:
		m.card.Present = true
		m.card.Device = dev
		m.card.Reason = ""
		m.ejected = false
		m.mountFailed = false
		m.autoFormatDone = false
		m.tryMountLocked()
	case !present && m.card.Present:
		m.requestMigrationCancelLocked("card removed")
		if m.card.Mounted {
			m.unmountLocked(true, "card removed")
		}
		m.card = StorageCardStatus{}
		m.ejected = false
		m.mountFailed = false
		m.autoFormatDone = false
	case present && m.card.Mounted:
		if !m.ops.Healthy(m.cfg.MountPointOrDefault()) {
			m.requestMigrationCancelLocked("card unresponsive")
			m.unmountLocked(true, "card unresponsive (removed or IO failure)")
		} else {
			m.refreshSpaceLocked()
		}
	case present && !m.card.Mounted:
		// Card sitting idle: mount unless safe-ejected or known bad.
		if !m.ejected && !m.mountFailed {
			m.tryMountLocked()
		}
	}
	m.maybeStartMigrationLocked()
	m.finishTransitionLocked()
}

// tryMountLocked runs the usability gate and mounts. Caller holds m.mu.
func (m *StorageManager) tryMountLocked() {
	dev, present := m.ops.CardDevice()
	if !present {
		return
	}
	mountPoint := m.cfg.MountPointOrDefault()
	m.card.Present = true
	m.card.Device = dev
	if err := m.ops.Prepare(dev, mountPoint); err != nil {
		m.card.Mounted = false
		m.card.Reason = err.Error()
		m.mountFailed = true
		// A truly blank card (no partitions, no data signature of any kind)
		// is auto-formatted FAT32 and mounted. Cards with unreadable but
		// recognizable content are never touched — the user decides in the
		// portal. One attempt per insertion.
		if !m.autoFormatDone && m.formatJob.Status != StorageFormatRunning && m.ops.CardIsBlank() {
			m.autoFormatDone = true
			m.card.Reason = "blank card detected; formatting as FAT32"
			m.formatJob = StorageFormatJob{Status: StorageFormatRunning, FS: StorageFormatFAT32, Auto: true, StartedAt: time.Now()}
			m.writeStateFileLocked()
			m.logf("[storage] card %s is blank; auto-formatting as FAT32", dev)
			go m.runFormatJob(StorageFormatFAT32)
			return
		}
		m.logf("[storage] card %s failed the usability gate: %v", dev, err)
		return
	}
	m.card.Mounted = true
	m.card.Reason = ""
	m.mountFailed = false
	m.refreshSpaceLocked()
	free := int64(m.cfg.MinCardFreeMBOrDefault()) * 1024 * 1024
	if m.card.FreeBytes < free {
		reason := fmt.Sprintf("card has %d bytes free, below the %d MB minimum", m.card.FreeBytes, m.cfg.MinCardFreeMBOrDefault())
		// If the unmount fails the card simply stays mounted (and usable);
		// only the failed unmount keeps it from being rejected.
		_ = m.unmountLocked(false, reason)
		m.mountFailed = true
		m.logf("[storage] %s", reason)
		return
	}
	m.logf("[storage] mounted %s at %s (%d bytes free)", dev, mountPoint, m.card.FreeBytes)
}

// unmountLocked detaches the card. Caller holds m.mu.
//
// On a non-lazy unmount failure the card is STILL mounted (e.g. a shell has
// its cwd on the mount point), so the in-memory state keeps saying mounted
// and an error is returned — marking it unmounted here would desynchronize
// state from reality and send the poll loop into mount/EBUSY churn. Lazy
// unmounts are used when the card is physically gone; their failures cannot
// leave a working mount behind, so state is cleared regardless.
func (m *StorageManager) unmountLocked(lazy bool, reason string) error {
	mountPoint := m.cfg.MountPointOrDefault()
	if err := m.ops.Unmount(mountPoint, lazy); err != nil {
		if !lazy {
			m.card.Reason = fmt.Sprintf("unmount failed: %v", err)
			m.warnf("[storage] unmount %s failed; card stays mounted: %v", mountPoint, err)
			return err
		}
		m.logf("[storage] lazy unmount %s: %v", mountPoint, err)
	}
	m.card.Mounted = false
	m.card.TotalBytes = 0
	m.card.FreeBytes = 0
	if reason != "" {
		m.card.Reason = reason
		m.logf("[storage] card detached: %s", reason)
	}
	return nil
}

func (m *StorageManager) refreshSpaceLocked() {
	free, total, err := m.ops.SpaceInfo(m.cfg.MountPointOrDefault())
	if err == nil {
		m.card.FreeBytes = free
		m.card.TotalBytes = total
	}
}

// finishTransitionLocked re-derives the effective mode and, on change,
// notifies subscribers and refreshes the state mirror. Caller holds m.mu.
func (m *StorageManager) finishTransitionLocked() {
	effective := deriveEffectiveMode(m.card.Mounted)
	if effective == m.lastEffective {
		m.writeStateFileLocked()
		return
	}
	m.lastEffective = effective
	m.logf("[storage] effective mode -> %d (card mounted %t)", effective, m.card.Mounted)
	for _, ch := range m.subscribers {
		select {
		case ch <- StorageEvent{EffectiveMode: effective}:
		default:
		}
	}
	m.writeStateFileLocked()
}

// writeStateFileLocked mirrors state for external processes. Best effort:
// on hosts without the runtime dir it logs once and stays silent afterwards.
// Caller holds m.mu.
func (m *StorageManager) writeStateFileLocked() {
	if m.statePath == "" {
		return
	}
	var b strings.Builder
	writeKV := func(k, v string) { fmt.Fprintf(&b, "%s=%s\n", k, v) }
	bool01 := func(v bool) string {
		if v {
			return "1"
		}
		return "0"
	}
	writeKV("SD_PRESENT", bool01(m.card.Present))
	writeKV("SD_MOUNTED", bool01(m.card.Mounted))
	writeKV("SD_DEVICE", m.card.Device)
	writeKV("SD_MOUNTPOINT", m.cfg.MountPointOrDefault())
	writeKV("EFFECTIVE_MODE", fmt.Sprintf("%d", deriveEffectiveMode(m.card.Mounted)))
	writeKV("SD_TOTAL_BYTES", fmt.Sprintf("%d", m.card.TotalBytes))
	writeKV("SD_FREE_BYTES", fmt.Sprintf("%d", m.card.FreeBytes))
	writeKV("REASON", strings.ReplaceAll(m.card.Reason, "\n", " "))
	writeKV("FORMAT_STATUS", m.formatJob.Status)
	writeKV("FORMAT_FS", m.formatJob.FS)
	writeKV("FORMAT_AUTO", bool01(m.formatJob.Auto))
	writeKV("FORMAT_ERROR", strings.ReplaceAll(m.formatJob.Error, "\n", " "))
	writeKV("MIGRATE_STATUS", m.migration.Status)
	writeKV("MIGRATE_DETAIL", strings.ReplaceAll(m.migration.Detail, "\n", " "))
	writeKV("MIGRATE_ERROR", strings.ReplaceAll(m.migration.Error, "\n", " "))
	writeKV("MIGRATE_MOVED_FILES", fmt.Sprintf("%d", m.migration.MovedFiles))
	writeKV("MIGRATE_MOVED_BYTES", fmt.Sprintf("%d", m.migration.MovedBytes))

	tmp := m.statePath + ".tmp"
	err := os.MkdirAll(filepath.Dir(m.statePath), 0o755)
	if err == nil {
		if err = os.WriteFile(tmp, []byte(b.String()), 0o644); err == nil {
			err = os.Rename(tmp, m.statePath)
		}
	}
	if err != nil && !m.stateWriteErr {
		m.stateWriteErr = true
		m.logf("[storage] cannot mirror state to %s (logged once): %v", m.statePath, err)
	}
}

func (m *StorageManager) logf(format string, args ...interface{}) {
	if m.logger != nil {
		m.logger.Info(format, args...)
	}
}

func (m *StorageManager) warnf(format string, args ...interface{}) {
	if m.logger != nil {
		m.logger.Warn(format, args...)
	}
}

// realStorageOps implements storageSysOps against the device.
type realStorageOps struct {
	device string // block device base name, e.g. "mmcblk2"
}

// CardDevice checks /sys/block for the controller and prefers the first
// partition; cards formatted without a partition table use the whole device.
func (o *realStorageOps) CardDevice() (string, bool) {
	sysPath := filepath.Join("/sys/block", o.device)
	if _, err := os.Stat(sysPath); err != nil {
		return "", false
	}
	part := o.device + "p1"
	if _, err := os.Stat(filepath.Join(sysPath, part)); err == nil {
		return "/dev/" + part, true
	}
	return "/dev/" + o.device, true
}

// CardIsBlank reports whether the card carries no recognizable content.
// The check is deliberately conservative: any partition, any signature blkid
// recognizes (filesystem, RAID, crypto, partition table), any MBR/GPT magic,
// or any failure to verify means NOT blank — we only ever auto-format a card
// we could positively identify as empty.
func (o *realStorageOps) CardIsBlank() bool {
	sysPath := filepath.Join("/sys/block", o.device)
	if _, err := os.Stat(filepath.Join(sysPath, o.device+"p1")); err == nil {
		return false // kernel sees a partition table
	}
	dev := "/dev/" + o.device
	if out, err := exec.Command("blkid", dev).CombinedOutput(); err == nil && len(strings.TrimSpace(string(out))) > 0 {
		return false // blkid found a signature
	}
	// blkid found nothing (or is unavailable): double-check the raw MBR/GPT
	// signatures before declaring the card blank.
	f, err := os.Open(dev)
	if err != nil {
		return false
	}
	defer f.Close()
	buf := make([]byte, 1024)
	if _, err := io.ReadFull(f, buf); err != nil {
		return false
	}
	if buf[510] == 0x55 && buf[511] == 0xAA {
		return false // MBR / FAT boot sector signature
	}
	if string(buf[512:520]) == "EFI PART" {
		return false // GPT header at LBA 1
	}
	return true
}

// Prepare runs the usability gate: best-effort fsck, read-write mount, and a
// probe write. Never formats.
func (o *realStorageOps) Prepare(dev, mountPoint string) error {
	if err := os.MkdirAll(mountPoint, 0o755); err != nil {
		return fmt.Errorf("create mount point: %w", err)
	}
	// Adopt an existing mount (e.g. left behind by a previous agent run that
	// the watchdog restarted) instead of mounting on top of it — a second
	// mount of the same device fails with EBUSY. The probe write below still
	// validates the adopted mount.
	if o.IsMounted(mountPoint) {
		probe := filepath.Join(mountPoint, ".aiden-write-probe")
		if err := os.WriteFile(probe, []byte("ok"), 0o644); err != nil {
			return fmt.Errorf("existing mount at %s is not writable: %w", mountPoint, err)
		}
		_ = os.Remove(probe)
		return nil
	}
	// fsck is advisory: a missing or failing checker must not reject the
	// card; the mount plus probe write below are the authoritative gate.
	_ = exec.Command("fsck", "-p", dev).Run()
	// ext4 gets a tighter journal commit; other filesystems reject those
	// options, so retry with the portable set.
	if out, err := exec.Command("mount", "-o", "noatime,errors=remount-ro,commit=2", dev, mountPoint).CombinedOutput(); err != nil {
		if out2, err2 := exec.Command("mount", "-o", "noatime", dev, mountPoint).CombinedOutput(); err2 != nil {
			return fmt.Errorf("mount %s: %v: %s / %v: %s", dev, err, strings.TrimSpace(string(out)), err2, strings.TrimSpace(string(out2)))
		}
	}
	probe := filepath.Join(mountPoint, ".aiden-write-probe")
	if err := os.WriteFile(probe, []byte("ok"), 0o644); err != nil {
		_ = exec.Command("umount", mountPoint).Run()
		return fmt.Errorf("card is not writable: %w", err)
	}
	_ = os.Remove(probe)
	return nil
}

func (o *realStorageOps) Unmount(mountPoint string, lazy bool) error {
	if !o.IsMounted(mountPoint) {
		return nil
	}
	_ = exec.Command("sync").Run()
	args := []string{mountPoint}
	if lazy {
		args = []string{"-l", mountPoint}
	}
	if out, err := exec.Command("umount", args...).CombinedOutput(); err != nil {
		return fmt.Errorf("umount %s: %v: %s", mountPoint, err, strings.TrimSpace(string(out)))
	}
	return nil
}

func (o *realStorageOps) IsMounted(mountPoint string) bool {
	data, err := os.ReadFile("/proc/self/mounts")
	if err != nil {
		return false
	}
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) >= 2 && fields[1] == mountPoint {
			return true
		}
	}
	return false
}

func (o *realStorageOps) Healthy(mountPoint string) bool {
	if !o.IsMounted(mountPoint) {
		return false
	}
	var st syscall.Statfs_t
	return syscall.Statfs(mountPoint, &st) == nil
}

func (o *realStorageOps) SpaceInfo(path string) (int64, int64, error) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(path, &st); err != nil {
		return 0, 0, err
	}
	bsize := int64(st.Bsize)
	return int64(st.Bavail) * bsize, int64(st.Blocks) * bsize, nil
}

// mbrPartitionStartLBA aligns the single partition to 4 MB (SD erase blocks).
const mbrPartitionStartLBA = 8192

// blkrrpartIoctl asks the kernel to re-read the partition table (BLKRRPART).
const blkrrpartIoctl = 0x125F

// buildMBRSector builds a 512-byte MBR with one partition spanning the card
// from mbrPartitionStartLBA to the end. Pure function so the partition math
// is host-testable without touching a block device.
func buildMBRSector(totalSectors uint64, partType byte) ([]byte, error) {
	if totalSectors <= mbrPartitionStartLBA+2048 {
		return nil, fmt.Errorf("device too small: %d sectors", totalSectors)
	}
	numSectors := totalSectors - mbrPartitionStartLBA
	if numSectors > 0xFFFFFFFF {
		numSectors = 0xFFFFFFFF // MBR limit (~2 TiB); clamp rather than fail
	}
	sector := make([]byte, 512)
	entry := sector[446:462]
	entry[0] = 0x00                                 // not bootable
	entry[1], entry[2], entry[3] = 0xFE, 0xFF, 0xFF // CHS begin: LBA marker
	entry[4] = partType                             // filesystem type
	entry[5], entry[6], entry[7] = 0xFE, 0xFF, 0xFF // CHS end: LBA marker
	putLE32(entry[8:12], mbrPartitionStartLBA)      // first LBA
	putLE32(entry[12:16], uint32(numSectors))       // sector count
	sector[510], sector[511] = 0x55, 0xAA           // boot signature
	return sector, nil
}

func putLE32(b []byte, v uint32) {
	b[0] = byte(v)
	b[1] = byte(v >> 8)
	b[2] = byte(v >> 16)
	b[3] = byte(v >> 24)
}

// mbrPartitionType maps a format filesystem to its MBR partition type byte.
func mbrPartitionType(fs string) (byte, error) {
	switch fs {
	case StorageFormatFAT32:
		return 0x0C, nil // W95 FAT32 (LBA)
	case StorageFormatExt4:
		return 0x83, nil // Linux
	case StorageFormatExFAT:
		return 0x07, nil // HPFS/NTFS/exFAT
	default:
		return 0, fmt.Errorf("unsupported filesystem %q", fs)
	}
}

// FormatDisk rewrites the MBR with a single partition and creates the
// filesystem on it. Erases the entire card.
func (o *realStorageOps) FormatDisk(fs string) (string, error) {
	partType, err := mbrPartitionType(fs)
	if err != nil {
		return "", err
	}
	totalSectors, err := o.deviceSectors()
	if err != nil {
		return "", err
	}
	sector, err := buildMBRSector(totalSectors, partType)
	if err != nil {
		return "", err
	}

	base := "/dev/" + o.device

	// BLKRRPART fails EBUSY while ANY partition of the device is still held:
	// umount returning does not mean the kernel has released the bdev (journal
	// commits and page-cache writeback are asynchronous), and udev's blkid
	// probe may briefly open the partition after change events. Make sure no
	// partition is mounted anymore, flush, and give stragglers a moment.
	if err := o.waitNoMounts(5 * time.Second); err != nil {
		return "", err
	}
	_ = exec.Command("sync").Run()
	time.Sleep(300 * time.Millisecond)

	f, err := os.OpenFile(base, os.O_WRONLY, 0)
	if err != nil {
		return "", fmt.Errorf("open %s: %w", base, err)
	}
	if _, err := f.WriteAt(sector, 0); err != nil {
		f.Close()
		return "", fmt.Errorf("write MBR to %s: %w", base, err)
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return "", fmt.Errorf("sync %s: %w", base, err)
	}
	// Ask the kernel to re-read the partition table. Retry with backoff for
	// up to ~10 s: transient holders (writeback, udev blkid) normally clear
	// within a couple of seconds.
	var rrErr syscall.Errno
	delay := 200 * time.Millisecond
	deadline := time.Now().Add(10 * time.Second)
	for {
		_, _, rrErr = syscall.Syscall(syscall.SYS_IOCTL, f.Fd(), blkrrpartIoctl, 0)
		if rrErr == 0 || time.Now().After(deadline) {
			break
		}
		time.Sleep(delay)
		if delay < 2*time.Second {
			delay *= 2
		}
	}
	f.Close()
	if rrErr != 0 {
		return "", fmt.Errorf("re-read partition table on %s: %v (holders: %s)", base, rrErr, o.deviceHolders())
	}

	part := base + "p1"
	if err := waitForPath(part, 10*time.Second); err != nil {
		return "", fmt.Errorf("partition %s did not appear: %w", part, err)
	}

	var cmd *exec.Cmd
	switch fs {
	case StorageFormatFAT32:
		// mkfs.vfat from the SDK-bundled dosfstools creates FAT32 volumes.
		cmd = exec.Command("mkfs.vfat", "-n", storageVolumeLabel, part)
	case StorageFormatExt4:
		cmd = exec.Command("mkfs.ext4", "-F", "-L", storageVolumeLabel, part)
	case StorageFormatExFAT:
		// mkfs.exfat from the SDK-bundled exfatprogs.
		cmd = exec.Command("mkfs.exfat", "-L", storageVolumeLabel, part)
	}
	if out, err := cmd.CombinedOutput(); err != nil {
		return "", fmt.Errorf("%s %s: %v: %s", cmd.Path, part, err, strings.TrimSpace(string(out)))
	}
	return part, nil
}

// waitNoMounts blocks until no partition of the card appears in the mount
// table (a lazy umount detaches asynchronously), or fails naming the mount.
func (o *realStorageOps) waitNoMounts(timeout time.Duration) error {
	prefix := "/dev/" + o.device
	deadline := time.Now().Add(timeout)
	for {
		mounted := ""
		if data, err := os.ReadFile("/proc/self/mounts"); err == nil {
			for _, line := range strings.Split(string(data), "\n") {
				fields := strings.Fields(line)
				if len(fields) >= 2 && strings.HasPrefix(fields[0], prefix) {
					mounted = fields[0] + " at " + fields[1]
					break
				}
			}
		}
		if mounted == "" {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("cannot format: %s is still mounted", mounted)
		}
		time.Sleep(200 * time.Millisecond)
	}
}

// deviceHolders reports what still references the card when BLKRRPART fails,
// so the error message names the culprit instead of a bare EBUSY: lingering
// mounts of any partition, and processes holding the device nodes open.
func (o *realStorageOps) deviceHolders() string {
	var findings []string

	// Any partition of the card still mounted?
	if data, err := os.ReadFile("/proc/self/mounts"); err == nil {
		for _, line := range strings.Split(string(data), "\n") {
			fields := strings.Fields(line)
			if len(fields) >= 2 && strings.HasPrefix(fields[0], "/dev/"+o.device) {
				findings = append(findings, fmt.Sprintf("%s mounted at %s", fields[0], fields[1]))
			}
		}
	}

	// Any process with an open fd on the device or a partition? (busybox has
	// no fuser/lsof; scan /proc/*/fd directly.)
	prefix := "/dev/" + o.device
	if procs, err := os.ReadDir("/proc"); err == nil {
		for _, proc := range procs {
			pid := proc.Name()
			if pid[0] < '0' || pid[0] > '9' {
				continue
			}
			fdDir := filepath.Join("/proc", pid, "fd")
			fds, err := os.ReadDir(fdDir)
			if err != nil {
				continue
			}
			for _, fd := range fds {
				target, err := os.Readlink(filepath.Join(fdDir, fd.Name()))
				if err != nil || !strings.HasPrefix(target, prefix) {
					continue
				}
				comm, _ := os.ReadFile(filepath.Join("/proc", pid, "comm"))
				findings = append(findings, fmt.Sprintf("pid %s (%s) holds %s", pid, strings.TrimSpace(string(comm)), target))
				break
			}
		}
	}

	if len(findings) == 0 {
		return "none visible (transient kernel reference, e.g. writeback)"
	}
	return strings.Join(findings, "; ")
}

// deviceSectors reads the card size in 512-byte sectors from sysfs.
func (o *realStorageOps) deviceSectors() (uint64, error) {
	data, err := os.ReadFile(filepath.Join("/sys/block", o.device, "size"))
	if err != nil {
		return 0, fmt.Errorf("read device size: %w", err)
	}
	var sectors uint64
	if _, err := fmt.Sscanf(strings.TrimSpace(string(data)), "%d", &sectors); err != nil {
		return 0, fmt.Errorf("parse device size %q: %w", strings.TrimSpace(string(data)), err)
	}
	return sectors, nil
}

func waitForPath(path string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for {
		if _, err := os.Stat(path); err == nil {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("timed out after %s", timeout)
		}
		time.Sleep(200 * time.Millisecond)
	}
}
