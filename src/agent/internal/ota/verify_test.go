package ota

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestVerifyFileSHA256AndSize(t *testing.T) {
	path := filepath.Join(t.TempDir(), "asset.img")
	content := []byte("verified bytes")
	if err := os.WriteFile(path, content, 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	hash := sha256.Sum256(content)

	if err := VerifyFile(path, int64(len(content)), hex.EncodeToString(hash[:])); err != nil {
		t.Fatalf("VerifyFile() error = %v", err)
	}
	if err := VerifyFile(path, int64(len(content)+1), hex.EncodeToString(hash[:])); err == nil || !strings.Contains(err.Error(), "size") {
		t.Fatalf("size error = %v", err)
	}
	if err := VerifyFile(path, int64(len(content)), strings.Repeat("0", 64)); err == nil || !strings.Contains(err.Error(), "sha256") {
		t.Fatalf("hash error = %v", err)
	}
}
