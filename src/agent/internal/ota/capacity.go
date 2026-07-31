package ota

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

const maxInt64 = int64(^uint64(0) >> 1)

type downloadPlan struct {
	assets map[string]ManifestAsset
	state  State
	target Slot
}

func (u *Updater) ensureStorageReady() error {
	mountPoint := filepath.Clean(u.config.StorageMountPoint)
	if u.config.StorageMountPoint == "" {
		return nil
	}
	mounted, err := mountPointIsActive(u.config.MountInfoPath, mountPoint)
	if err != nil {
		return fmt.Errorf("inspect dedicated OTA storage mount: %w", err)
	}
	if !mounted {
		return fmt.Errorf("dedicated OTA storage is not mounted at %s", mountPoint)
	}
	return nil
}

func mountPointIsActive(mountInfoPath, mountPoint string) (bool, error) {
	f, err := os.Open(mountInfoPath)
	if err != nil {
		return false, err
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) >= 5 && filepath.Clean(unescapeMountInfoPath(fields[4])) == mountPoint {
			return true, nil
		}
	}
	if err := scanner.Err(); err != nil {
		return false, err
	}
	return false, nil
}

func unescapeMountInfoPath(path string) string {
	replacer := strings.NewReplacer(
		`\134`, `\`,
		`\040`, " ",
		`\011`, "\t",
		`\012`, "\n",
	)
	return replacer.Replace(path)
}

func pathIsWithin(root, path string) bool {
	root = filepath.Clean(root)
	path = filepath.Clean(path)
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func (u *Updater) ensureDownloadCapacity(plan downloadPlan) error {
	remaining, err := u.remainingDownloadBytes(plan)
	if err != nil {
		return err
	}
	if remaining == 0 {
		u.logf("ota space: no download bytes needed")
		return nil
	}
	if u.config.DownloadSafetyMarginBytes > maxInt64-remaining {
		return fmt.Errorf("OTA download size overflow")
	}
	required := remaining + u.config.DownloadSafetyMarginBytes
	if err := os.MkdirAll(u.config.DownloadDir, 0o755); err != nil {
		return fmt.Errorf("create OTA download dir: %w", err)
	}
	available, err := u.availableBytes(u.config.DownloadDir)
	if err != nil {
		return fmt.Errorf("inspect OTA download capacity: %w", err)
	}
	if available < required {
		return fmt.Errorf(
			"insufficient OTA download space: need %s remaining downloads plus %s safety margin, available %s on %s",
			formatBytes(remaining),
			formatBytes(u.config.DownloadSafetyMarginBytes),
			formatBytes(available),
			u.config.StorageMountPoint,
		)
	}
	u.logf(
		"ota space: available=%s remaining_downloads=%s safety_margin=%s",
		formatBytes(available),
		formatBytes(remaining),
		formatBytes(u.config.DownloadSafetyMarginBytes),
	)
	return nil
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

func filesystemAvailableBytes(path string) (int64, error) {
	var stat syscall.Statfs_t
	if err := syscall.Statfs(path, &stat); err != nil {
		return 0, err
	}
	blockSize := uint64(stat.Bsize)
	availableBlocks := uint64(stat.Bavail)
	if blockSize == 0 || availableBlocks > uint64(maxInt64)/blockSize {
		return 0, fmt.Errorf("filesystem available byte count overflow")
	}
	return int64(availableBlocks * blockSize), nil
}
