package ota

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestCheckDownloadSpace tests Step 2: pre-download space check.
func TestCheckDownloadSpace(t *testing.T) {
	tests := []struct {
		name            string
		manifest        Manifest
		skipParts       []string // parts to skip (hash matches)
		wantErr         bool
		wantErrContains string
	}{
		{
			name: "sufficient space",
			manifest: Manifest{
				Version:   "test-v1",
				BuildTime: "2026-05-21T12:00:00Z",
				Parts: []ManifestPart{
					{
						Name: "oem",
						Asset: &ManifestAsset{
							Name:   "oem.img.tar.gz",
							Size:   1 << 20, // 1 MB
							SHA256: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
						},
					},
				},
			},
			wantErr: false,
		},
		{
			name: "excludes already downloaded and verified assets",
			manifest: Manifest{
				Version:   "test-v1",
				BuildTime: "2026-05-21T12:00:00Z",
				Parts: []ManifestPart{
					{
						Name: "oem",
						Asset: &ManifestAsset{
							Name:   "oem.img.tar.gz",
							Size:   1 << 20, // 1 MB
							SHA256: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
						},
					},
					{
						Name: "rootfs",
						Asset: &ManifestAsset{
							Name:   "rootfs.img.tar.gz",
							Size:   1 << 40, // 1 TB - too large, but will be skipped
							SHA256: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
						},
					},
				},
			},
			skipParts: []string{"rootfs"}, // rootfs hash matches, won't be downloaded
			wantErr:   false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			env := newUpdaterTestEnv(t)

			// Setup state with hash matches for skipParts
			if len(tt.skipParts) > 0 {
				state := env.state
				if state.Slots == nil {
					state.Slots = map[string]SlotPartitionInfo{}
				}
				slotInfo := SlotPartitionInfo{
					Partitions: map[string]PartitionVersion{},
				}
				for _, partName := range tt.skipParts {
					// Find the asset SHA256 for this part
					for _, part := range tt.manifest.Parts {
						if part.Name == partName && part.Asset != nil {
							slotInfo.Partitions[partName] = PartitionVersion{
								Version: tt.manifest.Version,
								Hash:    part.Asset.SHA256,
							}
							break
						}
					}
				}
				state.Slots["b"] = slotInfo
				env.state = state
				env.saveState(t)
			}

			updater := env.updater()
			err := updater.checkDownloadSpace(context.Background(), tt.manifest, SlotB)

			if tt.wantErr {
				if err == nil {
					t.Fatalf("checkDownloadSpace() expected error, got nil")
				}
				if tt.wantErrContains != "" && !strings.Contains(err.Error(), tt.wantErrContains) {
					t.Errorf("checkDownloadSpace() error = %v, want contains %q", err, tt.wantErrContains)
				}
			} else {
				if err != nil {
					t.Fatalf("checkDownloadSpace() error = %v, want nil", err)
				}
			}
		})
	}
}

// TestStatfsAvailableSpace tests the statfs helper function.
func TestStatfsAvailableSpace(t *testing.T) {
	tmpDir := t.TempDir()

	available, err := statfsAvailableSpace(tmpDir)
	if err != nil {
		t.Fatalf("statfsAvailableSpace() error = %v", err)
	}

	if available <= 0 {
		t.Errorf("statfsAvailableSpace() = %d, want > 0", available)
	}

	// Create a file to consume some space, then check again
	largeFile := filepath.Join(tmpDir, "large.dat")
	data := make([]byte, 1<<20) // 1 MB
	if err := os.WriteFile(largeFile, data, 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	available2, err := statfsAvailableSpace(tmpDir)
	if err != nil {
		t.Fatalf("statfsAvailableSpace() after write error = %v", err)
	}

	if available2 >= available {
		t.Errorf("statfsAvailableSpace() after write = %d, should be less than %d", available2, available)
	}
}

// TestFormatBytesHuman tests human-readable byte formatting.
func TestFormatBytesHuman(t *testing.T) {
	tests := []struct {
		bytes int64
		want  string
	}{
		{0, "0 B"},
		{1023, "1023 B"},
		{1024, "1.0 KB"},
		{1536, "1.5 KB"},
		{1 << 20, "1.0 MB"},
		{26 * 1 << 20, "26.0 MB"},
		{1 << 30, "1.0 GB"},
		{1 << 40, "1.0 TB"},
	}

	for _, tt := range tests {
		t.Run(fmt.Sprintf("%d", tt.bytes), func(t *testing.T) {
			got := formatBytesHuman(tt.bytes)
			if got != tt.want {
				t.Errorf("formatBytesHuman(%d) = %q, want %q", tt.bytes, got, tt.want)
			}
		})
	}
}
