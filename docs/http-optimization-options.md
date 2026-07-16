# Additional HTTP Optimization Options

Beyond connection pool optimization and warmup, here are other potential optimization techniques:

## 1. HTTP Compression ✅ Recommended

### Current Status
Go's `http.Transport` **enables gzip compression by default** when you don't set custom headers. It automatically:
- Adds `Accept-Encoding: gzip` to requests
- Decompresses responses transparently
- Only applies to response bodies (not request bodies by default)

### Check if it's working
```go
// In your HTTP client code, compression is already enabled unless you explicitly:
// 1. Set Accept-Encoding header manually
// 2. Set DisableCompression = true
```

### Potential improvements
- **Request compression**: For large payloads (e.g., sending long transcripts to LLM), compress request body
- **Verify compression is active**: Add logging to check if responses are actually compressed

### Trade-offs
- ✅ Pros: Reduces bandwidth (typically 60-80% for text), faster transfer
- ⚠️ Cons: CPU overhead for compression/decompression (usually negligible)
- 💡 Most effective for: LLM prompts/responses (text), less useful for audio (already compressed)

## 2. HTTP/2 ✅ Recommended

### Current Status
Go's HTTP client supports HTTP/2 by default when:
- Server supports it
- Connection is HTTPS
- `ForceAttemptHTTP2` is not disabled

### Benefits
- Multiplexing: Multiple requests over one connection
- Header compression (HPACK)
- Server push capability
- Better for high-latency networks

### Implementation
```go
// Already enabled by default in Go 1.6+
// Explicitly enable:
transport.ForceAttemptHTTP2 = true
```

### Trade-offs
- ✅ Pros: Better multiplexing, header compression, single connection
- ⚠️ Cons: All requests on one connection share bandwidth, head-of-line blocking at TCP layer
- 💡 Most effective for: Multiple concurrent requests

## 3. Connection Coalescing 🤔 Maybe

### What it is
HTTP/2 feature where multiple domains sharing the same IP can use one connection

### Benefits
- Fewer TLS handshakes
- Better connection reuse

### Trade-offs
- Already handled by HTTP/2 implementation
- Requires certificate to be valid for multiple domains
- Limited control in Go's standard library

## 4. Request Batching 🤔 Maybe

### What it is
Combine multiple API calls into one request (if API supports it)

### Example
```go
// Instead of:
stt() -> llm() -> tts()  // 3 round trips

// Batch:
combined_api(audio) -> {transcript, response, speech}  // 1 round trip
```

### Trade-offs
- ✅ Pros: Dramatically reduces round trips
- ⚠️ Cons: Requires API support, higher latency for first response, complex error handling
- 💡 Most effective for: Custom API endpoints you control

## 5. DNS Caching 🤔 Already optimized

### Current Status
Go's DNS resolver caches results

### Potential improvement
```go
transport.DialContext = (&net.Dialer{
    Timeout:   30 * time.Second,
    KeepAlive: 30 * time.Second,
    // Resolver already caches
}).DialContext
```

### Trade-offs
- Already working by default
- Marginal gains (DNS lookup is usually < 50ms and cached)

## 6. Early Hints (103 status) ❌ Not applicable

Only useful for browser/server scenarios with resource preloading

## 7. Streaming Responses ✅ Already used

Your codebase already uses streaming for STT/LLM/TTS, which is optimal for real-time responses.

## 8. Local Caching ✅ Recommended

### What it is
Cache LLM responses for identical prompts

### Implementation
```go
// In-memory cache with TTL
cache := make(map[string]CachedResponse)
// Or use Redis/disk cache
```

### Trade-offs
- ✅ Pros: Zero latency for cache hits, reduces API costs
- ⚠️ Cons: Memory usage, stale responses, cache invalidation complexity
- 💡 Most effective for: Repeated queries, FAQ-style interactions

## 9. Parallel Requests ⚠️ Use with caution

### What it is
Make STT/LLM calls in parallel when possible

### Example
```go
// If you have multiple independent requests
go stt1()
go stt2()
```

### Trade-offs
- ✅ Pros: Lower total latency
- ⚠️ Cons: Higher instantaneous load, may hit rate limits
- 💡 Most effective for: Multiple independent operations

## 10. TCP Optimization 🤔 Advanced

### Potential tweaks
```go
transport.DialContext = (&net.Dialer{
    Timeout:   30 * time.Second,
    KeepAlive: 30 * time.Second,
    // Enable TCP Fast Open (requires OS support)
}).DialContext
```

### Trade-offs
- Very low-level optimization
- Minimal gains (few ms)
- OS and network dependent

---

## Recommended Next Steps for Your Project

Given your use case (voice interaction, single-user, not high concurrency):

### High Priority ✅
1. **Verify HTTP/2 is active**: Add logging to check if HTTP/2 is being used
2. **Verify compression is working**: Log response `Content-Encoding` headers
3. **Request body compression**: For large LLM prompts (if you send long context)

### Medium Priority 🤔
4. **Response caching**: Cache common/repeated LLM responses
5. **Monitor actual bottlenecks**: Use tracing to see where time is spent (network vs processing)

### Low Priority ❌
6. DNS caching (already optimized)
7. TCP tuning (marginal gains)
8. Batching (requires API changes)

---

## Implementation Priority

For **immediate impact with low effort**:
1. Add compression verification logging
2. Enable explicit HTTP/2 
3. Compress large request bodies if sending >10KB prompts

Would you like me to implement any of these?
