package ota

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
)

func VerifyFile(path string, expectedSize int64, expectedSHA256 string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	return VerifyReader(f, expectedSize, expectedSHA256)
}

func VerifyReader(r io.Reader, expectedSize int64, expectedSHA256 string) error {
	h := sha256.New()
	n, err := io.Copy(h, r)
	if err != nil {
		return err
	}
	if expectedSize >= 0 && n != expectedSize {
		return fmt.Errorf("size %d, want %d", n, expectedSize)
	}
	got := hex.EncodeToString(h.Sum(nil))
	if got != expectedSHA256 {
		return fmt.Errorf("sha256 %s, want %s", got, expectedSHA256)
	}
	return nil
}
