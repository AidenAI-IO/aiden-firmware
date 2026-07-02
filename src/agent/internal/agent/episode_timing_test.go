package agent

import (
	"testing"
)

func TestPercentileInt64(t *testing.T) {
	tests := []struct {
		name   string
		values []int64
		p      float64
		want   int64
	}{
		{
			name:   "empty slice",
			values: []int64{},
			p:      0.5,
			want:   0,
		},
		{
			name:   "single value",
			values: []int64{42},
			p:      0.5,
			want:   42,
		},
		{
			name:   "p50 of 10 values",
			values: []int64{10, 20, 30, 40, 50, 60, 70, 80, 90, 100},
			p:      0.5,
			want:   50,
		},
		{
			name:   "p95 of 10 values",
			values: []int64{10, 20, 30, 40, 50, 60, 70, 80, 90, 100},
			p:      0.95,
			want:   90,
		},
		{
			name:   "p99 of 10 values",
			values: []int64{10, 20, 30, 40, 50, 60, 70, 80, 90, 100},
			p:      0.99,
			want:   90,
		},
		{
			name:   "p0 returns first",
			values: []int64{10, 20, 30},
			p:      0.0,
			want:   10,
		},
		{
			name:   "p100 returns last",
			values: []int64{10, 20, 30},
			p:      1.0,
			want:   30,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := percentileInt64(tt.values, tt.p)
			if got != tt.want {
				t.Errorf("percentileInt64(%v, %v) = %v, want %v", tt.values, tt.p, got, tt.want)
			}
		})
	}
}

func TestAvgInt64(t *testing.T) {
	tests := []struct {
		name   string
		values []int64
		want   float64
	}{
		{
			name:   "empty slice",
			values: []int64{},
			want:   0,
		},
		{
			name:   "single value",
			values: []int64{42},
			want:   42,
		},
		{
			name:   "multiple values",
			values: []int64{10, 20, 30, 40, 50},
			want:   30,
		},
		{
			name:   "uneven values",
			values: []int64{1, 2, 3},
			want:   2,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := avgInt64(tt.values)
			if got != tt.want {
				t.Errorf("avgInt64(%v) = %v, want %v", tt.values, got, tt.want)
			}
		})
	}
}

func TestEpisodeDerivedMetrics_ToolLatencyByType(t *testing.T) {
	events := []TaskEpisodeEvent{
		{Type: runEventToolCall, ToolName: "Read", Ts: "2026-07-02T10:00:00.000Z"},
		{Type: "tool_result", ToolName: "Read", Ts: "2026-07-02T10:00:01.500Z"},
		{Type: runEventToolCall, ToolName: "Read", Ts: "2026-07-02T10:00:02.000Z"},
		{Type: "tool_result", ToolName: "Read", Ts: "2026-07-02T10:00:03.200Z"},
		{Type: runEventToolCall, ToolName: "Bash", Ts: "2026-07-02T10:00:04.000Z"},
		{Type: "tool_result", ToolName: "Bash", Ts: "2026-07-02T10:00:05.000Z"},
	}

	metrics := episodeDerivedMetrics(events)

	// Check that tool_latency_by_type exists
	toolLatencyByType, ok := metrics["tool_latency_by_type"].(map[string]interface{})
	if !ok {
		t.Fatal("tool_latency_by_type not found in metrics")
	}

	// Check Read tool stats
	readStats, ok := toolLatencyByType["Read"].(map[string]interface{})
	if !ok {
		t.Fatal("Read tool stats not found")
	}
	if readStats["count"] != 2 {
		t.Errorf("Read count = %v, want 2", readStats["count"])
	}

	// Check Bash tool stats
	bashStats, ok := toolLatencyByType["Bash"].(map[string]interface{})
	if !ok {
		t.Fatal("Bash tool stats not found")
	}
	if bashStats["count"] != 1 {
		t.Errorf("Bash count = %v, want 1", bashStats["count"])
	}
}

func TestEpisodeDerivedMetrics_MemoryRetrieve(t *testing.T) {
	duration := int64(250)
	events := []TaskEpisodeEvent{
		{
			Type:       runEventMemoryRetrieve,
			Ts:         "2026-07-02T10:00:00.000Z",
			DurationMs: &duration,
			Metadata: map[string]interface{}{
				"skill_count": 5,
				"tool_count":  10,
				"success":     true,
			},
		},
	}

	metrics := episodeDerivedMetrics(events)

	if got := metrics["memory_retrieve_ms"]; got != duration {
		t.Errorf("memory_retrieve_ms = %v, want %v", got, duration)
	}
}

func TestEpisodeDerivedMetrics_SessionBegin(t *testing.T) {
	duration := int64(150)
	events := []TaskEpisodeEvent{
		{
			Type:       runEventSessionBegin,
			Ts:         "2026-07-02T10:00:00.000Z",
			DurationMs: &duration,
			Metadata: map[string]interface{}{
				"rotated": true,
			},
		},
	}

	metrics := episodeDerivedMetrics(events)

	if got := metrics["session_begin_ms"]; got != duration {
		t.Errorf("session_begin_ms = %v, want %v", got, duration)
	}
}

func TestEpisodeDerivedMetrics_IterationTiming(t *testing.T) {
	iter1 := int64(1000)
	iter2 := int64(2000)
	iter3 := int64(1500)
	events := []TaskEpisodeEvent{
		{Type: runEventIterationEnd, Ts: "2026-07-02T10:00:01.000Z", DurationMs: &iter1},
		{Type: runEventIterationEnd, Ts: "2026-07-02T10:00:03.000Z", DurationMs: &iter2},
		{Type: runEventIterationEnd, Ts: "2026-07-02T10:00:04.500Z", DurationMs: &iter3},
	}

	metrics := episodeDerivedMetrics(events)

	// Check that iteration durations are recorded
	durations, ok := metrics["iteration_durations_ms"].([]int64)
	if !ok {
		t.Fatal("iteration_durations_ms not found")
	}
	if len(durations) != 3 {
		t.Errorf("len(iteration_durations_ms) = %v, want 3", len(durations))
	}

	// Check avg
	if got := metrics["iteration_ms_avg"].(float64); got != 1500 {
		t.Errorf("iteration_ms_avg = %v, want 1500", got)
	}

	// Check p50 (middle value of sorted [1000, 1500, 2000])
	if got := metrics["iteration_ms_p50"].(int64); got != 1500 {
		t.Errorf("iteration_ms_p50 = %v, want 1500", got)
	}
}
