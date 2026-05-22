package agent

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	"golang.org/x/sys/unix"
)

type FileLock struct {
	path string
	file *os.File
}

func NewFileLock(dir string) *FileLock {
	return &FileLock{path: filepath.Join(dir, "memory.lock")}
}

func (fl *FileLock) Lock(timeout time.Duration) error {
	if err := os.MkdirAll(filepath.Dir(fl.path), 0o755); err != nil {
		return fmt.Errorf("create lock directory: %w", err)
	}

	f, err := os.OpenFile(fl.path, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return fmt.Errorf("open lock file: %w", err)
	}

	deadline := time.Now().Add(timeout)
	backoff := 5 * time.Millisecond

	for {
		err = unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB)
		if err == nil {
			fl.file = f
			return nil
		}

		if time.Now().After(deadline) {
			f.Close()
			return fmt.Errorf("acquire file lock %s: timed out after %v", fl.path, timeout)
		}

		time.Sleep(backoff)
		backoff *= 2
		if backoff > 200*time.Millisecond {
			backoff = 200 * time.Millisecond
		}
	}
}

func (fl *FileLock) Unlock() error {
	if fl.file == nil {
		return nil
	}
	err := unix.Flock(int(fl.file.Fd()), unix.LOCK_UN)
	closeErr := fl.file.Close()
	fl.file = nil
	if err != nil {
		return fmt.Errorf("release file lock: %w", err)
	}
	if closeErr != nil {
		return fmt.Errorf("close lock file: %w", closeErr)
	}
	return nil
}
