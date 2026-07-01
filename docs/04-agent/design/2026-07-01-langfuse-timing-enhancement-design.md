# Langfuse Timing Enhancement Design

## Overview

增强 Langfuse 遥测系统，补充阻塞 agent loop 的关键耗时指标，帮助识别性能瓶颈和优化机会。

## Background

当前 Langfuse 集成已经捕获了基础的 span 时间戳和部分聚合指标（tool_latency_ms_avg/max），但缺少以下关键耗时信息：

1. **Memory 操作耗时** - retrieve 和 record 操作阻塞主循环，但没有独立追踪
2. **LLM 调用细化** - generation span 存在，但未按 role 分类统计
3. **Session 边界处理耗时** - beginSession 和边界判断的时间
4. **工具耗时统计不足** - 只有 avg/max，缺少百分位数和按类型分组
5. **Prompt cache 命中率** - 已在 RunMetrics 中计算，但未上报到 Langfuse
6. **迭代级别耗时分解** - 无法看到单次迭代内各阶段的时间占比

这些缺失的指标导致性能分析时难以快速定位瓶颈。

## Goals

1. 为所有阻塞操作添加独立的 Langfuse span
2. 在 trace metadata 中补充聚合耗时统计
3. 支持按 role、工具类型等维度分组分析
4. 上报 prompt cache 命中率到 Langfuse
5. 保持向后兼容，不破坏现有的 episode 结构

## Non-Goals

- 不重构现有的 episode recorder 架构
- 不引入新的第三方依赖
- 不追踪非阻塞的异步操作（如 episode export）

## Design

### 1. Episode Events 扩展

在 `TaskEpisodeEvent` 中添加新的事件类型和字段：

```go
// 新增事件类型
const (
    runEventMemoryRetrieve = "memory_retrieve"
    runEventSessionBegin   = "session_begin"
    runEventIterationStart = "iteration_start"
    runEventIterationEnd   = "iteration_end"
)

// TaskEpisodeEvent 添加字段
type TaskEpisodeEvent struct {
    // ... 现有字段 ...
    
    // 新增字段
    DurationMs  *int64             `json:"duration_ms,omitempty"`   // 操作耗时（毫秒）
    Metadata    map[string]interface{} `json:"metadata,omitempty"`  // 额外元数据
}
```

### 2. Runtime 中记录耗时事件

#### 2.1 Memory Retrieve

在 `runtime.go` 的 `Run()` 方法中：

```go
// 在 memory retrieve 前后记录
if r.memoryPlane != nil {
    retrieveStart := time.Now()
    retrieved, retrieveErr := r.memoryPlane.Retrieve(ctx, retrieveReq)
    retrieveDuration := time.Since(retrieveStart).Milliseconds()
    
    if episodeRecorder != nil {
        episodeRecorder.RecordEvent(TaskEpisodeEvent{
            Type:       runEventMemoryRetrieve,
            Ts:         retrieveStart.Format(time.RFC3339Nano),
            DurationMs: &retrieveDuration,
            Metadata: map[string]interface{}{
                "skill_count": len(skillNames),
                "tool_count":  len(toolNamesFromTools(availableTools)),
                "success":     retrieveErr == nil,
            },
        })
    }
    
    if retrieveErr != nil {
        if r.logger != nil {
            r.logger.Warn("[memory] retrieve failed: %v", retrieveErr)
        }
    } else {
        memoryContext = retrieved
    }
}
```

#### 2.2 Session Begin

在 `beginSession()` 调用前后：

```go
sessionBeginStart := time.Now()
beginResult, err := r.beginSession(ctx, SessionBeginRequest{...})
sessionBeginDuration := time.Since(sessionBeginStart).Milliseconds()

if episodeRecorder != nil {
    episodeRecorder.RecordEvent(TaskEpisodeEvent{
        Type:       runEventSessionBegin,
        Ts:         sessionBeginStart.Format(time.RFC3339Nano),
        DurationMs: &sessionBeginDuration,
        Metadata: map[string]interface{}{
            "rotated":               beginResult.Boundary.Rotated,
            "classifier_used":       beginResult.Boundary.ClassifierUsed,
            "pending_chunks_count": beginResult.Boundary.PendingChunksRecalled,
        },
    })
}
```

#### 2.3 Iteration Tracking

在 `roleCollaborativeExecutor.Call()` 中添加迭代追踪：

```go
// 在每次迭代开始时
if e.Recorder != nil {
    e.Recorder.RecordEvent(TaskEpisodeEvent{
        Type: runEventIterationStart,
        Ts:   time.Now().Format(time.RFC3339Nano),
        Metadata: map[string]interface{}{
            "iteration": iterationCount,
        },
    })
}

// 在迭代结束时
if e.Recorder != nil {
    iterDuration := time.Since(iterationStartTime).Milliseconds()
    e.Recorder.RecordEvent(TaskEpisodeEvent{
        Type:       runEventIterationEnd,
        Ts:         time.Now().Format(time.RFC3339Nano),
        DurationMs: &iterDuration,
        Metadata: map[string]interface{}{
            "iteration":       iterationCount,
            "tool_calls":      toolCallsInIteration,
            "llm_calls":       llmCallsInIteration,
        },
    })
}
```

### 3. LLM 调用细化

#### 3.1 在 telemetryPromptCall 中添加 role

```go
type telemetryPromptCall struct {
    ID              string
    Role            string  // "planner", "verifier", "default_final_review"
    StartedAt       time.Time
    EndedAt         time.Time
    Input           interface{}
    Output          interface{}
    UsageDetails    map[string]interface{}
    CostDetails     map[string]float64
    ModelParameters map[string]interface{}
    Metadata        map[string]interface{}
    Media           []telemetryMedia
    Error           string
}
```

#### 3.2 在 usageTrackingModel 中记录 role

从 context 中提取 role 信息：

```go
func (m *usageTrackingModel) GenerateContent(ctx context.Context, messages []llms.MessageContent, options ...llms.CallOption) (*llms.ContentResponse, error) {
    startedAt := time.Now()
    res, err := m.inner.GenerateContent(ctx, messages, options...)
    endedAt := time.Now()
    
    // 从 context 提取 role
    role := extractRoleFromContext(ctx) // 新增辅助函数
    
    if m.promptCapture != nil {
        m.promptCapture.Record(ctx, startedAt, endedAt, messages, options, res, err, m.contextWindow(), role)
    }
    
    // ... 现有逻辑 ...
}

// 在 role_executor.go 中设置 context
func (e *roleCollaborativeExecutor) generateRoleContent(ctx context.Context, role RoleName, messages []llms.MessageContent, options ...llms.CallOption) (*llms.ContentResponse, error) {
    callCtx, cancel := context.WithTimeout(ctx, roleModelCallTimeout)
    defer cancel()
    callCtx = contextWithTelemetryRole(callCtx, role) // 已存在
    // ... 调用 model ...
}
```

### 4. Langfuse Exporter 增强

#### 4.1 新增 Span 类型

在 `episode_exporter.go` 中处理新事件：

```go
case runEventMemoryRetrieve:
    parentID := phaseSpanID
    duration := int64(0)
    if event.DurationMs != nil {
        duration = *event.DurationMs
    }
    body := map[string]interface{}{
        "id":                  uuid.NewString(),
        "traceId":             traceID,
        "parentObservationId": parentID,
        "name":                "memory/retrieve",
        "startTime":           langfuseRFC3339(eventTime),
        "endTime":             langfuseRFC3339(eventTime.Add(time.Duration(duration) * time.Millisecond)),
        "environment":         e.cfg.EnvironmentOrDefault(),
        "metadata":            event.Metadata,
    }
    if version != "" {
        body["version"] = version
    }
    evt, err := newLangfuseEvent("span-create", eventTime, body)
    if err != nil {
        return nil, err
    }
    batch = append(batch, evt)

case runEventSessionBegin:
    // 类似处理
    
case runEventIterationStart:
    // 记录迭代窗口开始
    
case runEventIterationEnd:
    // 创建迭代 span
```

#### 4.2 增强 Trace Metadata

在 `episodeDerivedMetrics()` 中添加新指标：

```go
func episodeDerivedMetrics(events []TaskEpisodeEvent) map[string]interface{} {
    metrics := map[string]interface{}{}
    
    // ... 现有逻辑 ...
    
    // 新增：Memory retrieve 耗时
    var memoryRetrieveDurations []int64
    for _, event := range events {
        if event.Type == runEventMemoryRetrieve && event.DurationMs != nil {
            memoryRetrieveDurations = append(memoryRetrieveDurations, *event.DurationMs)
        }
    }
    if len(memoryRetrieveDurations) > 0 {
        metrics["memory_retrieve_ms"] = memoryRetrieveDurations[0] // 通常只有一次
    }
    
    // 新增：Session begin 耗时
    var sessionBeginDurations []int64
    for _, event := range events {
        if event.Type == runEventSessionBegin && event.DurationMs != nil {
            sessionBeginDurations = append(sessionBeginDurations, *event.DurationMs)
        }
    }
    if len(sessionBeginDurations) > 0 {
        metrics["session_begin_ms"] = sessionBeginDurations[0]
    }
    
    // 新增：迭代耗时统计
    var iterationDurations []int64
    for _, event := range events {
        if event.Type == runEventIterationEnd && event.DurationMs != nil {
            iterationDurations = append(iterationDurations, *event.DurationMs)
        }
    }
    if len(iterationDurations) > 0 {
        metrics["iteration_durations_ms"] = iterationDurations
        metrics["iteration_ms_avg"] = avgInt64(iterationDurations)
        metrics["iteration_ms_p50"] = percentileInt64(iterationDurations, 0.5)
        metrics["iteration_ms_p95"] = percentileInt64(iterationDurations, 0.95)
        metrics["iteration_ms_p99"] = percentileInt64(iterationDurations, 0.99)
    }
    
    // 增强：工具耗时百分位数
    if len(toolLatencies) > 0 {
        sorted := make([]int64, len(toolLatencies))
        copy(sorted, toolLatencies)
        sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
        
        metrics["tool_latency_ms_p50"] = percentileInt64(sorted, 0.5)
        metrics["tool_latency_ms_p95"] = percentileInt64(sorted, 0.95)
        metrics["tool_latency_ms_p99"] = percentileInt64(sorted, 0.99)
        metrics["tool_latency_ms_avg"] = float64(total) / float64(len(toolLatencies))
        metrics["tool_latency_ms_max"] = sorted[len(sorted)-1]
    }
    
    // 新增：按工具类型分组统计
    toolLatenciesByType := make(map[string][]int64)
    for index, event := range events {
        if event.Type == runEventToolCall {
            if pair, ok := toolPairsByCall[index]; ok && pair.HasResult {
                toolName := strings.TrimSpace(event.ToolName)
                if toolName == "" {
                    toolName = "unknown"
                }
                start := parseEpisodeTime(event.Ts, startTime)
                if !pair.ResultTime.Before(start) {
                    latency := pair.ResultTime.Sub(start).Milliseconds()
                    toolLatenciesByType[toolName] = append(toolLatenciesByType[toolName], latency)
                }
            }
        }
    }
    if len(toolLatenciesByType) > 0 {
        toolStats := make(map[string]interface{})
        for toolName, latencies := range toolLatenciesByType {
            sorted := make([]int64, len(latencies))
            copy(sorted, latencies)
            sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
            
            toolStats[toolName] = map[string]interface{}{
                "count": len(latencies),
                "avg":   avgInt64(latencies),
                "p50":   percentileInt64(sorted, 0.5),
                "p95":   percentileInt64(sorted, 0.95),
                "max":   sorted[len(sorted)-1],
            }
        }
        metrics["tool_latency_by_type"] = toolStats
    }
    
    return metrics
}

// 辅助函数
func avgInt64(values []int64) float64 {
    if len(values) == 0 {
        return 0
    }
    var sum int64
    for _, v := range values {
        sum += v
    }
    return float64(sum) / float64(len(values))
}

func percentileInt64(sortedValues []int64, p float64) int64 {
    if len(sortedValues) == 0 {
        return 0
    }
    if p <= 0 {
        return sortedValues[0]
    }
    if p >= 1 {
        return sortedValues[len(sortedValues)-1]
    }
    index := int(float64(len(sortedValues)-1) * p)
    return sortedValues[index]
}
```

#### 4.3 LLM 调用按 role 统计

在 generation span metadata 中添加 role，并在 trace metadata 中聚合：

```go
// 在 promptGenerationBody() 中
body["metadata"] = map[string]interface{}{
    "role":         call.Role, // 新增
    "prompt_index": index + 1,
}

// 在 episodeDerivedMetrics() 中添加 LLM 调用统计
func aggregateLLMCallMetrics(prompts []telemetryPromptCall) map[string]interface{} {
    byRole := make(map[string][]time.Duration)
    
    for _, call := range prompts {
        role := strings.TrimSpace(call.Role)
        if role == "" {
            role = "unknown"
        }
        duration := call.EndedAt.Sub(call.StartedAt)
        byRole[role] = append(byRole[role], duration)
    }
    
    result := make(map[string]interface{})
    for role, durations := range byRole {
        if len(durations) == 0 {
            continue
        }
        
        ms := make([]int64, len(durations))
        for i, d := range durations {
            ms[i] = d.Milliseconds()
        }
        sort.Slice(ms, func(i, j int) bool { return ms[i] < ms[j] })
        
        result[role+"_call_count"] = len(durations)
        result[role+"_call_ms_avg"] = avgInt64(ms)
        result[role+"_call_ms_p50"] = percentileInt64(ms, 0.5)
        result[role+"_call_ms_p95"] = percentileInt64(ms, 0.95)
    }
    
    return result
}
```

### 5. Prompt Cache 命中率上报

#### 5.1 在 Trace Metadata 中添加

```go
func (e *EpisodeExporter) traceMetadata(episode TaskEpisode) map[string]interface{} {
    meta := map[string]interface{}{
        // ... 现有字段 ...
    }
    
    // 新增：从 episode.Extra 中提取 cache 命中率
    if episode.Extra != nil {
        if promptTokens, ok := usageMetricInt(episode.Extra["prompt_tokens"]); ok && promptTokens > 0 {
            if cachedTokens, ok := usageMetricInt(episode.Extra["cached_prompt_tokens"]); ok {
                cacheHitRate := float64(cachedTokens) / float64(promptTokens)
                meta["prompt_cache_hit_rate"] = cacheHitRate
                meta["cached_prompt_tokens"] = cachedTokens
            }
        }
    }
    
    // ... 其他逻辑 ...
    
    return meta
}
```

#### 5.2 在 Generation Span 中添加

```go
func (e *EpisodeExporter) promptGenerationBody(...) map[string]interface{} {
    // ... 现有逻辑 ...
    
    // 添加 cache 命中信息
    if len(call.UsageDetails) > 0 {
        body["usageDetails"] = call.UsageDetails
        
        // 如果有 cached tokens，计算命中率
        if promptTokens, ok := call.UsageDetails["input"].(int); ok && promptTokens > 0 {
            if cachedTokens, ok := call.UsageDetails["cached_input"].(int); ok && cachedTokens > 0 {
                if metadata, ok := body["metadata"].(map[string]interface{}); ok {
                    metadata["cache_hit_rate"] = float64(cachedTokens) / float64(promptTokens)
                }
            }
        }
    }
    
    return body
}
```

### 6. 在 Runtime 中记录 cached tokens

在 `usageTrackingModel` 中确保记录 cached tokens：

```go
func (m *usageTrackingModel) GenerateContent(ctx context.Context, messages []llms.MessageContent, options ...llms.CallOption) (*llms.ContentResponse, error) {
    // ... 现有逻辑 ...
    
    if res != nil && m.metrics != nil {
        usage := res.Usage
        m.metrics.PromptTokens += usage.PromptTokens
        m.metrics.CompletionTokens += usage.CompletionTokens
        m.metrics.TotalTokens += usage.TotalTokens
        
        // 记录 cached prompt tokens
        if details := usage.PromptTokensDetails; details != nil {
            m.metrics.CachedPromptTokens += details.CachedTokens
        }
        
        // 在 promptCapture 中也记录
        if m.promptCapture != nil {
            usageDetails := map[string]interface{}{
                "input":  usage.PromptTokens,
                "output": usage.CompletionTokens,
                "total":  usage.TotalTokens,
            }
            if details := usage.PromptTokensDetails; details != nil && details.CachedTokens > 0 {
                usageDetails["cached_input"] = details.CachedTokens
            }
            // ... 传递给 promptCapture.Record() ...
        }
    }
    
    return res, err
}
```

## Data Flow

```
Runtime.Run()
  ├─ [新增] RecordEvent(memory_retrieve) with duration
  ├─ [新增] RecordEvent(session_begin) with duration
  └─ executor.Call()
       └─ for each iteration:
            ├─ [新增] RecordEvent(iteration_start)
            ├─ generateRoleContent() 
            │    └─ [增强] promptCapture.Record() with role & cached_tokens
            ├─ executeToolCall()
            │    └─ [已有] RecordEvent(tool_call + tool_result)
            └─ [新增] RecordEvent(iteration_end) with duration

EpisodeExporter.buildLangfuseBatch()
  ├─ [新增] 处理 memory_retrieve event → span
  ├─ [新增] 处理 session_begin event → span
  ├─ [新增] 处理 iteration_start/end events → span
  ├─ [增强] generation span metadata 添加 role
  ├─ [增强] trace metadata 添加聚合统计:
  │    ├─ memory_retrieve_ms
  │    ├─ session_begin_ms
  │    ├─ iteration_ms_avg/p50/p95/p99
  │    ├─ tool_latency_ms_p50/p95/p99
  │    ├─ tool_latency_by_type
  │    ├─ {role}_call_count/ms_avg/p50/p95
  │    └─ prompt_cache_hit_rate
  └─ Ingest to Langfuse
```

## Implementation Plan

### Phase 1: 基础事件记录（1-2h）
1. 在 `TaskEpisodeEvent` 中添加 `DurationMs` 和 `Metadata` 字段
2. 在 `runtime.go` 中添加 memory_retrieve 和 session_begin 事件记录
3. 添加辅助函数 `avgInt64()` 和 `percentileInt64()`

### Phase 2: 迭代追踪（1-2h）
1. 在 `roleCollaborativeExecutor.Call()` 中添加迭代计数器
2. 记录 iteration_start 和 iteration_end 事件
3. 在迭代 metadata 中记录 tool_calls 和 llm_calls 计数

### Phase 3: LLM 调用细化（1h）
1. 在 `telemetryPromptCall` 中添加 Role 字段
2. 在 `promptCapture.Record()` 中接收 role 参数
3. 在 `usageTrackingModel` 中传递 role

### Phase 4: Langfuse Export 增强（2h）
1. 在 `episode_exporter.go` 中处理新事件类型
2. 实现 `episodeDerivedMetrics()` 增强逻辑
3. 实现 LLM 调用按 role 统计

### Phase 5: Cache 命中率（30min）
1. 在 `usageTrackingModel` 中确保记录 cached_prompt_tokens
2. 在 trace metadata 中添加 prompt_cache_hit_rate
3. 在 generation span metadata 中添加 cache_hit_rate

### Phase 6: 测试验证（1-2h）
1. 单元测试：百分位数计算、分组统计
2. 集成测试：运行完整任务，验证 Langfuse UI 显示
3. 文档更新：更新 telemetry-langfuse.md

## Testing Strategy

### Unit Tests

1. **百分位数计算**
```go
func TestPercentileInt64(t *testing.T) {
    values := []int64{10, 20, 30, 40, 50, 60, 70, 80, 90, 100}
    assert.Equal(t, int64(50), percentileInt64(values, 0.5))
    assert.Equal(t, int64(95), percentileInt64(values, 0.95))
    assert.Equal(t, int64(99), percentileInt64(values, 0.99))
}
```

2. **分组统计**
```go
func TestToolLatencyByType(t *testing.T) {
    events := []TaskEpisodeEvent{
        {Type: runEventToolCall, ToolName: "screenshot", Ts: "2026-07-01T10:00:00Z"},
        {Type: "tool_result", ToolName: "screenshot", Ts: "2026-07-01T10:00:01.500Z"},
        {Type: runEventToolCall, ToolName: "screenshot", Ts: "2026-07-01T10:00:02Z"},
        {Type: "tool_result", ToolName: "screenshot", Ts: "2026-07-01T10:00:03.200Z"},
    }
    
    metrics := episodeDerivedMetrics(events)
    stats := metrics["tool_latency_by_type"].(map[string]interface{})
    screenshotStats := stats["screenshot"].(map[string]interface{})
    
    assert.Equal(t, 2, screenshotStats["count"])
    assert.InDelta(t, 1350.0, screenshotStats["avg"], 50)
}
```

### Integration Tests

1. 运行完整的 agent task
2. 检查生成的 episode.yaml 和 events.jsonl
3. 验证 Langfuse UI 中：
   - 新的 span 类型正确显示
   - Trace metadata 包含新指标
   - Generation span 有 role 标记
   - Cache 命中率正确显示

## Documentation Updates

更新 `docs/04-agent/telemetry-langfuse.md`：

### 新增 Span 类型

| Aiden Episode | Langfuse |
| --- | --- |
| `memory_retrieve` | Span `memory/retrieve` (耗时 + skill/tool 计数) |
| `session_begin` | Span `session/begin` (耗时 + rotated/classifier 信息) |
| `iteration_start/end` | Span `iteration_N` (完整迭代耗时) |

### 新增 Metadata 字段

| Field | Description |
| --- | --- |
| `memory_retrieve_ms` | Memory retrieve 操作耗时 |
| `session_begin_ms` | Session begin 操作耗时 |
| `iteration_ms_avg/p50/p95/p99` | 迭代耗时统计 |
| `tool_latency_ms_p50/p95/p99` | 工具延迟百分位数 |
| `tool_latency_by_type` | 按工具类型分组的耗时统计 |
| `{role}_call_count` | 各 role 的 LLM 调用次数 |
| `{role}_call_ms_avg/p50/p95` | 各 role 的 LLM 调用耗时 |
| `prompt_cache_hit_rate` | Prompt cache 命中率 (0-1) |
| `cached_prompt_tokens` | 缓存命中的 token 数量 |

## Risks and Mitigations

### Risk 1: Event 数据量增加
**影响**: 每个任务增加 3-5 个 event，episode.jsonl 文件变大

**缓解**:
- 新增的 event 数量有限（memory_retrieve、session_begin、iteration_start/end）
- 单个任务通常只有 1-5 次迭代，增量可控
- DurationMs 和 Metadata 都是可选字段，未来可以根据需要选择性记录

### Risk 2: Langfuse Span 数量增加
**影响**: 每个 trace 的 span 数量增加，可能影响 UI 渲染

**缓解**:
- Langfuse 支持大量 span（tested up to 1000+）
- 新增 span 有限（memory、session、iterations）
- Span 有清晰的层级关系，UI 可折叠

### Risk 3: 百分位数计算性能
**影响**: 排序操作可能影响 export 性能

**缓解**:
- 百分位数只在 export 时计算一次，不在热路径
- 典型任务的工具调用数量 < 100，排序开销可忽略
- 如果发现性能问题，可以采样计算

### Risk 4: 向后兼容性
**影响**: 旧版本 agent 生成的 episode 缺少新字段

**缓解**:
- 所有新字段都是可选的
- Exporter 在处理时会检查字段存在性
- 缺少新字段时降级为现有行为

## Success Metrics

1. **功能完整性**
   - 所有新 span 类型正确生成
   - Trace metadata 包含所有新指标
   - Cache 命中率准确显示

2. **性能影响**
   - Episode export 耗时增加 < 10%
   - Episode 文件大小增加 < 20%

3. **可用性**
   - 在 Langfuse UI 中可以快速识别慢操作
   - 可以按 role 或工具类型筛选分析
   - Cache 命中率帮助优化 prompt 设计

## Future Enhancements

1. **动态采样** - 对于高频任务，可以采样记录详细耗时
2. **实时告警** - 基于 Langfuse webhook，当某个操作耗时超过阈值时告警
3. **自动优化建议** - 分析历史数据，自动识别优化机会
4. **更细粒度的工具统计** - 按工具参数（如 screenshot 区域大小）分组统计
