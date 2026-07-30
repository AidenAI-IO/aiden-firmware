package ota

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// partNameFromAsset extracts part name from asset filename (e.g., "oem.img.tar.gz" -> "oem")
func partNameFromAsset(assetName string) string {
	base := strings.TrimSuffix(assetName, ".img.tar.gz")
	base = strings.TrimSuffix(base, ".img")
	// Handle boot_a/boot_b -> boot, oem_a/oem_b -> oem, etc
	if strings.HasSuffix(base, "_a") || strings.HasSuffix(base, "_b") {
		return base[:len(base)-2]
	}
	return base
}

// TestCleanupOldDownloadCache tests Step 1: cleanup of old download cache before downloading.
func TestCleanupOldDownloadCache(t *testing.T) {
	tests := []struct {
		name               string
		existingFiles      map[string]string // filename -> content
		currentManifest    []string          // asset names in current manifest
		expectedRemaining  []string          // files that should remain
		expectedDeleted    []string          // files that should be deleted
		downloadDirMissing bool              // if true, download dir doesn't exist
	}{
		{
			name: "removes old packages not in current manifest",
			existingFiles: map[string]string{
				"oem.img.tar.gz":     "old-oem-data",
				"rootfs.img.tar.gz":  "old-rootfs-data",
				"boot.img.tar.gz":    "obsolete-boot",
				"unknown.img.tar.gz": "unknown-file",
			},
			currentManifest: []string{
				"oem.img.tar.gz",
				"rootfs.img.tar.gz",
			},
			expectedRemaining: []string{
				"oem.img.tar.gz",
				"rootfs.img.tar.gz",
			},
			expectedDeleted: []string{
				"boot.img.tar.gz",
				"unknown.img.tar.gz",
			},
		},
		{
			name: "removes old .part files not in current manifest",
			existingFiles: map[string]string{
				"oem.img.tar.gz":       "current-oem",
				"oem.img.tar.gz.part":  "current-oem-partial",
				"boot.img.tar.gz.part": "old-boot-partial",
			},
			currentManifest: []string{
				"oem.img.tar.gz",
			},
			expectedRemaining: []string{
				"oem.img.tar.gz",
				"oem.img.tar.gz.part",
			},
			expectedDeleted: []string{
				"boot.img.tar.gz.part",
			},
		},
		{
			name: "preserves .part files matching current manifest",
			existingFiles: map[string]string{
				"oem.img.tar.gz.part":    "partial-download",
				"rootfs.img.tar.gz.part": "partial-rootfs",
			},
			currentManifest: []string{
				"oem.img.tar.gz",
				"rootfs.img.tar.gz",
			},
			expectedRemaining: []string{
				"oem.img.tar.gz.part",
				"rootfs.img.tar.gz.part",
			},
			expectedDeleted: []string{},
		},
		{
			name:               "handles missing download directory gracefully",
			existingFiles:      map[string]string{},
			currentManifest:    []string{"oem.img.tar.gz"},
			expectedRemaining:  []string{},
			expectedDeleted:    []string{},
			downloadDirMissing: true,
		},
		{
			name: "removes all files when manifest is empty",
			existingFiles: map[string]string{
				"oem.img.tar.gz":       "old",
				"rootfs.img.tar.gz":    "old",
				"boot.img.tar.gz.part": "old-partial",
			},
			currentManifest:   []string{},
			expectedRemaining: []string{},
			expectedDeleted: []string{
				"oem.img.tar.gz",
				"rootfs.img.tar.gz",
				"boot.img.tar.gz.part",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			env := newUpdaterTestEnv(t)

			// Setup: create existing files in download dir
			if !tt.downloadDirMissing {
				if err := os.MkdirAll(env.downloadDir, 0o755); err != nil {
					t.Fatalf("MkdirAll(downloadDir) error = %v", err)
				}
				for name, content := range tt.existingFiles {
					path := filepath.Join(env.downloadDir, name)
					if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
						t.Fatalf("WriteFile(%s) error = %v", name, err)
					}
				}
			}

			// Build manifest with current assets
			manifest := Manifest{
				Version:   "test-version",
				BuildTime: "2026-05-21T12:00:00Z",
				Parts:     []ManifestPart{},
			}
			for _, assetName := range tt.currentManifest {
				manifest.Parts = append(manifest.Parts, ManifestPart{
					Name: partNameFromAsset(assetName),
					Asset: &ManifestAsset{
						Name:   assetName,
						Size:   100,
						SHA256: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
					},
				})
			}

			// Execute cleanup
			updater := env.updater()
			if err := updater.cleanupOldDownloadCache(context.Background(), manifest); err != nil {
				t.Fatalf("cleanupOldDownloadCache() error = %v", err)
			}

			// Verify remaining files
			if !tt.downloadDirMissing {
				entries, err := os.ReadDir(env.downloadDir)
				if err != nil && !os.IsNotExist(err) {
					t.Fatalf("ReadDir(downloadDir) error = %v", err)
				}
				remaining := []string{}
				for _, entry := range entries {
					remaining = append(remaining, entry.Name())
				}

				// Check expected remaining
				for _, expectedFile := range tt.expectedRemaining {
					found := false
					for _, actualFile := range remaining {
						if actualFile == expectedFile {
							found = true
							break
						}
					}
					if !found {
						t.Errorf("expected file %q to remain, but it was deleted", expectedFile)
					}
				}

				// Check expected deleted
				for _, deletedFile := range tt.expectedDeleted {
					for _, actualFile := range remaining {
						if actualFile == deletedFile {
							t.Errorf("expected file %q to be deleted, but it remains", deletedFile)
						}
					}
				}
			}
		})
	}
}
