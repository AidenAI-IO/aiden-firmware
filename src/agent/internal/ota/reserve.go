package ota

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
)

const (
	otaReserveFileName = ".ota-reserve"
	reserveWriteChunk  = 1 << 20
	maxInt64           = int64(^uint64(0) >> 1)
)

type downloadPlan struct {
	assets map[string]ManifestAsset
	state  State
	target Slot
}

func (u *Updater) reservePath() string {
	return filepath.Join(u.config.DownloadDir, otaReserveFileName)
}

// ensureReserveSpace keeps cache files plus the allocated reserve file at the
// configured OTA budget. Partial and verified downloads therefore remain
// resumable without giving ordinary workloads access to the unused balance.
func (u *Updater) ensureReserveSpace() error {
	if u.config.DownloadDir == "" || u.config.ReserveSizeBytes <= 0 {
		return nil
	}
	if err := os.MkdirAll(u.config.DownloadDir, 0o755); err != nil {
		return fmt.Errorf("create OTA download dir: %w", err)
	}
	cacheBytes, err := u.downloadCacheBytes()
	if err != nil {
		return err
	}
	target := u.config.ReserveSizeBytes - cacheBytes
	if target <= 0 {
		if err := os.Remove(u.reservePath()); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("remove exhausted OTA reserve: %w", err)
		}
		u.logf("ota reserve: cache=%s uses configured budget=%s", formatBytes(cacheBytes), formatBytes(u.config.ReserveSizeBytes))
		return nil
	}
	if err := allocateReserveFile(u.reservePath(), target); err != nil {
		return fmt.Errorf("allocate OTA reserve %s: %w", formatBytes(target), err)
	}
	u.logf("ota reserve: ready file=%s size=%s cache=%s budget=%s", u.reservePath(), formatBytes(target), formatBytes(cacheBytes), formatBytes(u.config.ReserveSizeBytes))
	return nil
}

func (u *Updater) releaseReserveForDownloads(plan downloadPlan) (bool, error) {
	needed, err := u.remainingDownloadBytes(plan)
	if err != nil {
		return false, err
	}
	if needed == 0 {
		u.logf("ota reserve: no download bytes needed")
		return false, nil
	}
	if u.config.ReserveSafetyMarginBytes > maxInt64-needed {
		return false, fmt.Errorf("OTA reserve size overflow")
	}
	needed += u.config.ReserveSafetyMarginBytes
	info, err := os.Stat(u.reservePath())
	if err != nil {
		if os.IsNotExist(err) {
			return false, fmt.Errorf("OTA reserve missing: need %s for remaining downloads; run `ota health` or free storage so the reserve can be recreated", formatBytes(needed))
		}
		return false, fmt.Errorf("stat OTA reserve: %w", err)
	}
	if info.Size() < needed {
		return false, fmt.Errorf("OTA reserve too small: need %s for remaining downloads and safety margin, reserved %s; increase reserve_size_bytes", formatBytes(needed), formatBytes(info.Size()))
	}
	if err := os.Remove(u.reservePath()); err != nil {
		return false, fmt.Errorf("release OTA reserve: %w", err)
	}
	if err := fsyncDirFor(u.reservePath()); err != nil {
		return false, fmt.Errorf("sync released OTA reserve: %w", err)
	}
	u.logf("ota reserve: released %s for downloads", formatBytes(info.Size()))
	return true, nil
}

func (u *Updater) remainingDownloadBytes(plan downloadPlan) (int64, error) {
	var total int64
	for partName, asset := range plan.assets {
		if targetPartitionHashMatches(plan.state, plan.target, partName, asset) {
			continue
		}
		dst := filepath.Join(u.config.DownloadDir, asset.Name)
		if u.verifyCachedDownload(dst, asset) == nil {
			continue
		}
		remaining := asset.Size
		if info, err := os.Stat(dst + ".part"); err == nil {
			if info.Size() <= asset.Size {
				remaining -= info.Size()
			}
		} else if !os.IsNotExist(err) {
			return 0, fmt.Errorf("stat partial download %s: %w", asset.Name, err)
		}
		if total > maxInt64-remaining {
			return 0, fmt.Errorf("OTA download size overflow")
		}
		total += remaining
	}
	return total, nil
}

func (u *Updater) downloadCacheBytes() (int64, error) {
	entries, err := os.ReadDir(u.config.DownloadDir)
	if err != nil {
		return 0, fmt.Errorf("read OTA download dir: %w", err)
	}
	var total int64
	for _, entry := range entries {
		if entry.IsDir() || entry.Name() == otaReserveFileName {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			return 0, fmt.Errorf("stat OTA cache %s: %w", entry.Name(), err)
		}
		if total > maxInt64-info.Size() {
			return 0, fmt.Errorf("OTA cache size overflow")
		}
		total += info.Size()
	}
	return total, nil
}

func allocateReserveFile(path string, size int64) error {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return err
	}
	info, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return err
	}
	if info.Size() > size {
		if err := f.Truncate(size); err != nil {
			_ = f.Close()
			return err
		}
	} else if info.Size() < size {
		if _, err := f.Seek(0, io.SeekEnd); err != nil {
			_ = f.Close()
			return err
		}
		zeros := make([]byte, reserveWriteChunk)
		remaining := size - info.Size()
		for remaining > 0 {
			chunk := int64(len(zeros))
			if remaining < chunk {
				chunk = remaining
			}
			written, err := f.Write(zeros[:chunk])
			if err != nil {
				_ = f.Close()
				return err
			}
			if int64(written) != chunk {
				_ = f.Close()
				return io.ErrShortWrite
			}
			remaining -= chunk
		}
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return fsyncDirFor(path)
}
