package agent

import (
	"encoding/json"
	"fmt"
	"sync"
	"testing"
	"time"
)

// TestEnqueueAndPoll tests basic enqueue and polling functionality
func TestEnqueueAndPoll(t *testing.T) {
	q := NewCommandQueue(nil)
	defer q.Stop()

	cmd1 := BridgeCommand{
		ID:        "test_1",
		Type:      "open_app",
		TimeoutMs: 5000,
	}
	cmd2 := BridgeCommand{
		ID:        "test_2",
		Type:      "clipboard_read",
		TimeoutMs: 3000,
	}

	// Enqueue commands
	if err := q.Enqueue(cmd1); err != nil {
		t.Fatalf("failed to enqueue cmd1: %v", err)
	}
	if err := q.Enqueue(cmd2); err != nil {
		t.Fatalf("failed to enqueue cmd2: %v", err)
	}

	// Duplicate ID should fail
	if err := q.Enqueue(cmd1); err == nil {
		t.Errorf("expected error when enqueuing duplicate ID")
	}

	// Empty ID should fail
	cmdEmpty := BridgeCommand{Type: "test"}
	if err := q.Enqueue(cmdEmpty); err == nil {
		t.Errorf("expected error when enqueuing empty ID")
	}

	// Poll commands (platform filter: all)
	polled := q.Poll("", 10)
	if len(polled) != 2 {
		t.Fatalf("expected 2 polled commands, got %d", len(polled))
	}
	if polled[0].ID != "test_1" || polled[1].ID != "test_2" {
		t.Errorf("polled command order wrong")
	}

	// Polling again should return empty (already in-flight)
	polled2 := q.Poll("", 10)
	if len(polled2) != 0 {
		t.Errorf("expected 0 commands on second poll, got %d", len(polled2))
	}

	// Verify commands are marked in-flight
	q.mu.RLock()
	if q.commands["test_1"].Status != StatusInFlight {
		t.Errorf("cmd1 status expected in_flight, got %v", q.commands["test_1"].Status)
	}
	q.mu.RUnlock()
}

// TestPollPlatformFilter tests platform filtering
func TestPollPlatformFilter(t *testing.T) {
	q := NewCommandQueue(nil)
	defer q.Stop()

	cmdIOS := BridgeCommand{
		ID:        "ios_cmd",
		Type:      "open_app",
		App:       "微信",
		TimeoutMs: 5000,
	}
	cmdAndroid := BridgeCommand{
		ID:        "android_cmd",
		Type:      "open_app",
		App:       "微信",
		TimeoutMs: 5000,
	}
	cmdGeneric := BridgeCommand{
		ID:        "generic_cmd",
		Type:      "clipboard_read",
		TimeoutMs: 3000,
	}

	q.Enqueue(cmdIOS)
	q.Enqueue(cmdAndroid)
	q.Enqueue(cmdGeneric)

	// Platform-specific open_app targeting is resolved app-side, so queue
	// polling no longer filters semantic commands by platform.
	polledIOS := q.Poll("ios", 10)
	if len(polledIOS) != 3 {
		t.Errorf("expected 3 commands for iOS, got %d", len(polledIOS))
	}

	// Requeue them for next test
	q.mu.Lock()
	q.commands["ios_cmd"].Status = StatusQueued
	q.commands["android_cmd"].Status = StatusQueued
	q.commands["generic_cmd"].Status = StatusQueued
	q.mu.Unlock()

	polledAndroid := q.Poll("android", 10)
	if len(polledAndroid) != 3 {
		t.Errorf("expected 3 commands for Android, got %d", len(polledAndroid))
	}
}

// TestSubmitAndQueryResult tests result storage and query
func TestSubmitAndQueryResult(t *testing.T) {
	q := NewCommandQueue(nil)
	defer q.Stop()

	cmd := BridgeCommand{ID: "cmd_1", Type: "clipboard_read"}
	q.Enqueue(cmd)

	// Before submission: status should be queued
	result, status := q.QueryResult("cmd_1")
	if status != StatusQueued {
		t.Errorf("expected queued, got %v", status)
	}
	if result != nil {
		t.Errorf("expected nil result before submission")
	}

	// Poll to mark in-flight
	q.Poll("", 10)
	_, status = q.QueryResult("cmd_1")
	if status != StatusInFlight {
		t.Errorf("expected in_flight, got %v", status)
	}

	// Submit result
	resp := BridgeCommandResponse{
		ID:     "cmd_1",
		OK:     true,
		Method: "clipboard",
		Data:   json.RawMessage(`{"text":"hello"}`),
	}
	if err := q.SubmitResult(resp); err != nil {
		t.Fatalf("failed to submit result: %v", err)
	}

	// After submission: should be completed with result
	result, status = q.QueryResult("cmd_1")
	if status != StatusCompleted {
		t.Errorf("expected completed, got %v", status)
	}
	if result == nil {
		t.Fatal("expected non-nil result")
	}
	if !result.Response.OK {
		t.Errorf("expected OK=true")
	}
	if result.Response.Method != "clipboard" {
		t.Errorf("expected method=clipboard, got %v", result.Response.Method)
	}

	// Command should be removed from pending
	q.mu.RLock()
	if _, exists := q.commands["cmd_1"]; exists {
		t.Errorf("command should be removed after result submission")
	}
	q.mu.RUnlock()

	// Submit result for non-existent command
	badResp := BridgeCommandResponse{ID: "nonexistent", OK: false}
	if err := q.SubmitResult(badResp); err == nil {
		t.Errorf("expected error for nonexistent command")
	}
}

// TestCommandExpiration tests TTL and cleanup
func TestCommandExpiration(t *testing.T) {
	q := NewCommandQueue(nil)
	defer q.Stop()

	// Create command that expires quickly
	cmd := BridgeCommand{ID: "expire_1", Type: "test"}
	q.Enqueue(cmd)

	// Manually set expiration to past
	q.mu.Lock()
	q.commands["expire_1"].ExpireAt = time.Now().Add(-1 * time.Second)
	q.mu.Unlock()

	// Run cleanup
	q.cleanup()

	// Command should be removed
	q.mu.RLock()
	if _, exists := q.commands["expire_1"]; exists {
		t.Errorf("expired command should be removed")
	}
	q.mu.RUnlock()

	// Test result TTL
	cmd2 := BridgeCommand{ID: "result_expire", Type: "test"}
	q.Enqueue(cmd2)
	q.Poll("", 10)
	resp := BridgeCommandResponse{ID: "result_expire", OK: true}
	q.SubmitResult(resp)

	// Manually expire result
	q.mu.Lock()
	q.results["result_expire"].CompletedAt = time.Now().Add(-3 * time.Minute)
	q.mu.Unlock()

	q.cleanup()

	// Result should be removed
	q.mu.RLock()
	if _, exists := q.results["result_expire"]; exists {
		t.Errorf("expired result should be removed")
	}
	q.mu.RUnlock()
}

// TestInFlightTimeout tests in-flight timeout and retry
func TestInFlightTimeout(t *testing.T) {
	q := NewCommandQueue(nil)
	defer q.Stop()

	cmd := BridgeCommand{ID: "timeout_1", Type: "test"}
	q.Enqueue(cmd)
	q.Poll("", 10)

	// Manually set in-flight time to past
	q.mu.Lock()
	pastTime := time.Now().Add(-35 * time.Second)
	q.commands["timeout_1"].InFlightAt = &pastTime
	q.mu.Unlock()

	// Run cleanup: should retry
	q.cleanup()

	q.mu.RLock()
	cmd1 := q.commands["timeout_1"]
	q.mu.RUnlock()

	if cmd1 == nil {
		t.Fatal("command should not be removed on first timeout")
	}
	if cmd1.Status != StatusQueued {
		t.Errorf("expected requeued, got %v", cmd1.Status)
	}
	if cmd1.RetryCount != 1 {
		t.Errorf("expected retry count 1, got %d", cmd1.RetryCount)
	}

	// Exceed max retries
	for i := 0; i < MaxRetries; i++ {
		q.Poll("", 10)
		q.mu.Lock()
		past := time.Now().Add(-35 * time.Second)
		q.commands["timeout_1"].InFlightAt = &past
		q.mu.Unlock()
		q.cleanup()
	}

	// Command should be dropped
	q.mu.RLock()
	_, exists := q.commands["timeout_1"]
	q.mu.RUnlock()

	if exists {
		t.Errorf("command should be dropped after max retries")
	}
}

// TestConcurrentAccess tests concurrent enqueue/poll/submit
func TestConcurrentAccess(t *testing.T) {
	q := NewCommandQueue(nil)
	defer q.Stop()

	var wg sync.WaitGroup
	const numGoroutines = 10
	const numCmds = 20

	// Enqueue concurrently
	wg.Add(numGoroutines)
	for g := 0; g < numGoroutines; g++ {
		go func(gid int) {
			defer wg.Done()
			for i := 0; i < numCmds; i++ {
				cmd := BridgeCommand{
					ID:   fmt.Sprintf("cmd_%d_%d", gid, i),
					Type: "test",
				}
				q.Enqueue(cmd)
			}
		}(g)
	}
	wg.Wait()

	// Poll concurrently
	wg.Add(numGoroutines)
	polledIDs := make(map[string]bool)
	var mu sync.Mutex

	for g := 0; g < numGoroutines; g++ {
		go func() {
			defer wg.Done()
			cmds := q.Poll("", 50)
			mu.Lock()
			for _, c := range cmds {
				polledIDs[c.ID] = true
			}
			mu.Unlock()
		}()
	}
	wg.Wait()

	// Submit results concurrently
	wg.Add(len(polledIDs))
	for id := range polledIDs {
		go func(cmdID string) {
			defer wg.Done()
			resp := BridgeCommandResponse{ID: cmdID, OK: true}
			q.SubmitResult(resp)
		}(id)
	}
	wg.Wait()

	// Verify results
	for id := range polledIDs {
		result, status := q.QueryResult(id)
		if status != StatusCompleted {
			t.Errorf("command %s expected completed, got %v", id, status)
		}
		if result == nil || !result.Response.OK {
			t.Errorf("command %s missing valid result", id)
		}
	}
}

// TestPollLimit tests the poll limit parameter
func TestPollLimit(t *testing.T) {
	q := NewCommandQueue(nil)
	defer q.Stop()

	for i := 0; i < 100; i++ {
		cmd := BridgeCommand{
			ID:   fmt.Sprintf("cmd_%d", i),
			Type: "test",
		}
		q.Enqueue(cmd)
	}

	// Poll with limit 10
	polled := q.Poll("", 10)
	if len(polled) != 10 {
		t.Errorf("expected 10, got %d", len(polled))
	}

	// Poll with limit 0: should default
	polled2 := q.Poll("", 0)
	if len(polled2) == 0 || len(polled2) > 50 {
		t.Errorf("poll with limit 0 returned unexpected count %d", len(polled2))
	}

	// Poll with limit > 50: should cap
	for i := 100; i < 200; i++ {
		cmd := BridgeCommand{
			ID:   fmt.Sprintf("cmd_%d", i),
			Type: "test",
		}
		q.Enqueue(cmd)
	}
	polled3 := q.Poll("", 1000)
	if len(polled3) > 50 {
		t.Errorf("poll with limit 1000 should cap at 50, got %d", len(polled3))
	}
}

// TestQueryExpiredCommand tests query for expired command
func TestQueryExpiredCommand(t *testing.T) {
	q := NewCommandQueue(nil)
	defer q.Stop()

	result, status := q.QueryResult("never_existed")
	if status != StatusExpired {
		t.Errorf("expected expired, got %v", status)
	}
	if result != nil {
		t.Errorf("expected nil result for expired command")
	}
}
