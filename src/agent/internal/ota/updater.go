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
	"time"
)

const (
	DefaultOTAConfigPath    = "/userdata/ota/config.json"
	DefaultOTAStateDir      = "/userdata/ota"
	DefaultGitHubAPIBase    = "https://api.github.com"
	MaxRemoteManifestBytes  = 1 << 20
	DefaultHTTPRequestLimit = 30 * time.Minute
)

type UpdaterConfig struct {
	ConfigPath             string                       `json:"-"`
	StateDir               string                       `json:"state_dir,omitempty"`
	DownloadDir            string                       `json:"download_dir,omitempty"`
	MiscPath               string                       `json:"misc_path,omitempty"`
	BlockDir               string                       `json:"block_dir,omitempty"`
	Repo                   string                       `json:"repo,omitempty"`
	Channel                string                       `json:"channel,omitempty"`
	APIBase                string                       `json:"api_base,omitempty"`
	ManifestAsset          string                       `json:"manifest_asset,omitempty"`
	PublicKeyPath          string                       `json:"public_key_path,omitempty"`
	PublicKey              ed25519.PublicKey            `json:"-"`
	FactoryVersion         string                       `json:"factory_version,omitempty"`
	FactoryBuildTime       string                       `json:"factory_build_time,omitempty"`
	FactoryPartitionHashes map[string]map[string]string `json:"factory_partition_hashes,omitempty"`
	PartitionSizes         map[string]int64             `json:"partition_sizes,omitempty"`
	GitHubToken            string                       `json:"github_token,omitempty"`
	GitHubTokenPath        string                       `json:"github_token_path,omitempty"`
	Interval               time.Duration                `json:"-"`
	Jitter                 time.Duration                `json:"-"`
	IntervalSeconds        int                          `json:"interval_seconds,omitempty"`
	JitterSeconds          int                          `json:"jitter_seconds,omitempty"`
	SwitchTries            uint8                        `json:"switch_tries,omitempty"`
	HealthTimeout          time.Duration                `json:"-"`
	HealthTimeoutSecs      int                          `json:"health_timeout_seconds,omitempty"`
	HealthPollInterval     time.Duration                `json:"-"`
	HTTPTimeout            time.Duration                `json:"-"`
	HTTPTimeoutSecs        int                          `json:"http_timeout_seconds,omitempty"`
	DryRun                 bool                         `json:"dry_run,omitempty"`
	TargetSlotOverride     string                       `json:"target_slot_override,omitempty"`
	Logger                 *log.Logger                  `json:"-"`
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
	config := DefaultUpdaterConfig()
	config.ConfigPath = path
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return config, nil
		}
		return UpdaterConfig{}, err
	}
	if err := json.Unmarshal(data, &config); err != nil {
		return UpdaterConfig{}, err
	}
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
	if config.MiscPath == "" {
		config.MiscPath = "/dev/block/by-name/misc"
	}
	if config.BlockDir == "" {
		config.BlockDir = "/dev/block/by-name"
	}
	if config.APIBase == "" {
		config.APIBase = DefaultGitHubAPIBase
	}
	if config.ManifestAsset == "" {
		config.ManifestAsset = "manifest.json"
	}
	if config.PublicKeyPath == "" {
		config.PublicKeyPath = "/oem/etc/ota_pubkey.pem"
	}
	if config.GitHubTokenPath == "" {
		config.GitHubTokenPath = filepath.Join(config.StateDir, "gh_token")
	}
	if config.Interval == 0 {
		if config.IntervalSeconds > 0 {
			config.Interval = time.Duration(config.IntervalSeconds) * time.Second
		} else {
			config.Interval = time.Hour
		}
	}
	if config.Jitter == 0 && config.JitterSeconds > 0 {
		config.Jitter = time.Duration(config.JitterSeconds) * time.Second
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
	if config.HTTPTimeout == 0 {
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
	u := &Updater{config: config, reboot: reboot, currentSlot: currentSlotFromProcCmdline}
	u.writeABData = u.writeABDataFile
	return u, nil
}

func (u *Updater) CheckOnce(ctx context.Context) (UpdateResult, error) {
	if err := u.ProcessPendingHealth(ctx); err != nil {
		u.recordError("health", err)
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
	httpCtx, cancelHTTP := u.httpContext(ctx)
	defer cancelHTTP()

	releaseURL, err := u.releaseURL()
	if err != nil {
		u.recordError("release", err)
		return UpdateResult{}, err
	}
	token := u.githubToken()
	assetsByName, err := FetchLatestReleaseAssets(httpCtx, releaseURL, token)
	if err != nil {
		u.recordError("release", err)
		return UpdateResult{}, err
	}
	manifestURL, err := requiredAssetURL(assetsByName, u.config.ManifestAsset)
	if err != nil {
		u.recordError("manifest", err)
		return UpdateResult{}, err
	}
	manifestBytes, err := fetchBytesWithTokenLimit(httpCtx, manifestURL, token, MaxRemoteManifestBytes)
	if err != nil {
		u.recordError("manifest", err)
		return UpdateResult{}, err
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
	manifestChannel := releaseManifestChannel(u.config.Channel)
	if manifest.Channel != "" && manifestChannel != "" && manifest.Channel != manifestChannel {
		err := fmt.Errorf("manifest channel %q, want %q", manifest.Channel, manifestChannel)
		u.recordError("manifest", err)
		return UpdateResult{}, err
	}
	if err := state.RejectDowngrade(manifest); err != nil {
		u.recordError("policy", err)
		return UpdateResult{}, err
	}
	if isNoUpdate(state, manifest) {
		return UpdateResult{NoUpdate: true, Version: manifest.Version, TargetSlot: target}, nil
	}
	if err := state.ValidateSelectiveUpdate(manifest, target); err != nil {
		err = fmt.Errorf("selective update rejected: %w", err)
		u.recordError("policy", err)
		return UpdateResult{}, err
	}

	selectedAssets := map[string]ManifestAsset{}
	downloaded := map[string]string{}
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
		assetURL, err := requiredAssetURL(assetsByName, asset.Name)
		if err != nil {
			u.recordError("asset", err)
			return UpdateResult{}, err
		}
		dst := filepath.Join(u.config.DownloadDir, asset.Name)
		if err := os.MkdirAll(u.config.DownloadDir, 0o755); err != nil {
			u.recordError("download", err)
			return UpdateResult{}, err
		}
		if err := DownloadFileWithToken(httpCtx, assetURL, dst, asset.Size, token); err != nil {
			u.recordError("download", err)
			return UpdateResult{}, err
		}
		if err := VerifyFile(dst, asset.Size, asset.SHA256); err != nil {
			u.recordError("verify", err)
			return UpdateResult{}, err
		}
		selectedAssets[part.Name] = asset
		downloaded[part.Name] = dst
	}
	if u.config.DryRun {
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
		state.DownloadedHashes[part] = asset.SHA256
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
		if err := writer.WritePart(part, target, image); err != nil {
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
	if u.reboot != nil {
		if err := u.reboot(); err != nil {
			u.recordError("reboot", err)
			return UpdateResult{}, err
		}
	}
	return UpdateResult{Updated: true, Version: manifest.Version, TargetSlot: target}, nil
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
			_ = os.Remove(u.pendingPath())
			return nil
		}
	}
	bootID := currentBootID()
	if err := ValidateHealthMarker(u.healthPath(), pending, bootID); err == nil {
		return u.commitPendingHealth(pending)
	}
	if err := WaitForHealth(ctx, u.healthPath(), pending, bootID, u.config.HealthTimeout, u.config.HealthPollInterval, u.reboot); err != nil {
		return err
	}
	return u.commitPendingHealth(pending)
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

func (u *Updater) RunDaemon(ctx context.Context) error {
	if err := u.ProcessPendingHealth(ctx); err != nil {
		u.logf("ota health: %v", err)
		u.recordError("health", err)
	}
	for {
		if _, err := u.CheckOnce(ctx); err != nil {
			u.logf("ota check: %v", err)
			u.recordError("check", err)
		}
		wait := u.config.Interval + randomJitter(u.config.Jitter)
		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
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
	if u.config.HTTPTimeout <= 0 {
		return context.WithCancel(parent)
	}
	return context.WithTimeout(parent, u.config.HTTPTimeout)
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

func (u *Updater) releaseURL() (string, error) {
	if u.config.Repo == "" {
		return u.config.APIBase, nil
	}
	base := strings.TrimRight(u.config.APIBase, "/")
	if _, err := url.ParseRequestURI(base); err != nil {
		return "", err
	}
	channel := strings.TrimSpace(u.config.Channel)
	if channel == "" || channel == "latest" || channel == "stable" {
		return base + "/repos/" + strings.Trim(u.config.Repo, "/") + "/releases/latest", nil
	}
	if tag, ok := strings.CutPrefix(channel, "tag:"); ok && tag != "" {
		return base + "/repos/" + strings.Trim(u.config.Repo, "/") + "/releases/tags/" + url.PathEscape(tag), nil
	}
	return "", fmt.Errorf("unsupported release channel %q: use stable, latest, or tag:<name>", u.config.Channel)
}

func releaseManifestChannel(channel string) string {
	channel = strings.TrimSpace(channel)
	if channel == "" || channel == "latest" || channel == "stable" || strings.HasPrefix(channel, "tag:") {
		return "stable"
	}
	return channel
}

func (u *Updater) validateAssetFitsPartition(partName string, target Slot, asset ManifestAsset) error {
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
	resp, err := http.DefaultClient.Do(req)
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

func randomJitter(max time.Duration) time.Duration {
	if max <= 0 {
		return 0
	}
	buf := make([]byte, 1)
	if _, err := rand.Read(buf); err != nil {
		return 0
	}
	return time.Duration(int64(buf[0]) * int64(max) / 255)
}
