package agent

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"
)

// StorageMode selects where governed application data is written.
// See docs/04-agent/storage-modes.md for the full design.
type StorageMode int

const (
	// StorageModeAuto resolves to StorageModeDual when a usable card is mounted.
	StorageModeAuto StorageMode = 0
	// StorageModeEMMCOnly leaves the SD card unmounted and untouched.
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
// interoperability; ext4 is journaled and has on-device fsck support.
const (
	StorageFormatFAT32 = "fat32"
	StorageFormatExt4  = "ext4"
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

// storageStateFileName mirrors runtime state for external processes (cmd/ota).
const defaultStorageStatePath = "/run/aiden/storage.state"

const storagePreferenceFileName = "storage_mode.json"

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
	Status     string    `json:"status"` // idle | running | success | failed
	FS         string    `json:"fs,omitempty"`
	Error      string    `json:"error,omitempty"`
	StartedAt  time.Time `json:"started_at,omitempty"`
	FinishedAt time.Time `json:"finished_at,omitempty"`
}

// StorageStatus is the API-facing snapshot of the storage subsystem.
type StorageStatus struct {
	PreferredMode  StorageMode       `json:"preferred_mode"`
	EffectiveMode  StorageMode       `json:"effective_mode"`
	Card           StorageCardStatus `json:"card"`
	FallingBack    bool              `json:"falling_back"`
	FallbackReason string            `json:"fallback_reason,omitempty"`
	MountPoint     string            `json:"mount_point"`
	FormatJob      StorageFormatJob  `json:"format_job"`
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
}

// StorageManager owns SD card mounting, mode derivation, and per-class
// directory resolution. It is the only writer of the preference file and the
// runtime state mirror.
type StorageManager struct {
	cfg          StorageConfig
	logger       *Logger
	ops          storageSysOps
	prefPath     string // "" disables persistence
	statePath    string // "" disables the state mirror
	emmcRoot     string
	pollInterval time.Duration

	mu             sync.Mutex
	preferred      StorageMode
	card           StorageCardStatus
	lastEffective  StorageMode
	ejected        bool // safe-eject latch; cleared when the card is removed
	mountFailed    bool // avoid retrying a failing mount every tick
	fallingBack    bool
	fallbackReason string
	presenceRaw    bool
	presenceCount  int
	subscribers    []chan StorageEvent
	stateWriteErr  bool             // log the mirror-write failure only once
	formatJob      StorageFormatJob // async format task state

	stopOnce sync.Once
	stop     chan struct{}
}

// NewStorageManager builds a manager with real platform operations.
// configDir hosts the preference file; empty disables persistence.
func NewStorageManager(cfg StorageConfig, configDir string, logger *Logger) *StorageManager {
	prefPath := ""
	if configDir != "" {
		prefPath = filepath.Join(configDir, storagePreferenceFileName)
	}
	m := newStorageManagerWithOps(cfg, &realStorageOps{device: cfg.DeviceOrDefault()}, prefPath, defaultStorageStatePath, "/userdata", logger)
	return m
}

func newStorageManagerWithOps(cfg StorageConfig, ops storageSysOps, prefPath, statePath, emmcRoot string, logger *Logger) *StorageManager {
	m := &StorageManager{
		cfg:           cfg,
		logger:        logger,
		ops:           ops,
		prefPath:      prefPath,
		statePath:     statePath,
		emmcRoot:      emmcRoot,
		pollInterval:  2 * time.Second,
		lastEffective: StorageModeEMMCOnly,
		formatJob:     StorageFormatJob{Status: StorageFormatIdle},
		stop:          make(chan struct{}),
	}
	m.preferred = m.loadPreference()
	return m
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
func deriveEffectiveMode(preferred StorageMode, cardMounted bool) StorageMode {
	if !cardMounted {
		return StorageModeEMMCOnly
	}
	if preferred == StorageModeAuto {
		return StorageModeDual
	}
	return preferred
}

// EffectiveMode returns the currently derived mode.
func (m *StorageManager) EffectiveMode() StorageMode {
	m.mu.Lock()
	defer m.mu.Unlock()
	return deriveEffectiveMode(m.preferred, m.card.Mounted)
}

// Status returns an API-facing snapshot.
func (m *StorageManager) Status() StorageStatus {
	m.mu.Lock()
	defer m.mu.Unlock()
	return StorageStatus{
		PreferredMode:  m.preferred,
		EffectiveMode:  deriveEffectiveMode(m.preferred, m.card.Mounted),
		Card:           m.card,
		FallingBack:    m.fallingBack,
		FallbackReason: m.fallbackReason,
		MountPoint:     m.cfg.MountPointOrDefault(),
		FormatJob:      m.formatJob,
	}
}

// SetPreferredMode validates, persists, and applies a new preference.
// Any preference may be set at any time; the effective mode is re-derived
// from card availability, so preferring dual storage without a card simply
// runs eMMC-only until a card appears.
func (m *StorageManager) SetPreferredMode(mode StorageMode) error {
	if mode < StorageModeAuto || mode > StorageModeDual {
		return fmt.Errorf("invalid storage mode %d", mode)
	}
	m.mu.Lock()
	if m.formatJob.Status == StorageFormatRunning {
		m.mu.Unlock()
		return fmt.Errorf("cannot change mode while a format job is running")
	}
	m.preferred = mode
	if err := m.persistPreferenceLocked(); err != nil {
		m.mu.Unlock()
		return err
	}
	if mode == StorageModeEMMCOnly && m.card.Mounted {
		m.unmountLocked(false, "")
	}
	if mode != StorageModeEMMCOnly && m.card.Present && !m.card.Mounted {
		m.ejected = false
		m.mountFailed = false
		m.tryMountLocked()
	}
	m.finishTransitionLocked()
	m.mu.Unlock()
	return nil
}

// SafeEject syncs and unmounts the card so it can be pulled. The card is not
// remounted until it is physically removed and reinserted, or the preference
// is set again to a card-using mode.
func (m *StorageManager) SafeEject() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.formatJob.Status == StorageFormatRunning {
		return fmt.Errorf("cannot eject while a format job is running")
	}
	if !m.card.Mounted {
		return fmt.Errorf("no mounted card to eject")
	}
	m.unmountLocked(false, "")
	m.ejected = true
	m.finishTransitionLocked()
	return nil
}

// StartFormat launches the asynchronous format job: fresh MBR with one
// partition plus a new filesystem. Erases the entire card, including any
// partitions not created by this device. confirm must equal
// StorageFormatConfirmToken. Progress is polled via Status().FormatJob.
// After formatting, the card is mounted unless the preferred mode is
// eMMC-only, whose "card untouched" semantics win.
func (m *StorageManager) StartFormat(fs, confirm string) error {
	if confirm != StorageFormatConfirmToken {
		return fmt.Errorf("format not confirmed")
	}
	if fs == "" {
		fs = StorageFormatFAT32
	}
	if fs != StorageFormatFAT32 && fs != StorageFormatExt4 {
		return fmt.Errorf("unsupported filesystem %q (want %s or %s)", fs, StorageFormatFAT32, StorageFormatExt4)
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
		m.unmountLocked(false, "")
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
	if m.preferred != StorageModeEMMCOnly {
		m.tryMountLocked()
	} else {
		m.logf("[storage] format (%s) done; card left unmounted (eMMC-only mode)", fs)
	}
	m.finishTransitionLocked()
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
// written to, creating it if needed. In dual mode a failed SD write path
// falls back to eMMC, subject to the eMMC free-space reserve, and raises the
// falling-back flag surfaced via Status.
func (m *StorageManager) ResolveDir(class StorageDataClass) (string, error) {
	m.mu.Lock()
	mode := deriveEffectiveMode(m.preferred, m.card.Mounted)
	mountPoint := m.cfg.MountPointOrDefault()
	m.mu.Unlock()

	if mode == StorageModeDual {
		sdDir := m.sdClassDir(mountPoint, class)
		if err := os.MkdirAll(sdDir, 0o755); err == nil {
			m.setFallback(false, "")
			return sdDir, nil
		} else {
			m.logf("[storage] SD dir %s unavailable, falling back to eMMC: %v", sdDir, err)
			m.setFallback(true, err.Error())
		}
	}
	dir := m.emmcClassDir(class)
	if mode == StorageModeDual {
		// Fallback writes must not starve the system partition.
		if err := m.checkEMMCReserve(dir); err != nil {
			return "", err
		}
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("create %s: %w", dir, err)
	}
	return dir, nil
}

// ReadRoots returns every root that may hold data of the given class, SD
// first. Callers merge their views across all roots.
func (m *StorageManager) ReadRoots(class StorageDataClass) []string {
	m.mu.Lock()
	mounted := m.card.Mounted
	mountPoint := m.cfg.MountPointOrDefault()
	m.mu.Unlock()

	var roots []string
	if mounted {
		roots = append(roots, m.sdClassDir(mountPoint, class))
	}
	return append(roots, m.emmcClassDir(class))
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

func (m *StorageManager) checkEMMCReserve(dir string) error {
	probe := dir
	for {
		if _, err := os.Stat(probe); err == nil {
			break
		}
		parent := filepath.Dir(probe)
		if parent == probe {
			break
		}
		probe = parent
	}
	free, _, err := m.ops.SpaceInfo(probe)
	if err != nil {
		return nil // cannot measure; do not block writes on it
	}
	reserve := int64(m.cfg.EMMCReserveMBOrDefault()) * 1024 * 1024
	if free < reserve {
		return fmt.Errorf("eMMC free space %d bytes is below the %d MB reserve; refusing fallback write", free, m.cfg.EMMCReserveMBOrDefault())
	}
	return nil
}

func (m *StorageManager) setFallback(active bool, reason string) {
	m.mu.Lock()
	changed := m.fallingBack != active
	m.fallingBack = active
	m.fallbackReason = reason
	if changed {
		m.writeStateFileLocked()
	}
	m.mu.Unlock()
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
		if m.preferred != StorageModeEMMCOnly {
			m.tryMountLocked()
		}
	case !present && m.card.Present:
		if m.card.Mounted {
			m.unmountLocked(true, "card removed")
		}
		m.card = StorageCardStatus{}
		m.ejected = false
		m.mountFailed = false
	case present && m.card.Mounted:
		if !m.ops.Healthy(m.cfg.MountPointOrDefault()) {
			m.unmountLocked(true, "card unresponsive (removed or IO failure)")
		} else {
			m.refreshSpaceLocked()
		}
	case present && !m.card.Mounted:
		// Card sitting idle: mount only if the mode wants it and we are not
		// ejected and not stuck on a known-bad card.
		if m.preferred != StorageModeEMMCOnly && !m.ejected && !m.mountFailed {
			m.tryMountLocked()
		}
	}
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
		m.logf("[storage] card %s failed the usability gate: %v", dev, err)
		return
	}
	m.card.Mounted = true
	m.card.Reason = ""
	m.mountFailed = false
	m.refreshSpaceLocked()
	free := int64(m.cfg.MinCardFreeMBOrDefault()) * 1024 * 1024
	if m.card.FreeBytes < free {
		m.card.Reason = fmt.Sprintf("card has %d bytes free, below the %d MB minimum", m.card.FreeBytes, m.cfg.MinCardFreeMBOrDefault())
		m.unmountLocked(false, m.card.Reason)
		m.mountFailed = true
		m.logf("[storage] %s", m.card.Reason)
		return
	}
	m.logf("[storage] mounted %s at %s (%d bytes free)", dev, mountPoint, m.card.FreeBytes)
}

// unmountLocked detaches the card. Caller holds m.mu.
func (m *StorageManager) unmountLocked(lazy bool, reason string) {
	mountPoint := m.cfg.MountPointOrDefault()
	if err := m.ops.Unmount(mountPoint, lazy); err != nil {
		m.logf("[storage] unmount %s: %v", mountPoint, err)
	}
	m.card.Mounted = false
	m.card.TotalBytes = 0
	m.card.FreeBytes = 0
	if reason != "" {
		m.card.Reason = reason
		m.logf("[storage] card detached: %s", reason)
	}
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
	effective := deriveEffectiveMode(m.preferred, m.card.Mounted)
	if effective == m.lastEffective {
		m.writeStateFileLocked()
		return
	}
	m.lastEffective = effective
	if effective == StorageModeEMMCOnly {
		// Fallback status is meaningless once we are eMMC-only by mode.
		m.fallingBack = false
		m.fallbackReason = ""
	}
	m.logf("[storage] effective mode -> %d (preferred %d, card mounted %t)", effective, m.preferred, m.card.Mounted)
	for _, ch := range m.subscribers {
		select {
		case ch <- StorageEvent{EffectiveMode: effective}:
		default:
		}
	}
	m.writeStateFileLocked()
}

// loadPreference reads the persisted preference; corruption falls back to auto.
func (m *StorageManager) loadPreference() StorageMode {
	if m.prefPath == "" {
		return StorageModeAuto
	}
	data, err := os.ReadFile(m.prefPath)
	if err != nil {
		return StorageModeAuto
	}
	var stored struct {
		Preferred StorageMode `json:"preferred"`
	}
	if err := json.Unmarshal(data, &stored); err != nil || stored.Preferred < StorageModeAuto || stored.Preferred > StorageModeDual {
		m.logf("[storage] invalid preference file %s, using auto", m.prefPath)
		return StorageModeAuto
	}
	return stored.Preferred
}

// persistPreferenceLocked writes the preference atomically. Caller holds m.mu.
func (m *StorageManager) persistPreferenceLocked() error {
	if m.prefPath == "" {
		return nil
	}
	data, err := json.Marshal(struct {
		Preferred StorageMode `json:"preferred"`
	}{Preferred: m.preferred})
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(m.prefPath), 0o755); err != nil {
		return err
	}
	tmp := m.prefPath + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, m.prefPath)
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
	writeKV("PREFERRED_MODE", fmt.Sprintf("%d", m.preferred))
	writeKV("EFFECTIVE_MODE", fmt.Sprintf("%d", deriveEffectiveMode(m.preferred, m.card.Mounted)))
	writeKV("SD_TOTAL_BYTES", fmt.Sprintf("%d", m.card.TotalBytes))
	writeKV("SD_FREE_BYTES", fmt.Sprintf("%d", m.card.FreeBytes))
	writeKV("FALLING_BACK", bool01(m.fallingBack))
	writeKV("REASON", strings.ReplaceAll(m.card.Reason, "\n", " "))
	writeKV("FORMAT_STATUS", m.formatJob.Status)
	writeKV("FORMAT_FS", m.formatJob.FS)
	writeKV("FORMAT_ERROR", strings.ReplaceAll(m.formatJob.Error, "\n", " "))

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
	// Ask the kernel to re-read the partition table; retry while it still
	// considers the old partitions busy.
	var rrErr syscall.Errno
	for attempt := 0; attempt < 5; attempt++ {
		_, _, rrErr = syscall.Syscall(syscall.SYS_IOCTL, f.Fd(), blkrrpartIoctl, 0)
		if rrErr == 0 {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	f.Close()
	if rrErr != 0 {
		return "", fmt.Errorf("re-read partition table on %s: %v", base, rrErr)
	}

	part := base + "p1"
	if err := waitForPath(part, 10*time.Second); err != nil {
		return "", fmt.Errorf("partition %s did not appear: %w", part, err)
	}

	var cmd *exec.Cmd
	switch fs {
	case StorageFormatFAT32:
		// busybox mkfs.vfat creates FAT32 volumes.
		cmd = exec.Command("mkfs.vfat", "-n", storageVolumeLabel, part)
	case StorageFormatExt4:
		cmd = exec.Command("mkfs.ext4", "-F", "-L", storageVolumeLabel, part)
	}
	if out, err := cmd.CombinedOutput(); err != nil {
		return "", fmt.Errorf("%s %s: %v: %s", cmd.Path, part, err, strings.TrimSpace(string(out)))
	}
	return part, nil
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
