package ota

import (
	"archive/tar"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
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
	src, imageSize, err := openPartitionImage(imagePath)
	if err != nil {
		return err
	}
	if max, ok := w.partitionSizes()[blockName]; ok && imageSize > max {
		_ = src.Close()
		return fmt.Errorf("image %s size %d is larger than partition %s size %d", imagePath, imageSize, blockName, max)
	}

	dstPath := filepath.Join(w.BlockDir, blockName)
	dst, err := os.OpenFile(dstPath, os.O_WRONLY|os.O_TRUNC, 0)
	if err != nil {
		_ = src.Close()
		return fmt.Errorf("open target partition %s: %w", dstPath, err)
	}
	copyErr := error(nil)
	reporter := newWriteProgressReporter(progress, part, blockName, imagePath, imageSize, defaultProgressInterval)
	reader := &cumulativeProgressReader{
		reader: src,
		onRead: reporter.maybeReport,
	}
	var written int64
	if written, copyErr = io.Copy(dst, reader); copyErr == nil {
		if written != imageSize {
			copyErr = fmt.Errorf("written bytes %d do not match expected image size %d for %s", written, imageSize, imagePath)
		} else {
			copyErr = dst.Sync()
		}
	}
	srcCloseErr := src.Close()
	closeErr := dst.Close()
	if copyErr != nil {
		return copyErr
	}
	if srcCloseErr != nil {
		return srcCloseErr
	}
	if closeErr != nil {
		return closeErr
	}
	reporter.complete(written)
	return nil
}

func openPartitionImage(path string) (io.ReadCloser, int64, error) {
	if isTarGzImagePath(path) {
		return openTarGzPartitionImage(path)
	}
	src, err := os.Open(path)
	if err != nil {
		return nil, 0, err
	}
	info, err := src.Stat()
	if err != nil {
		_ = src.Close()
		return nil, 0, err
	}
	return src, info.Size(), nil
}

func isTarGzImagePath(path string) bool {
	return strings.HasSuffix(strings.ToLower(path), ".tar.gz")
}

func openTarGzPartitionImage(path string) (io.ReadCloser, int64, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, 0, err
	}
	expectedImageName := expectedTarGzImageName(path)
	if !strings.HasSuffix(strings.ToLower(expectedImageName), ".img") {
		_ = file.Close()
		return nil, 0, fmt.Errorf("tar.gz image %s does not name an .img file", path)
	}
	imageSize, err := validateTarGzPartitionImage(file, path, expectedImageName)
	if err != nil {
		_ = file.Close()
		return nil, 0, err
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		_ = file.Close()
		return nil, 0, fmt.Errorf("seek tar.gz image %s: %w", path, err)
	}
	gz, err := gzip.NewReader(file)
	if err != nil {
		_ = file.Close()
		return nil, 0, fmt.Errorf("open gzip image %s: %w", path, err)
	}
	tr := tar.NewReader(gz)
	for {
		header, err := tr.Next()
		if err == io.EOF {
			_ = gz.Close()
			_ = file.Close()
			return nil, 0, fmt.Errorf("tar.gz image %s contains no .img file", path)
		}
		if err != nil {
			_ = gz.Close()
			_ = file.Close()
			return nil, 0, fmt.Errorf("read tar.gz image %s: %w", path, err)
		}
		if header.FileInfo().IsDir() {
			continue
		}
		if header.Typeflag != tar.TypeReg && header.Typeflag != tar.TypeRegA {
			_ = gz.Close()
			_ = file.Close()
			return nil, 0, fmt.Errorf("tar.gz image %s contains unsupported entry %q", path, header.Name)
		}
		if filepath.Base(header.Name) != expectedImageName {
			_ = gz.Close()
			_ = file.Close()
			return nil, 0, fmt.Errorf("tar.gz image %s entry %q, want %s", path, header.Name, expectedImageName)
		}
		return &tarGzImageReader{path: path, file: file, gz: gz, tar: tr}, imageSize, nil
	}
}

func validateTarGzPartitionImage(file *os.File, path string, expectedImageName string) (int64, error) {
	gz, err := gzip.NewReader(file)
	if err != nil {
		return 0, fmt.Errorf("open gzip image %s: %w", path, err)
	}
	tr := tar.NewReader(gz)
	imageSize, scanErr := scanTarGzPartitionImage(tr, path, expectedImageName)
	closeErr := gz.Close()
	if scanErr != nil {
		return 0, scanErr
	}
	if closeErr != nil {
		return 0, closeErr
	}
	return imageSize, nil
}

func scanTarGzPartitionImage(tr *tar.Reader, path string, expectedImageName string) (int64, error) {
	foundImage := false
	var imageSize int64
	for {
		header, err := tr.Next()
		if err == io.EOF {
			if !foundImage {
				return 0, fmt.Errorf("tar.gz image %s contains no .img file", path)
			}
			return imageSize, nil
		}
		if err != nil {
			return 0, fmt.Errorf("read tar.gz image %s: %w", path, err)
		}
		if header.FileInfo().IsDir() {
			continue
		}
		if header.Typeflag != tar.TypeReg && header.Typeflag != tar.TypeRegA {
			return 0, fmt.Errorf("tar.gz image %s contains unsupported entry %q", path, header.Name)
		}
		if foundImage {
			return 0, fmt.Errorf("tar.gz image %s contains multiple image files", path)
		}
		if filepath.Base(header.Name) != expectedImageName {
			return 0, fmt.Errorf("tar.gz image %s entry %q, want %s", path, header.Name, expectedImageName)
		}
		foundImage = true
		imageSize = header.Size
	}
}

func expectedTarGzImageName(path string) string {
	base := filepath.Base(path)
	return base[:len(base)-len(".tar.gz")]
}

func verifyPartitionImage(path string, expectedSHA256 string) error {
	src, _, err := openPartitionImage(path)
	if err != nil {
		return err
	}
	h := sha256.New()
	copyErr := error(nil)
	if _, copyErr = io.Copy(h, src); copyErr == nil {
		copyErr = src.Close()
	} else {
		_ = src.Close()
	}
	if copyErr != nil {
		return copyErr
	}
	got := hex.EncodeToString(h.Sum(nil))
	if got != expectedSHA256 {
		return fmt.Errorf("image sha256 %s, want %s", got, expectedSHA256)
	}
	return nil
}

type tarGzImageReader struct {
	path       string
	file       *os.File
	gz         *gzip.Reader
	tar        *tar.Reader
	done       bool
	pendingErr error
}

func (r *tarGzImageReader) Read(p []byte) (int, error) {
	if r.pendingErr != nil {
		err := r.pendingErr
		r.pendingErr = nil
		return 0, err
	}
	if r.done {
		return 0, io.EOF
	}
	n, err := r.tar.Read(p)
	if err != io.EOF {
		return n, err
	}
	r.done = true
	if tailErr := r.ensureNoExtraRegularFiles(); tailErr != nil {
		if n > 0 {
			r.pendingErr = tailErr
			return n, nil
		}
		return 0, tailErr
	}
	if n > 0 {
		return n, nil
	}
	return 0, io.EOF
}

func (r *tarGzImageReader) Close() error {
	gzipErr := r.gz.Close()
	fileErr := r.file.Close()
	if gzipErr != nil {
		return gzipErr
	}
	return fileErr
}

func (r *tarGzImageReader) ensureNoExtraRegularFiles() error {
	for {
		header, err := r.tar.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return fmt.Errorf("read tar.gz image %s: %w", r.path, err)
		}
		if header.FileInfo().IsDir() {
			continue
		}
		if header.Typeflag == tar.TypeReg || header.Typeflag == tar.TypeRegA {
			return fmt.Errorf("tar.gz image %s contains multiple image files", r.path)
		}
		return fmt.Errorf("tar.gz image %s contains unsupported entry %q", r.path, header.Name)
	}
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
