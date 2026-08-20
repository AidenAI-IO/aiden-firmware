package agent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCommitEpisodeDoesNotSynchronouslyExtractProcedure(t *testing.T) {
	ctx := context.Background()
	memoryDir := filepath.Join(t.TempDir(), "memory")
	plane := NewFilesystemMemoryPlane(memoryDir, DefaultMemoryExtractionConfig(), nil)

	episode := TaskEpisode{
		ID:        "ep_test_steps",
		Status:    "active",
		StartedAt: "2026-06-02T10:00:00Z",
		EndedAt:   "2026-06-02T10:00:30Z",
		UserGoal:  "在美团添加蜜雪冰城",
		Tags:      []string{"购物"},
		Entities:  []string{"美团App", "蜜雪冰城"},
		DeviceScope: map[string]string{
			"device_id": "test_device",
		},
		Outcome: TaskEpisodeOutcome{
			Success:        true,
			VerifierReason: "已添加",
		},
		Events: []TaskEpisodeEvent{
			{
				Type:      runEventToolCall,
				ToolName:  "launch_app",
				ToolInput: `{"app_name":"美团"}`,
				Content:   "打开美团App",
			},
			{
				Type:        "tool_result",
				ToolName:    "launch_app",
				Observation: "launched",
			},
			{
				Type: "verifier_decision",
				ObservedState: &observedWorldState{
					AppName:  "美团",
					PageName: "首页",
				},
			},
			{
				Type:      runEventToolCall,
				ToolName:  "touch_gesture",
				ToolInput: `{"__arg1":"{\"type\":\"tap\",\"point\":{\"x\":500,\"y\":850},\"description\":\"点击购物车按钮\"}"}`,
				Content:   "点击购物车按钮",
			},
			{
				Type:        "tool_result",
				ToolName:    "touch_gesture",
				Observation: "tapped",
			},
			{
				Type: "verifier_decision",
				ObservedState: &observedWorldState{
					AppName:  "美团",
					PageName: "购物车",
				},
			},
			{
				Type:      runEventToolCall,
				ToolName:  "ui_query",
				ToolInput: `{"text":"蜜雪冰城"}`,
				Content:   "搜索蜜雪冰城",
			},
			{
				Type:        "tool_result",
				ToolName:    "ui_query",
				Observation: "found: 蜜雪冰城",
			},
		},
	}

	if err := plane.CommitEpisode(ctx, episode); err != nil {
		t.Fatalf("CommitEpisode: %v", err)
	}

	procedureFiles, err := filepath.Glob(filepath.Join(memoryDir, "device", "procedures", "*.yaml"))
	if err != nil || len(procedureFiles) != 0 {
		t.Fatalf("synchronous procedure files = %#v error=%v, want none before Episode Memory Worker runs", procedureFiles, err)
	}
}

func TestCommitEpisodeDoesNotUseVerifierStateForProcedureIndexing(t *testing.T) {
	ctx := context.Background()
	memoryDir := filepath.Join(t.TempDir(), "memory")
	plane := NewFilesystemMemoryPlane(memoryDir, DefaultMemoryExtractionConfig(), nil)

	episode := TaskEpisode{
		ID:        "ep_page_index",
		Status:    "active",
		StartedAt: "2026-06-02T10:00:00Z",
		EndedAt:   "2026-06-02T10:00:30Z",
		UserGoal:  "测试页面索引",
		Tags:      []string{"测试"},
		Entities:  []string{"测试App"},
		DeviceScope: map[string]string{
			"device_id": "test_device",
		},
		Outcome: TaskEpisodeOutcome{Success: true},
		Events: []TaskEpisodeEvent{
			{Type: runEventToolCall, ToolName: "echo", ToolInput: "{}"},
			{Type: "tool_result", ToolName: "echo"},
			{
				Type: "verifier_decision",
				ObservedState: &observedWorldState{
					AppName:  "测试App",
					PageName: "设置页",
				},
			},
		},
	}

	if err := plane.CommitEpisode(ctx, episode); err != nil {
		t.Fatalf("CommitEpisode: %v", err)
	}

	procedureFiles, err := filepath.Glob(filepath.Join(memoryDir, "device", "procedures", "*.yaml"))
	if err != nil || len(procedureFiles) != 0 {
		t.Fatalf("synchronous procedure files = %#v error=%v, want none", procedureFiles, err)
	}
}

func TestCommitEpisodeDoesNotSynchronouslyExtractNavigation(t *testing.T) {
	ctx := context.Background()
	memoryDir := filepath.Join(t.TempDir(), "memory")
	plane := NewFilesystemMemoryPlane(memoryDir, DefaultMemoryExtractionConfig(), nil)

	episode := TaskEpisode{
		ID:        "ep_nav",
		Status:    "active",
		StartedAt: "2026-06-02T10:00:00Z",
		EndedAt:   "2026-06-02T10:00:30Z",
		UserGoal:  "测试导航抽取",
		Tags:      []string{"导航"},
		Entities:  []string{"美团"},
		DeviceScope: map[string]string{
			"device_id": "test_device",
		},
		Outcome: TaskEpisodeOutcome{Success: true},
		Events: []TaskEpisodeEvent{
			{
				Type: "verifier_decision",
				ObservedState: &observedWorldState{
					AppName:  "美团",
					PageName: "首页",
				},
			},
			{
				Type:      runEventToolCall,
				ToolName:  "touch_gesture",
				ToolInput: `{"__arg1":"{\"type\":\"tap\",\"point\":{\"x\":800,\"y\":900},\"description\":\"点击购物车\"}"}`,
				Content:   "点击购物车",
			},
			{Type: "tool_result", ToolName: "touch_gesture"},
			{
				Type: "verifier_decision",
				ObservedState: &observedWorldState{
					AppName:  "美团",
					PageName: "购物车",
				},
			},
		},
	}

	if err := plane.CommitEpisode(ctx, episode); err != nil {
		t.Fatalf("CommitEpisode: %v", err)
	}

	navFiles, err := filepath.Glob(filepath.Join(memoryDir, "device", "navigation", "*.yaml"))
	if err != nil || len(navFiles) != 0 {
		t.Fatalf("synchronous navigation files = %#v error=%v, want none before Episode Memory Worker runs", navFiles, err)
	}
}

// TestDeviceMemoryAppProfileAccumulation 验证改进 5：app_profile 累积更新
func TestDeviceMemoryAppProfileAccumulation(t *testing.T) {
	ctx := context.Background()
	memoryDir := filepath.Join(t.TempDir(), "memory")
	plane := NewFilesystemMemoryPlane(memoryDir, DefaultMemoryExtractionConfig(), nil)

	// 第一次成功任务
	ep1 := TaskEpisode{
		ID:        "ep1",
		Status:    "active",
		StartedAt: "2026-06-02T10:00:00Z",
		EndedAt:   "2026-06-02T10:00:10Z",
		UserGoal:  "打开美团首页",
		Entities:  []string{"美团App"},
		DeviceScope: map[string]string{
			"device_id": "test_device",
		},
		Outcome: TaskEpisodeOutcome{Success: true},
		Events: []TaskEpisodeEvent{
			{Type: runEventToolCall, ToolName: "launch_app"},
			{Type: "tool_result", ToolName: "launch_app"},
			{
				Type: "verifier_decision",
				ObservedState: &observedWorldState{
					AppName:  "美团",
					PageName: "首页",
				},
			},
		},
	}

	if err := plane.CommitEpisode(ctx, ep1); err != nil {
		t.Fatalf("CommitEpisode ep1: %v", err)
	}

	// 第二次成功任务，新增页面和工具
	ep2 := TaskEpisode{
		ID:        "ep2",
		Status:    "active",
		StartedAt: "2026-06-02T10:01:00Z",
		EndedAt:   "2026-06-02T10:01:10Z",
		UserGoal:  "进入美团购物车",
		Entities:  []string{"美团App"},
		DeviceScope: map[string]string{
			"device_id": "test_device",
		},
		Outcome: TaskEpisodeOutcome{Success: true},
		Events: []TaskEpisodeEvent{
			{Type: runEventToolCall, ToolName: "touch_gesture"},
			{Type: "tool_result", ToolName: "touch_gesture"},
			{
				Type: "verifier_decision",
				ObservedState: &observedWorldState{
					AppName:  "美团",
					PageName: "购物车",
				},
			},
		},
	}

	if err := plane.CommitEpisode(ctx, ep2); err != nil {
		t.Fatalf("CommitEpisode ep2: %v", err)
	}

	appFiles, err := filepath.Glob(filepath.Join(memoryDir, "device", "apps", "*.yaml"))
	if err != nil || len(appFiles) == 0 {
		t.Fatalf("expected app_profile, got paths=%#v err=%v", appFiles, err)
	}

	data, err := os.ReadFile(appFiles[0])
	if err != nil {
		t.Fatalf("read app_profile: %v", err)
	}

	content := string(data)
	// 验证 pages_seen 累积了两个页面
	if !strings.Contains(content, "pages_seen:") {
		t.Errorf("app_profile should have pages_seen field:\n%s", content)
	}
	if !strings.Contains(content, "首页") || !strings.Contains(content, "购物车") {
		t.Errorf("app_profile should accumulate pages from multiple episodes:\n%s", content)
	}
	// 验证 tools_used 累积
	if !strings.Contains(content, "tools_used:") {
		t.Errorf("app_profile should have tools_used field:\n%s", content)
	}
	if !strings.Contains(content, "launch_app") || !strings.Contains(content, "touch_gesture") {
		t.Errorf("app_profile should accumulate tools:\n%s", content)
	}
	if strings.Contains(content, "success_count:") {
		t.Errorf("deterministic app_profile should not infer success from Episode outcome:\n%s", content)
	}
	// 验证 content 渲染为人类可读
	if !strings.Contains(content, "Pages observed:") || !strings.Contains(content, "Tools used:") {
		t.Errorf("app_profile content should be human-readable:\n%s", content)
	}
}

func TestDeviceMemoryAppProfileDoesNotTurnOutcomeIntoKnownIssue(t *testing.T) {
	ctx := context.Background()
	memoryDir := filepath.Join(t.TempDir(), "memory")
	plane := NewFilesystemMemoryPlane(memoryDir, DefaultMemoryExtractionConfig(), nil)

	episode := TaskEpisode{
		ID:        "ep_fail",
		Status:    "active",
		StartedAt: "2026-06-02T10:00:00Z",
		EndedAt:   "2026-06-02T10:00:30Z",
		UserGoal:  "测试失败记录",
		Entities:  []string{"测试App"},
		DeviceScope: map[string]string{
			"device_id": "test_device",
		},
		Outcome: TaskEpisodeOutcome{
			Success:       false,
			FailureReason: "网络超时",
		},
		Events: []TaskEpisodeEvent{
			{Type: runEventToolCall, ToolName: "launch_app"},
			{Type: "tool_result", ToolName: "launch_app"},
			{
				Type: "verifier_decision",
				ObservedState: &observedWorldState{
					AppName:  "测试App",
					PageName: "加载页",
				},
			},
		},
	}

	if err := plane.CommitEpisode(ctx, episode); err != nil {
		t.Fatalf("CommitEpisode: %v", err)
	}

	appFiles, err := filepath.Glob(filepath.Join(memoryDir, "device", "apps", "*.yaml"))
	if err != nil || len(appFiles) == 0 {
		t.Fatalf("expected app_profile even on failure, got paths=%#v err=%v", appFiles, err)
	}

	data, err := os.ReadFile(appFiles[0])
	if err != nil {
		t.Fatalf("read app_profile: %v", err)
	}

	content := string(data)
	// 失败时仍然记录 pages_seen
	if !strings.Contains(content, "加载页") {
		t.Errorf("app_profile should record pages even on failure:\n%s", content)
	}
	if strings.Contains(content, "known_issues:") || strings.Contains(content, "网络超时") {
		t.Errorf("deterministic app_profile should not infer a known issue from Episode outcome:\n%s", content)
	}
}
