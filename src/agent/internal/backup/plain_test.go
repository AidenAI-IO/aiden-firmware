package backup

import (
	"bytes"
	"context"
	"encoding/binary"
	"path/filepath"
	"testing"
	"time"
)

func TestPlainArchiveRoundTripAndCorruption(t *testing.T) {
	roots := testRoots(t)
	writeTestFile(t, filepath.Join(roots.Userdata, "agent/memory/profile.md"), "private data\n", 0o600)
	plan, err := NewPlanner(roots).Plan(context.Background(), PlanOptions{
		Mode: ModeSameDevice, Components: []ComponentID{ComponentAgentMemory},
		Source: SourceIdentity{HardwareID: "board-1"},
	})
	if err != nil {
		t.Fatal(err)
	}
	material := NewPlainMaterial(plan.Manifest.CreatedAt)
	var archive bytes.Buffer
	if err := WriteArchive(context.Background(), &archive, plan, material, nil); err != nil {
		t.Fatal(err)
	}
	result, err := VerifyArchive(context.Background(), bytes.NewReader(archive.Bytes()), nil)
	if err != nil {
		t.Fatal(err)
	}
	if result.Header.Protection.Algorithm != "sha256-chunked" || result.Header.Protection.KDF != "none" {
		t.Fatalf("unexpected protection: %+v", result.Header.Protection)
	}
	if result.Manifest.Mode != ModeSameDevice || result.Manifest.Source.HardwareID != "board-1" {
		t.Fatal("same-device identity lost")
	}
	// Raw frame payload is a gzip stream, without encryption or hidden keys.
	data := archive.Bytes()
	start := headerPrefixSize + int(binary.BigEndian.Uint32(data[10:14])) + recordHeaderSize
	if !bytes.Equal(data[start:start+2], []byte{0x1f, 0x8b}) {
		t.Fatal("plain payload is not gzip")
	}
	tampered := append([]byte(nil), data...)
	tampered[start+5] ^= 1
	if _, err := VerifyArchive(context.Background(), bytes.NewReader(tampered), nil); ErrorCode(err) != "hash_mismatch" {
		t.Fatalf("corrupted frame error: %v", err)
	}
	if _, err := VerifyArchive(context.Background(), bytes.NewReader(data[:len(data)-1]), nil); ErrorCode(err) != "archive_truncated" {
		t.Fatalf("truncated footer error: %v", err)
	}
	if _, err := VerifyArchive(context.Background(), bytes.NewReader(append(append([]byte(nil), data...), 1)), nil); err == nil {
		t.Fatal("trailing data accepted")
	}
}

func TestFullDefaultComponentsIncludesAdvanced(t *testing.T) {
	for _, sd := range []bool{false, true} {
		selected := map[ComponentID]bool{}
		for _, id := range DefaultComponents(ModeSameDevice, sd) {
			selected[id] = true
		}
		for _, def := range ComponentDefinitions() {
			if selected[def.ID] != (!def.RequiresSD || sd) {
				t.Fatalf("SD=%v component=%s selected=%v", sd, def.ID, selected[def.ID])
			}
		}
	}
}

func TestPlainHeaderRejectsKeyDerivationParameters(t *testing.T) {
	header := NewPlainMaterial(time.Time{}).Header
	header.Protection.MemoryKiB = DefaultMemoryKiB
	if _, err := DeriveKeyMaterial(header, nil); err == nil {
		t.Fatal("plain header accepted KDF parameters")
	}
}
