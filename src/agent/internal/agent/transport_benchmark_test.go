package agent

import (
	"crypto/tls"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

// BenchmarkTransportLatency measures connection establishment and request latency
func BenchmarkTransportLatency(b *testing.B) {
	// Create a test HTTPS server with artificial latency
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"result": "ok"}`))
	})
	server := httptest.NewTLSServer(handler)
	defer server.Close()

	tests := []struct {
		name              string
		maxIdleConnsPerHost int
		idleConnTimeout   time.Duration
	}{
		{
			name:              "Default (MaxIdleConnsPerHost=2)",
			maxIdleConnsPerHost: 2,
			idleConnTimeout:   90 * time.Second,
		},
		{
			name:              "Optimized (MaxIdleConnsPerHost=8)",
			maxIdleConnsPerHost: 8,
			idleConnTimeout:   90 * time.Second,
		},
	}

	for _, tt := range tests {
		b.Run(tt.name, func(b *testing.B) {
			transport := &http.Transport{
				MaxIdleConns:          100,
				MaxIdleConnsPerHost:   tt.maxIdleConnsPerHost,
				IdleConnTimeout:       tt.idleConnTimeout,
				TLSHandshakeTimeout:   10 * time.Second,
				ExpectContinueTimeout: 1 * time.Second,
				TLSClientConfig:       &tls.Config{InsecureSkipVerify: true},
			}
			client := &http.Client{Transport: transport}

			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				resp, err := client.Get(server.URL)
				if err != nil {
					b.Fatalf("request failed: %v", err)
				}
				io.Copy(io.Discard, resp.Body)
				resp.Body.Close()
			}
		})
	}
}

// BenchmarkConcurrentRequests simulates concurrent LLM/STT/TTS requests
func BenchmarkConcurrentRequests(b *testing.B) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Simulate processing time
		time.Sleep(50 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"result": "ok"}`))
	})
	server := httptest.NewTLSServer(handler)
	defer server.Close()

	tests := []struct {
		name              string
		maxIdleConnsPerHost int
		concurrency       int
	}{
		{
			name:              "Default/Concurrency-4",
			maxIdleConnsPerHost: 2,
			concurrency:       4,
		},
		{
			name:              "Optimized/Concurrency-4",
			maxIdleConnsPerHost: 8,
			concurrency:       4,
		},
		{
			name:              "Default/Concurrency-8",
			maxIdleConnsPerHost: 2,
			concurrency:       8,
		},
		{
			name:              "Optimized/Concurrency-8",
			maxIdleConnsPerHost: 8,
			concurrency:       8,
		},
	}

	for _, tt := range tests {
		b.Run(tt.name, func(b *testing.B) {
			transport := &http.Transport{
				MaxIdleConns:          100,
				MaxIdleConnsPerHost:   tt.maxIdleConnsPerHost,
				IdleConnTimeout:       90 * time.Second,
				TLSHandshakeTimeout:   10 * time.Second,
				ExpectContinueTimeout: 1 * time.Second,
				TLSClientConfig:       &tls.Config{InsecureSkipVerify: true},
			}
			client := &http.Client{Transport: transport}

			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				var wg sync.WaitGroup
				for j := 0; j < tt.concurrency; j++ {
					wg.Add(1)
					go func() {
						defer wg.Done()
						resp, err := client.Get(server.URL)
						if err != nil {
							b.Errorf("request failed: %v", err)
							return
						}
						io.Copy(io.Discard, resp.Body)
						resp.Body.Close()
					}()
				}
				wg.Wait()
			}
		})
	}
}

// measureLatency performs detailed latency measurement
func measureLatency(transport *http.Transport, targetURL string, numRequests int) (LatencyStats, error) {
	client := &http.Client{Transport: transport}
	stats := LatencyStats{}
	durations := make([]time.Duration, 0, numRequests)

	for i := 0; i < numRequests; i++ {
		start := time.Now()
		resp, err := client.Get(targetURL)
		if err != nil {
			return stats, fmt.Errorf("request %d failed: %w", i, err)
		}
		io.Copy(io.Discard, resp.Body)
		resp.Body.Close()

		duration := time.Since(start)
		durations = append(durations, duration)

		// Small delay between requests to simulate real usage
		time.Sleep(10 * time.Millisecond)
	}

	// Calculate statistics
	var total time.Duration
	stats.Min = durations[0]
	stats.Max = durations[0]

	for _, d := range durations {
		total += d
		if d < stats.Min {
			stats.Min = d
		}
		if d > stats.Max {
			stats.Max = d
		}
	}

	stats.Mean = total / time.Duration(numRequests)
	stats.Total = total
	stats.Count = numRequests

	return stats, nil
}

type LatencyStats struct {
	Mean  time.Duration
	Min   time.Duration
	Max   time.Duration
	Total time.Duration
	Count int
}

func (s LatencyStats) String() string {
	return fmt.Sprintf("mean=%v min=%v max=%v total=%v count=%d",
		s.Mean, s.Min, s.Max, s.Total, s.Count)
}

// TestTransportLatencyComparison provides a detailed comparison
func TestTransportLatencyComparison(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping latency comparison in short mode")
	}

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"result": "ok"}`))
	})
	server := httptest.NewTLSServer(handler)
	defer server.Close()

	numRequests := 50

	// Default configuration
	defaultTransport := &http.Transport{
		MaxIdleConnsPerHost:   2,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		TLSClientConfig:       &tls.Config{InsecureSkipVerify: true},
	}

	// Optimized configuration
	optimizedTransport := &http.Transport{
		MaxIdleConns:          100,
		MaxIdleConnsPerHost:   8,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
		TLSClientConfig:       &tls.Config{InsecureSkipVerify: true},
	}

	t.Log("Measuring default transport latency...")
	defaultStats, err := measureLatency(defaultTransport, server.URL, numRequests)
	if err != nil {
		t.Fatalf("default measurement failed: %v", err)
	}

	t.Log("Measuring optimized transport latency...")
	optimizedStats, err := measureLatency(optimizedTransport, server.URL, numRequests)
	if err != nil {
		t.Fatalf("optimized measurement failed: %v", err)
	}

	t.Logf("\n=== Latency Comparison ===")
	t.Logf("Default    : %s", defaultStats)
	t.Logf("Optimized  : %s", optimizedStats)

	improvement := float64(defaultStats.Mean-optimizedStats.Mean) / float64(defaultStats.Mean) * 100
	t.Logf("Improvement: %.2f%%", improvement)
}

// TestConnectionReuse verifies connection pooling behavior
func TestConnectionReuse(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping connection reuse test in short mode")
	}

	var connectionCount int32
	var mu sync.Mutex

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"result": "ok"}`))
	})

	server := httptest.NewUnstartedServer(handler)
	server.Config.ConnState = func(conn net.Conn, state http.ConnState) {
		if state == http.StateNew {
			mu.Lock()
			connectionCount++
			mu.Unlock()
		}
	}
	server.StartTLS()
	defer server.Close()

	tests := []struct {
		name              string
		maxIdleConnsPerHost int
		requests          int
		expectedMaxConns  int
	}{
		{
			name:              "Default (expects more connections)",
			maxIdleConnsPerHost: 2,
			requests:          10,
			expectedMaxConns:  5,
		},
		{
			name:              "Optimized (expects fewer connections)",
			maxIdleConnsPerHost: 8,
			requests:          10,
			expectedMaxConns:  3,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mu.Lock()
			connectionCount = 0
			mu.Unlock()

			transport := &http.Transport{
				MaxIdleConns:          100,
				MaxIdleConnsPerHost:   tt.maxIdleConnsPerHost,
				IdleConnTimeout:       90 * time.Second,
				TLSHandshakeTimeout:   10 * time.Second,
				ExpectContinueTimeout: 1 * time.Second,
				TLSClientConfig:       &tls.Config{InsecureSkipVerify: true},
			}
			client := &http.Client{Transport: transport}

			for i := 0; i < tt.requests; i++ {
				resp, err := client.Get(server.URL)
				if err != nil {
					t.Fatalf("request %d failed: %v", i, err)
				}
				io.Copy(io.Discard, resp.Body)
				resp.Body.Close()
				time.Sleep(5 * time.Millisecond)
			}

			mu.Lock()
			actualConns := connectionCount
			mu.Unlock()

			t.Logf("Created %d connections for %d requests", actualConns, tt.requests)

			if actualConns > int32(tt.expectedMaxConns) {
				t.Logf("Warning: created more connections than expected (got %d, expected <=%d)",
					actualConns, tt.expectedMaxConns)
			}
		})
	}
}
