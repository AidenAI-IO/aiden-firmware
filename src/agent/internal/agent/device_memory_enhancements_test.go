package agent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestDeviceMemoryProcedureStepsExtraction 验证改进 1：procedure 记录动作详情
func TestDeviceMemoryProcedureStepsExtraction(t *testing.T) {
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
				Type:            runEventToolCall,
				ToolName:        "launch_app",
				ToolInput:       `{"app_name":"美团"}`,
				ToolDescription: "打开美团App",
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
				Type:            runEventToolCall,
				ToolName:        "touch_gesture",
				ToolInput:       `{"__arg1":"{\"type\":\"tap\",\"point\":{\"x\":500,\"y\":850},\"description\":\"点击购物车按钮\"}"}`,
				ToolDescription: "点击购物车按钮",
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
				Type:            runEventToolCall,
				ToolName:        "ui_query",
				ToolInput:       `{"text":"蜜雪冰城"}`,
				ToolDescription: "搜索蜜雪冰城",
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
	if err != nil || len(procedureFiles) == 0 {
		t.Fatalf("expected procedure file, got paths=%#v err=%v", procedureFiles, err)
	}

	data, err := os.ReadFile(procedureFiles[0])
	if err != nil {
		t.Fatalf("read procedure: %v", err)
	}

	content := string(data)
	// 验证有 Steps 结构
	if !strings.Contains(content, "steps:") {
		t.Errorf("procedure should contain 'steps:' field\n%s", content)
	}
	// 验证坐标被提取
	if !strings.Contains(content, "x=500") || !strings.Contains(content, "y=850") {
		t.Errorf("procedure steps should contain extracted coordinates\n%s", content)
	}
	// 验证 description 被保留
	if !strings.Contains(content, "点击购物车按钮") {
		t.Errorf("procedure steps should contain tool description\n%s", content)
	}
	// 验证 page_name 被记录
	if !strings.Contains(content, "page_name: 购物车") || !strings.Contains(content, "page_name: 首页") {
		t.Errorf("procedure steps should contain page_name from verifier\n%s", content)
	}
}

// TestDeviceMemoryPageIndexing 验证改进 2：按 page_name 索引
func TestDeviceMemoryPageIndexing(t *testing.T) {
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
	if err != nil || len(procedureFiles) == 0 {
		t.Fatalf("expected procedure, got paths=%#v err=%v", procedureFiles, err)
	}

	data, err := os.ReadFile(procedureFiles[0])
	if err != nil {
		t.Fatalf("read procedure: %v", err)
	}

	content := string(data)
	if !strings.Contains(content, "page_name: 设置页") {
		t.Errorf("procedure should have page_name field:\n%s", content)
	}
	if !strings.Contains(content, "entities:") || !strings.Contains(content, "设置页") {
		t.Errorf("procedure should include page_name in entities for search:\n%s", content)
	}
}

// TestDeviceMemoryNavigationFacts 验证改进 3：导航知识抽取
func TestDeviceMemoryNavigationFacts(t *testing.T) {
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
				Type:            runEventToolCall,
				ToolName:        "touch_gesture",
				ToolInput:       `{"__arg1":"{\"type\":\"tap\",\"point\":{\"x\":800,\"y\":900},\"description\":\"点击购物车\"}"}`,
				ToolDescription: "点击购物车",
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
	if err != nil || len(navFiles) == 0 {
		t.Fatalf("expected navigation file, got paths=%#v err=%v", navFiles, err)
	}

	data, err := os.ReadFile(navFiles[0])
	if err != nil {
		t.Fatalf("read navigation: %v", err)
	}

	content := string(data)
	if !strings.Contains(content, "美团/首页") || !strings.Contains(content, "美团/购物车") {
		t.Errorf("navigation should record from→to transition:\n%s", content)
	}
	if !strings.Contains(content, "touch_gesture") {
		t.Errorf("navigation should record tool used:\n%s", content)
	}
	if !strings.Contains(content, "x=800") || !strings.Contains(content, "y=900") {
		t.Errorf("navigation should record coordinates:\n%s", content)
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
	// 验证 success_count 递增
	if !strings.Contains(content, "success_count: 2") {
		t.Errorf("app_profile should track success_count:\n%s", content)
	}
	// 验证 content 渲染为人类可读
	if !strings.Contains(content, "Pages observed:") || !strings.Contains(content, "Tools used:") {
		t.Errorf("app_profile content should be human-readable:\n%s", content)
	}
}

// TestDeviceMemoryAppProfileFailureTracking 验证改进 5：失败时记录已知问题
func TestDeviceMemoryAppProfileFailureTracking(t *testing.T) {
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
	// 记录 known_issues
	if !strings.Contains(content, "known_issues:") || !strings.Contains(content, "网络超时") {
		t.Errorf("app_profile should record known_issues on failure:\n%s", content)
	}
}
