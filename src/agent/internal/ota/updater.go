package ota

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"aiden-agent/internal/netproxy"
)

const (
	DefaultOTAConfigPath               = "/userdata/ota/config.json"
	DefaultOTAStateDir                 = "/userdata/ota"
	DefaultOTAUpdateLockName           = "update.lock"
	DefaultOTAReserveSizeBytes         = 200 << 20
	DefaultOTAReserveSafetyMarginBytes = 4 << 20
	DefaultReleaseURL                  = "https://api.github.com/repos/AidenAI-IO/aiden-firmware/releases/latest"
	MaxRemoteManifestBytes             = 1 << 20
	DefaultHTTPRequestLimit            = 30 * time.Minute
	DefaultHTTPResponseHeaderTimeout   = 30 * time.Second
)

var ErrUpdateAlreadyRunning = errors.New("ota update already running")

type UpdaterConfig struct {
	ConfigPath               string                       `json:"-"`
	StateDir                 string                       `json:"state_dir,omitempty"`
	DownloadDir              string                       `json:"download_dir,omitempty"`
	DownloadDirConfigured    bool                         `json:"-"`
	AlternateDownloadDirs    []string                     `json:"-"`
	ReserveSizeBytes         int64                        `json:"reserve_size_bytes,omitempty"`
	ReserveSafetyMarginBytes int64                        `json:"reserve_safety_margin_bytes,omitempty"`
	UpdateLockPath           string                       `json:"update_lock_path,omitempty"`
	MiscPath                 string                       `json:"misc_path,omitempty"`
	BlockDir                 string                       `json:"block_dir,omitempty"`
	ManifestURL              string                       `json:"manifest_url,omitempty"`
	ReleaseURL               string                       `json:"-"` // Test override for default release URL
	PublicKeyPath            string                       `json:"public_key_path,omitempty"`
	PublicKey                ed25519.PublicKey            `json:"-"`
	FactoryVersion           string                       `json:"factory_version,omitempty"`
	FactoryBuildTime         string                       `json:"factory_build_time,omitempty"`
	FactoryPartitionHashes   map[string]map[string]string `json:"factory_partition_hashes,omitempty"`
	PartitionSizes           map[string]int64             `json:"partition_sizes,omitempty"`
	GitHubToken              string                       `json:"github_token,omitempty"`
	GitHubTokenPath          string                       `json:"github_token_path,omitempty"`
	GitHubProxyURL           string                       `json:"github_proxy_url,omitempty"`
	SwitchTries              uint8                        `json:"switch_tries,omitempty"`
	HealthTimeout            time.Duration                `json:"-"`
	HealthTimeoutSecs        int                          `json:"health_timeout_seconds,omitempty"`
	HealthPollInterval       time.Duration                `json:"-"`
	HTTPTimeout              time.Duration                `json:"-"`
	HTTPTimeoutSecs          int                          `json:"http_timeout_seconds,omitempty"`
	DryRun                   bool                         `json:"dry_run,omitempty"`
	TargetSlotOverride       string                       `json:"target_slot_override,omitempty"`
	Logger                   *log.Logger                  `json:"-"`
}

type UpdateResult struct {
	Updated    bool
	NoUpdate   bool
	Version    string
	TargetSlot Slot
}

type Updater struct {
	config      UpdaterConfig
	reboot      func() error
	writeABData func(ABData) error
	currentSlot func() (Slot, bool, error)
}

func LoadUpdaterConfig(path string) (UpdaterConfig, error) {
	if path == "" {
		path = DefaultOTAConfigPath
	}
	config := UpdaterConfig{ConfigPath: path}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return normalizeUpdaterConfig(config)
		}
		return UpdaterConfig{}, err
	}
	if err := json.Unmarshal(data, &config); err != nil {
		return UpdaterConfig{}, err
	}
	config.DownloadDirConfigured = strings.TrimSpace(config.DownloadDir) != ""
	config.ConfigPath = path
	return normalizeUpdaterConfig(config)
}

func DefaultUpdaterConfig() UpdaterConfig {
	config, _ := normalizeUpdaterConfig(UpdaterConfig{})
	return config
}

func normalizeUpdaterConfig(config UpdaterConfig) (UpdaterConfig, error) {
	if config.StateDir == "" {
		config.StateDir = DefaultOTAStateDir
	}
	if config.DownloadDir == "" {
		config.DownloadDir = filepath.Join(config.StateDir, "downloads")
	}
	if config.ReserveSizeBytes == 0 {
		config.ReserveSizeBytes = DefaultOTAReserveSizeBytes
	}
	if config.ReserveSizeBytes < 0 {
		return UpdaterConfig{}, fmt.Errorf("reserve_size_bytes must be non-negative")
	}
	if config.ReserveSafetyMarginBytes == 0 {
		config.ReserveSafetyMarginBytes = DefaultOTAReserveSafetyMarginBytes
	}
	if config.ReserveSafetyMarginBytes < 0 {
		return UpdaterConfig{}, fmt.Errorf("reserve_safety_margin_bytes must be non-negative")
	}
	if config.ReserveSafetyMarginBytes >= config.ReserveSizeBytes {
		return UpdaterConfig{}, fmt.Errorf("reserve_safety_margin_bytes must be smaller than reserve_size_bytes")
	}
	if config.UpdateLockPath == "" {
		config.UpdateLockPath = filepath.Join(config.StateDir, DefaultOTAUpdateLockName)
	}
	if config.MiscPath == "" {
		config.MiscPath = "/dev/block/by-name/misc"
	}
	if config.BlockDir == "" {
		config.BlockDir = "/dev/block/by-name"
	}
	if config.PublicKeyPath == "" {
		config.PublicKeyPath = "/oem/etc/ota_pubkey.pem"
	}
	if config.GitHubTokenPath == "" {
		config.GitHubTokenPath = filepath.Join(config.StateDir, "gh_token")
	}
	if config.SwitchTries == 0 {
		config.SwitchTries = 3
	}
	if config.HealthTimeout == 0 {
		if config.HealthTimeoutSecs > 0 {
			config.HealthTimeout = time.Duration(config.HealthTimeoutSecs) * time.Second
		} else {
			config.HealthTimeout = 5 * time.Minute
		}
	}
	if config.HealthPollInterval == 0 {
		config.HealthPollInterval = time.Second
	}
	if config.HTTPTimeout <= 0 {
		if config.HTTPTimeoutSecs > 0 {
			config.HTTPTimeout = time.Duration(config.HTTPTimeoutSecs) * time.Second
		} else {
			config.HTTPTimeout = DefaultHTTPRequestLimit
		}
	}
	if config.SwitchTries > MaxTries {
		return UpdaterConfig{}, fmt.Errorf("switch_tries %d exceeds %d", config.SwitchTries, MaxTries)
	}
	return config, nil
}

func NewUpdater(config UpdaterConfig, reboot func() error) (*Updater, error) {
	config, err := normalizeUpdaterConfig(config)
	if err != nil {
		return nil, err
	}
	u := &Updater{
		config:      config,
		reboot:      reboot,
		currentSlot: currentSlotFromProcCmdline,
	}
	u.writeABData = u.writeABDataFile
	return u, nil
}

func (u *Updater) CheckOnce(ctx context.Context) (UpdateResult, error) {
	unlock, err := u.acquireUpdateLock()
	if err != nil {
		u.logf("ota check: %v", err)
		return UpdateResult{}, err
	}
	defer unlock()
	return u.checkOnceLocked(ctx)
}

func (u *Updater) checkOnceLocked(ctx context.Context) (UpdateResult, error) {
	u.logf("ota check: start")
	if err := u.ProcessPendingHealth(ctx); err != nil {
		u.recordError("health", err)
		return UpdateResult{}, err
	}
	if err := u.cleanupAlternateDownloadDirs(); err != nil {
		u.recordError("cleanup", err)
		return UpdateResult{}, err
	}

	state, err := u.loadState()
	if err != nil {
		return UpdateResult{}, err
	}
	ab, err := u.readABData()
	if err != nil {
		u.recordError("read-misc", err)
		return UpdateResult{}, err
	}
	miscActive, ok := ab.ActiveSlot()
	if !ok {
		err := errors.New("misc has no bootable active slot")
		u.recordError("read-misc", err)
		return UpdateResult{}, err
	}
	active := miscActive
	if u.currentSlot != nil {
		running, runningOK, err := u.currentSlot()
		if err != nil {
			u.recordError("current-slot", err)
			return UpdateResult{}, err
		}
		if runningOK {
			active = running
		}
	}
	target := inactiveSlot(active)
	if u.config.TargetSlotOverride != "" {
		target, err = parseSlotName(u.config.TargetSlotOverride)
		if err != nil {
			u.recordError("target-slot", err)
			return UpdateResult{}, err
		}
	}
	u.logf("ota check: active_slot=%s target_slot=%s", slotLogName(active), slotLogName(target))

	var assetsByName map[string]string
	var manifestBytes []byte
	token := u.githubToken()

	if u.config.ManifestURL != "" {
		u.logf("ota manifest: downloading from direct URL %s", sanitizeURLForLog(u.config.ManifestURL))
		directToken := ""
		if isGitHubURL(u.config.ManifestURL) {
			directToken = token
		}
		manifestBytes, err = u.fetchBytesWithTokenLimit(ctx, u.config.ManifestURL, directToken, MaxRemoteManifestBytes)
		if err != nil {
			u.recordError("manifest", err)
			return UpdateResult{}, err
		}
	} else {
		releaseURL := u.config.ReleaseURL
		if releaseURL == "" {
			releaseURL = DefaultReleaseURL
		}
		u.logf("ota release: fetching %s", releaseURL)
		assetsByName, err = u.fetchLatestReleaseAssets(ctx, releaseURL, token)
		if err != nil {
			u.recordError("release", err)
			return UpdateResult{}, err
		}
		u.logf("ota release: found %d assets", len(assetsByName))
		manifestURL, err := requiredAssetURL(assetsByName, "manifest.json")
		if err != nil {
			u.recordError("manifest", err)
			return UpdateResult{}, err
		}
		u.logf("ota manifest: downloading manifest.json from %s", manifestURL)
		manifestBytes, err = u.fetchBytesWithTokenLimit(ctx, manifestURL, token, MaxRemoteManifestBytes)
		if err != nil {
			u.recordError("manifest", err)
			return UpdateResult{}, err
		}
	}
	publicKey, err := u.publicKey()
	if err != nil {
		u.recordError("manifest", err)
		return UpdateResult{}, err
	}
	manifest, err := VerifyManifestJSON(manifestBytes, publicKey)
	if err != nil {
		u.recordError("manifest", err)
		return UpdateResult{}, err
	}
	u.logf("ota manifest: verified version=%s channel=%s build_time=%s parts=%d", manifest.Version, logValue(manifest.Channel, "<unset>"), manifest.BuildTime, len(manifest.Parts))
	if err := state.RejectDowngrade(manifest); err != nil {
		u.recordError("policy", err)
		return UpdateResult{}, err
	}
	if isNoUpdate(state, manifest) {
		u.logf("ota check: no update version=%s build_time=%s", manifest.Version, manifest.BuildTime)
		return UpdateResult{NoUpdate: true, Version: manifest.Version, TargetSlot: target}, nil
	}
	if err := state.ValidateSelectiveUpdate(manifest, target); err != nil {
		err = fmt.Errorf("selective update rejected: %w", err)
		u.recordError("policy", err)
		return UpdateResult{}, err
	}

	selectedAssets := map[string]ManifestAsset{}
	for _, part := range manifest.Parts {
		asset, err := ResolveAsset(part, target)
		if err != nil {
			u.recordError("asset", err)
			return UpdateResult{}, err
		}
		if err := u.validateAssetFitsPartition(part.Name, target, asset); err != nil {
			u.recordError("asset", err)
			return UpdateResult{}, err
		}
		selectedAssets[part.Name] = asset
	}
	plan := downloadPlan{assets: selectedAssets, state: state, target: target}

	if err := u.cleanupOldDownloadCache(plan); err != nil {
		u.recordError("cleanup", err)
		return UpdateResult{}, err
	}
	if err := u.ensureReserveSpace(); err != nil {
		u.recordError("reserve", err)
		return UpdateResult{}, err
	}
	reserveReleased, err := u.releaseReserveForDownloads(plan)
	if err != nil {
		u.recordError("reserve", err)
		return UpdateResult{}, err
	}
	defer func() {
		if reserveReleased {
			if err := u.ensureReserveSpace(); err != nil {
				u.logf("ota reserve: restore after failed update: %v", err)
			}
		}
	}()

	downloaded := map[string]string{}
	for _, part := range manifest.Parts {
		asset := selectedAssets[part.Name]
		if targetPartitionHashMatches(state, target, part.Name, asset) {
			u.logf("ota partition: %s skipped; target slot %s hash matches manifest", part.Name, slotLogName(target))
			continue
		}
		var assetURL string
		var assetToken string
		if asset.URL != "" {
			assetURL = asset.URL
			u.logf("ota asset: %s using direct URL from manifest", asset.Name)
			if isGitHubURL(assetURL) {
				assetToken = token
			}
		} else if u.config.ManifestURL != "" {
			var err error
			assetURL, err = deriveAssetURL(u.config.ManifestURL, asset.Name)
			if err != nil {
				u.recordError("asset", err)
				return UpdateResult{}, err
			}
			u.logf("ota asset: %s using URL derived from manifest URL", asset.Name)
			if isGitHubURL(assetURL) {
				assetToken = token
			}
		} else {
			var err error
			assetURL, err = requiredAssetURL(assetsByName, asset.Name)
			if err != nil {
				u.recordError("asset", err)
				return UpdateResult{}, err
			}
			u.logf("ota asset: %s using URL from release API", asset.Name)
			assetToken = token
		}
		dst := filepath.Join(u.config.DownloadDir, asset.Name)
		if u.cachedDownloadVerified(dst, asset) {
			downloaded[part.Name] = dst
			continue
		}
		if err := os.MkdirAll(u.config.DownloadDir, 0o755); err != nil {
			u.recordError("download", err)
			return UpdateResult{}, err
		}
		u.logf("ota download: %s start size=%s url=%s dst=%s", asset.Name, formatBytes(asset.Size), sanitizeURLForLog(assetURL), dst)
		if err := u.downloadFileWithToken(ctx, assetURL, dst, asset.Size, assetToken); err != nil {
			u.recordError("download", err)
			return UpdateResult{}, err
		}
		if err := VerifyFile(dst, asset.Size, asset.SHA256); err != nil {
			err = u.discardInvalidDownload(dst, err)
			u.recordError("verify", err)
			return UpdateResult{}, err
		}
		u.logf("ota verify: %s sha256 ok", asset.Name)
		if err := u.verifyDownloadedImage(dst, asset); err != nil {
			err = u.discardInvalidDownload(dst, err)
			u.recordError("verify", err)
			return UpdateResult{}, err
		}
		downloaded[part.Name] = dst
	}
	if reserveReleased {
		if err := u.ensureReserveSpace(); err != nil {
			u.recordError("reserve", err)
			return UpdateResult{}, err
		}
		reserveReleased = false
	}
	if u.config.DryRun {
		u.logf("ota check: dry-run complete version=%s target_slot=%s", manifest.Version, slotLogName(target))
		return UpdateResult{Updated: true, Version: manifest.Version, TargetSlot: target}, nil
	}
	state.Phase = "writing"
	state.TargetVersion = manifest.Version
	state.TargetBuildTime = manifest.BuildTime
	state.ActiveSlot = active
	state.TargetSlot = target
	state.DownloadedAssets = downloaded
	state.DownloadedHashes = map[string]string{}
	for part, asset := range selectedAssets {
		state.DownloadedHashes[part] = partitionSHA256ForAsset(asset)
	}
	if state.Slots == nil {
		state.Slots = map[string]SlotPartitionInfo{}
	}
	targetKey := targetNameForState(target)
	snapshot := copySlotPartitionInfo(state.Slots[targetKey])
	if len(snapshot.Partitions) > 0 {
		state.PendingTargetSlot = &snapshot
	} else {
		state.PendingTargetSlot = nil
	}
	delete(state.Slots, targetKey)
	if err := SaveState(u.statePath(), state); err != nil {
		u.recordError("state", err)
		return UpdateResult{}, err
	}

	writer := PartitionWriter{BlockDir: u.config.BlockDir, ActiveSlot: active, PartitionSizes: u.partitionSizes()}
	for part, image := range downloaded {
		blockName, err := writer.ResolveBlockName(part, target)
		if err != nil {
			u.recordError("write", err)
			return UpdateResult{}, err
		}
		u.logf("ota write: %s -> %s start image=%s", part, blockName, image)
		if err := writer.WritePartWithProgress(part, target, image, u.logWriteProgress); err != nil {
			u.recordError("write", err)
			return UpdateResult{}, err
		}
	}
	if err := DeleteStaleHealthMarker(u.healthPath()); err != nil {
		u.recordError("health", err)
		return UpdateResult{}, err
	}
	nonce, err := generateNonce()
	if err != nil {
		u.recordError("pending", err)
		return UpdateResult{}, err
	}
	targetName, _ := slotName(target)
	pending := PendingBoot{TargetSlot: targetName, TargetVersion: manifest.Version, TargetBuildTime: manifest.BuildTime, Nonce: nonce}
	if err := WritePendingBoot(u.pendingPath(), pending); err != nil {
		u.recordError("pending", err)
		return UpdateResult{}, err
	}
	state.Phase = "pending-reboot"
	state.PendingBootNonce = nonce
	if err := SaveState(u.statePath(), state); err != nil {
		u.recordError("state", err)
		return UpdateResult{}, err
	}
	if err := ab.SetActive(target, u.config.SwitchTries, false); err != nil {
		_ = os.Remove(u.pendingPath())
		u.recordError("misc", err)
		return UpdateResult{}, err
	}
	if err := u.writeABData(ab); err != nil {
		_ = os.Remove(u.pendingPath())
		u.recordError("misc", err)
		return UpdateResult{}, err
	}
	u.logf("ota misc: switched active slot to %s tries=%d", slotLogName(target), u.config.SwitchTries)
	if u.reboot != nil {
		u.logf("ota reboot: requested after switching to slot %s", slotLogName(target))
		if err := u.reboot(); err != nil {
			u.recordError("reboot", err)
			return UpdateResult{}, err
		}
	}
	return UpdateResult{Updated: true, Version: manifest.Version, TargetSlot: target}, nil
}

func (u *Updater) acquireUpdateLock() (func(), error) {
	lockPath := u.config.UpdateLockPath
	if lockPath == "" {
		lockPath = filepath.Join(u.config.StateDir, DefaultOTAUpdateLockName)
	}
	if err := os.MkdirAll(filepath.Dir(lockPath), 0o755); err != nil {
		return nil, fmt.Errorf("create ota update lock dir: %w", err)
	}
	lockFile, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open ota update lock: %w", err)
	}
	if err := syscall.Flock(int(lockFile.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = lockFile.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) || errors.Is(err, syscall.EAGAIN) {
			return nil, fmt.Errorf("%w: %s", ErrUpdateAlreadyRunning, lockPath)
		}
		return nil, fmt.Errorf("lock ota update: %w", err)
	}
	if err := lockFile.Truncate(0); err == nil {
		_, _ = lockFile.Seek(0, 0)
		_, _ = fmt.Fprintf(lockFile, "%d\n", os.Getpid())
	}
	return func() {
		_ = syscall.Flock(int(lockFile.Fd()), syscall.LOCK_UN)
		_ = lockFile.Close()
	}, nil
}

func (u *Updater) cachedDownloadVerified(path string, asset ManifestAsset) bool {
	if err := u.verifyCachedDownload(path, asset); err != nil {
		if !os.IsNotExist(err) {
			u.logf("ota download: %s cached file ignored: %v", asset.Name, err)
		}
		return false
	}
	u.logf("ota download: %s skipped; cached file verified dst=%s", asset.Name, path)
	return true
}

func (u *Updater) verifyCachedDownload(path string, asset ManifestAsset) error {
	if err := VerifyFile(path, asset.Size, asset.SHA256); err != nil {
		return err
	}
	return u.verifyDownloadedImage(path, asset)
}

func (u *Updater) discardInvalidDownload(path string, verifyErr error) error {
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("%w; remove invalid download %s: %v", verifyErr, filepath.Base(path), err)
	}
	u.logf("ota download: removed invalid cached file %s", filepath.Base(path))
	return verifyErr
}

func targetPartitionHashMatches(state State, target Slot, partName string, asset ManifestAsset) bool {
	targetName, err := slotName(target)
	if err != nil {
		return false
	}
	slotState, ok := state.Slots[targetName]
	if !ok {
		return false
	}
	local, ok := slotState.Partitions[partName]
	return ok && local.Version != "" && local.Hash == partitionSHA256ForAsset(asset)
}

func (u *Updater) verifyDownloadedImage(path string, asset ManifestAsset) error {
	if asset.ImageSHA256 == "" {
		return nil
	}
	if err := verifyPartitionImage(path, asset.ImageSHA256); err != nil {
		return fmt.Errorf("%s image_sha256: %w", asset.Name, err)
	}
	u.logf("ota verify: %s image_sha256 ok", asset.Name)
	return nil
}

func (u *Updater) ProcessPendingHealth(ctx context.Context) error {
	data, err := os.ReadFile(u.pendingPath())
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	var pending PendingBoot
	if err := json.Unmarshal(data, &pending); err != nil {
		return err
	}
	u.logf("ota health: pending slot=%s version=%s timeout=%s", pending.TargetSlot, pending.TargetVersion, u.config.HealthTimeout)
	pendingSlot, err := parseSlotName(pending.TargetSlot)
	if err != nil {
		return err
	}
	if u.currentSlot != nil {
		running, ok, err := u.currentSlot()
		if err != nil {
			return err
		}
		if ok && running != pendingSlot {
			ab, err := u.readABData()
			if err != nil {
				return err
			}
			if miscActive, activeOK := ab.ActiveSlot(); activeOK && miscActive == pendingSlot {
				runningName, _ := slotName(running)
				return fmt.Errorf("pending target slot %s is selected in misc but running slot is %s", pending.TargetSlot, runningName)
			}
			return u.clearPendingAfterRollback(running)
		}
	}
	bootID := currentBootID()
	if err := ValidateHealthMarker(u.healthPath(), pending, bootID); err == nil {
		u.logf("ota health: marker valid, committing slot=%s", pending.TargetSlot)
		return u.commitPendingHealth(pending)
	}
	u.logf("ota health: waiting for marker path=%s", u.healthPath())
	if err := WaitForHealth(ctx, u.healthPath(), pending, bootID, u.config.HealthTimeout, u.config.HealthPollInterval, u.reboot); err != nil {
		return err
	}
	u.logf("ota health: marker received, committing slot=%s", pending.TargetSlot)
	return u.commitPendingHealth(pending)
}

func (u *Updater) clearPendingAfterRollback(running Slot) error {
	state, err := u.loadState()
	if err != nil {
		return err
	}
	state.Phase = "rolled-back"
	state.ActiveSlot = running
	state.TargetSlot = running
	state.TargetVersion = ""
	state.TargetBuildTime = ""
	state.PendingBootNonce = ""
	state.PendingBootID = ""
	state.PendingTargetSlot = nil
	if err := SaveState(u.statePath(), state); err != nil {
		return err
	}
	if err := os.Remove(u.pendingPath()); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

func (u *Updater) commitPendingHealth(pending PendingBoot) error {
	slot, err := parseSlotName(pending.TargetSlot)
	if err != nil {
		return err
	}
	ab, err := u.readABData()
	if err != nil {
		return err
	}
	if err := ab.MarkSuccessful(slot); err != nil {
		return err
	}
	if err := u.writeABData(ab); err != nil {
		return err
	}
	state, err := u.loadState()
	if err != nil {
		return err
	}
	state.Phase = "committed"
	state.CurrentVersion = pending.TargetVersion
	state.CurrentBuildTime = pending.TargetBuildTime
	state.LastCommittedVersion = pending.TargetVersion
	state.LastCommittedBuildTime = pending.TargetBuildTime
	state.ActiveSlot = slot
	state.TargetVersion = ""
	state.TargetBuildTime = ""
	slotKey, err := slotName(slot)
	if err != nil {
		return err
	}
	if state.Slots == nil {
		state.Slots = map[string]SlotPartitionInfo{}
	}
	slotState := SlotPartitionInfo{}
	if state.PendingTargetSlot != nil {
		slotState = copySlotPartitionInfo(*state.PendingTargetSlot)
	}
	state.PendingBootNonce = ""
	state.PendingBootID = ""
	state.PendingTargetSlot = nil
	state.LastError = ""
	if len(slotState.Partitions) == 0 {
		slotState = state.Slots[slotKey]
	}
	if slotState.Partitions == nil {
		slotState.Partitions = map[string]PartitionVersion{}
	}
	for part, hash := range state.DownloadedHashes {
		slotState.Partitions[part] = PartitionVersion{Version: pending.TargetVersion, Hash: hash}
	}
	state.Slots[slotKey] = slotState
	if err := SaveState(u.statePath(), state); err != nil {
		return err
	}
	return os.Remove(u.pendingPath())
}

func (u *Updater) ProcessPendingHealthOnce(ctx context.Context) error {
	u.logf("ota health: processing pending boot")
	unlock, lockErr := u.acquireUpdateLock()
	if lockErr != nil {
		u.logf("ota health: %v", lockErr)
		if !errors.Is(lockErr, ErrUpdateAlreadyRunning) {
			u.recordError("health", lockErr)
		}
		return lockErr
	}
	defer unlock()

	healthErr := u.ProcessPendingHealth(ctx)
	if healthErr != nil {
		u.logf("ota health: %v", healthErr)
		u.recordError("health", healthErr)
	}
	cleanupErr := u.cleanupAlternateDownloadDirs()
	if cleanupErr != nil {
		u.logf("ota cleanup: %v", cleanupErr)
		if healthErr == nil {
			u.recordError("cleanup", cleanupErr)
		} else {
			return healthErr
		}
		return cleanupErr
	}
	reserveErr := u.ensureReserveSpace()
	if reserveErr != nil {
		u.logf("ota reserve: %v", reserveErr)
		if healthErr == nil {
			u.recordError("reserve", reserveErr)
			return reserveErr
		}
	}
	if healthErr != nil {
		return healthErr
	}
	u.logf("ota health: complete")
	return nil
}

func (u *Updater) Status() (State, ABData, error) {
	state, stateErr := u.loadState()
	ab, abErr := u.readABData()
	if stateErr != nil && !os.IsNotExist(stateErr) {
		return State{}, ABData{}, stateErr
	}
	if abErr != nil {
		return state, ABData{}, abErr
	}
	return state, ab, nil
}

func (u *Updater) VerifyManifestFile(path string) (Manifest, error) {
	ctx, cancel := u.httpContext(context.Background())
	defer cancel()
	data, err := readLocalOrRemoteManifest(ctx, path)
	if err != nil {
		return Manifest{}, err
	}
	key, err := u.publicKey()
	if err != nil {
		return Manifest{}, err
	}
	return VerifyManifestJSON(data, key)
}

func (u *Updater) httpContext(parent context.Context) (context.Context, context.CancelFunc) {
	timeout := u.config.HTTPTimeout
	if timeout <= 0 {
		timeout = DefaultHTTPRequestLimit
	}
	return context.WithTimeout(parent, timeout)
}

func readLocalOrRemoteManifest(ctx context.Context, path string) ([]byte, error) {
	parsed, err := url.Parse(path)
	if err == nil && (parsed.Scheme == "http" || parsed.Scheme == "https") {
		return fetchBytesWithTokenLimit(ctx, path, "", MaxRemoteManifestBytes)
	}
	return os.ReadFile(path)
}

func (u *Updater) loadState() (State, error) {
	state, err := LoadState(u.statePath())
	if err != nil {
		if os.IsNotExist(err) {
			return u.initializeFactoryState()
		}
		return State{}, err
	}
	if state.Slots == nil {
		state.Slots = map[string]SlotPartitionInfo{}
	}
	return state, nil
}

func (u *Updater) initializeFactoryState() (State, error) {
	if u.config.FactoryVersion == "" || u.config.FactoryBuildTime == "" || len(u.config.FactoryPartitionHashes) == 0 {
		return State{}, errors.New("missing OTA state: factory_version, factory_build_time, and factory_partition_hashes are required")
	}
	if err := validateFactoryPartitionHashes(u.config.FactoryPartitionHashes); err != nil {
		return State{}, fmt.Errorf("invalid OTA factory state: %w", err)
	}
	state := NewFactoryState(u.config.FactoryVersion, u.config.FactoryBuildTime, u.config.FactoryPartitionHashes)
	if err := SaveState(u.statePath(), state); err != nil {
		return State{}, err
	}
	return state, nil
}

func (u *Updater) readABData() (ABData, error) {
	f, err := os.Open(u.config.MiscPath)
	if err != nil {
		return ABData{}, err
	}
	defer f.Close()
	return ReadABData(f)
}

func (u *Updater) writeABDataFile(ab ABData) error {
	f, err := os.OpenFile(u.config.MiscPath, os.O_WRONLY, 0)
	if err != nil {
		return err
	}
	writeErr := WriteABDataAt(f, ab)
	if writeErr == nil {
		writeErr = f.Sync()
	}
	closeErr := f.Close()
	if writeErr != nil {
		return writeErr
	}
	return closeErr
}

func (u *Updater) publicKey() (ed25519.PublicKey, error) {
	if len(u.config.PublicKey) == ed25519.PublicKeySize {
		return u.config.PublicKey, nil
	}
	data, err := os.ReadFile(u.config.PublicKeyPath)
	if err != nil {
		return nil, err
	}
	return ParseEd25519PublicKeyPEM(data)
}

func sanitizeURLForLog(rawURL string) string {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return "<malformed-url>"
	}
	parsed.RawQuery = ""
	parsed.Fragment = ""
	return parsed.String()
}

func deriveAssetURL(manifestURL, assetName string) (string, error) {
	parsed, err := url.Parse(manifestURL)
	if err != nil {
		return "", fmt.Errorf("invalid manifest URL: %w", err)
	}

	// Extract directory from manifest path
	// Example: /repos/owner/repo/releases/download/tag/manifest.json
	//       -> /repos/owner/repo/releases/download/tag/
	lastSlash := strings.LastIndex(parsed.Path, "/")
	if lastSlash < 0 {
		return "", fmt.Errorf("manifest URL has no directory component: %s", manifestURL)
	}
	baseDir := parsed.Path[:lastSlash+1]

	// Construct asset URL by replacing manifest filename with asset name
	parsed.Path = baseDir + assetName
	parsed.RawQuery = ""
	parsed.Fragment = ""

	return parsed.String(), nil
}

func (u *Updater) validateAssetFitsPartition(partName string, target Slot, asset ManifestAsset) error {
	if isCompressedImageAssetName(asset.Name) {
		return nil
	}
	blockName := partitionBlockName(partName, target)
	if max, ok := u.partitionSizes()[blockName]; ok && asset.Size > max {
		return fmt.Errorf("asset %s size %d is larger than partition %s size %d", asset.Name, asset.Size, blockName, max)
	}
	return nil
}

func partitionBlockName(partName string, target Slot) string {
	if target == SlotA {
		return partName + "_a"
	}
	return partName + "_b"
}

func (u *Updater) partitionSizes() map[string]int64 {
	sizes := map[string]int64{}
	for name, size := range DefaultProductionPartitionSizes {
		sizes[name] = size
	}
	for name, size := range u.config.PartitionSizes {
		sizes[name] = size
	}
	return sizes
}

func (u *Updater) fetchLatestReleaseAssets(parent context.Context, releaseURL string, token string) (map[string]string, error) {
	ctx, cancel := u.httpContext(parent)
	defer cancel()
	return FetchLatestReleaseAssetsWithProxy(ctx, releaseURL, token, u.config.GitHubProxyURL)
}

func (u *Updater) fetchBytesWithTokenLimit(parent context.Context, url string, token string, limit int64) ([]byte, error) {
	ctx, cancel := u.httpContext(parent)
	defer cancel()

	// Apply GitHub proxy if configured
	fetchURL := ApplyGitHubProxy(url, u.config.GitHubProxyURL)
	if fetchURL != url {
		fmt.Fprintf(os.Stderr, "ota: using GitHub proxy for manifest download\n")
	}

	return fetchBytesWithTokenLimit(ctx, fetchURL, token, limit)
}

func (u *Updater) downloadFileWithToken(parent context.Context, url string, dst string, expectedSize int64, token string) error {
	ctx, cancel := u.httpContext(parent)
	defer cancel()
	return DownloadFileWithOptions(ctx, url, dst, expectedSize, DownloadOptions{
		BearerToken:    token,
		GitHubProxyURL: u.config.GitHubProxyURL,
		Progress:       u.logDownloadProgress,
	})
}

func (u *Updater) githubToken() string {
	if u.config.GitHubToken != "" {
		return u.config.GitHubToken
	}
	data, err := os.ReadFile(u.config.GitHubTokenPath)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

func (u *Updater) statePath() string   { return filepath.Join(u.config.StateDir, "state.json") }
func (u *Updater) pendingPath() string { return filepath.Join(u.config.StateDir, "pending_boot.json") }
func (u *Updater) healthPath() string  { return filepath.Join(u.config.StateDir, "health.ok") }

func (u *Updater) recordError(phase string, err error) {
	state, loadErr := u.loadState()
	if loadErr != nil {
		return
	}
	state.Phase = phase
	state.LastError = err.Error()
	_ = SaveState(u.statePath(), state)
}

func (u *Updater) logf(format string, args ...any) {
	if u.config.Logger != nil {
		u.config.Logger.Printf(format, args...)
	}
}

// cleanupOldDownloadCache keeps only verified assets and resumable partials
// needed for the selected target slot. The reserve file is managed separately.
func (u *Updater) cleanupOldDownloadCache(plan downloadPlan) error {
	downloadDir := u.config.DownloadDir
	if downloadDir == "" {
		return nil
	}

	assetsByName := make(map[string]ManifestAsset)
	validCached := make(map[string]bool)
	for partName, asset := range plan.assets {
		if targetPartitionHashMatches(plan.state, plan.target, partName, asset) {
			continue
		}
		assetsByName[asset.Name] = asset
		if u.verifyCachedDownload(filepath.Join(downloadDir, asset.Name), asset) == nil {
			validCached[asset.Name] = true
		}
	}

	// Read download directory
	entries, err := os.ReadDir(downloadDir)
	if os.IsNotExist(err) {
		// Directory doesn't exist, nothing to clean
		return nil
	}
	if err != nil {
		return fmt.Errorf("read download dir: %w", err)
	}

	// Check each file
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		if name == otaReserveFileName {
			continue
		}
		if asset, ok := assetsByName[name]; ok && validCached[asset.Name] {
			continue
		}
		if strings.HasSuffix(name, ".part") {
			baseName := strings.TrimSuffix(name, ".part")
			if asset, ok := assetsByName[baseName]; ok && !validCached[baseName] {
				if info, infoErr := entry.Info(); infoErr == nil && info.Size() <= asset.Size {
					continue
				}
			}
		}

		// Delete old file
		path := filepath.Join(downloadDir, name)
		if err := os.Remove(path); err != nil {
			return fmt.Errorf("remove stale OTA cache %s: %w", name, err)
		}
		u.logf("ota cleanup: removed old file %s", name)
	}

	return nil
}

var defaultOTAHTTPClient = newOTAHTTPClient()

func newOTAHTTPClient() *http.Client {
	transport, ok := http.DefaultTransport.(*http.Transport)
	if !ok {
		return &http.Client{Transport: http.DefaultTransport}
	}
	clone := transport.Clone()
	clone.ResponseHeaderTimeout = DefaultHTTPResponseHeaderTimeout
	clone.Proxy = otaProxyFromEnvironment
	return &http.Client{Transport: clone}
}

func otaProxyFromEnvironment(req *http.Request) (*url.URL, error) {
	return netproxy.ProxyFromEnvironment(req, "http", "https", "socks5")
}

func (u *Updater) logDownloadProgress(progress DownloadProgress) {
	if u.config.Logger == nil {
		return
	}
	name := filepath.Base(progress.Path)
	amount := formatDownloadAmount(progress.Bytes, progress.Total)
	if progress.Complete {
		u.logf("ota download: %s complete %s", name, amount)
		return
	}
	resume := ""
	if progress.ResumedFrom > 0 {
		resume = fmt.Sprintf(" resumed_from=%s", formatBytes(progress.ResumedFrom))
	}
	percent := ""
	if progress.Total > 0 {
		percent = fmt.Sprintf(" (%d%%)", progress.Bytes*100/progress.Total)
	}
	u.logf("ota download: %s progress %s%s%s", name, amount, percent, resume)
}

func (u *Updater) logWriteProgress(progress WriteProgress) {
	if u.config.Logger == nil {
		return
	}
	amount := formatDownloadAmount(progress.Bytes, progress.Total)
	if progress.Complete {
		u.logf("ota write: %s -> %s complete %s", progress.Part, progress.BlockName, amount)
		return
	}
	percent := ""
	if progress.Total > 0 {
		percent = fmt.Sprintf(" (%d%%)", progress.Bytes*100/progress.Total)
	}
	u.logf("ota write: %s -> %s progress %s%s", progress.Part, progress.BlockName, amount, percent)
}

func formatDownloadAmount(bytes int64, total int64) string {
	if total >= 0 {
		return fmt.Sprintf("%s/%s", formatBytes(bytes), formatBytes(total))
	}
	return formatBytes(bytes)
}

func formatBytes(bytes int64) string {
	if bytes < 0 {
		return "unknown"
	}
	if bytes < 1024 {
		return fmt.Sprintf("%d B", bytes)
	}
	value := float64(bytes)
	for _, unit := range []string{"KiB", "MiB", "GiB"} {
		value /= 1024
		if value < 1024 || unit == "GiB" {
			return fmt.Sprintf("%.1f %s", value, unit)
		}
	}
	return fmt.Sprintf("%.1f GiB", value)
}

func slotLogName(slot Slot) string {
	name, err := slotName(slot)
	if err != nil {
		return fmt.Sprint(slot)
	}
	return name
}

func logValue(value string, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return value
}

func requiredAssetURL(assets map[string]string, name string) (string, error) {
	url, ok := assets[name]
	if !ok || url == "" {
		return "", fmt.Errorf("missing required release asset %s", name)
	}
	return url, nil
}

func fetchBytes(ctx context.Context, url string) ([]byte, error) {
	return fetchBytesWithToken(ctx, url, "")
}

func fetchBytesWithToken(ctx context.Context, url string, bearerToken string) ([]byte, error) {
	return fetchBytesWithTokenLimit(ctx, url, bearerToken, -1)
}

func fetchBytesWithTokenLimit(ctx context.Context, url string, bearerToken string, limit int64) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	if bearerToken != "" {
		req.Header.Set("Authorization", "Bearer "+bearerToken)
	}
	resp, err := defaultOTAHTTPClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("fetch %s status %d", url, resp.StatusCode)
	}
	if limit < 0 {
		return io.ReadAll(resp.Body)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("manifest too large: exceeds %d bytes", limit)
	}
	return data, nil
}

func targetNameForState(slot Slot) string {
	name, _ := slotName(slot)
	return name
}

func isNoUpdate(state State, manifest Manifest) bool {
	return state.LastCommittedVersion != "" &&
		state.LastCommittedBuildTime != "" &&
		manifest.Version == state.LastCommittedVersion &&
		manifest.BuildTime == state.LastCommittedBuildTime
}

func inactiveSlot(active Slot) Slot {
	if active == SlotA {
		return SlotB
	}
	return SlotA
}

func parseSlotName(name string) (Slot, error) {
	switch strings.TrimPrefix(strings.ToLower(name), "_") {
	case "a":
		return SlotA, nil
	case "b":
		return SlotB, nil
	default:
		return SlotA, fmt.Errorf("invalid slot %q", name)
	}
}

func currentSlotFromProcCmdline() (Slot, bool, error) {
	data, err := os.ReadFile("/proc/cmdline")
	if err != nil {
		if os.IsNotExist(err) {
			return SlotA, false, nil
		}
		return SlotA, false, err
	}
	return currentSlotFromCmdline(string(data))
}

func currentRootSlotFromProcCmdline() (Slot, bool, error) {
	data, err := os.ReadFile("/proc/cmdline")
	if err != nil {
		if os.IsNotExist(err) {
			return SlotA, false, nil
		}
		return SlotA, false, err
	}
	return rootSlotFromCmdline(string(data))
}

func currentSlotFromCmdline(cmdline string) (Slot, bool, error) {
	for _, field := range strings.Fields(cmdline) {
		value, ok := strings.CutPrefix(field, "aiden.slot_suffix=")
		if !ok {
			continue
		}
		slot, err := parseSlotName(value)
		if err != nil {
			return SlotA, false, err
		}
		return slot, true, nil
	}
	return SlotA, false, nil
}

func rootSlotFromCmdline(cmdline string) (Slot, bool, error) {
	for _, field := range strings.Fields(cmdline) {
		value, ok := strings.CutPrefix(field, "root=")
		if !ok {
			continue
		}
		value = strings.Trim(strings.ToLower(value), "\"'")
		switch {
		case value == "partlabel=rootfs_a" || value == "rootfs_a" || strings.HasSuffix(value, "/rootfs_a") || value == "/dev/mmcblk0p9":
			return SlotA, true, nil
		case value == "partlabel=rootfs_b" || value == "rootfs_b" || strings.HasSuffix(value, "/rootfs_b") || value == "/dev/mmcblk0p10":
			return SlotB, true, nil
		default:
			return SlotA, false, fmt.Errorf("unsupported root device %q", value)
		}
	}
	return SlotA, false, nil
}

func copySlotPartitionInfo(info SlotPartitionInfo) SlotPartitionInfo {
	if len(info.Partitions) == 0 {
		return SlotPartitionInfo{}
	}
	parts := make(map[string]PartitionVersion, len(info.Partitions))
	for name, version := range info.Partitions {
		parts[name] = version
	}
	return SlotPartitionInfo{Partitions: parts}
}

func generateNonce() (string, error) {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf), nil
}

func currentBootID() string {
	data, err := os.ReadFile("/proc/sys/kernel/random/boot_id")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}
