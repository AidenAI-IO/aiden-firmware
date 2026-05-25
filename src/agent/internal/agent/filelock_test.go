package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func TestFileLockBasicLockUnlock(t *testing.T) {
	dir := t.TempDir()
	fl := NewFileLock(dir)

	if err := fl.Lock(time.Second); err != nil {
		t.Fatalf("Lock() error = %v", err)
	}
	if err := fl.Unlock(); err != nil {
		t.Fatalf("Unlock() error = %v", err)
	}
}

func TestFileLockTimeout(t *testing.T) {
	dir := t.TempDir()

	fl1 := NewFileLock(dir)
	if err := fl1.Lock(time.Second); err != nil {
		t.Fatalf("first Lock() error = %v", err)
	}
	defer fl1.Unlock()

	fl2 := NewFileLock(dir)
	start := time.Now()
	err := fl2.Lock(100 * time.Millisecond)
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("expected timeout error, got nil")
	}
	if elapsed < 80*time.Millisecond {
		t.Fatalf("expected to wait at least ~100ms, waited %v", elapsed)
	}
	if !contains(err.Error(), "timed out") {
		t.Fatalf("expected timeout message, got %q", err.Error())
	}
}

func TestFileLockReleasedAfterUnlock(t *testing.T) {
	dir := t.TempDir()

	fl1 := NewFileLock(dir)
	if err := fl1.Lock(time.Second); err != nil {
		t.Fatalf("Lock() error = %v", err)
	}
	if err := fl1.Unlock(); err != nil {
		t.Fatalf("Unlock() error = %v", err)
	}

	fl2 := NewFileLock(dir)
	if err := fl2.Lock(100 * time.Millisecond); err != nil {
		t.Fatalf("second Lock() after unlock should succeed, got: %v", err)
	}
	fl2.Unlock()
}

func TestMemoryManagerConcurrentGoroutines(t *testing.T) {
	dir := t.TempDir()
	const goroutines = 20
	const iterations = 10

	var wg sync.WaitGroup
	errs := make(chan error, goroutines*iterations)

	for g := range goroutines {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for i := range iterations {
				mgr := NewMemoryManager(dir)
				records := []MessageRecord{
					{Role: "human", Content: fmt.Sprintf("goroutine-%d-iter-%d", id, i)},
					{Role: "ai", Content: fmt.Sprintf("response-%d-%d", id, i)},
				}
				if err := mgr.persistSnapshot("shared", records); err != nil {
					errs <- fmt.Errorf("goroutine %d iter %d persist: %w", id, i, err)
					return
				}
			}
		}(g)
	}

	wg.Wait()
	close(errs)

	for err := range errs {
		t.Fatal(err)
	}

	data, err := os.ReadFile(filepath.Join(dir, "shared.json"))
	if err != nil {
		t.Fatalf("read final snapshot: %v", err)
	}
	var records []MessageRecord
	if err := json.Unmarshal(data, &records); err != nil {
		t.Fatalf("final snapshot is not valid JSON: %v\nraw: %s", err, string(data))
	}
	if len(records) != 2 {
		t.Fatalf("expected 2 records in final snapshot, got %d", len(records))
	}
}

func TestMemoryManagerConcurrentMultiProcess(t *testing.T) {
	if os.Getenv("MEMORY_LOCK_CHILD") == "1" {
		runChildProcess()
		return
	}

	dir := t.TempDir()
	const procs = 5
	const writesPerProc = 20

	var wg sync.WaitGroup
	errs := make(chan error, procs)

	for p := range procs {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			cmd := exec.Command(os.Args[0],
				"-test.run=TestMemoryManagerConcurrentMultiProcess",
				"-test.v",
			)
			cmd.Env = append(os.Environ(),
				"MEMORY_LOCK_CHILD=1",
				fmt.Sprintf("MEMORY_LOCK_DIR=%s", dir),
				fmt.Sprintf("MEMORY_LOCK_ID=%d", id),
				fmt.Sprintf("MEMORY_LOCK_WRITES=%d", writesPerProc),
			)
			out, err := cmd.CombinedOutput()
			if err != nil {
				errs <- fmt.Errorf("child %d failed: %v\noutput: %s", id, err, out)
			}
		}(p)
	}

	wg.Wait()
	close(errs)

	for err := range errs {
		t.Fatal(err)
	}

	data, err := os.ReadFile(filepath.Join(dir, "shared.json"))
	if err != nil {
		t.Fatalf("read final snapshot: %v", err)
	}
	var records []MessageRecord
	if err := json.Unmarshal(data, &records); err != nil {
		t.Fatalf("final snapshot corrupted: %v\nraw: %s", err, string(data))
	}
	if len(records) != 2 {
		t.Fatalf("expected 2 records, got %d", len(records))
	}
}

func runChildProcess() {
	dir := os.Getenv("MEMORY_LOCK_DIR")
	id := os.Getenv("MEMORY_LOCK_ID")
	writes := 20
	fmt.Sscanf(os.Getenv("MEMORY_LOCK_WRITES"), "%d", &writes)

	mgr := NewMemoryManager(dir)
	for i := range writes {
		records := []MessageRecord{
			{Role: "human", Content: fmt.Sprintf("proc-%s-write-%d", id, i)},
			{Role: "ai", Content: fmt.Sprintf("reply-%s-%d", id, i)},
		}
		if err := mgr.persistSnapshot("shared", records); err != nil {
			fmt.Fprintf(os.Stderr, "persist error: %v\n", err)
			os.Exit(1)
		}
	}
}

func TestMemoryManagerNoDeadlockMultipleAgents(t *testing.T) {
	dir := t.TempDir()
	const goroutines = 10

	var wg sync.WaitGroup
	done := make(chan struct{})

	for g := range goroutines {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			mgr := NewMemoryManager(dir)
			agentName := fmt.Sprintf("agent-%d", id)
			_, err := mgr.Get(agentName, MemoryConfig{Type: "buffer"})
			if err != nil {
				t.Errorf("Get for %s: %v", agentName, err)
				return
			}
			if err := mgr.Save(context.Background(), agentName); err != nil {
				t.Errorf("Save for %s: %v", agentName, err)
				return
			}
			if err := mgr.ClearSession(context.Background(), agentName); err != nil {
				t.Errorf("ClearSession for %s: %v", agentName, err)
			}
		}(g)
	}

	go func() {
		wg.Wait()
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(30 * time.Second):
		t.Fatal("deadlock detected: test did not complete within 30 seconds")
	}
}

func TestMemoryManagerConcurrentReadWrite(t *testing.T) {
	dir := t.TempDir()
	const goroutines = 10
	const iterations = 20

	mgr := NewMemoryManager(dir)
	records := []MessageRecord{
		{Role: "human", Content: "seed"},
		{Role: "ai", Content: "seed-reply"},
	}
	if err := mgr.persistSnapshot("default", records); err != nil {
		t.Fatalf("seed persist: %v", err)
	}

	var wg sync.WaitGroup
	errs := make(chan error, goroutines*iterations)

	for g := range goroutines {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for i := range iterations {
				m := NewMemoryManager(dir)
				if id%2 == 0 {
					r := []MessageRecord{
						{Role: "human", Content: fmt.Sprintf("w-%d-%d", id, i)},
						{Role: "ai", Content: fmt.Sprintf("wr-%d-%d", id, i)},
					}
					if err := m.persistSnapshot("default", r); err != nil {
						errs <- fmt.Errorf("write g%d i%d: %w", id, i, err)
						return
					}
				} else {
					if _, err := m.Get("default", MemoryConfig{Type: "buffer"}); err != nil {
						errs <- fmt.Errorf("read g%d i%d: %w", id, i, err)
						return
					}
				}
			}
		}(g)
	}

	wg.Wait()
	close(errs)

	for err := range errs {
		t.Fatal(err)
	}

	data, err := os.ReadFile(filepath.Join(dir, "default.json"))
	if err != nil {
		t.Fatalf("read final: %v", err)
	}
	var final []MessageRecord
	if err := json.Unmarshal(data, &final); err != nil {
		t.Fatalf("final snapshot corrupted: %v", err)
	}
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > 0 && containsHelper(s, substr))
}

func containsHelper(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
