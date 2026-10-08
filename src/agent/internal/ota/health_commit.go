package ota

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

func (u *Updater) abCommitPath() string {
	return filepath.Join(u.config.StateDir, "ab-commit.pending")
}

// prepareABCommit durably saves the old, CRC-protected metadata before the
// health commit overwrites misc. Callers hold the OTA update lock throughout.
func (u *Updater) prepareABCommit(previous ABData) error {
	path := u.abCommitPath()
	tmp := path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	defer os.Remove(tmp)
	_, writeErr := f.Write(previous.Marshal())
	if writeErr == nil {
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

// recoverABCommit repairs a torn health write before another OTA operation.
// Valid metadata is preserved, including changes made by SPL on a later boot.
// Always sync it before retiring the backup: the interrupted writer may have
// stopped in Sync even when the new bytes are readable from the page cache.
func (u *Updater) recoverABCommit() error {
	raw, err := os.ReadFile(u.abCommitPath())
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	previous, err := ParseABData(raw)
	if err != nil {
		return fmt.Errorf("invalid A/B health commit backup: %w", err)
	}
	f, err := os.OpenFile(u.miscPathForAccess(), os.O_RDWR, 0)
	if err != nil {
		return err
	}
	currentRaw := make([]byte, ABDataSize)
	_, readErr := f.ReadAt(currentRaw, ABMetadataOffset)
	var syncErr error
	if readErr == nil {
		syncErr = f.Sync()
	}
	closeErr := f.Close()
	if readErr != nil {
		return readErr
	}
	if syncErr != nil {
		return syncErr
	}
	if closeErr != nil {
		return closeErr
	}
	current, err := ParseABData(currentRaw)
	if err != nil {
		current = previous
		if err := u.writeABData(current); err != nil {
			return err
		}
	}
	if err := u.verifyABCommit(current); err != nil {
		return err
	}
	if err := u.finishPublishedHealthCommit(current); err != nil {
		return err
	}
	return u.clearABCommit()
}

// A process can stop after publishing committed state but before removing the
// pending marker. Finish that cleanup before the next boot's health aggregator,
// which correctly refuses to create a marker for an already committed state.
func (u *Updater) finishPublishedHealthCommit(ab ABData) error {
	raw, err := os.ReadFile(u.pendingPath())
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	var pending PendingBoot
	if err := json.Unmarshal(raw, &pending); err != nil {
		return err
	}
	slot, err := parseSlotName(pending.TargetSlot)
	if err != nil {
		return err
	}
	state, err := LoadState(u.statePath())
	if err != nil {
		return err
	}
	if state.ActiveSlot != slot || !ab.Slots[slot].SuccessfulBoot || state.PendingBootNonce != "" || state.TargetVersion != "" ||
		state.CurrentVersion != pending.TargetVersion || state.CurrentBuildTime != pending.TargetBuildTime ||
		state.LastCommittedVersion != pending.TargetVersion || state.LastCommittedBuildTime != pending.TargetBuildTime {
		return nil
	}
	// Re-sync state publication if the previous writer stopped at its directory
	// fsync, then durably retire the pending marker before discarding the backup.
	state.Phase, state.LastError = "committed", ""
	if err := SaveState(u.statePath(), state); err != nil {
		return err
	}
	if err := os.Remove(u.pendingPath()); err != nil {
		return err
	}
	return fsyncDirFor(u.pendingPath())
}

func (u *Updater) verifyABCommit(expected ABData) error {
	actual, err := u.readABData()
	if err != nil {
		return err
	}
	if !bytes.Equal(actual.Marshal(), expected.Marshal()) {
		return fmt.Errorf("A/B health commit readback mismatch")
	}
	return nil
}

func (u *Updater) clearABCommit() error {
	path := u.abCommitPath()
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return err
	}
	return fsyncDirFor(path)
}
