package ota

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
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

func (w PartitionWriter) WritePart(part string, targetSlot Slot, imagePath string) error {
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
	if _, copyErr = io.Copy(dst, src); copyErr == nil {
		copyErr = dst.Sync()
	}
	closeErr := dst.Close()
	if copyErr != nil {
		return copyErr
	}
	return closeErr
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
