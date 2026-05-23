package ota

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

var syncDir = func(f *os.File) error { return f.Sync() }

type State struct {
	Phase                  string                       `json:"phase,omitempty"`
	CurrentVersion         string                       `json:"current_version,omitempty"`
	CurrentBuildTime       string                       `json:"current_build_time,omitempty"`
	TargetVersion          string                       `json:"target_version,omitempty"`
	TargetBuildTime        string                       `json:"target_build_time,omitempty"`
	ActiveSlot             Slot                         `json:"active_slot"`
	TargetSlot             Slot                         `json:"target_slot"`
	DownloadedAssets       map[string]string            `json:"downloaded_assets,omitempty"`
	DownloadedHashes       map[string]string            `json:"downloaded_hashes,omitempty"`
	Slots                  map[string]SlotPartitionInfo `json:"slots"`
	LastCommittedVersion   string                       `json:"last_committed_version,omitempty"`
	LastCommittedBuildTime string                       `json:"last_committed_build_time,omitempty"`
	LastError              string                       `json:"last_error,omitempty"`
	Retry                  RetryMetadata                `json:"retry,omitempty"`
	PendingBootNonce       string                       `json:"pending_boot_nonce,omitempty"`
	PendingBootID          string                       `json:"pending_boot_id,omitempty"`
	PendingTargetSlot      *SlotPartitionInfo           `json:"pending_target_slot,omitempty"`
}

type RetryMetadata struct {
	Count      int    `json:"count,omitempty"`
	NextAt     string `json:"next_at,omitempty"`
	LastReason string `json:"last_reason,omitempty"`
}

type SlotPartitionInfo struct {
	Partitions map[string]PartitionVersion `json:"partitions"`
}

type PartitionVersion struct {
	Version string `json:"version"`
	Hash    string `json:"hash"`
}

var factorySlotNames = []string{"a", "b"}
var factoryPartitionNames = []string{"boot", "oem", "rootfs"}

func NewFactoryState(version string, buildTime string, hashes map[string]map[string]string) State {
	state := State{
		Phase:                  "factory",
		CurrentVersion:         version,
		CurrentBuildTime:       buildTime,
		LastCommittedVersion:   version,
		LastCommittedBuildTime: buildTime,
		Slots:                  map[string]SlotPartitionInfo{},
	}
	for _, slot := range []Slot{SlotA, SlotB} {
		parts := map[string]PartitionVersion{}
		name, _ := slotName(slot)
		for _, part := range factoryPartitionNames {
			parts[part] = PartitionVersion{Version: version, Hash: hashes[name][part]}
		}
		state.Slots[name] = SlotPartitionInfo{Partitions: parts}
	}
	return state
}

func uniformFactoryPartitionHashes(hash string) map[string]map[string]string {
	hashes := map[string]map[string]string{}
	for _, slot := range factorySlotNames {
		hashes[slot] = map[string]string{}
		for _, part := range factoryPartitionNames {
			hashes[slot][part] = hash
		}
	}
	return hashes
}

func validateFactoryPartitionHashes(hashes map[string]map[string]string) error {
	for _, slot := range factorySlotNames {
		parts, ok := hashes[slot]
		if !ok {
			return fmt.Errorf("factory_partition_hashes missing slot %s", slot)
		}
		for _, part := range factoryPartitionNames {
			hash := strings.TrimSpace(parts[part])
			if hash == "" {
				return fmt.Errorf("factory_partition_hashes.%s.%s is required", slot, part)
			}
			decoded, err := hex.DecodeString(hash)
			if err != nil || len(decoded) != 32 {
				return fmt.Errorf("factory_partition_hashes.%s.%s must be a sha256 hex digest", slot, part)
			}
		}
	}
	return nil
}

func LoadState(path string) (State, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return State{}, err
	}
	var state State
	if err := json.Unmarshal(data, &state); err != nil {
		return State{}, err
	}
	return state, nil
}

func SaveState(path string, state State) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return err
	}
	writeErr := error(nil)
	if _, writeErr = f.Write(data); writeErr == nil {
		writeErr = f.Sync()
	}
	closeErr := f.Close()
	if writeErr != nil {
		return writeErr
	}
	if closeErr != nil {
		return closeErr
	}
	if err := os.Rename(tmp, path); err != nil {
		return err
	}
	return fsyncDirFor(path)
}

func (s State) RejectDowngrade(manifest Manifest) error {
	if manifest.Version == s.LastCommittedVersion {
		if manifest.BuildTime == s.LastCommittedBuildTime {
			return nil
		}
		return fmt.Errorf("reject downgrade: same version %q already committed", manifest.Version)
	}
	if s.LastCommittedBuildTime == "" {
		return nil
	}
	next, err := time.Parse(time.RFC3339, manifest.BuildTime)
	if err != nil {
		return fmt.Errorf("invalid manifest build_time: %w", err)
	}
	committed, err := time.Parse(time.RFC3339, s.LastCommittedBuildTime)
	if err != nil {
		return fmt.Errorf("invalid committed build_time: %w", err)
	}
	if !next.After(committed) {
		return fmt.Errorf("reject downgrade: build_time %s is not later than committed %s", manifest.BuildTime, s.LastCommittedBuildTime)
	}
	return nil
}

func (s State) ValidateSelectiveUpdate(manifest Manifest, targetSlot Slot) error {
	targetSlotName, err := slotName(targetSlot)
	if err != nil {
		return err
	}
	included := map[string]bool{}
	requirements := map[string]PartitionVersion{}
	for _, part := range manifest.Parts {
		included[part.Name] = true
		for _, raw := range part.RequiresPartitions {
			name, req, err := parsePartitionRequirement(raw)
			if err != nil {
				return err
			}
			requirements[name] = req
		}
	}
	if included["boot"] && included["oem"] && included["rootfs"] {
		return nil
	}
	slotState, ok := s.Slots[targetSlotName]
	if !ok {
		return fmt.Errorf("target slot %s has no partition state", targetSlotName)
	}
	for _, part := range []string{"boot", "oem", "rootfs"} {
		if included[part] {
			continue
		}
		req, ok := requirements[part]
		if !ok {
			return fmt.Errorf("selective update missing requires_partitions for %s", part)
		}
		local, ok := slotState.Partitions[part]
		if !ok || local.Version != req.Version || local.Hash != req.Hash {
			return fmt.Errorf("selective update requires %s %s/%s, local is %s/%s", part, req.Version, req.Hash, local.Version, local.Hash)
		}
	}
	return nil
}

func (s *State) CommitUpdate(manifest Manifest, targetSlot Slot, assets map[string]ManifestAsset) error {
	slot, err := slotName(targetSlot)
	if err != nil {
		return err
	}
	if s.Slots == nil {
		s.Slots = map[string]SlotPartitionInfo{}
	}
	slotState := s.Slots[slot]
	if slotState.Partitions == nil {
		slotState.Partitions = map[string]PartitionVersion{}
	}
	for part, asset := range assets {
		slotState.Partitions[part] = PartitionVersion{Version: manifest.Version, Hash: asset.SHA256}
	}
	s.Slots[slot] = slotState
	s.LastCommittedVersion = manifest.Version
	s.LastCommittedBuildTime = manifest.BuildTime
	s.CurrentVersion = manifest.Version
	s.CurrentBuildTime = manifest.BuildTime
	s.ActiveSlot = targetSlot
	return nil
}

func parsePartitionRequirement(raw string) (string, PartitionVersion, error) {
	name, rest, ok := strings.Cut(raw, "=")
	if !ok {
		return "", PartitionVersion{}, fmt.Errorf("invalid requires_partitions entry %q", raw)
	}
	version, hash, ok := strings.Cut(rest, ":")
	if !ok || name == "" || version == "" || hash == "" {
		return "", PartitionVersion{}, fmt.Errorf("invalid requires_partitions entry %q", raw)
	}
	if name != "boot" && name != "oem" && name != "rootfs" {
		return "", PartitionVersion{}, fmt.Errorf("unknown required partition %q", name)
	}
	return name, PartitionVersion{Version: version, Hash: hash}, nil
}

func slotName(slot Slot) (string, error) {
	switch slot {
	case SlotA:
		return "a", nil
	case SlotB:
		return "b", nil
	default:
		return "", fmt.Errorf("invalid slot %d", slot)
	}
}

func fsyncDirFor(path string) error {
	dir, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer dir.Close()
	if err := syncDir(dir); err != nil {
		if errors.Is(err, syscall.ENOTSUP) || errors.Is(err, syscall.EINVAL) {
			return nil
		}
		return err
	}
	return nil
}
