package ota

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDefaultProductionRootFSPartitionSizeMatchesBoardConfig(t *testing.T) {
	const wantRootFSSize = int64(1536 << 20)

	if DefaultRootFSPartitionSize != wantRootFSSize {
		t.Fatalf("DefaultRootFSPartitionSize = %d, want %d", DefaultRootFSPartitionSize, wantRootFSSize)
	}
	for _, name := range []string{"rootfs_a", "rootfs_b"} {
		if got := DefaultProductionPartitionSizes[name]; got != wantRootFSSize {
			t.Fatalf("DefaultProductionPartitionSizes[%q] = %d, want %d", name, got, wantRootFSSize)
		}
	}
}

func TestWriterWritesOnlyInactiveCanonicalPartitions(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"boot_b", "oem_b", "rootfs_b"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte{}, 0o644); err != nil {
			t.Fatalf("WriteFile(%s) error = %v", name, err)
		}
	}
	src := filepath.Join(dir, "boot_b.img")
	if err := os.WriteFile(src, []byte("boot image"), 0o644); err != nil {
		t.Fatalf("WriteFile(src) error = %v", err)
	}
	w := PartitionWriter{BlockDir: dir, ActiveSlot: SlotA, PartitionSizes: map[string]int64{"boot_b": 100}}
	if err := w.WritePart("boot", SlotB, src); err != nil {
		t.Fatalf("WritePart() error = %v", err)
	}
	got, err := os.ReadFile(filepath.Join(dir, "boot_b"))
	if err != nil {
		t.Fatalf("ReadFile(block) error = %v", err)
	}
	if string(got) != "boot image" {
		t.Fatalf("block content = %q", got)
	}
}

func TestWriterVerifiesPartitionReadback(t *testing.T) {
	dir := t.TempDir()
	body := []byte("boot image")
	if err := os.WriteFile(filepath.Join(dir, "boot_b"), body, 0o644); err != nil {
		t.Fatalf("WriteFile(block) error = %v", err)
	}
	src := filepath.Join(dir, "boot_b.img")
	if err := os.WriteFile(src, body, 0o644); err != nil {
		t.Fatalf("WriteFile(src) error = %v", err)
	}
	w := PartitionWriter{BlockDir: dir, ActiveSlot: SlotA, PartitionSizes: map[string]int64{"boot_b": 100}}
	if err := w.VerifyPart("boot", SlotB, src, testSHA256Hex(body)); err != nil {
		t.Fatalf("VerifyPart() error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "boot_b"), []byte("corruption"), 0o644); err != nil {
		t.Fatalf("WriteFile(corruption) error = %v", err)
	}
	if err := w.VerifyPart("boot", SlotB, src, testSHA256Hex(body)); err == nil || !strings.Contains(err.Error(), "readback sha256") {
		t.Fatalf("VerifyPart() error = %v, want readback hash mismatch", err)
	}
}

func TestWriterExtractsTarGzImageBeforeWriting(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "rootfs_b"), []byte{}, 0o644); err != nil {
		t.Fatalf("WriteFile(block) error = %v", err)
	}
	src := filepath.Join(dir, "rootfs.img.tar.gz")
	if err := os.WriteFile(src, testTarGzImage(t, "rootfs.img", []byte("rootfs image")), 0o644); err != nil {
		t.Fatalf("WriteFile(src) error = %v", err)
	}
	w := PartitionWriter{BlockDir: dir, ActiveSlot: SlotA, PartitionSizes: map[string]int64{"rootfs_b": 100}}
	if err := w.WritePart("rootfs", SlotB, src); err != nil {
		t.Fatalf("WritePart() error = %v", err)
	}
	got, err := os.ReadFile(filepath.Join(dir, "rootfs_b"))
	if err != nil {
		t.Fatalf("ReadFile(block) error = %v", err)
	}
	if string(got) != "rootfs image" {
		t.Fatalf("block content = %q", got)
	}
}

func TestWriterRejectsOversizedTarGzImageByExtractedSize(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "boot_b"), []byte("old boot"), 0o644); err != nil {
		t.Fatalf("WriteFile(block) error = %v", err)
	}
	src := filepath.Join(dir, "boot_b.img.tar.gz")
	if err := os.WriteFile(src, testTarGzImage(t, "boot_b.img", []byte("12345")), 0o644); err != nil {
		t.Fatalf("WriteFile(src) error = %v", err)
	}
	w := PartitionWriter{BlockDir: dir, ActiveSlot: SlotA, PartitionSizes: map[string]int64{"boot_b": 4}}
	if err := w.WritePart("boot", SlotB, src); err == nil || !strings.Contains(err.Error(), "larger than partition") {
		t.Fatalf("WritePart() error = %v, want extracted size rejection", err)
	}
	got, err := os.ReadFile(filepath.Join(dir, "boot_b"))
	if err != nil {
		t.Fatalf("ReadFile(block) error = %v", err)
	}
	if string(got) != "old boot" {
		t.Fatalf("block content = %q, want unchanged", got)
	}
}

func TestWriterRejectsTarGzImageNameMismatch(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "boot_b"), []byte("old boot"), 0o644); err != nil {
		t.Fatalf("WriteFile(block) error = %v", err)
	}
	src := filepath.Join(dir, "boot_b.img.tar.gz")
	if err := os.WriteFile(src, testTarGzImage(t, "boot_a.img", []byte("boot image")), 0o644); err != nil {
		t.Fatalf("WriteFile(src) error = %v", err)
	}
	w := PartitionWriter{BlockDir: dir, ActiveSlot: SlotA, PartitionSizes: map[string]int64{"boot_b": 100}}
	if err := w.WritePart("boot", SlotB, src); err == nil || !strings.Contains(err.Error(), "want boot_b.img") {
		t.Fatalf("WritePart() error = %v, want image name mismatch", err)
	}
	got, err := os.ReadFile(filepath.Join(dir, "boot_b"))
	if err != nil {
		t.Fatalf("ReadFile(block) error = %v", err)
	}
	if string(got) != "old boot" {
		t.Fatalf("block content = %q, want unchanged", got)
	}
}

func TestWriterRejectsMultiEntryTarGzBeforeWriting(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "rootfs_b"), []byte("old rootfs"), 0o644); err != nil {
		t.Fatalf("WriteFile(block) error = %v", err)
	}
	src := filepath.Join(dir, "rootfs.img.tar.gz")
	archive := testTarGzWithEntries(t, []testTarEntry{
		{name: "rootfs.img", body: []byte("rootfs image")},
		{name: "extra.img", body: []byte("extra image")},
	})
	if err := os.WriteFile(src, archive, 0o644); err != nil {
		t.Fatalf("WriteFile(src) error = %v", err)
	}
	w := PartitionWriter{BlockDir: dir, ActiveSlot: SlotA, PartitionSizes: map[string]int64{"rootfs_b": 100}}
	if err := w.WritePart("rootfs", SlotB, src); err == nil || !strings.Contains(err.Error(), "multiple image files") {
		t.Fatalf("WritePart() error = %v, want multiple image rejection", err)
	}
	got, err := os.ReadFile(filepath.Join(dir, "rootfs_b"))
	if err != nil {
		t.Fatalf("ReadFile(block) error = %v", err)
	}
	if string(got) != "old rootfs" {
		t.Fatalf("block content = %q, want unchanged", got)
	}
}

func TestWriterRejectsShortRawImageRead(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "boot_b"), []byte{}, 0o644); err != nil {
		t.Fatalf("WriteFile(block) error = %v", err)
	}
	src := filepath.Join(dir, "boot_b.img")
	image := bytes.Repeat([]byte("x"), 100*1024)
	if err := os.WriteFile(src, image, 0o644); err != nil {
		t.Fatalf("WriteFile(src) error = %v", err)
	}
	w := PartitionWriter{BlockDir: dir, ActiveSlot: SlotA, PartitionSizes: map[string]int64{"boot_b": int64(len(image))}}
	truncated := false
	var reports []WriteProgress
	err := w.WritePartWithProgress("boot", SlotB, src, func(progress WriteProgress) {
		reports = append(reports, progress)
		if progress.Complete || truncated {
			return
		}
		truncated = true
		if truncateErr := os.Truncate(src, progress.Bytes); truncateErr != nil {
			t.Fatalf("Truncate(src) error = %v", truncateErr)
		}
	})
	if err == nil || !strings.Contains(err.Error(), "written bytes") {
		t.Fatalf("WritePartWithProgress() error = %v, want written byte mismatch", err)
	}
	for _, report := range reports {
		if report.Complete {
			t.Fatalf("progress report = %+v, want no completion on short read", report)
		}
	}
}

func TestWriterReportsProgressAndCompletion(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "boot_b"), []byte{}, 0o644); err != nil {
		t.Fatalf("WriteFile(block) error = %v", err)
	}
	src := filepath.Join(dir, "boot_b.img")
	if err := os.WriteFile(src, []byte("boot image"), 0o644); err != nil {
		t.Fatalf("WriteFile(src) error = %v", err)
	}
	w := PartitionWriter{BlockDir: dir, ActiveSlot: SlotA, PartitionSizes: map[string]int64{"boot_b": 100}}
	var reports []WriteProgress
	err := w.WritePartWithProgress("boot", SlotB, src, func(progress WriteProgress) {
		reports = append(reports, progress)
	})
	if err != nil {
		t.Fatalf("WritePartWithProgress() error = %v", err)
	}
	if len(reports) == 0 {
		t.Fatalf("no progress reports")
	}
	last := reports[len(reports)-1]
	if !last.Complete {
		t.Fatalf("last progress report = %+v, want Complete", last)
	}
	if last.Part != "boot" || last.BlockName != "boot_b" || last.ImagePath != src {
		t.Fatalf("last progress report identity = %+v", last)
	}
	if last.Bytes != int64(len("boot image")) || last.Total != int64(len("boot image")) {
		t.Fatalf("last progress report = %+v, want full byte count", last)
	}
}

func TestWriterRejectsMissingPartitionPath(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "boot_b.img")
	if err := os.WriteFile(src, []byte("boot image"), 0o644); err != nil {
		t.Fatalf("WriteFile(src) error = %v", err)
	}
	w := PartitionWriter{BlockDir: dir, ActiveSlot: SlotA}
	if err := w.WritePart("boot", SlotB, src); err == nil || !strings.Contains(err.Error(), "open target partition") {
		t.Fatalf("missing partition error = %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "boot_b")); !os.IsNotExist(err) {
		t.Fatalf("missing partition path was created, stat err = %v", err)
	}
}

func TestWriterRejectsActiveUnknownAndOversized(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "img")
	if err := os.WriteFile(src, []byte("12345"), 0o644); err != nil {
		t.Fatalf("WriteFile(src) error = %v", err)
	}
	w := PartitionWriter{BlockDir: dir, ActiveSlot: SlotA, PartitionSizes: map[string]int64{"boot_b": 4}}
	if err := w.WritePart("boot", SlotA, src); err == nil || !strings.Contains(err.Error(), "active slot") {
		t.Fatalf("active error = %v", err)
	}
	if err := w.WritePart("env", SlotB, src); err == nil || !strings.Contains(err.Error(), "unknown part") {
		t.Fatalf("unknown error = %v", err)
	}
	if err := w.WritePart("boot", SlotB, src); err == nil || !strings.Contains(err.Error(), "larger than partition") {
		t.Fatalf("oversize error = %v", err)
	}
}

func TestWriterRejectsOversizedDefaultProductionPartition(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "boot_b"), []byte("old boot"), 0o644); err != nil {
		t.Fatalf("WriteFile(block) error = %v", err)
	}
	src := filepath.Join(dir, "boot_b.img")
	if err := os.WriteFile(src, make([]byte, 32<<20+1), 0o644); err != nil {
		t.Fatalf("WriteFile(src) error = %v", err)
	}
	w := PartitionWriter{BlockDir: dir, ActiveSlot: SlotA}
	if err := w.WritePart("boot", SlotB, src); err == nil || !strings.Contains(err.Error(), "larger than partition") {
		t.Fatalf("default oversize error = %v", err)
	}
	got, err := os.ReadFile(filepath.Join(dir, "boot_b"))
	if err != nil {
		t.Fatalf("ReadFile(block) error = %v", err)
	}
	if string(got) != "old boot" {
		t.Fatalf("block content = %q, want unchanged", got)
	}
}

func TestWriterPreservesDefaultLimitsWithPartialOverrides(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "boot_b"), []byte("old boot"), 0o644); err != nil {
		t.Fatalf("WriteFile(block) error = %v", err)
	}
	src := filepath.Join(dir, "boot_b.img")
	if err := os.WriteFile(src, make([]byte, 32<<20+1), 0o644); err != nil {
		t.Fatalf("WriteFile(src) error = %v", err)
	}
	w := PartitionWriter{BlockDir: dir, ActiveSlot: SlotA, PartitionSizes: map[string]int64{"oem_b": 1}}
	if err := w.WritePart("boot", SlotB, src); err == nil || !strings.Contains(err.Error(), "larger than partition") {
		t.Fatalf("partial override oversize error = %v", err)
	}
}

type testTarEntry struct {
	name string
	body []byte
}

func testTarGzWithEntries(t *testing.T, entries []testTarEntry) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for _, entry := range entries {
		if err := tw.WriteHeader(&tar.Header{Name: entry.name, Mode: 0o644, Size: int64(len(entry.body))}); err != nil {
			t.Fatalf("WriteHeader(%s) error = %v", entry.name, err)
		}
		if _, err := tw.Write(entry.body); err != nil {
			t.Fatalf("Write(%s) error = %v", entry.name, err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("Close(tar) error = %v", err)
	}
	if err := gz.Close(); err != nil {
		t.Fatalf("Close(gzip) error = %v", err)
	}
	return buf.Bytes()
}
