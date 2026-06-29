package agent

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestTelemetryConfigDefaults(t *testing.T) {
	cfg := TelemetryConfig{}
	if cfg.EnabledOrDefault() {
		t.Fatal("EnabledOrDefault() = true, want false")
	}
	if cfg.ProviderOrDefault() != "langfuse" {
		t.Fatalf("ProviderOrDefault() = %q, want langfuse", cfg.ProviderOrDefault())
	}
	if !cfg.UploadScreenshotsOrDefault() {
		t.Fatal("UploadScreenshotsOrDefault() = false, want true")
	}
	if cfg.UploadTimeoutOrDefault() != 30*time.Second {
		t.Fatalf("UploadTimeoutOrDefault() = %s, want 30s", cfg.UploadTimeoutOrDefault())
	}
	if cfg.MaxRetryOrDefault() != 2 {
		t.Fatalf("MaxRetryOrDefault() = %d, want 2", cfg.MaxRetryOrDefault())
	}
}

func TestConfigValidateTelemetryRequiresBaseURL(t *testing.T) {
	enabled := true
	cfg := Config{
		Model: ModelConfig{Provider: "fake"},
		Telemetry: TelemetryConfig{
			Enabled: &enabled,
		},
	}
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "telemetry.base_url") {
		t.Fatalf("Validate() = %v, want telemetry.base_url error", err)
	}
}

func TestConfigValidateTelemetryRequiresKeys(t *testing.T) {
	enabled := true
	cfg := Config{
		Model: ModelConfig{Provider: "fake"},
		Telemetry: TelemetryConfig{
			Enabled: &enabled,
			BaseURL: "http://langfuse.test",
		},
	}
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "telemetry.public_key") {
		t.Fatalf("Validate() = %v, want telemetry.public_key error", err)
	}

	cfg.Telemetry.PublicKey = "pk-test"
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "telemetry.secret_key") {
		t.Fatalf("Validate() = %v, want telemetry.secret_key error", err)
	}
}

func TestBuildLangfuseBatchMapsPlannerToolVerifier(t *testing.T) {
	start := time.Date(2026, 6, 3, 10, 0, 0, 0, time.UTC)
	canFinish := true
	episode := TaskEpisode{
		ID:        "ep_test_001",
		StartedAt: start.Format(time.RFC3339Nano),
		EndedAt:   start.Add(12 * time.Second).Format(time.RFC3339Nano),
		UserGoal:  "打开系统设置",
		Outcome: TaskEpisodeOutcome{
			Success:     true,
			FinalAnswer: "已打开设置",
		},
		Tags: []string{"settings"},
		Extra: map[string]interface{}{
			"total_duration_ms": 12000.0,
			"prompt_tokens":     100.0,
			"completion_tokens": 20.0,
			"total_tokens":      120.0,
			"model":             "openrouter/test-model",
		},
		Events: []TaskEpisodeEvent{
			{
				EventID:   "evt1",
				Ts:        start.Format(time.RFC3339Nano),
				Type:      "planner_decision",
				Role:      "planner",
				Objective: "打开系统设置",
				Plan:      []string{"打开系统设置"},
				NextStep:  "点击设置图标",
			},
			{
				EventID: "evt_todo",
				Ts:      start.Add(time.Second).Format(time.RFC3339Nano),
				Type:    runEventTodoUpdate,
				Content: "点击设置图标",
				Todo: &TodoState{
					Mode:      TodoModePlanned,
					Objective: "打开系统设置",
					Revision:  1,
					CurrentID: "todo-r1-step1",
					Items: []TodoItem{
						{
							ID:        "todo-r1-step1",
							Text:      "点击设置图标",
							Status:    TodoInProgress,
							Source:    TodoSourceCommittedPlan,
							StepIndex: 1,
						},
					},
				},
				SpeechEligible: true,
			},
			{
				EventID:   "evt2",
				Ts:        start.Add(2 * time.Second).Format(time.RFC3339Nano),
				Type:      runEventToolCall,
				ToolName:  "mouse_click",
				ToolInput: `{"x":100,"y":200}`,
				Content:   "点击设置。",
			},
			{
				EventID:       "evt3",
				Ts:            start.Add(3 * time.Second).Format(time.RFC3339Nano),
				Type:          "tool_result",
				ToolName:      "mouse_click",
				Observation:   `{"action_output":"clicked"}`,
				ScreenshotRef: "artifacts/step_003.jpeg",
			},
			{
				EventID:   "evt4",
				Ts:        start.Add(5 * time.Second).Format(time.RFC3339Nano),
				Type:      "verifier_decision",
				CanFinish: &canFinish,
				Reason:    "设置页面已打开",
				Content:   "已打开设置",
			},
			{
				EventID: "evt_todo_closed",
				Ts:      start.Add(6 * time.Second).Format(time.RFC3339Nano),
				Type:    runEventTodoClosed,
				Reason:  "final_answer",
				Todo: &TodoState{
					Mode:      TodoModePlanned,
					Objective: "打开系统设置",
					Revision:  1,
					CurrentID: "todo-r1-step1",
					Items: []TodoItem{
						{
							ID:        "todo-r1-step1",
							Text:      "点击设置图标",
							Status:    TodoDone,
							Source:    TodoSourceCommittedPlan,
							StepIndex: 1,
						},
					},
				},
			},
		},
	}

	exporter := NewEpisodeExporter(TelemetryConfig{
		Enabled: boolPtr(true),
		BaseURL: "http://langfuse.test",
		Tags:    []string{"aiden-hardware"},
	}, nil)
	batch, err := exporter.buildLangfuseBatch(context.Background(), episode, t.TempDir())
	if err != nil {
		t.Fatalf("buildLangfuseBatch() error = %v", err)
	}
	if len(batch) < 5 {
		t.Fatalf("batch len = %d, want at least 5 events", len(batch))
	}

	types := map[string]int{}
	names := map[string]int{}
	var usageBody map[string]interface{}
	var toolBody map[string]interface{}
	for _, event := range batch {
		types[event.Type]++
		var body map[string]interface{}
		if err := json.Unmarshal(event.Body, &body); err != nil {
			t.Fatalf("decode body: %v", err)
		}
		if event.Type == "generation-create" {
			usageBody = body
		}
		if name, _ := body["name"].(string); name != "" {
			names[name]++
			if name == "tool/mouse_click" {
				toolBody = body
			}
		}
	}
	if types["trace-create"] != 1 {
		t.Fatalf("trace-create count = %d, want 1", types["trace-create"])
	}
	if types["generation-create"] != 1 {
		t.Fatalf("generation-create count = %d, want 1", types["generation-create"])
	}
	if types["score-create"] != 1 {
		t.Fatalf("score-create count = %d, want 1", types["score-create"])
	}
	if usageBody["model"] != "openrouter/test-model" {
		t.Fatalf("usage generation model = %v, want openrouter/test-model", usageBody["model"])
	}
	usageDetails, ok := usageBody["usageDetails"].(map[string]interface{})
	if !ok {
		t.Fatalf("usageDetails missing or wrong type: %#v", usageBody["usageDetails"])
	}
	if usageDetails["input"] != float64(100) || usageDetails["output"] != float64(20) || usageDetails["total"] != float64(120) {
		t.Fatalf("usageDetails = %#v, want input/output/total 100/20/120", usageDetails)
	}
	if names["planner"] != 1 {
		t.Fatalf("planner span count = %d, want 1", names["planner"])
	}
	if names[runEventTodoUpdate] != 1 {
		t.Fatalf("todo_update event count = %d, want 1", names[runEventTodoUpdate])
	}
	if names[runEventTodoClosed] != 1 {
		t.Fatalf("todo_closed event count = %d, want 1", names[runEventTodoClosed])
	}
	if names["tool/mouse_click"] != 1 {
		t.Fatalf("tool span count = %d, want 1", names["tool/mouse_click"])
	}
	toolInput, ok := toolBody["input"].(map[string]interface{})
	if !ok {
		t.Fatalf("tool span input missing: %#v", toolBody["input"])
	}
	if _, ok := toolInput["speech"]; ok {
		t.Fatalf("tool span should not include speech input: %#v", toolInput)
	}
	toolMetadata, ok := toolBody["metadata"].(map[string]interface{})
	if !ok {
		t.Fatalf("tool span metadata missing: %#v", toolBody["metadata"])
	}
	if _, ok := toolMetadata["speech"]; ok {
		t.Fatalf("tool span should not include speech metadata: %#v", toolMetadata)
	}
	if names["verifier"] != 1 {
		t.Fatalf("verifier span count = %d, want 1", names["verifier"])
	}
}

func TestBuildLangfuseBatchAddsTraceIdentityAndFailureScore(t *testing.T) {
	start := time.Date(2026, 6, 3, 10, 0, 0, 0, time.UTC)
	episode := TaskEpisode{
		ID:          "ep_failure_001",
		StartedAt:   start.Format(time.RFC3339Nano),
		EndedAt:     start.Add(time.Second).Format(time.RFC3339Nano),
		UserGoal:    "打开设置",
		DeviceScope: map[string]string{"device_id": "device-a"},
		Outcome: TaskEpisodeOutcome{
			Success:       false,
			FailureReason: "verifier rejected completion",
		},
		Extra: map[string]interface{}{
			"runtime_id": "runtime-a",
		},
	}

	exporter := NewEpisodeExporter(TelemetryConfig{Enabled: boolPtr(true), BaseURL: "http://langfuse.test"}, nil)
	batch, err := exporter.buildLangfuseBatch(context.Background(), episode, t.TempDir())
	if err != nil {
		t.Fatalf("buildLangfuseBatch() error = %v", err)
	}

	var traceBody map[string]interface{}
	var scoreBody map[string]interface{}
	for _, event := range batch {
		var body map[string]interface{}
		if err := json.Unmarshal(event.Body, &body); err != nil {
			t.Fatalf("decode body: %v", err)
		}
		switch event.Type {
		case "trace-create":
			traceBody = body
		case "score-create":
			scoreBody = body
		}
	}
	if traceBody["userId"] != "device-a" {
		t.Fatalf("trace userId = %v, want device-a", traceBody["userId"])
	}
	if traceBody["sessionId"] != "runtime-a" {
		t.Fatalf("trace sessionId = %v, want runtime-a", traceBody["sessionId"])
	}
	if traceBody["public"] != false {
		t.Fatalf("trace public = %v, want false", traceBody["public"])
	}
	if scoreBody["value"] != float64(0) {
		t.Fatalf("failure score value = %v, want 0", scoreBody["value"])
	}
	if scoreBody["comment"] != "verifier rejected completion" {
		t.Fatalf("failure score comment = %v", scoreBody["comment"])
	}
}

func TestBuildLangfuseBatchParentsGenerationsAndToolResults(t *testing.T) {
	start := time.Date(2026, 6, 3, 10, 0, 0, 0, time.UTC)
	episode := TaskEpisode{
		ID:        "ep_parenting_001",
		StartedAt: start.Format(time.RFC3339Nano),
		EndedAt:   start.Add(8 * time.Second).Format(time.RFC3339Nano),
		UserGoal:  "打开设置",
		Outcome:   TaskEpisodeOutcome{Success: true, FinalAnswer: "done"},
		Events: []TaskEpisodeEvent{
			{
				EventID: "evt_plan",
				Ts:      start.Add(2 * time.Second).Format(time.RFC3339Nano),
				Type:    "planner_decision",
				Plan:    []string{"点击设置"},
			},
			{
				EventID:   "evt_tool",
				Ts:        start.Add(3 * time.Second).Format(time.RFC3339Nano),
				Type:      runEventToolCall,
				ToolName:  "mouse_click",
				ToolInput: `{"x":1,"y":2}`,
			},
			{
				EventID:     "evt_result",
				Ts:          start.Add(5 * time.Second).Format(time.RFC3339Nano),
				Type:        "tool_result",
				ToolName:    "mouse_click",
				Observation: `{"ok":true}`,
			},
		},
	}
	promptCalls := []telemetryPromptCall{
		{
			ID:        "22222222-2222-2222-2222-222222222222",
			Role:      string(RolePlanner),
			StartedAt: start.Add(time.Second),
			EndedAt:   start.Add(1500 * time.Millisecond),
			Input:     []map[string]interface{}{{"role": "human"}},
			Output:    map[string]interface{}{"choices": []map[string]interface{}{{"content": "plan"}}},
		},
	}

	exporter := NewEpisodeExporter(TelemetryConfig{Enabled: boolPtr(true), BaseURL: "http://langfuse.test"}, nil)
	batch, err := exporter.buildLangfuseBatch(context.Background(), episode, t.TempDir(), promptCalls)
	if err != nil {
		t.Fatalf("buildLangfuseBatch() error = %v", err)
	}

	var iterationID string
	var generationParent string
	var toolID string
	var toolEndTime string
	var resultParent string
	for _, event := range batch {
		var body map[string]interface{}
		if err := json.Unmarshal(event.Body, &body); err != nil {
			t.Fatalf("decode body: %v", err)
		}
		switch body["name"] {
		case "iteration_1":
			iterationID, _ = body["id"].(string)
		case "planner_prompt_1":
			generationParent, _ = body["parentObservationId"].(string)
		case "tool/mouse_click":
			toolID, _ = body["id"].(string)
			toolEndTime, _ = body["endTime"].(string)
		case "tool_result/mouse_click":
			resultParent, _ = body["parentObservationId"].(string)
		}
	}
	if iterationID == "" {
		t.Fatal("missing iteration span")
	}
	if generationParent != iterationID {
		t.Fatalf("generation parent = %q, want iteration %q", generationParent, iterationID)
	}
	if toolID == "" {
		t.Fatal("missing tool span")
	}
	if resultParent != toolID {
		t.Fatalf("tool result parent = %q, want tool %q", resultParent, toolID)
	}
	if toolEndTime != langfuseRFC3339(start.Add(5*time.Second)) {
		t.Fatalf("tool endTime = %q, want result time", toolEndTime)
	}
}

func TestBuildLangfuseBatchUsesCapturedPromptsForGenerations(t *testing.T) {
	start := time.Date(2026, 6, 3, 10, 0, 0, 0, time.UTC)
	episode := TaskEpisode{
		ID:        "ep_prompt_capture",
		StartedAt: start.Format(time.RFC3339Nano),
		EndedAt:   start.Add(time.Second).Format(time.RFC3339Nano),
		UserGoal:  "debug prompt capture",
		Outcome:   TaskEpisodeOutcome{Success: true, FinalAnswer: "done"},
		Extra: map[string]interface{}{
			"prompt_tokens":     999,
			"completion_tokens": 111,
			"total_tokens":      1110,
			"model":             "openrouter/test-model",
		},
	}
	promptCalls := []telemetryPromptCall{
		{
			ID:        "11111111-1111-1111-1111-111111111111",
			Role:      string(RolePlanner),
			StartedAt: start,
			EndedAt:   start.Add(100 * time.Millisecond),
			Input: []map[string]interface{}{
				{
					"role": "system",
					"parts": []map[string]interface{}{
						{"type": "text", "text": "complete planner system prompt"},
					},
				},
				{
					"role": "human",
					"parts": []map[string]interface{}{
						{"type": "text", "text": "complete user prompt"},
					},
				},
			},
			Output: map[string]interface{}{
				"choices": []map[string]interface{}{{"content": "planner output"}},
			},
			UsageDetails: map[string]int{"input": 10, "output": 2, "total": 12},
			CostDetails:  map[string]float64{"total": 0.0012},
			ModelParameters: map[string]interface{}{
				"temperature": 0.2,
				"max_tokens":  128,
			},
			Metadata: map[string]interface{}{
				"tools_count": 1,
				"tool_schemas": []map[string]interface{}{
					{
						"type": "function",
						"function": map[string]interface{}{
							"name":        "echo",
							"description": "Echo text.",
							"parameters": map[string]interface{}{
								"type": "object",
								"properties": map[string]interface{}{
									"value": map[string]interface{}{"type": "string"},
								},
								"required": []string{"value"},
							},
						},
					},
				},
			},
		},
	}

	exporter := NewEpisodeExporter(TelemetryConfig{Enabled: boolPtr(true), BaseURL: "http://langfuse.test"}, nil)
	batch, err := exporter.buildLangfuseBatch(context.Background(), episode, t.TempDir(), promptCalls)
	if err != nil {
		t.Fatalf("buildLangfuseBatch() error = %v", err)
	}
	var generations []map[string]interface{}
	for _, event := range batch {
		if event.Type != "generation-create" {
			continue
		}
		var body map[string]interface{}
		if err := json.Unmarshal(event.Body, &body); err != nil {
			t.Fatalf("decode generation body: %v", err)
		}
		generations = append(generations, body)
	}
	if len(generations) != 1 {
		t.Fatalf("generation count = %d, want 1 captured prompt generation", len(generations))
	}
	if generations[0]["name"] != "planner_prompt_1" {
		t.Fatalf("generation name = %v, want planner_prompt_1", generations[0]["name"])
	}
	input, ok := generations[0]["input"].([]interface{})
	if !ok || len(input) != 2 {
		t.Fatalf("generation input = %#v, want 2 messages", generations[0]["input"])
	}
	first, ok := input[0].(map[string]interface{})
	if !ok {
		t.Fatalf("first message = %#v", input[0])
	}
	parts, ok := first["parts"].([]interface{})
	if !ok || len(parts) != 1 {
		t.Fatalf("first message parts = %#v", first["parts"])
	}
	part, ok := parts[0].(map[string]interface{})
	if !ok || part["text"] != "complete planner system prompt" {
		t.Fatalf("captured prompt part = %#v", parts[0])
	}
	usageDetails, ok := generations[0]["usageDetails"].(map[string]interface{})
	if !ok {
		t.Fatalf("usageDetails missing: %#v", generations[0]["usageDetails"])
	}
	if usageDetails["input"] != float64(10) || usageDetails["output"] != float64(2) || usageDetails["total"] != float64(12) {
		t.Fatalf("usageDetails = %#v, want 10/2/12", usageDetails)
	}
	costDetails, ok := generations[0]["costDetails"].(map[string]interface{})
	if !ok || costDetails["total"] != 0.0012 {
		t.Fatalf("costDetails = %#v, want total cost", generations[0]["costDetails"])
	}
	modelParameters, ok := generations[0]["modelParameters"].(map[string]interface{})
	if !ok || modelParameters["temperature"] != 0.2 || modelParameters["max_tokens"] != float64(128) {
		t.Fatalf("modelParameters = %#v, want temperature/max_tokens", generations[0]["modelParameters"])
	}
	if _, ok := modelParameters["tools_count"]; ok {
		t.Fatalf("modelParameters = %#v, did not expect tools_count", modelParameters)
	}
	if _, ok := modelParameters["tools"]; ok {
		t.Fatalf("modelParameters = %#v, did not expect tools", modelParameters)
	}
	metadata, ok := generations[0]["metadata"].(map[string]interface{})
	if !ok {
		t.Fatalf("generation metadata = %#v, want map", generations[0]["metadata"])
	}
	if metadata["role"] != string(RolePlanner) || metadata["prompt_index"] != float64(1) {
		t.Fatalf("generation metadata = %#v, want role/prompt_index", metadata)
	}
	if metadata["tools_count"] != float64(1) {
		t.Fatalf("generation metadata = %#v, want tools_count=1", metadata)
	}
	tools, ok := metadata["tool_schemas"].([]interface{})
	if !ok || len(tools) != 1 {
		t.Fatalf("generation metadata.tool_schemas = %#v, want one tool definition", metadata["tool_schemas"])
	}
	tool, ok := tools[0].(map[string]interface{})
	if !ok {
		t.Fatalf("tool definition = %#v", tools[0])
	}
	function, ok := tool["function"].(map[string]interface{})
	if !ok || function["name"] != "echo" {
		t.Fatalf("tool function = %#v, want echo", tool["function"])
	}
	parameters, ok := function["parameters"].(map[string]interface{})
	if !ok || parameters["type"] != "object" {
		t.Fatalf("tool parameters = %#v, want object schema", function["parameters"])
	}
}

func TestBuildLangfuseBatchUploadsCapturedPromptMedia(t *testing.T) {
	var mediaRequest langfuseMediaCreateRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/public/media" {
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		body, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(body, &mediaRequest); err != nil {
			t.Fatalf("decode media request: %v", err)
		}
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"mediaId":"prompt-media-1","uploadUrl":null}`))
	}))
	defer server.Close()

	start := time.Date(2026, 6, 25, 10, 0, 0, 0, time.UTC)
	image := []byte("prompt-jpeg-bytes")
	promptMedia := newTelemetryPromptMedia("image/jpeg", image)
	callID := "11111111-1111-1111-1111-111111111111"
	promptCalls := []telemetryPromptCall{{
		ID:        callID,
		Role:      string(RolePlanner),
		StartedAt: start,
		EndedAt:   start.Add(time.Millisecond),
		Input: []map[string]interface{}{{
			"role": "human",
			"parts": []map[string]interface{}{{
				"type":      "binary",
				"mime_type": "image/jpeg",
				"size":      len(image),
				"data":      promptMedia.Placeholder,
			}},
		}},
		Media: []telemetryPromptMedia{promptMedia},
	}}
	episode := TaskEpisode{
		ID:        "ep_prompt_media",
		StartedAt: start.Format(time.RFC3339Nano),
		EndedAt:   start.Add(time.Second).Format(time.RFC3339Nano),
		UserGoal:  "inspect screenshot",
		Outcome:   TaskEpisodeOutcome{Success: true, FinalAnswer: "done"},
	}
	exporter := NewEpisodeExporter(TelemetryConfig{
		Enabled:   boolPtr(true),
		BaseURL:   server.URL,
		PublicKey: "pk-test",
		SecretKey: "sk-test",
	}, nil)

	batch, err := exporter.buildLangfuseBatch(context.Background(), episode, t.TempDir(), promptCalls)
	if err != nil {
		t.Fatalf("buildLangfuseBatch() error = %v", err)
	}
	if len(promptCalls[0].Media) != 0 {
		t.Fatalf("prompt media retained after upload: %d item(s)", len(promptCalls[0].Media))
	}
	if mediaRequest.ObservationID != callID {
		t.Fatalf("media observationId = %q, want %q", mediaRequest.ObservationID, callID)
	}
	if mediaRequest.Field != "input" {
		t.Fatalf("media field = %q, want input", mediaRequest.Field)
	}

	for _, event := range batch {
		if event.Type != "generation-create" {
			continue
		}
		var body map[string]interface{}
		if err := json.Unmarshal(event.Body, &body); err != nil {
			t.Fatalf("decode generation body: %v", err)
		}
		encoded := string(event.Body)
		if strings.Contains(encoded, base64.StdEncoding.EncodeToString(image)) {
			t.Fatalf("generation body contains inline base64: %s", encoded)
		}
		if !strings.Contains(encoded, "id=prompt-media-1") {
			t.Fatalf("generation body missing media token: %s", encoded)
		}
		return
	}
	t.Fatal("missing generation-create event")
}

func TestBuildLangfuseBatchOmitsPromptImagesWhenScreenshotUploadDisabled(t *testing.T) {
	var mediaRequests int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mediaRequests++
		http.Error(w, "media upload must be disabled", http.StatusInternalServerError)
	}))
	defer server.Close()

	start := time.Date(2026, 6, 25, 10, 0, 0, 0, time.UTC)
	image := []byte("prompt-jpeg-bytes")
	promptMedia := newTelemetryPromptMedia("image/jpeg", image)
	pdf := []byte("%PDF-1.7 prompt bytes")
	pdfMedia := newTelemetryPromptMedia("application/pdf", pdf)
	promptCalls := []telemetryPromptCall{{
		ID:        "11111111-1111-1111-1111-111111111111",
		Role:      string(RolePlanner),
		StartedAt: start,
		EndedAt:   start.Add(time.Millisecond),
		Input: []map[string]interface{}{{
			"role": "human",
			"parts": []map[string]interface{}{{
				"type":      "binary",
				"mime_type": "image/jpeg",
				"size":      len(image),
				"data":      promptMedia.Placeholder,
			}, {
				"type":      "binary",
				"mime_type": "application/pdf",
				"size":      len(pdf),
				"data":      pdfMedia.Placeholder,
			}},
		}},
		Media: []telemetryPromptMedia{promptMedia, pdfMedia},
	}}
	uploadScreenshots := false
	exporter := NewEpisodeExporter(TelemetryConfig{
		Enabled:           boolPtr(true),
		BaseURL:           server.URL,
		PublicKey:         "pk-test",
		SecretKey:         "sk-test",
		UploadScreenshots: &uploadScreenshots,
	}, nil)
	episode := TaskEpisode{
		ID:        "ep_prompt_media_disabled",
		StartedAt: start.Format(time.RFC3339Nano),
		EndedAt:   start.Add(time.Second).Format(time.RFC3339Nano),
		UserGoal:  "inspect screenshot",
		Outcome:   TaskEpisodeOutcome{Success: true, FinalAnswer: "done"},
	}

	batch, err := exporter.buildLangfuseBatch(context.Background(), episode, t.TempDir(), promptCalls)
	if err != nil {
		t.Fatalf("buildLangfuseBatch() error = %v", err)
	}
	if len(promptCalls[0].Media) != 0 {
		t.Fatalf("prompt media retained when upload is disabled: %d item(s)", len(promptCalls[0].Media))
	}
	if mediaRequests != 0 {
		t.Fatalf("media API requests = %d, want 0", mediaRequests)
	}
	for _, event := range batch {
		if event.Type != "generation-create" {
			continue
		}
		encoded := string(event.Body)
		if strings.Contains(encoded, promptMedia.Placeholder) {
			t.Fatalf("generation body retained media placeholder: %s", encoded)
		}
		if strings.Contains(encoded, pdfMedia.Placeholder) {
			t.Fatalf("generation body retained non-image media placeholder: %s", encoded)
		}
		if strings.Contains(encoded, base64.StdEncoding.EncodeToString(image)) {
			t.Fatalf("generation body contains inline base64: %s", encoded)
		}
		if strings.Contains(encoded, base64.StdEncoding.EncodeToString(pdf)) {
			t.Fatalf("generation body contains inline non-image base64: %s", encoded)
		}
		if !strings.Contains(encoded, "[media omitted: upload disabled]") {
			t.Fatalf("generation body missing disabled placeholder: %s", encoded)
		}
		return
	}
	t.Fatal("missing generation-create event")
}

func TestExportEpisodeDirUploadsToLangfuse(t *testing.T) {
	var ingestionCalls int
	var mediaObservationID string
	var toolResultID string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/api/public/ingestion":
			ingestionCalls++
			user, pass, ok := r.BasicAuth()
			if !ok || user != "pk-test" || pass != "sk-test" {
				t.Errorf("unexpected auth: ok=%v user=%q pass=%q", ok, user, pass)
			}
			body, _ := io.ReadAll(r.Body)
			var req langfuseIngestionRequest
			if err := json.Unmarshal(body, &req); err != nil {
				t.Fatalf("decode ingestion request: %v", err)
			}
			if len(req.Batch) == 0 {
				t.Fatal("expected non-empty ingestion batch")
			}
			for _, event := range req.Batch {
				if event.Type != "span-create" {
					continue
				}
				var body map[string]interface{}
				if err := json.Unmarshal(event.Body, &body); err != nil {
					t.Fatalf("decode span body: %v", err)
				}
				if body["name"] != "tool_result/screenshot" {
					continue
				}
				toolResultID, _ = body["id"].(string)
				output, ok := body["output"].(map[string]interface{})
				if !ok {
					t.Fatalf("tool result output = %#v, want object with screenshot", body["output"])
				}
				screenshot, _ := output["screenshot"].(string)
				if !strings.Contains(screenshot, "id=media-123") {
					t.Fatalf("tool result screenshot = %q, want media token", screenshot)
				}
			}
			w.WriteHeader(http.StatusMultiStatus)
			_, _ = w.Write([]byte(`{"successes":[{"id":"ok","status":201}],"errors":[]}`))
		case r.Method == http.MethodPost && r.URL.Path == "/api/public/media":
			body, _ := io.ReadAll(r.Body)
			var req langfuseMediaCreateRequest
			if err := json.Unmarshal(body, &req); err != nil {
				t.Fatalf("decode media create request: %v", err)
			}
			mediaObservationID = req.ObservationID
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"mediaId":"media-123","uploadUrl":null}`))
		default:
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
	}))
	defer server.Close()

	episodeDir := t.TempDir()
	start := time.Date(2026, 6, 3, 10, 0, 0, 0, time.UTC)
	episode := TaskEpisode{
		ID:        "ep_export_test",
		StartedAt: start.Format(time.RFC3339Nano),
		EndedAt:   start.Add(5 * time.Second).Format(time.RFC3339Nano),
		UserGoal:  "打开时钟",
		Outcome: TaskEpisodeOutcome{
			Success:     true,
			FinalAnswer: "done",
		},
	}
	if err := os.WriteFile(filepath.Join(episodeDir, "episode.yaml"), []byte(`
id: ep_export_test
started_at: "`+episode.StartedAt+`"
ended_at: "`+episode.EndedAt+`"
user_goal: 打开时钟
outcome:
  success: true
  final_answer: done
`), 0o644); err != nil {
		t.Fatalf("write episode.yaml: %v", err)
	}
	eventsPath := filepath.Join(episodeDir, "events.jsonl")
	if err := writeEpisodeEventsJSONL(eventsPath, []TaskEpisodeEvent{
		{
			EventID:  "evt1",
			Ts:       start.Format(time.RFC3339Nano),
			Type:     "planner_decision",
			NextStep: "打开时钟应用",
			Plan:     []string{"打开时钟应用"},
		},
		{
			EventID:       "evt2",
			Ts:            start.Add(time.Second).Format(time.RFC3339Nano),
			Type:          "tool_result",
			ToolName:      "screenshot",
			Observation:   `{"action_output":"ok"}`,
			ScreenshotRef: "artifacts/step_002.jpeg",
		},
	}); err != nil {
		t.Fatalf("write events.jsonl: %v", err)
	}
	artifactsDir := filepath.Join(episodeDir, "artifacts")
	if err := os.MkdirAll(artifactsDir, 0o755); err != nil {
		t.Fatalf("mkdir artifacts: %v", err)
	}
	if err := os.WriteFile(filepath.Join(artifactsDir, "step_002.jpeg"), []byte("fakejpeg"), 0o644); err != nil {
		t.Fatalf("write screenshot: %v", err)
	}

	exporter := NewEpisodeExporter(TelemetryConfig{
		Enabled:           boolPtr(true),
		BaseURL:           server.URL,
		PublicKey:         "pk-test",
		SecretKey:         "sk-test",
		UploadScreenshots: boolPtr(true),
		MaxRetry:          0,
	}, nil)
	if err := exporter.ExportEpisodeDir(context.Background(), episodeDir, episode); err != nil {
		t.Fatalf("ExportEpisodeDir() error = %v", err)
	}
	if ingestionCalls == 0 {
		t.Fatal("expected ingestion request")
	}
	if strings.TrimSpace(mediaObservationID) == "" {
		t.Fatal("expected media create request to include observationId")
	}
	if mediaObservationID != toolResultID {
		t.Fatalf("media observationId = %q, want tool result id %q", mediaObservationID, toolResultID)
	}
}

func TestExportEpisodeDirIngestsTraceWhenScreenshotUploadWouldExhaustDeadline(t *testing.T) {
	var ingestionCalls int
	var mediaCalls int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/api/public/ingestion":
			ingestionCalls++
			w.WriteHeader(http.StatusMultiStatus)
			_, _ = w.Write([]byte(`{"successes":[{"id":"ok","status":201}],"errors":[]}`))
		case r.Method == http.MethodPost && r.URL.Path == "/api/public/media":
			mediaCalls++
			time.Sleep(200 * time.Millisecond)
			http.Error(w, "media upload should have been skipped", http.StatusGatewayTimeout)
		default:
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
	}))
	defer server.Close()

	episodeDir := t.TempDir()
	start := time.Date(2026, 6, 23, 9, 30, 0, 0, time.UTC)
	episode := TaskEpisode{
		ID:        "ep_export_deadline_test",
		StartedAt: start.Format(time.RFC3339Nano),
		EndedAt:   start.Add(5 * time.Second).Format(time.RFC3339Nano),
		UserGoal:  "失败也要上传 trace",
		Outcome: TaskEpisodeOutcome{
			Success:       false,
			FailureReason: "agent restarted before the task episode completed",
		},
	}
	if err := os.WriteFile(filepath.Join(episodeDir, "episode.yaml"), []byte(`
id: ep_export_deadline_test
status: interrupted
started_at: "`+episode.StartedAt+`"
ended_at: "`+episode.EndedAt+`"
user_goal: 失败也要上传 trace
outcome:
  success: false
  failure_reason: agent restarted before the task episode completed
`), 0o644); err != nil {
		t.Fatalf("write episode.yaml: %v", err)
	}
	if err := writeEpisodeEventsJSONL(filepath.Join(episodeDir, "events.jsonl"), []TaskEpisodeEvent{
		{
			EventID:   "evt1",
			Ts:        start.Format(time.RFC3339Nano),
			Type:      runEventToolCall,
			ToolName:  "screenshot",
			ToolInput: `{}`,
			Content:   "截图",
		},
		{
			EventID:       "evt2",
			Ts:            start.Add(time.Second).Format(time.RFC3339Nano),
			Type:          "tool_result",
			ToolName:      "screenshot",
			Observation:   `{"format":"jpeg","size":100}`,
			ScreenshotRef: "artifacts/step_002.jpeg",
		},
	}); err != nil {
		t.Fatalf("write events.jsonl: %v", err)
	}
	artifactsDir := filepath.Join(episodeDir, "artifacts")
	if err := os.MkdirAll(artifactsDir, 0o755); err != nil {
		t.Fatalf("mkdir artifacts: %v", err)
	}
	if err := os.WriteFile(filepath.Join(artifactsDir, "step_002.jpeg"), []byte("fakejpeg"), 0o644); err != nil {
		t.Fatalf("write screenshot: %v", err)
	}

	exporter := NewEpisodeExporter(TelemetryConfig{
		Enabled:           boolPtr(true),
		BaseURL:           server.URL,
		PublicKey:         "pk-test",
		SecretKey:         "sk-test",
		UploadScreenshots: boolPtr(true),
		MaxRetry:          0,
	}, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	if err := exporter.ExportEpisodeDir(ctx, episodeDir, episode); err != nil {
		t.Fatalf("ExportEpisodeDir() error = %v", err)
	}
	if ingestionCalls == 0 {
		t.Fatal("expected ingestion request even when screenshot upload cannot fit within export deadline")
	}
	if mediaCalls != 0 {
		t.Fatalf("mediaCalls = %d, want screenshot upload skipped to preserve trace ingestion budget", mediaCalls)
	}
}

func TestLangfuseScreenshotUploadContextUsesConfiguredTimeout(t *testing.T) {
	screenshotCtx, cancel, ok := langfuseScreenshotUploadContext(context.Background(), 120*time.Millisecond)
	if !ok {
		t.Fatal("langfuseScreenshotUploadContext() ok = false, want true")
	}
	defer cancel()
	deadline, ok := screenshotCtx.Deadline()
	if !ok {
		t.Fatal("screenshot context missing deadline")
	}
	remaining := time.Until(deadline)
	if remaining <= 0 || remaining > 120*time.Millisecond {
		t.Fatalf("screenshot context timeout = %s, want <= 120ms", remaining)
	}
}

func TestLangfuseScreenshotUploadContextReservesTraceIngestionBudget(t *testing.T) {
	parentCtx, parentCancel := context.WithTimeout(context.Background(), 2*langfuseTraceIngestReserve)
	defer parentCancel()
	parentDeadline, ok := parentCtx.Deadline()
	if !ok {
		t.Fatal("parent context missing deadline")
	}

	screenshotCtx, cancel, ok := langfuseScreenshotUploadContext(parentCtx, 30*time.Second)
	if !ok {
		t.Fatal("langfuseScreenshotUploadContext() ok = false, want true")
	}
	defer cancel()
	deadline, ok := screenshotCtx.Deadline()
	if !ok {
		t.Fatal("screenshot context missing deadline")
	}
	if deadline.After(parentDeadline.Add(-langfuseTraceIngestReserve + 100*time.Millisecond)) {
		t.Fatalf("screenshot deadline = %s, want trace ingestion reserve before parent deadline %s", deadline, parentDeadline)
	}
}

func TestRuntimeStartupExportsInterruptedEpisodeToLangfuse(t *testing.T) {
	ctx := context.Background()
	configDir := t.TempDir()
	memoryDir := filepath.Join(configDir, "memory")
	store := NewTaskEpisodeStore(filepath.Join(memoryDir, "episodes"))
	recorder := NewPersistentEpisodeRecorder(MemoryRetrieveRequest{
		Input:     "打开设置",
		EpisodeID: "ep_langfuse_interrupted",
	}, MemoryContext{}, store)

	if err := recorder.Start(ctx); err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	recorder.RecordPlannerDecision(plannerDecision{
		Objective: "打开设置",
		Plan:      []string{"打开设置"},
		NextStep:  "点击设置",
	})

	done := make(chan map[string]interface{}, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/public/ingestion" {
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			http.NotFound(w, r)
			return
		}
		user, pass, ok := r.BasicAuth()
		if !ok || user != "pk-test" || pass != "sk-test" {
			t.Errorf("unexpected auth: ok=%v user=%q pass=%q", ok, user, pass)
		}
		body, _ := io.ReadAll(r.Body)
		var req langfuseIngestionRequest
		if err := json.Unmarshal(body, &req); err != nil {
			t.Errorf("decode ingestion request: %v", err)
		}
		var traceBody map[string]interface{}
		var scoreBody map[string]interface{}
		for _, event := range req.Batch {
			var eventBody map[string]interface{}
			if err := json.Unmarshal(event.Body, &eventBody); err != nil {
				t.Errorf("decode event body: %v", err)
				continue
			}
			switch event.Type {
			case "trace-create":
				traceBody = eventBody
			case "score-create":
				scoreBody = eventBody
			}
		}
		select {
		case done <- map[string]interface{}{"trace": traceBody, "score": scoreBody}:
		default:
		}
		w.WriteHeader(http.StatusMultiStatus)
		_, _ = w.Write([]byte(`{"successes":[{"id":"ok","status":201}],"errors":[]}`))
	}))
	defer server.Close()

	NewRuntimeWithDeps(
		Config{
			ConfigDir: configDir,
			Model: ModelConfig{
				Provider: "fake",
				Model:    "test-model",
			},
			Telemetry: TelemetryConfig{
				Enabled:   boolPtr(true),
				BaseURL:   server.URL,
				PublicKey: "pk-test",
				SecretKey: "sk-test",
				MaxRetry:  0,
			},
		},
		&testModelResolver{model: &scriptedModel{}},
		NewMemoryManager(memoryDir),
		NewBuiltinToolSet(HIDConfig{}, AudioConfig{}, SearchConfig{}, ProxyConfig{}),
		NewSkillIndex(),
	)

	var payload map[string]interface{}
	select {
	case payload = <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for langfuse ingestion")
	}

	traceBody, ok := payload["trace"].(map[string]interface{})
	if !ok || traceBody == nil {
		t.Fatalf("missing trace-create body: %#v", payload)
	}
	scoreBody, ok := payload["score"].(map[string]interface{})
	if !ok || scoreBody == nil {
		t.Fatalf("missing score-create body: %#v", payload)
	}
	meta, ok := traceBody["metadata"].(map[string]interface{})
	if !ok {
		t.Fatalf("trace metadata = %#v", traceBody["metadata"])
	}
	if meta["episode_id"] != "ep_langfuse_interrupted" {
		t.Fatalf("metadata.episode_id = %v", meta["episode_id"])
	}
	if meta["status"] != "interrupted" {
		t.Fatalf("metadata.status = %v", meta["status"])
	}
	if meta["failure_reason"] != "agent restarted before the task episode completed" {
		t.Fatalf("metadata.failure_reason = %v", meta["failure_reason"])
	}
	if meta["model"] != "fake/test-model" {
		t.Fatalf("metadata.model = %v", meta["model"])
	}
	if meta["interruption_source"] != "agent_restart" {
		t.Fatalf("metadata.interruption_source = %v", meta["interruption_source"])
	}
	if scoreBody["value"] != float64(0) {
		t.Fatalf("score value = %v, want 0", scoreBody["value"])
	}
	tags, ok := traceBody["tags"].([]interface{})
	if !ok {
		t.Fatalf("trace tags = %#v", traceBody["tags"])
	}
	for _, want := range []string{"interrupted", "status:interrupted", "failure"} {
		if !jsonListContains(tags, want) {
			t.Fatalf("trace tags missing %q: %#v", want, tags)
		}
	}
}

func TestBuildLangfuseBatchMapsDefaultModePlannerTools(t *testing.T) {
	start := time.Date(2026, 6, 11, 10, 0, 0, 0, time.UTC)
	episode := TaskEpisode{
		ID:        "ep_default_001",
		StartedAt: start.Format(time.RFC3339Nano),
		EndedAt:   start.Add(3 * time.Second).Format(time.RFC3339Nano),
		UserGoal:  "echo test",
		Outcome: TaskEpisodeOutcome{
			Success:     true,
			FinalAnswer: "done",
		},
		Events: []TaskEpisodeEvent{
			{
				EventID:   "evt_tool",
				Ts:        start.Add(time.Second).Format(time.RFC3339Nano),
				Type:      runEventToolCall,
				Role:      "planner",
				ToolName:  "echo",
				ToolInput: `{"__arg1":"ok"}`,
			},
			{
				EventID:     "evt_result",
				Ts:          start.Add(2 * time.Second).Format(time.RFC3339Nano),
				Type:        "tool_result",
				Role:        "planner",
				ToolName:    "echo",
				Observation: "ok",
			},
			{
				EventID: "evt_finish",
				Ts:      start.Add(3 * time.Second).Format(time.RFC3339Nano),
				Type:    "default_finish",
				Role:    "planner",
				Content: "done",
			},
		},
	}

	exporter := NewEpisodeExporter(TelemetryConfig{Enabled: boolPtr(true), BaseURL: "http://langfuse.test"}, nil)
	batch, err := exporter.buildLangfuseBatch(context.Background(), episode, t.TempDir())
	if err != nil {
		t.Fatalf("buildLangfuseBatch() error = %v", err)
	}

	names := map[string]int{}
	var traceMeta map[string]interface{}
	for _, event := range batch {
		var body map[string]interface{}
		if err := json.Unmarshal(event.Body, &body); err != nil {
			t.Fatalf("decode body: %v", err)
		}
		if event.Type == "trace-create" {
			traceMeta, _ = body["metadata"].(map[string]interface{})
		}
		if name, _ := body["name"].(string); name != "" {
			names[name]++
		}
	}
	if names["phase/default"] != 1 {
		t.Fatalf("phase/default count = %d, want 1; names=%#v", names["phase/default"], names)
	}
	if names["planner/tool/echo"] != 1 {
		t.Fatalf("planner/tool/echo count = %d, want 1; names=%#v", names["planner/tool/echo"], names)
	}
	if names["planner/default_finish"] != 1 {
		t.Fatalf("planner/default_finish count = %d, want 1; names=%#v", names["planner/default_finish"], names)
	}
	if traceMeta["default_finish"] != true {
		t.Fatalf("trace metadata default_finish = %#v, want true", traceMeta["default_finish"])
	}
	if traceMeta["loop_mode"] != "default" {
		t.Fatalf("trace metadata loop_mode = %#v, want default", traceMeta["loop_mode"])
	}
}

func TestBuildLangfuseBatchMapsLoopPhaseTransitions(t *testing.T) {
	start := time.Date(2026, 6, 11, 11, 0, 0, 0, time.UTC)
	canFinish := true
	episode := TaskEpisode{
		ID:        "ep_committed_001",
		StartedAt: start.Format(time.RFC3339Nano),
		EndedAt:   start.Add(10 * time.Second).Format(time.RFC3339Nano),
		UserGoal:  "open settings",
		Outcome: TaskEpisodeOutcome{
			Success:     true,
			FinalAnswer: "done",
		},
		Events: []TaskEpisodeEvent{
			{
				EventID: "evt_enter",
				Ts:      start.Add(time.Second).Format(time.RFC3339Nano),
				Type:    "loop_phase",
				Role:    "planner",
				Content: "plan",
				Reason:  "enter_plan_mode",
			},
			{
				EventID: "evt_commit_phase",
				Ts:      start.Add(2 * time.Second).Format(time.RFC3339Nano),
				Type:    "loop_phase",
				Role:    "planner",
				Content: "execution",
				Reason:  "commit_plan",
			},
			{
				EventID:   "evt_plan",
				Ts:        start.Add(2 * time.Second).Format(time.RFC3339Nano),
				Type:      "planner_decision",
				Role:      "planner",
				Objective: "open settings",
				Plan:      []string{"open settings"},
				NextStep:  "tap settings",
			},
			{
				EventID:   "evt_verifier",
				Ts:        start.Add(5 * time.Second).Format(time.RFC3339Nano),
				Type:      "verifier_decision",
				CanFinish: &canFinish,
				Content:   "done",
			},
		},
	}

	exporter := NewEpisodeExporter(TelemetryConfig{
		Enabled: boolPtr(true),
		BaseURL: "http://langfuse.test",
		Tags:    []string{"aiden-hardware"},
	}, nil)
	batch, err := exporter.buildLangfuseBatch(context.Background(), episode, t.TempDir())
	if err != nil {
		t.Fatalf("buildLangfuseBatch() error = %v", err)
	}

	var traceTags []interface{}
	var traceMeta map[string]interface{}
	names := map[string]int{}
	for _, event := range batch {
		var body map[string]interface{}
		if err := json.Unmarshal(event.Body, &body); err != nil {
			t.Fatalf("decode body: %v", err)
		}
		if event.Type == "trace-create" {
			traceTags, _ = body["tags"].([]interface{})
			traceMeta, _ = body["metadata"].(map[string]interface{})
		}
		if name, _ := body["name"].(string); name != "" {
			names[name]++
		}
	}
	if names["phase/default"] != 1 || names["phase/plan"] != 1 || names["phase/execution"] != 1 {
		t.Fatalf("unexpected phase spans: %#v", names)
	}
	if !jsonListContains(traceTags, "loop:plan") || !jsonListContains(traceTags, "loop:execution") || !jsonListContains(traceTags, "loop:committed") {
		t.Fatalf("trace tags = %#v, want loop phase tags", traceTags)
	}
	if traceMeta["loop_mode"] != "committed" {
		t.Fatalf("trace metadata loop_mode = %#v, want committed", traceMeta["loop_mode"])
	}
	if intMetricFromMeta(traceMeta, "enter_plan_mode_count") != 1 || intMetricFromMeta(traceMeta, "commit_plan_count") != 1 {
		t.Fatalf("trace metadata phase counts = %#v", traceMeta)
	}
}

func intMetricFromMeta(meta map[string]interface{}, key string) int {
	if meta == nil {
		return 0
	}
	switch v := meta[key].(type) {
	case int:
		return v
	case float64:
		return int(v)
	default:
		return 0
	}
}

func jsonListContains(values []interface{}, want string) bool {
	for _, value := range values {
		if got, _ := value.(string); got == want {
			return true
		}
	}
	return false
}
