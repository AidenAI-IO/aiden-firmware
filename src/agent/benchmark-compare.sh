#!/bin/bash

echo "=== HTTP Transport 优化效果对比测试 ==="
echo ""
echo "正在运行基准测试对比..."
echo ""

cd /Users/apple/IdeaProjects/aiden-firmware/src/agent

echo "----------------------------------------"
echo "1. 单请求延迟测试 (100次)"
echo "----------------------------------------"
go test -bench=BenchmarkTransportLatency -benchmem -benchtime=100x ./internal/agent 2>&1 | grep -E "Benchmark|ns/op|B/op"

echo ""
echo "----------------------------------------"
echo "2. 并发请求测试 (50次，4并发)"
echo "----------------------------------------"
go test -bench="BenchmarkConcurrentRequests.*Concurrency-4" -benchmem -benchtime=50x ./internal/agent 2>&1 | grep -E "Benchmark|ns/op|B/op"

echo ""
echo "----------------------------------------"
echo "3. 并发请求测试 (50次，8并发)"
echo "----------------------------------------"
go test -bench="BenchmarkConcurrentRequests.*Concurrency-8" -benchmem -benchtime=50x ./internal/agent 2>&1 | grep -E "Benchmark|ns/op|B/op"

echo ""
echo "----------------------------------------"
echo "4. 连接复用测试"
echo "----------------------------------------"
go test -run=TestConnectionReuse ./internal/agent 2>&1 | grep -E "RUN|PASS|Created.*connections"

echo ""
echo "========================================="
echo "测试完成！"
echo ""
echo "优化总结："
echo "- MaxIdleConnsPerHost: 2 -> 8"
echo "- IdleConnTimeout: 保持 90s"
echo "- TLSHandshakeTimeout: 10s"
echo "- ExpectContinueTimeout: 新增 1s"
echo ""
echo "预期收益："
echo "- 减少连接建立次数，特别是 TLS 握手"
echo "- 提高连接复用率"
echo "- 降低并发场景下的延迟和内存分配"
echo "========================================="
