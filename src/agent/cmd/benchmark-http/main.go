package main

import (
	"bytes"
	"crypto/tls"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"sort"
	"time"
)

var (
	endpoint   = flag.String("endpoint", "https://apibest.ai/v1", "API endpoint")
	apiKey     = flag.String("key", "", "API key")
	model      = flag.String("model", "gpt-4o-mini", "Model name")
	requests   = flag.Int("n", 10, "Number of requests")
	optimized  = flag.Bool("optimized", false, "Use optimized transport settings")
	concurrent = flag.Int("c", 1, "Concurrent requests")
)

type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type ChatRequest struct {
	Model    string    `json:"model"`
	Messages []Message `json:"messages"`
}

type LatencyMetrics struct {
	Durations     []time.Duration
	Mean          time.Duration
	Median        time.Duration
	P95           time.Duration
	P99           time.Duration
	Min           time.Duration
	Max           time.Duration
	TotalDuration time.Duration
}

func main() {
	flag.Parse()

	if *apiKey == "" {
		fmt.Println("Error: API key is required (-key flag)")
		return
	}

	transport := createTransport(*optimized)
	client := &http.Client{
		Transport: transport,
		Timeout:   30 * time.Second,
	}

	configName := "Default"
	if *optimized {
		configName = "Optimized"
	}

	fmt.Printf("=== HTTP Transport Benchmark ===\n")
	fmt.Printf("Configuration: %s\n", configName)
	fmt.Printf("Endpoint: %s\n", *endpoint)
	fmt.Printf("Model: %s\n", *model)
	fmt.Printf("Requests: %d\n", *requests)
	fmt.Printf("Concurrency: %d\n\n", *concurrent)

	if *concurrent == 1 {
		runSequential(client)
	} else {
		runConcurrent(client)
	}
}

func createTransport(optimized bool) *http.Transport {
	transport := http.DefaultTransport.(*http.Transport).Clone()

	if optimized {
		transport.MaxIdleConnsPerHost = 8
		fmt.Println("Transport settings: MaxIdleConnsPerHost=8 (optimized)")
	} else {
		transport.MaxIdleConnsPerHost = 2
		fmt.Println("Transport settings: MaxIdleConnsPerHost=2 (baseline)")
	}

	return transport
}

func runSequential(client *http.Client) {
	durations := make([]time.Duration, 0, *requests)

	fmt.Println("Running sequential requests...")
	for i := 0; i < *requests; i++ {
		start := time.Now()
		err := makeRequest(client, i+1)
		duration := time.Since(start)

		if err != nil {
			fmt.Printf("[%d/%d] ERROR: %v (took %v)\n", i+1, *requests, err, duration)
		} else {
			fmt.Printf("[%d/%d] OK (took %v)\n", i+1, *requests, duration)
			durations = append(durations, duration)
		}

		// Small delay to allow connection reuse
		time.Sleep(50 * time.Millisecond)
	}

	printMetrics(durations)
}

func runConcurrent(client *http.Client) {
	fmt.Println("Running concurrent requests...")

	type result struct {
		index    int
		duration time.Duration
		err      error
	}

	results := make(chan result, *requests)
	sem := make(chan struct{}, *concurrent)

	overallStart := time.Now()

	for i := 0; i < *requests; i++ {
		go func(index int) {
			sem <- struct{}{}
			defer func() { <-sem }()

			start := time.Now()
			err := makeRequest(client, index+1)
			duration := time.Since(start)

			results <- result{index: index + 1, duration: duration, err: err}
		}(i)
	}

	durations := make([]time.Duration, 0, *requests)
	successCount := 0

	for i := 0; i < *requests; i++ {
		res := <-results
		if res.err != nil {
			fmt.Printf("[%d/%d] ERROR: %v (took %v)\n", res.index, *requests, res.err, res.duration)
		} else {
			fmt.Printf("[%d/%d] OK (took %v)\n", res.index, *requests, res.duration)
			durations = append(durations, res.duration)
			successCount++
		}
	}

	overallDuration := time.Since(overallStart)
	fmt.Printf("\nTotal execution time: %v\n", overallDuration)
	fmt.Printf("Successful requests: %d/%d\n", successCount, *requests)
	fmt.Printf("Requests/second: %.2f\n\n", float64(successCount)/overallDuration.Seconds())

	printMetrics(durations)
}

func makeRequest(client *http.Client, index int) error {
	reqBody := ChatRequest{
		Model: *model,
		Messages: []Message{
			{Role: "user", Content: fmt.Sprintf("Say 'test-%d' only", index)},
		},
	}

	body, err := json.Marshal(reqBody)
	if err != nil {
		return fmt.Errorf("marshal request: %w", err)
	}

	req, err := http.NewRequest("POST", *endpoint+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+*apiKey)

	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("do request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("status %d: %s", resp.StatusCode, string(body))
	}

	// Read and discard response body
	_, err = io.Copy(io.Discard, resp.Body)
	return err
}

func printMetrics(durations []time.Duration) {
	if len(durations) == 0 {
		fmt.Println("\nNo successful requests to analyze")
		return
	}

	metrics := calculateMetrics(durations)

	fmt.Println("\n=== Latency Metrics ===")
	fmt.Printf("Requests analyzed: %d\n", len(durations))
	fmt.Printf("Mean:              %v\n", metrics.Mean)
	fmt.Printf("Median:            %v\n", metrics.Median)
	fmt.Printf("P95:               %v\n", metrics.P95)
	fmt.Printf("P99:               %v\n", metrics.P99)
	fmt.Printf("Min:               %v\n", metrics.Min)
	fmt.Printf("Max:               %v\n", metrics.Max)
	fmt.Printf("Total:             %v\n", metrics.TotalDuration)
}

func calculateMetrics(durations []time.Duration) LatencyMetrics {
	sorted := make([]time.Duration, len(durations))
	copy(sorted, durations)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })

	var total time.Duration
	for _, d := range sorted {
		total += d
	}

	mean := total / time.Duration(len(sorted))
	median := sorted[len(sorted)/2]
	p95 := sorted[int(float64(len(sorted))*0.95)]
	p99 := sorted[int(float64(len(sorted))*0.99)]
	min := sorted[0]
	max := sorted[len(sorted)-1]

	return LatencyMetrics{
		Durations:     sorted,
		Mean:          mean,
		Median:        median,
		P95:           p95,
		P99:           p99,
		Min:           min,
		Max:           max,
		TotalDuration: total,
	}
}
