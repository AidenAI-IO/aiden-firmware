package agent

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func waitForConnectionWarmup(t *testing.T, warmer *ConnectionWarmer) {
	t.Helper()
	if warmer == nil {
		return
	}
	deadline := time.After(3 * time.Second)
	ticker := time.NewTicker(time.Millisecond)
	defer ticker.Stop()
	for {
		warmer.mu.Lock()
		warming := warmer.warming
		warmer.mu.Unlock()
		if !warming {
			return
		}
		select {
		case <-deadline:
			t.Fatal("connection warmup did not finish")
		case <-ticker.C:
		}
	}
}

func TestConnectionWarmer(t *testing.T) {
	var requestCount int32

	// Create a test server
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&requestCount, 1)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	// Create warmer with test server endpoint
	client := &http.Client{Timeout: 5 * time.Second}
	endpoints := []string{server.URL}
	warmer := NewConnectionWarmer(client, endpoints)

	// Test async warmup
	ctx := context.Background()
	warmer.WarmupAsync(ctx)

	// Wait a bit for async operation to complete
	time.Sleep(100 * time.Millisecond)

	// Should have made one request
	if count := atomic.LoadInt32(&requestCount); count != 1 {
		t.Errorf("Expected 1 request, got %d", count)
	}
}

func TestConnectionWarmerMultipleEndpoints(t *testing.T) {
	var requestCount int32

	// Create multiple test servers
	server1 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&requestCount, 1)
		w.WriteHeader(http.StatusOK)
	}))
	defer server1.Close()

	server2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&requestCount, 1)
		w.WriteHeader(http.StatusOK)
	}))
	defer server2.Close()

	// Create warmer with multiple endpoints
	client := &http.Client{Timeout: 5 * time.Second}
	endpoints := []string{server1.URL, server2.URL}
	warmer := NewConnectionWarmer(client, endpoints)

	// Warmup should hit both endpoints
	ctx := context.Background()
	warmer.WarmupAsync(ctx)

	// Wait for async operations to complete
	time.Sleep(100 * time.Millisecond)

	if count := atomic.LoadInt32(&requestCount); count != 2 {
		t.Errorf("Expected 2 requests (one per endpoint), got %d", count)
	}
}

func TestConnectionWarmerTimeout(t *testing.T) {
	// Create a slow server
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(200 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	// Create warmer with short timeout
	client := &http.Client{Timeout: 50 * time.Millisecond}
	endpoints := []string{server.URL}
	warmer := NewConnectionWarmer(client, endpoints)

	// Should timeout but not crash
	ctx := context.Background()
	warmer.WarmupAsync(ctx) // Should complete quickly despite timeout

	// Wait a bit to ensure async operation completes
	time.Sleep(100 * time.Millisecond)
}

func TestConnectionWarmerUpdatesEndpointsDuringWarmup(t *testing.T) {
	entered, release := make(chan struct{}, 1), make(chan struct{})
	var oldCalls, newCalls atomic.Int32
	oldServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		oldCalls.Add(1)
		entered <- struct{}{}
		<-release
		w.WriteHeader(http.StatusOK)
	}))
	defer oldServer.Close()
	newServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		newCalls.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer newServer.Close()
	defer func() {
		select {
		case <-release:
		default:
			close(release)
		}
	}()
	warmer := NewConnectionWarmer(oldServer.Client(), []string{oldServer.URL})
	warmer.WarmupAsync(context.Background())
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("initial warmup did not start")
	}
	endpoints := []string{newServer.URL}
	warmer.SetEndpoints(endpoints)
	endpoints[0] = oldServer.URL // The warmer must own its endpoint snapshot.
	warmer.WarmupAsync(context.Background())
	close(release)
	waitForConnectionWarmup(t, warmer)
	warmer.WarmupAsync(context.Background())
	waitForConnectionWarmup(t, warmer)
	warmer.SetEndpoints(nil)
	warmer.WarmupAsync(context.Background())
	waitForConnectionWarmup(t, warmer)
	if oldCalls.Load() != 1 || newCalls.Load() != 1 {
		t.Fatalf("warmup calls: old=%d new=%d, want one each", oldCalls.Load(), newCalls.Load())
	}
}

func TestCollectWarmupEndpoints(t *testing.T) {
	tests := []struct {
		name     string
		cfg      Config
		expected int
	}{
		{
			name: "LLM and STT endpoints",
			cfg: Config{
				Model: ModelConfig{BaseURL: "https://api.openai.com/v1"},
				STT:   STTConfig{BaseURL: "https://api.openai.com/v1"},
			},
			expected: 1, // Same base URL, should be deduplicated
		},
		{
			name: "Different endpoints",
			cfg: Config{
				Model: ModelConfig{BaseURL: "https://api.openai.com/v1"},
				STT:   STTConfig{BaseURL: "https://api.groq.com/openai/v1"},
			},
			expected: 2,
		},
		{
			name:     "No endpoints",
			cfg:      Config{},
			expected: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			endpoints := collectWarmupEndpoints(tt.cfg)
			if len(endpoints) != tt.expected {
				t.Errorf("Expected %d endpoints, got %d: %v", tt.expected, len(endpoints), endpoints)
			}
		})
	}
}
