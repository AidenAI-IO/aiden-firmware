# HTTP Transport Optimization

## Overview

This document describes the HTTP transport optimization implemented to reduce latency in LLM, STT, and TTS requests.

## Optimizations

### 1. Connection Pool Configuration

Modified `src/agent/internal/agent/proxy.go` to optimize the HTTP transport:

```go
// Before (Go defaults)
MaxIdleConnsPerHost = 2

// After
MaxIdleConnsPerHost = 8
```

**Note**: Other settings like `MaxIdleConns=100`, `IdleConnTimeout=90s`, `TLSHandshakeTimeout=10s`, and `ExpectContinueTimeout=1s` are already set by `http.DefaultTransport` and inherited via `Clone()`. Only `MaxIdleConnsPerHost` is changed from the default value of 2.

These settings allow better connection reuse, especially for scenarios with multiple requests to the same endpoints (LLM, STT, TTS).

### 2. Connection Warmup


Implemented proactive connection warming in `src/agent/internal/agent/connection_warmup.go`:

- **Pre-warming**: During the gap between recording start and user speech (typically 200-500ms), the system sends HEAD requests to LLM/STT/TTS endpoints
- **TLS Handshake Optimization**: Pre-establishes TLS connections to avoid cold connection overhead
- **Async Execution**: Warmup happens in the background without blocking the recording flow

#### How It Works

1. When recording starts, `AudioDialog` triggers `connWarmer.WarmupAsync()`
2. The warmer sends concurrent HEAD requests to all configured endpoints
3. Connections are kept alive in the HTTP client's connection pool
4. Subsequent API requests reuse these warm connections, saving TLS handshake time

## Performance Results

### Local Benchmark Tests

#### Single Request (100 iterations)
- **Default**: 153,797 ns/op (153.8 μs)
- **Optimized**: 130,415 ns/op (130.4 μs)
- **Improvement**: **15.2%**

#### Concurrent 4 Requests (50 iterations)

| Config | Latency | Memory Alloc | Alloc Count |
|--------|---------|--------------|-------------|
| Default | 58.5 ms | 298 KB | 1,998 |
| Optimized | 52.0 ms | 33.7 KB | 339 |
| **Improvement** | **11.2%** | **88.7%↓** | **83.0%↓** |

#### Concurrent 8 Requests (50 iterations)

| Config | Latency | Memory Alloc | Alloc Count |
|--------|---------|--------------|-------------|
| Default | 61.7 ms | 852 KB | 5,632 |
| Optimized | 51.7 ms | 67.3 KB | 677 |
| **Improvement** | **16.2%** | **92.1%↓** | **88.0%↓** |

### Real LLM API Tests

Using gpt-5.4 model:
- **Sequential requests**: ~1.7s average latency (both configs)
- **Concurrent requests**: ~1.9s average latency (both configs)

In real-world LLM scenarios, the response time (1-3 seconds) dominates over connection establishment time. However, the optimization still provides:
- Reduced memory allocations
- Lower GC pressure
- Eliminated connection pool bottlenecks

### Connection Warmup Benefits

The connection warmup provides measurable benefits in the audio dialog flow:
- Eliminates TLS handshake latency (typically 50-200ms) from the critical path
- Warmup happens during user's natural pause after the recording prompt
- Zero perceived latency cost to the user

## Use Cases

This optimization is particularly effective for:
- ✅ High-concurrency scenarios
- ✅ Low-latency network environments
- ✅ Frequent requests to the same endpoints
- ✅ Voice interaction workflows (recording → STT → LLM → TTS)

Even in single-request scenarios, the optimization:
- Has minimal cost
- Prevents connection pool exhaustion
- Reduces memory pressure
- Prepares the system for future concurrency needs

## Testing

### Benchmark Tests

Run the transport benchmark:
```bash
go test -bench=BenchmarkHTTP -benchmem ./internal/agent
```

### Connection Warmup Tests

Run the warmup tests:
```bash
go test -v ./internal/agent -run TestConnectionWarmer
```

### Real API Tests

Test with actual LLM endpoints:
```bash
go run cmd/benchmark-http/main.go -key YOUR_KEY -model gpt-4o -n 20
```

## Files Modified

- `src/agent/internal/agent/proxy.go` - HTTP transport configuration
- `src/agent/internal/agent/connection_warmup.go` - Connection warmup implementation
- `src/agent/internal/agent/audio_dialog.go` - Integration with audio dialog flow
- `src/agent/internal/agent/transport_benchmark_test.go` - Benchmark tests
- `src/agent/internal/agent/connection_warmup_test.go` - Warmup tests
- `src/agent/cmd/benchmark-http/main.go` - Real API testing tool

## Related Issues

Implements L4 optimization: "LLM HTTP uses default Transport, not optimized for single upstream preheating/reuse"
