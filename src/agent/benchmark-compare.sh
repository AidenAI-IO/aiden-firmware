#!/bin/bash

echo "=== HTTP Transport Optimization Comparison Test ==="
echo ""
echo "Running benchmark comparison..."
echo ""

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
cd "$SCRIPT_DIR" || exit 1

echo "----------------------------------------"
echo "1. Single Request Latency Test (100 iterations)"
echo "----------------------------------------"
go test -bench=BenchmarkTransportLatency -benchmem -benchtime=100x ./internal/agent 2>&1 | grep -E "Benchmark|ns/op|B/op"

echo ""
echo "----------------------------------------"
echo "2. Concurrent Requests Test (50 iterations, 4 concurrent)"
echo "----------------------------------------"
go test -bench="BenchmarkConcurrentRequests.*Concurrency-4" -benchmem -benchtime=50x ./internal/agent 2>&1 | grep -E "Benchmark|ns/op|B/op"

echo ""
echo "----------------------------------------"
echo "3. Concurrent Requests Test (50 iterations, 8 concurrent)"
echo "----------------------------------------"
go test -bench="BenchmarkConcurrentRequests.*Concurrency-8" -benchmem -benchtime=50x ./internal/agent 2>&1 | grep -E "Benchmark|ns/op|B/op"

echo ""
echo "----------------------------------------"
echo "4. Connection Reuse Test"
echo "----------------------------------------"
go test -run=TestConnectionReuse ./internal/agent 2>&1 | grep -E "RUN|PASS|Created.*connections"

echo ""
echo "========================================="
echo "Test completed!"
echo ""
echo "Optimization summary:"
echo "- MaxIdleConnsPerHost: 2 -> 8"
echo "- IdleConnTimeout: kept at 90s"
echo "- TLSHandshakeTimeout: 10s"
echo "- ExpectContinueTimeout: added 1s"
echo ""
echo "Expected benefits:"
echo "- Reduce connection establishment, especially TLS handshake"
echo "- Improve connection reuse rate"
echo "- Lower latency and memory allocation in concurrent scenarios"
echo "========================================="
