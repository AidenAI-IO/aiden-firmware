package agent

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"
)

const storageMegabyte = uint64(1024 * 1024)

// StorageLevel is the final storage alert level after remediation.
type StorageLevel string

const (
	StorageLevelNormal    StorageLevel = "normal"
	StorageLevelWarning   StorageLevel = "warning"
	StorageLevelCritical  StorageLevel = "critical"
	StorageLevelEmergency StorageLevel = "emergency"
)

type CheckReason string

const (
	CheckReasonStartup  CheckReason = "startup"
	CheckReasonPeriodic CheckReason = "periodic"
	CheckReasonWrite    CheckReason = "write_failure"
	CheckReasonManual   CheckReason = "manual"
)

type StorageCapability string

const (
	StorageCapabilityLLMHTTPLog         StorageCapability = "llm_http_log"
	StorageCapabilityAudioArchive       StorageCapability = "audio_archive"
	StorageCapabilitySessionArchive     StorageCapability = "session_archive"
	StorageCapabilitySessionPersistence StorageCapability = "session_persistence"
)

// StorageSample is a point-in-time filesystem sample.
type StorageSample struct {
	TotalBytes     uint64
	AvailableBytes uint64
}

// StorageSampler samples a storage path. Tests can inject deterministic samples.
type StorageSampler interface {
	Sample(ctx context.Context, path string) (StorageSample, error)
}

type statfsStorageSampler struct{}

func (statfsStorageSampler) Sample(_ context.Context, path string) (StorageSample, error) {
	var stat syscall.Statfs_t
	if err := syscall.Statfs(path, &stat); err != nil {
		return StorageSample{}, fmt.Errorf("statfs %q: %w", path, err)
	}
	return StorageSample{
		TotalBytes:     stat.Blocks * uint64(stat.Bsize),
		AvailableBytes: stat.Bavail * uint64(stat.Bsize),
	}, nil
}

type StorageCleanupResult struct {
	Timestamp  time.Time `json:"timestamp"`
	Cleaner    string    `json:"cleaner"`
	FreedBytes uint64    `json:"freed_bytes"`
	Status     string    `json:"status"`
	Error      string    `json:"error,omitempty"`
}

// StorageStatus is a consistent snapshot of the final state after remediation.
type StorageStatus struct {
	Path                     string                 `json:"path"`
	TotalBytes               uint64                 `json:"total_bytes"`
	UsedBytes                uint64                 `json:"used_bytes"`
	AvailableBytes           uint64                 `json:"available_bytes"`
	PercentUsed              float64                `json:"percent_used"`
	Level                    StorageLevel           `json:"alert_level"`
	UnavailableCapabilities  []StorageCapability    `json:"unavailable_capabilities"`
	Revision                 uint64                 `json:"status_revision"`
	LastCleanupAt            time.Time              `json:"last_cleanup,omitempty"`
	LastCleanupFreedBytes    uint64                 `json:"last_cleanup_freed_bytes"`
	CleanupHistory           []StorageCleanupResult `json:"cleanup_history,omitempty"`
	LastCleanupResults       []StorageCleanupResult `json:"-"`
	CurrentCleanupFreedBytes uint64                 `json:"-"`
	CheckedAt                time.Time              `json:"checked_at"`
}

type StorageCheckRequest struct {
	Reason  CheckReason
	Force   bool
	Targets []string
}

// StorageCleaner deletes one class of reclaimable files.
type StorageCleaner interface {
	Name() string
	Priority() int
	EstimateReclaimable(ctx context.Context) (uint64, error)
	Clean(ctx context.Context) (freed uint64, err error)
}

type forceStorageCleaner interface {
	ForceClean(ctx context.Context) (freed uint64, err error)
}

type storageCleanerLevel interface {
	MinimumLevel() StorageLevel
}

type StorageDegradedModeConfig struct {
	DisableLLMHTTPLog     bool `toml:"disable_llm_http_log"`
	DisableAudioArchive   bool `toml:"disable_audio_archive"`
	DisableSessionArchive bool `toml:"disable_session_archive"`
}

type StorageCleanupConfig struct {
	Enabled                     bool  `toml:"enabled"`
	LLMHTTPLogRetentionDays     []int `toml:"llm_http_log_retention_days,omitempty"`
	AudioArchiveRetentionDays   []int `toml:"audio_archive_retention_days,omitempty"`
	SessionArchiveRetentionDays []int `toml:"session_archive_retention_days,omitempty"`
	CleanupRetryIntervalSeconds int   `toml:"cleanup_retry_interval_seconds,omitempty"`
}

// StorageConfig controls persistent storage monitoring and remediation.
type StorageConfig struct {
	Enabled              bool                      `toml:"enabled"`
	RootPath             string                    `toml:"root_path,omitempty"`
	CheckIntervalSeconds int                       `toml:"check_interval_seconds,omitempty"`
	WarningThresholdMB   uint64                    `toml:"warning_threshold_mb,omitempty"`
	CriticalThresholdMB  uint64                    `toml:"critical_threshold_mb,omitempty"`
	EmergencyThresholdMB uint64                    `toml:"emergency_threshold_mb,omitempty"`
	RecoveryHysteresisMB uint64                    `toml:"recovery_hysteresis_mb,omitempty"`
	DegradedMode         StorageDegradedModeConfig `toml:"degraded_mode,omitempty"`
	Cleanup              StorageCleanupConfig      `toml:"cleanup,omitempty"`
}

func DefaultStorageConfig() StorageConfig {
	return StorageConfig{
		Enabled:              true,
		RootPath:             "/userdata",
		CheckIntervalSeconds: 300,
		WarningThresholdMB:   50,
		CriticalThresholdMB:  10,
		EmergencyThresholdMB: 5,
		RecoveryHysteresisMB: 5,
		DegradedMode: StorageDegradedModeConfig{
			DisableLLMHTTPLog:     true,
			DisableAudioArchive:   true,
			DisableSessionArchive: true,
		},
		Cleanup: StorageCleanupConfig{
			Enabled:                     true,
			LLMHTTPLogRetentionDays:     []int{7, 3, 1, 0},
			AudioArchiveRetentionDays:   []int{30, 7, 0},
			SessionArchiveRetentionDays: []int{30},
			CleanupRetryIntervalSeconds: 60,
		},
	}
}

func (c StorageConfig) Validate() error {
	if !c.Enabled {
		return nil
	}
	if strings.TrimSpace(c.RootPath) == "" {
		return fmt.Errorf("storage.root_path is required when storage.enabled=true")
	}
	if c.CheckIntervalSeconds <= 0 {
		return fmt.Errorf("storage.check_interval_seconds must be > 0, got %d", c.CheckIntervalSeconds)
	}
	if c.EmergencyThresholdMB == 0 || c.CriticalThresholdMB <= c.EmergencyThresholdMB || c.WarningThresholdMB <= c.CriticalThresholdMB {
		return fmt.Errorf("storage thresholds must satisfy emergency < critical < warning, got %d < %d < %d", c.EmergencyThresholdMB, c.CriticalThresholdMB, c.WarningThresholdMB)
	}
	if c.Cleanup.CleanupRetryIntervalSeconds < 0 {
		return fmt.Errorf("storage.cleanup.cleanup_retry_interval_seconds must be >= 0, got %d", c.Cleanup.CleanupRetryIntervalSeconds)
	}
	retentionFields := []struct {
		name   string
		values []int
	}{
		{name: "llm_http_log_retention_days", values: c.Cleanup.LLMHTTPLogRetentionDays},
		{name: "audio_archive_retention_days", values: c.Cleanup.AudioArchiveRetentionDays},
		{name: "session_archive_retention_days", values: c.Cleanup.SessionArchiveRetentionDays},
	}
	for _, field := range retentionFields {
		for _, value := range field.values {
			if value < 0 {
				return fmt.Errorf("storage.cleanup.%s must contain values >= 0, got %d", field.name, value)
			}
		}
	}
	return nil
}

type VoiceNotificationSink interface {
	Publish(ctx context.Context, event VoiceNotificationEvent) error
}

type VoiceNotificationEvent struct {
	Code      string                 `json:"code"`
	DedupeKey string                 `json:"dedupe_key"`
	Severity  StorageLevel           `json:"severity"`
	State     string                 `json:"state"`
	Params    map[string]interface{} `json:"params,omitempty"`
}

// StorageMonitor owns storage sampling, remediation and final-state projection.
type StorageMonitor struct {
	config   StorageConfig
	sampler  StorageSampler
	logger   *Logger
	cleaners []StorageCleaner
	notifier VoiceNotificationSink

	checkMu  sync.Mutex
	statusMu sync.RWMutex
	status   StorageStatus

	notificationActive bool
	notifiedLevel      StorageLevel
	lastNotifiedEvent  VoiceNotificationEvent
	lastCleanupAttempt time.Time
	writeCheckMu       sync.Mutex
	writeCheckPending  bool

	ctx     context.Context
	cancel  context.CancelFunc
	wg      sync.WaitGroup
	startMu sync.Mutex
	started bool
}

func NewStorageMonitor(config StorageConfig, sampler StorageSampler, logger *Logger, cleaners []StorageCleaner, notifier VoiceNotificationSink) *StorageMonitor {
	if sampler == nil {
		sampler = statfsStorageSampler{}
	}
	cleaners = append([]StorageCleaner(nil), cleaners...)
	sort.SliceStable(cleaners, func(i, j int) bool { return cleaners[i].Priority() < cleaners[j].Priority() })
	ctx, cancel := context.WithCancel(context.Background())
	return &StorageMonitor{
		config:   config,
		sampler:  sampler,
		logger:   logger,
		cleaners: cleaners,
		notifier: notifier,
		ctx:      ctx,
		cancel:   cancel,
	}
}

func (m *StorageMonitor) Status() StorageStatus {
	m.statusMu.RLock()
	defer m.statusMu.RUnlock()
	return cloneStorageStatus(m.status)
}

func cloneStorageStatus(status StorageStatus) StorageStatus {
	status.UnavailableCapabilities = append([]StorageCapability(nil), status.UnavailableCapabilities...)
	status.CleanupHistory = append([]StorageCleanupResult(nil), status.CleanupHistory...)
	status.LastCleanupResults = append([]StorageCleanupResult(nil), status.LastCleanupResults...)
	return status
}

func (m *StorageMonitor) CheckAndRemediate(ctx context.Context, request StorageCheckRequest) (StorageStatus, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	m.checkMu.Lock()
	defer m.checkMu.Unlock()

	path := strings.TrimSpace(m.config.RootPath)
	if path == "" {
		path = "/userdata"
	}
	if m.logger != nil {
		m.logger.Debug("storage_monitor: check started reason=%s path=%s", request.Reason, path)
	}
	previous := m.Status()
	initial, err := m.sampleStatus(ctx, path)
	if err != nil {
		return StorageStatus{}, err
	}
	if m.logger != nil && initial.Level != StorageLevelNormal {
		m.logger.Warn("storage_monitor: %s %s (%dMB available), warning threshold: %dMB", path, initial.Level, initial.AvailableBytes/storageMegabyte, m.config.WarningThresholdMB)
	}
	final := initial
	var cleanupHistory []StorageCleanupResult
	var totalFreed uint64

	retryInterval := time.Duration(m.config.Cleanup.CleanupRetryIntervalSeconds) * time.Second
	cleanupDue := m.lastCleanupAttempt.IsZero() || retryInterval <= 0 || time.Since(m.lastCleanupAttempt) >= retryInterval
	bypassRetry := request.Force || request.Reason == CheckReasonManual || request.Reason == CheckReasonWrite
	maintenanceCheck := request.Reason == CheckReasonStartup || request.Reason == CheckReasonPeriodic
	shouldClean := m.config.Cleanup.Enabled && (request.Force || request.Reason == CheckReasonManual || initial.Level != StorageLevelNormal || maintenanceCheck) && (cleanupDue || bypassRetry)
	if shouldClean {
		m.lastCleanupAttempt = time.Now()
		for _, cleaner := range m.cleaners {
			if !cleanerSelected(cleaner.Name(), request.Targets) {
				continue
			}
			currentLevel := m.applyRecoveryHysteresis(previous.Level, final.AvailableBytes, final.Level)
			if leveled, ok := cleaner.(storageCleanerLevel); ok && !request.Force && request.Reason != CheckReasonManual && storageLevelRank(currentLevel) < storageLevelRank(leveled.MinimumLevel()) {
				continue
			}
			var freed uint64
			var cleanErr error
			if m.logger != nil {
				m.logger.Info("storage_cleanup: running cleaner %s (priority %d)", cleaner.Name(), cleaner.Priority())
			}
			if request.Force {
				if forceCleaner, ok := cleaner.(forceStorageCleaner); ok {
					freed, cleanErr = forceCleaner.ForceClean(ctx)
				} else {
					freed, cleanErr = cleaner.Clean(ctx)
				}
			} else {
				estimate, estimateErr := cleaner.EstimateReclaimable(ctx)
				if estimateErr != nil {
					cleanupHistory = append(cleanupHistory, StorageCleanupResult{Timestamp: time.Now(), Cleaner: cleaner.Name(), Status: "failed", Error: estimateErr.Error()})
					if m.logger != nil {
						m.logger.Warn("storage_cleanup: cleaner %s estimate failed: %v", cleaner.Name(), estimateErr)
					}
					continue
				}
				if estimate == 0 {
					continue
				}
				freed, cleanErr = cleaner.Clean(ctx)
			}
			result := StorageCleanupResult{Timestamp: time.Now(), Cleaner: cleaner.Name(), FreedBytes: freed, Status: "success"}
			if cleanErr != nil {
				result.Status = "failed"
				result.Error = cleanErr.Error()
			}
			if m.logger != nil {
				if cleanErr != nil {
					m.logger.Warn("storage_cleanup: cleaner %s failed after freeing %dMB: %v", cleaner.Name(), freed/storageMegabyte, cleanErr)
				} else {
					m.logger.Info("storage_cleanup: %s freed %dMB", cleaner.Name(), freed/storageMegabyte)
				}
			}
			cleanupHistory = append(cleanupHistory, result)
			totalFreed += freed

			resampled, sampleErr := m.sampleStatus(ctx, path)
			if sampleErr != nil {
				return StorageStatus{}, fmt.Errorf("resample after cleaner %q: %w", cleaner.Name(), sampleErr)
			}
			final = resampled
			effectiveLevel := m.applyRecoveryHysteresis(previous.Level, final.AvailableBytes, final.Level)
			if effectiveLevel == StorageLevelNormal && !request.Force {
				break
			}
		}
	}

	final.Level = m.applyRecoveryHysteresis(previous.Level, final.AvailableBytes, final.Level)
	final.UnavailableCapabilities = m.unavailableCapabilities(final.Level)
	final.Revision = previous.Revision + 1
	if len(cleanupHistory) > 0 {
		final.LastCleanupFreedBytes = totalFreed
		final.CurrentCleanupFreedBytes = totalFreed
		final.LastCleanupResults = append([]StorageCleanupResult(nil), cleanupHistory...)
		final.LastCleanupAt = cleanupHistory[len(cleanupHistory)-1].Timestamp
		final.CleanupHistory = append(append([]StorageCleanupResult(nil), previous.CleanupHistory...), cleanupHistory...)
		if len(final.CleanupHistory) > 50 {
			final.CleanupHistory = append([]StorageCleanupResult(nil), final.CleanupHistory[len(final.CleanupHistory)-50:]...)
		}
	} else {
		final.LastCleanupAt = previous.LastCleanupAt
		final.LastCleanupFreedBytes = previous.LastCleanupFreedBytes
		final.CleanupHistory = previous.CleanupHistory
		final.LastCleanupResults = nil
		final.CurrentCleanupFreedBytes = 0
	}

	m.statusMu.Lock()
	m.status = cloneStorageStatus(final)
	m.statusMu.Unlock()
	if m.logger != nil && (previous.Level != final.Level || len(cleanupHistory) > 0) {
		m.logger.Info("storage_monitor: final level %s, %s %dMB available", final.Level, final.Path, final.AvailableBytes/storageMegabyte)
	}
	if m.logger != nil && !reflect.DeepEqual(previous.UnavailableCapabilities, final.UnavailableCapabilities) {
		if len(final.UnavailableCapabilities) == 0 {
			m.logger.Info("storage_monitor: storage capabilities recovered")
		} else {
			m.logger.Warn("storage_monitor: degraded capabilities=%v", final.UnavailableCapabilities)
		}
	}
	m.publishStatusTransition(ctx, final)
	return cloneStorageStatus(final), nil
}

func (m *StorageMonitor) publishStatusTransition(ctx context.Context, status StorageStatus) {
	if m.notifier == nil {
		return
	}
	if status.Level == StorageLevelNormal {
		if !m.notificationActive {
			return
		}
		event := m.notificationEvent(status, "resolved", m.notifiedLevel)
		if err := m.notifier.Publish(ctx, event); err != nil {
			if m.logger != nil {
				m.logger.Warn("storage notification publish failed: %v", err)
			}
			return
		}
		m.notificationActive = false
		m.notifiedLevel = StorageLevelNormal
		m.lastNotifiedEvent = VoiceNotificationEvent{}
		if m.logger != nil {
			m.logger.Info("storage_alert: published code=storage state=resolved dedupe_key=storage:device")
		}
		return
	}
	event := m.notificationEvent(status, "active", status.Level)
	if m.notificationActive && m.notifiedLevel == status.Level && reflect.DeepEqual(m.lastNotifiedEvent.Params, event.Params) {
		return
	}
	if err := m.notifier.Publish(ctx, event); err != nil {
		if m.logger != nil {
			m.logger.Warn("storage notification publish failed: %v", err)
		}
		return
	}
	m.notificationActive = true
	m.notifiedLevel = status.Level
	m.lastNotifiedEvent = event
	if m.logger != nil {
		m.logger.Info("storage_alert: published code=storage severity=%s dedupe_key=storage:device", status.Level)
	}
}

func (m *StorageMonitor) notificationEvent(status StorageStatus, state string, severity StorageLevel) VoiceNotificationEvent {
	return VoiceNotificationEvent{
		Code:      "storage",
		DedupeKey: "storage:device",
		Severity:  severity,
		State:     state,
		Params: map[string]interface{}{
			"path":                     status.Path,
			"available_mb":             status.AvailableBytes / storageMegabyte,
			"cleanup_result":           append([]StorageCleanupResult(nil), status.LastCleanupResults...),
			"unavailable_capabilities": append([]StorageCapability(nil), status.UnavailableCapabilities...),
		},
	}
}

func cleanerSelected(name string, targets []string) bool {
	if len(targets) == 0 {
		return true
	}
	for _, target := range targets {
		target = strings.TrimSpace(target)
		if target != "" && (name == target || strings.HasPrefix(name, target+"_")) {
			return true
		}
	}
	return false
}

func (m *StorageMonitor) sampleStatus(ctx context.Context, path string) (StorageStatus, error) {
	sample, err := m.sampler.Sample(ctx, path)
	if err != nil {
		return StorageStatus{}, fmt.Errorf("sample storage: %w", err)
	}
	used := uint64(0)
	if sample.TotalBytes >= sample.AvailableBytes {
		used = sample.TotalBytes - sample.AvailableBytes
	}
	percent := float64(0)
	if sample.TotalBytes > 0 {
		percent = float64(used) / float64(sample.TotalBytes) * 100
	}
	now := time.Now()
	return StorageStatus{
		Path:           path,
		TotalBytes:     sample.TotalBytes,
		UsedBytes:      used,
		AvailableBytes: sample.AvailableBytes,
		PercentUsed:    percent,
		Level:          m.levelForAvailable(sample.AvailableBytes),
		CheckedAt:      now,
	}, nil
}

func (m *StorageMonitor) levelForAvailable(availableBytes uint64) StorageLevel {
	switch {
	case availableBytes <= m.config.EmergencyThresholdMB*storageMegabyte:
		return StorageLevelEmergency
	case availableBytes <= m.config.CriticalThresholdMB*storageMegabyte:
		return StorageLevelCritical
	case availableBytes <= m.config.WarningThresholdMB*storageMegabyte:
		return StorageLevelWarning
	default:
		return StorageLevelNormal
	}
}

func (m *StorageMonitor) applyRecoveryHysteresis(previous StorageLevel, availableBytes uint64, raw StorageLevel) StorageLevel {
	if previous == "" || previous == StorageLevelNormal || storageLevelRank(raw) >= storageLevelRank(previous) {
		return raw
	}
	var thresholdMB uint64
	switch previous {
	case StorageLevelWarning:
		thresholdMB = m.config.WarningThresholdMB
	case StorageLevelCritical:
		thresholdMB = m.config.CriticalThresholdMB
	case StorageLevelEmergency:
		thresholdMB = m.config.EmergencyThresholdMB
	default:
		return raw
	}
	if availableBytes <= (thresholdMB+m.config.RecoveryHysteresisMB)*storageMegabyte {
		return previous
	}
	return raw
}

func storageLevelRank(level StorageLevel) int {
	switch level {
	case StorageLevelWarning:
		return 1
	case StorageLevelCritical:
		return 2
	case StorageLevelEmergency:
		return 3
	default:
		return 0
	}
}

func (m *StorageMonitor) unavailableCapabilities(level StorageLevel) []StorageCapability {
	if level != StorageLevelCritical && level != StorageLevelEmergency {
		return nil
	}
	capabilities := make([]StorageCapability, 0, 4)
	if m.config.DegradedMode.DisableLLMHTTPLog {
		capabilities = append(capabilities, StorageCapabilityLLMHTTPLog)
	}
	if m.config.DegradedMode.DisableAudioArchive {
		capabilities = append(capabilities, StorageCapabilityAudioArchive)
	}
	if m.config.DegradedMode.DisableSessionArchive {
		capabilities = append(capabilities, StorageCapabilitySessionArchive)
	}
	capabilities = append(capabilities, StorageCapabilitySessionPersistence)
	return capabilities
}

func (m *StorageMonitor) Start() error {
	if !m.config.Enabled {
		return nil
	}
	m.startMu.Lock()
	if m.started {
		m.startMu.Unlock()
		return nil
	}
	m.started = true
	m.startMu.Unlock()

	_, initialErr := m.CheckAndRemediate(m.ctx, StorageCheckRequest{Reason: CheckReasonStartup})
	m.wg.Add(1)
	go m.monitorLoop()
	return initialErr
}

func (m *StorageMonitor) monitorLoop() {
	defer m.wg.Done()
	interval := time.Duration(m.config.CheckIntervalSeconds) * time.Second
	if interval <= 0 {
		interval = 5 * time.Minute
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-m.ctx.Done():
			return
		case <-ticker.C:
			if _, err := m.CheckAndRemediate(m.ctx, StorageCheckRequest{Reason: CheckReasonPeriodic}); err != nil && m.logger != nil {
				m.logger.Warn("storage monitor periodic check failed: %v", err)
			}
		}
	}
}

func (m *StorageMonitor) Stop() {
	m.cancel()
	m.wg.Wait()
}

func (m *StorageMonitor) SetVoiceNotificationSink(notifier VoiceNotificationSink) {
	m.checkMu.Lock()
	defer m.checkMu.Unlock()
	m.notifier = notifier
}

func (m *StorageMonitor) ForceCleanup(path string) error {
	if strings.TrimSpace(path) != "" && path != m.config.RootPath {
		return fmt.Errorf("storage monitor only manages %q", m.config.RootPath)
	}
	_, err := m.CheckAndRemediate(context.Background(), StorageCheckRequest{Reason: CheckReasonManual, Force: true})
	return err
}

func (m *StorageMonitor) AllowWrite(capability StorageCapability) bool {
	status := m.Status()
	for _, unavailable := range status.UnavailableCapabilities {
		if unavailable == capability {
			return false
		}
	}
	return true
}

// HandleWriteError schedules an immediate remediation check for storage exhaustion or write protection.
func (m *StorageMonitor) HandleWriteError(err error) bool {
	if m == nil || (!errors.Is(err, syscall.ENOSPC) && !errors.Is(err, syscall.EROFS)) {
		return false
	}
	m.writeCheckMu.Lock()
	if m.writeCheckPending {
		m.writeCheckMu.Unlock()
		return true
	}
	m.writeCheckPending = true
	m.writeCheckMu.Unlock()
	go func() {
		defer func() {
			m.writeCheckMu.Lock()
			m.writeCheckPending = false
			m.writeCheckMu.Unlock()
		}()
		_, _ = m.CheckAndRemediate(context.Background(), StorageCheckRequest{Reason: CheckReasonWrite})
	}()
	return true
}
