package agent

import (
	"golang.org/x/sys/unix"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

func TestGPIOWatcherStopJoinsBeforeClosingDescriptor(t *testing.T) {
	path := filepath.Join(t.TempDir(), "value")
	if err := os.WriteFile(path, []byte("1"), 0600); err != nil {
		t.Fatal(err)
	}
	watcher := &GPIOWatcher{valuePath: path, stopChan: make(chan struct{})}
	if err := watcher.Start(); err != nil {
		t.Fatal(err)
	}
	fd := watcher.fd
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(watcher.Stop)
	}
	wg.Wait()
	select {
	case <-watcher.done:
	default:
		t.Fatal("poller still running after Stop")
	}
	if _, err := unix.FcntlInt(uintptr(fd), unix.F_GETFD, 0); err == nil {
		t.Fatal("retired FD is still open")
	}
	if err := watcher.Start(); err == nil {
		t.Fatal("restarted retired watcher")
	}
}
