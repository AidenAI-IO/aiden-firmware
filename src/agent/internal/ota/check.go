package ota

import (
	"context"
	"fmt"
	"os"
)

type CheckResult struct {
	Available      bool   `json:"available"`
	Version        string `json:"version"`
	BuildTime      string `json:"build_time"`
	CurrentVersion string `json:"current_version"`
}

// CheckAvailable verifies release metadata without downloading images, writing
// partitions, processing health, or changing the public transaction state.
func (u *Updater) CheckAvailable(ctx context.Context) (CheckResult, error) {
	if err := u.ensureStorageReady(); err != nil {
		return CheckResult{}, err
	}
	unlock, err := u.acquireUpdateLock()
	if err != nil {
		return CheckResult{}, err
	}
	defer unlock()
	if _, err := os.Stat(u.pendingPath()); err == nil {
		return CheckResult{}, fmt.Errorf("an OTA boot is awaiting health confirmation")
	} else if !os.IsNotExist(err) {
		return CheckResult{}, err
	}
	state, err := LoadState(u.statePath())
	if os.IsNotExist(err) {
		if u.config.FactoryVersion == "" || u.config.FactoryBuildTime == "" {
			return CheckResult{}, fmt.Errorf("missing OTA factory version and build time")
		}
		state = NewFactoryState(u.config.FactoryVersion, u.config.FactoryBuildTime, u.config.FactoryPartitionHashes)
	} else if err != nil {
		return CheckResult{}, err
	}
	manifest, _, err := u.fetchUpdateManifest(ctx)
	if err != nil {
		return CheckResult{}, err
	}
	if err := state.RejectDowngrade(manifest); err != nil {
		return CheckResult{}, err
	}
	return CheckResult{Available: !isNoUpdate(state, manifest), Version: manifest.Version,
		BuildTime: manifest.BuildTime, CurrentVersion: state.LastCommittedVersion}, nil
}
