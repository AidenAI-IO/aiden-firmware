package ota

import (
	"encoding/binary"
	"errors"
	"fmt"
	"hash/crc32"
	"io"
	"os"
)

const (
	ABMetadataOffset = 4 * 512
	ABDataSize       = 32
	DefaultMiscSize  = 4 * 1024 * 1024
	MaxPriority      = 15
	MaxTries         = 7
)

type Slot int

const (
	SlotA Slot = iota
	SlotB
)

type SlotData struct {
	Priority       uint8
	TriesRemaining uint8
	SuccessfulBoot bool
}

type ABData struct {
	VersionMajor uint8
	VersionMinor uint8
	Slots        [2]SlotData
	LastBoot     Slot
	CRC32        uint32
}

func FactoryABData() ABData {
	return ABData{
		VersionMajor: 1,
		VersionMinor: 0,
		Slots: [2]SlotData{
			{Priority: MaxPriority, SuccessfulBoot: true},
			{},
		},
		LastBoot: SlotA,
	}
}

func ParseABData(raw []byte) (ABData, error) {
	if len(raw) != ABDataSize {
		return ABData{}, fmt.Errorf("AB metadata size %d, want %d", len(raw), ABDataSize)
	}
	if string(raw[:4]) != "\x00AB0" {
		return ABData{}, errors.New("invalid AB metadata magic")
	}
	if raw[4] != 1 || raw[5] != 0 {
		return ABData{}, fmt.Errorf("unsupported AB metadata version %d.%d", raw[4], raw[5])
	}
	if raw[6] != 0 || raw[7] != 0 {
		return ABData{}, errors.New("AB metadata reserved1 bytes are nonzero")
	}
	if raw[16] > byte(SlotB) {
		return ABData{}, fmt.Errorf("invalid AB metadata last_boot %d", raw[16])
	}
	for i, b := range raw[17:28] {
		if b != 0 {
			return ABData{}, fmt.Errorf("AB metadata reserved2 byte %d is nonzero", 17+i)
		}
	}
	storedCRC := binary.BigEndian.Uint32(raw[28:32])
	calculatedCRC := crc32.ChecksumIEEE(raw[:28])
	if storedCRC != calculatedCRC {
		return ABData{}, fmt.Errorf("invalid AB metadata CRC %#x, want %#x", storedCRC, calculatedCRC)
	}

	data := ABData{
		VersionMajor: raw[4],
		VersionMinor: raw[5],
		LastBoot:     Slot(raw[16]),
		CRC32:        storedCRC,
	}
	for i := range data.Slots {
		offset := 8 + i*4
		data.Slots[i] = SlotData{
			Priority:       raw[offset],
			TriesRemaining: raw[offset+1],
			SuccessfulBoot: raw[offset+2] != 0,
		}
		if data.Slots[i].Priority > MaxPriority {
			return ABData{}, fmt.Errorf("slot %d priority %d exceeds %d", i, data.Slots[i].Priority, MaxPriority)
		}
		if data.Slots[i].TriesRemaining > MaxTries {
			return ABData{}, fmt.Errorf("slot %d tries %d exceeds %d", i, data.Slots[i].TriesRemaining, MaxTries)
		}
		if raw[offset+2] > 1 {
			return ABData{}, fmt.Errorf("slot %d successful_boot %d is not 0 or 1", i, raw[offset+2])
		}
		if raw[offset+3] != 0 {
			return ABData{}, fmt.Errorf("slot %d reserved byte is nonzero", i)
		}
		if data.Slots[i].SuccessfulBoot && data.Slots[i].TriesRemaining > 0 {
			return ABData{}, fmt.Errorf("slot %d has successful_boot with nonzero tries", i)
		}
	}
	return data, nil
}

func (d ABData) Marshal() []byte {
	raw := make([]byte, ABDataSize)
	copy(raw[:4], []byte{0x00, 'A', 'B', '0'})
	raw[4] = d.VersionMajor
	raw[5] = d.VersionMinor
	for i, slot := range d.Slots {
		offset := 8 + i*4
		raw[offset] = slot.Priority
		raw[offset+1] = slot.TriesRemaining
		if slot.SuccessfulBoot {
			raw[offset+2] = 1
		}
	}
	raw[16] = byte(d.LastBoot)
	binary.BigEndian.PutUint32(raw[28:32], crc32.ChecksumIEEE(raw[:28]))
	return raw
}

func (d ABData) Bootable(slot Slot) bool {
	if slot != SlotA && slot != SlotB {
		return false
	}
	s := d.Slots[slot]
	return s.Priority > 0 && (s.SuccessfulBoot || s.TriesRemaining > 0)
}

func (d ABData) ActiveSlot() (Slot, bool) {
	best := SlotA
	found := false
	for _, slot := range []Slot{SlotA, SlotB} {
		if !d.Bootable(slot) {
			continue
		}
		if !found || d.Slots[slot].Priority > d.Slots[best].Priority {
			best = slot
			found = true
		}
	}
	return best, found
}

func (d *ABData) SetActive(slot Slot, tries uint8, successful bool) error {
	if slot != SlotA && slot != SlotB {
		return fmt.Errorf("invalid slot %d", slot)
	}
	if tries > MaxTries {
		return fmt.Errorf("tries %d exceeds %d", tries, MaxTries)
	}
	if successful && tries > 0 {
		return fmt.Errorf("successful slot cannot have nonzero tries")
	}

	other := SlotA
	if slot == SlotA {
		other = SlotB
	}
	d.Slots[slot] = SlotData{Priority: MaxPriority, TriesRemaining: tries, SuccessfulBoot: successful}
	if d.Slots[other].Priority >= MaxPriority {
		d.Slots[other].Priority = MaxPriority - 1
	}
	return nil
}

// MarkUnbootable prevents the bootloader from selecting slot while an update
// is in progress. It intentionally preserves the existing 32-byte metadata
// layout and relies on Marshal to refresh the compatible CRC.
func (d *ABData) MarkUnbootable(slot Slot) error {
	if slot != SlotA && slot != SlotB {
		return fmt.Errorf("invalid slot %d", slot)
	}
	d.Slots[slot] = SlotData{}
	return nil
}

func (d *ABData) MarkSuccessful(slot Slot) error {
	if slot != SlotA && slot != SlotB {
		return fmt.Errorf("invalid slot %d", slot)
	}
	d.Slots[slot].SuccessfulBoot = true
	d.Slots[slot].TriesRemaining = 0
	return nil
}

func ReadABData(r io.ReaderAt) (ABData, error) {
	raw := make([]byte, ABDataSize)
	if _, err := r.ReadAt(raw, ABMetadataOffset); err != nil {
		return ABData{}, err
	}
	return ParseABData(raw)
}

func WriteABData(w io.Writer, data ABData) error {
	if _, err := w.Write(make([]byte, ABMetadataOffset)); err != nil {
		return err
	}
	_, err := w.Write(data.Marshal())
	return err
}

func WriteABDataAt(w io.WriterAt, data ABData) error {
	_, err := w.WriteAt(data.Marshal(), ABMetadataOffset)
	return err
}

func CreateMiscImage(path string, size int64) error {
	return InitMisc(path, size)
}

func InitMisc(path string, size int64) error {
	if size <= 0 {
		return fmt.Errorf("misc size must be positive")
	}
	flags := os.O_RDWR | os.O_CREATE
	if info, err := os.Stat(path); err == nil && shouldTruncateMiscForInit(info.Mode()) {
		flags |= os.O_TRUNC
	}
	f, err := os.OpenFile(path, flags, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()

	if info, err := f.Stat(); err == nil && shouldTruncateMiscForInit(info.Mode()) {
		if err := f.Truncate(size); err != nil {
			return err
		}
	}
	return WriteABDataAt(f, FactoryABData())
}

func shouldTruncateMiscForInit(mode os.FileMode) bool {
	return mode.IsRegular()
}
