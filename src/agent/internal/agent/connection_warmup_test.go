package agent

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

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
