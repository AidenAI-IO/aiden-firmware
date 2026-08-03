package ota

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

// TestDownloadENOSPCCleansPartFile tests Step 3: cleanup .part file on ENOSPC.
func TestDownloadENOSPCCleansPartFile(t *testing.T) {
	tests := []struct {
		name           string
		simulateError  error
		expectPartKept bool
	}{
		{
			name:           "ENOSPC deletes .part file",
			simulateError:  syscall.ENOSPC,
			expectPartKept: false,
		},
		{
			name:           "wrapped ENOSPC deletes .part file",
			simulateError:  fmt.Errorf("sync download: %w", syscall.ENOSPC),
			expectPartKept: false,
		},
		{
			name:           "network error keeps .part file for resume",
			simulateError:  fmt.Errorf("connection reset"),
			expectPartKept: true,
		},
		{
			name:           "timeout keeps .part file for resume",
			simulateError:  context.DeadlineExceeded,
			expectPartKept: true,
		},
		{
			name:           "cancellation keeps .part file for resume",
			simulateError:  context.Canceled,
			expectPartKept: true,
		},
		{
			name:           "system timeout keeps .part file for resume",
			simulateError:  syscall.ETIMEDOUT,
			expectPartKept: true,
		},
		{
			name:           "EOF keeps .part file for resume",
			simulateError:  io.EOF,
			expectPartKept: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tmpDir := t.TempDir()
			dst := filepath.Join(tmpDir, "test.img.tar.gz")
			part := dst + ".part"

			// Simulate a failed download by creating a .part file
			if err := os.WriteFile(part, []byte("partial-data"), 0o644); err != nil {
				t.Fatalf("WriteFile(.part) error = %v", err)
			}

			// Call handleDownloadError with the simulated error
			handleDownloadError(part, tt.simulateError)

			// Check if .part file was deleted or kept
			_, err := os.Stat(part)
			partExists := !os.IsNotExist(err)

			if tt.expectPartKept && !partExists {
				t.Errorf(".part file was deleted, expected it to be kept for resume")
			}
			if !tt.expectPartKept && partExists {
				t.Errorf(".part file still exists, expected it to be deleted on ENOSPC")
			}
		})
	}
}
