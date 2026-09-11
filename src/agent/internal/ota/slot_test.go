package ota

import (
	"bytes"
	"encoding/binary"
	"hash/crc32"
	"os"
	"path/filepath"
	"testing"
)

func TestFactoryABDataRoundTrip(t *testing.T) {
	data := FactoryABData()
	raw := data.Marshal()

	if len(raw) != ABDataSize {
		t.Fatalf("Marshal() length = %d, want %d", len(raw), ABDataSize)
	}
	if !bytes.Equal(raw[:4], []byte{0x00, 'A', 'B', '0'}) {
		t.Fatalf("magic = %q, want \\x00AB0", raw[:4])
	}
	if got, want := binary.BigEndian.Uint32(raw[28:32]), crc32.ChecksumIEEE(raw[:28]); got != want {
		t.Fatalf("encoded CRC = %#x, want %#x", got, want)
	}
	if raw[6] != 0 || raw[7] != 0 {
		t.Fatalf("reserved1 = % x, want zero", raw[6:8])
	}
	if raw[16] != byte(SlotA) {
		t.Fatalf("last_boot = %d, want slot A", raw[16])
	}
	if !bytes.Equal(raw[17:28], make([]byte, 11)) {
		t.Fatalf("reserved2 = % x, want zero", raw[17:28])
	}
	if got, want := raw[8:16], []byte{15, 0, 1, 0, 0, 0, 0, 0}; !bytes.Equal(got, want) {
		t.Fatalf("slot records = % x, want % x", got, want)
	}

	parsed, err := ParseABData(raw)
	if err != nil {
		t.Fatalf("ParseABData() error = %v", err)
	}
	if parsed.Slots[0] != (SlotData{Priority: 15, TriesRemaining: 0, SuccessfulBoot: true}) {
		t.Fatalf("slot A = %+v", parsed.Slots[0])
	}
	if parsed.Slots[1] != (SlotData{Priority: 0, TriesRemaining: 0, SuccessfulBoot: false}) {
		t.Fatalf("slot B = %+v", parsed.Slots[1])
	}
}

func TestParseABDataRejectsInvalidFields(t *testing.T) {
	tests := []struct {
		name   string
		mutate func([]byte)
	}{
		{"bad magic", func(raw []byte) { raw[1] = 'X' }},
		{"bad major", func(raw []byte) { raw[4] = 2 }},
		{"bad minor", func(raw []byte) { raw[5] = 1 }},
		{"nonzero reserved1", func(raw []byte) { raw[6] = 1 }},
		{"priority too large", func(raw []byte) { raw[8] = 16 }},
		{"tries too large", func(raw []byte) { raw[9] = 8 }},
		{"successful not boolean", func(raw []byte) { raw[10] = 2 }},
		{"slot reserved nonzero", func(raw []byte) { raw[11] = 1 }},
		{"last_boot invalid", func(raw []byte) { raw[16] = 2 }},
		{"reserved2 nonzero", func(raw []byte) { raw[17] = 1 }},
		{"successful with tries", func(raw []byte) { raw[9], raw[10] = 1, 1 }},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			raw := FactoryABData().Marshal()
			tt.mutate(raw)
			binary.BigEndian.PutUint32(raw[28:32], crc32.ChecksumIEEE(raw[:28]))
			if _, err := ParseABData(raw); err == nil {
				t.Fatal("ParseABData() error = nil, want rejection")
			}
		})
	}
}

func TestParseABDataReadsLastBootFromByte16(t *testing.T) {
	raw := FactoryABData().Marshal()
	raw[16] = byte(SlotB)
	binary.BigEndian.PutUint32(raw[28:32], crc32.ChecksumIEEE(raw[:28]))

	data, err := ParseABData(raw)
	if err != nil {
		t.Fatalf("ParseABData() error = %v", err)
	}
	if data.LastBoot != SlotB {
		t.Fatalf("LastBoot = %d, want SlotB", data.LastBoot)
	}
}

func TestParseABDataRejectsBadCRC(t *testing.T) {
	raw := FactoryABData().Marshal()
	raw[8] ^= 0xff

	_, err := ParseABData(raw)
	if err == nil {
		t.Fatal("ParseABData() error = nil, want CRC error")
	}
}

func TestSetActiveMakesTargetBootable(t *testing.T) {
	data := FactoryABData()
	if err := data.SetActive(SlotB, 7, false); err != nil {
		t.Fatalf("SetActive() error = %v", err)
	}

	if data.Slots[0] != (SlotData{Priority: 14, TriesRemaining: 0, SuccessfulBoot: true}) {
		t.Fatalf("slot A = %+v", data.Slots[0])
	}
	if data.Slots[1] != (SlotData{Priority: 15, TriesRemaining: 7, SuccessfulBoot: false}) {
		t.Fatalf("slot B = %+v", data.Slots[1])
	}
	if _, err := ParseABData(data.Marshal()); err != nil {
		t.Fatalf("ParseABData(Marshal()) error = %v", err)
	}
}

func TestSetActiveCanSetSuccessful(t *testing.T) {
	data := FactoryABData()
	if err := data.SetActive(SlotB, 0, true); err != nil {
		t.Fatalf("SetActive() error = %v", err)
	}
	if data.Slots[1] != (SlotData{Priority: 15, TriesRemaining: 0, SuccessfulBoot: true}) {
		t.Fatalf("slot B = %+v", data.Slots[1])
	}
}

func TestSetActiveRejectsSuccessfulWithTries(t *testing.T) {
	data := FactoryABData()
	if err := data.SetActive(SlotB, 1, true); err == nil {
		t.Fatal("SetActive(successful=true, tries=1) error = nil, want error")
	}
}

func TestMarkSuccessfulCommitsSlot(t *testing.T) {
	data := FactoryABData()
	if err := data.SetActive(SlotB, 7, false); err != nil {
		t.Fatalf("SetActive() error = %v", err)
	}
	if err := data.MarkSuccessful(SlotB); err != nil {
		t.Fatalf("MarkSuccessful() error = %v", err)
	}

	if data.Slots[1] != (SlotData{Priority: 15, TriesRemaining: 0, SuccessfulBoot: true}) {
		t.Fatalf("slot B = %+v", data.Slots[1])
	}
	if !data.Slots[0].SuccessfulBoot {
		t.Fatalf("slot A successful = false, want previous rollback slot still successful")
	}
	if data.Slots[0].TriesRemaining != 0 {
		t.Fatalf("slot A tries = %d, want unchanged zero", data.Slots[0].TriesRemaining)
	}
}

func TestMarkUnbootablePreservesLayoutAndCRC(t *testing.T) {
	data := FactoryABData()
	if err := data.SetActive(SlotB, 3, false); err != nil {
		t.Fatalf("SetActive() error = %v", err)
	}
	if err := data.MarkUnbootable(SlotB); err != nil {
		t.Fatalf("MarkUnbootable() error = %v", err)
	}
	if got := data.Slots[SlotB]; got != (SlotData{}) {
		t.Fatalf("slot B = %+v, want zero/unbootable", got)
	}
	if data.Bootable(SlotB) {
		t.Fatal("slot B remains bootable")
	}
	raw := data.Marshal()
	if len(raw) != ABDataSize {
		t.Fatalf("metadata size = %d, want %d", len(raw), ABDataSize)
	}
	parsed, err := ParseABData(raw)
	if err != nil {
		t.Fatalf("ParseABData(Marshal()) error = %v", err)
	}
	if parsed.Slots[SlotB] != (SlotData{}) {
		t.Fatalf("parsed slot B = %+v, want zero/unbootable", parsed.Slots[SlotB])
	}
}

func TestShouldTruncateMiscForInitOnlyRegularFiles(t *testing.T) {
	if !shouldTruncateMiscForInit(0) {
		t.Fatal("regular file mode should truncate")
	}
	for _, mode := range []os.FileMode{os.ModeDevice, os.ModeDevice | os.ModeCharDevice, os.ModeNamedPipe, os.ModeSocket} {
		if shouldTruncateMiscForInit(mode) {
			t.Fatalf("mode %v should not truncate", mode)
		}
	}
}

func TestCreateMiscImageWritesFactoryMetadataAtOffset4(t *testing.T) {
	path := filepath.Join(t.TempDir(), "misc.img")
	if err := CreateMiscImage(path, 4*1024*1024); err != nil {
		t.Fatalf("CreateMiscImage() error = %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat misc: %v", err)
	}
	if info.Size() != 4*1024*1024 {
		t.Fatalf("misc size = %d, want 4 MiB", info.Size())
	}
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("open misc: %v", err)
	}
	defer f.Close()
	data, err := ReadABData(f)
	if err != nil {
		t.Fatalf("ReadABData() error = %v", err)
	}
	if data.Slots[0] != (SlotData{Priority: 15, SuccessfulBoot: true}) || data.Slots[1] != (SlotData{}) {
		t.Fatalf("factory slots = %+v", data.Slots)
	}
	raw := make([]byte, ABMetadataOffset+4)
	if _, err := f.ReadAt(raw, 0); err != nil {
		t.Fatalf("read raw misc: %v", err)
	}
	if got := raw[ABMetadataOffset : ABMetadataOffset+4]; !bytes.Equal(got, []byte{0x00, 'A', 'B', '0'}) {
		t.Fatalf("metadata magic at offset %d = %q, want magic", ABMetadataOffset, got)
	}
	if got := bytes.Index(raw[:ABMetadataOffset], []byte{0x00, 'A', 'B', '0'}); got >= 0 {
		t.Fatalf("metadata magic found before SDK offset at byte %d", got)
	}
}

func TestReadWriteABDataAtOffset(t *testing.T) {
	dev := make([]byte, ABMetadataOffset+ABDataSize+16)
	buf := bytes.NewBuffer(dev[:0])

	data := FactoryABData()
	if err := WriteABData(buf, data); err != nil {
		t.Fatalf("WriteABData() error = %v", err)
	}

	read, err := ReadABData(bytes.NewReader(buf.Bytes()))
	if err != nil {
		t.Fatalf("ReadABData() error = %v", err)
	}
	if read.Slots != data.Slots {
		t.Fatalf("read slots = %+v, want %+v", read.Slots, data.Slots)
	}
	if got := buf.Bytes()[ABMetadataOffset : ABMetadataOffset+4]; !bytes.Equal(got, []byte{0x00, 'A', 'B', '0'}) {
		t.Fatalf("metadata offset bytes = %q, want magic", got)
	}
}
