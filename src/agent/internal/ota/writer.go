package ota

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"
)

const (
	DefaultBootPartitionSize   int64 = 32 << 20
	DefaultOEMPartitionSize    int64 = 256 << 20
	DefaultRootFSPartitionSize int64 = 1536 << 20
)

var DefaultProductionPartitionSizes = map[string]int64{
	"boot_a":   DefaultBootPartitionSize,
	"boot_b":   DefaultBootPartitionSize,
	"oem_a":    DefaultOEMPartitionSize,
	"oem_b":    DefaultOEMPartitionSize,
	"rootfs_a": DefaultRootFSPartitionSize,
	"rootfs_b": DefaultRootFSPartitionSize,
}

type PartitionWriter struct {
	BlockDir       string
	ActiveSlot     Slot
	PartitionSizes map[string]int64
}

type WriteProgress struct {
	Part      string
	BlockName string
	ImagePath string
	Bytes     int64
	Total     int64
	Complete  bool
}

func (w PartitionWriter) WritePart(part string, targetSlot Slot, imagePath string) error {
	return w.WritePartWithProgress(part, targetSlot, imagePath, nil)
}

func (w PartitionWriter) WritePartWithProgress(part string, targetSlot Slot, imagePath string, progress func(WriteProgress)) error {
	blockName, err := w.ResolveBlockName(part, targetSlot)
	if err != nil {
		return err
	}
	if targetSlot == w.ActiveSlot {
		targetSlotName, _ := slotName(targetSlot)
		return fmt.Errorf("refusing to write active slot %s", targetSlotName)
	}
	info, err := os.Stat(imagePath)
	if err != nil {
		return err
	}
	if max, ok := w.partitionSizes()[blockName]; ok && info.Size() > max {
		return fmt.Errorf("image %s size %d is larger than partition %s size %d", imagePath, info.Size(), blockName, max)
	}

	src, err := os.Open(imagePath)
	if err != nil {
		return err
	}
	defer src.Close()
	dstPath := filepath.Join(w.BlockDir, blockName)
	dst, err := os.OpenFile(dstPath, os.O_WRONLY|os.O_TRUNC, 0)
	if err != nil {
		return fmt.Errorf("open target partition %s: %w", dstPath, err)
	}
	copyErr := error(nil)
	reporter := newWriteProgressReporter(progress, part, blockName, imagePath, info.Size(), defaultProgressInterval)
	reader := &cumulativeProgressReader{
		reader: src,
		onRead: reporter.maybeReport,
	}
	if _, copyErr = io.Copy(dst, reader); copyErr == nil {
		copyErr = dst.Sync()
	}
	closeErr := dst.Close()
	if copyErr != nil {
		return copyErr
	}
	if closeErr != nil {
		return closeErr
	}
	reporter.complete(info.Size())
	return nil
}

func (w PartitionWriter) partitionSizes() map[string]int64 {
	sizes := map[string]int64{}
	for name, size := range DefaultProductionPartitionSizes {
		sizes[name] = size
	}
	for name, size := range w.PartitionSizes {
		sizes[name] = size
	}
	return sizes
}

func (w PartitionWriter) ResolveBlockName(part string, targetSlot Slot) (string, error) {
	if targetSlot != SlotA && targetSlot != SlotB {
		return "", fmt.Errorf("invalid target slot %d", targetSlot)
	}
	if part != "boot" && part != "oem" && part != "rootfs" {
		return "", fmt.Errorf("unknown part %q", part)
	}
	targetSlotName, err := slotName(targetSlot)
	if err != nil {
		return "", err
	}
	return part + "_" + targetSlotName, nil
}

type writeProgressReporter struct {
	progress    func(WriteProgress)
	part        string
	blockName   string
	imagePath   string
	total       int64
	interval    time.Duration
	lastReport  time.Time
	nextPercent int64
}

func newWriteProgressReporter(progress func(WriteProgress), part string, blockName string, imagePath string, total int64, interval time.Duration) *writeProgressReporter {
	if interval <= 0 {
		interval = defaultProgressInterval
	}
	return &writeProgressReporter{
		progress:    progress,
		part:        part,
		blockName:   blockName,
		imagePath:   imagePath,
		total:       total,
		interval:    interval,
		lastReport:  time.Now(),
		nextPercent: 10,
	}
}

func (r *writeProgressReporter) maybeReport(bytes int64) {
	if r.progress == nil {
		return
	}
	now := time.Now()
	shouldReport := now.Sub(r.lastReport) >= r.interval
	if r.total > 0 {
		percent := bytes * 100 / r.total
		if percent >= r.nextPercent && percent < 100 {
			shouldReport = true
			for r.nextPercent <= percent {
				r.nextPercent += 10
			}
		}
	}
	if !shouldReport {
		return
	}
	r.lastReport = now
	reportWriteProgress(r.progress, r.part, r.blockName, r.imagePath, bytes, r.total, false)
}

func (r *writeProgressReporter) complete(bytes int64) {
	reportWriteProgress(r.progress, r.part, r.blockName, r.imagePath, bytes, r.total, true)
}

func reportWriteProgress(progress func(WriteProgress), part string, blockName string, imagePath string, bytes int64, total int64, complete bool) {
	if progress == nil {
		return
	}
	progress(WriteProgress{
		Part:      part,
		BlockName: blockName,
		ImagePath: imagePath,
		Bytes:     bytes,
		Total:     total,
		Complete:  complete,
	})
}
