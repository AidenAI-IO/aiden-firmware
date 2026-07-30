package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	modelpkg "aiden-agent/internal/agent/model"

	"github.com/tmc/langchaingo/chains"
	"github.com/tmc/langchaingo/llms"
)

type retryingTextInputVision struct {
	calls int
}

func (v *retryingTextInputVision) AnalyzeScreen(_ context.Context, _ screenshotResult, req textInputScreenAnalysisRequest) (textInputScreenAnalysis, error) {
	v.calls++
	if v.calls == 1 {
		var truncated any
		err := json.Unmarshal([]byte(`{"target_matched":`), &truncated)
		return textInputScreenAnalysis{}, fmt.Errorf("parse screen analysis: %w", err)
	}
	return textInputScreenAnalysis{FieldText: req.TargetText, TargetMatched: true}, nil
}

type textInputVisionRecordingModel struct {
	options  []llms.CallOption
	content  string
	contents []string
	calls    int
}

func (m *textInputVisionRecordingModel) GenerateContent(_ context.Context, _ []llms.MessageContent, options ...llms.CallOption) (*llms.ContentResponse, error) {
	m.options = options
	m.calls++
	content := m.content
	if len(m.contents) > 0 {
		index := m.calls - 1
		if index >= len(m.contents) {
			index = len(m.contents) - 1
		}
		content = m.contents[index]
	}
	if content == "" {
		content = `{}`
	}
	return &llms.ContentResponse{Choices: []*llms.ContentChoice{{Content: content}}}, nil
}

func (m *textInputVisionRecordingModel) Call(context.Context, string, ...llms.CallOption) (string, error) {
	return "", nil
}

func (m *textInputVisionRecordingModel) CallOptions() []chains.ChainCallOption { return nil }

func (m *textInputVisionRecordingModel) Spec() modelpkg.ModelSpec { return modelpkg.ModelSpec{} }

func TestVisionJSONBoundsOutputTokens(t *testing.T) {
	model := &textInputVisionRecordingModel{}
	vision := &llmTextInputVision{models: model}
	if _, err := vision.visionJSON(context.Background(), "return json", screenshotResult{Data: "ZmFrZQ=="}); err != nil {
		t.Fatalf("visionJSON() error = %v", err)
	}
	options := llms.CallOptions{}
	for _, option := range model.options {
		option(&options)
	}
	if !options.JSONMode {
		t.Fatal("visionJSON() must request JSON mode")
	}
	if options.MaxTokens != textInputVisionMaxTokens {
		t.Fatalf("MaxTokens = %d, want %d", options.MaxTokens, textInputVisionMaxTokens)
	}
	if options.MaxTokens < 4096 {
		t.Fatalf("MaxTokens = %d, want at least 4096", options.MaxTokens)
	}
}

func TestTextInputAnalysisPromptDescribesLastDirectPart(t *testing.T) {
	prompt := buildTextInputAnalysisPrompt(textInputScreenAnalysisRequest{
		Phase:            textInputPhaseAfterType,
		Platform:         "ios",
		TargetText:       "你好我是Aiden，",
		LastDirectInput:  "Aiden,",
		LastDirectTarget: "Aiden，",
	})
	for _, want := range []string{
		`Last direct HID input: "Aiden,"`,
		`Expected rendered text for that direct part: "Aiden，"`,
		`"target_matched": false`,
		"Use visual meaning, not a code-point comparison",
		"committed with no active candidate/preedit box, set composition_pending=false",
	} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("prompt missing %q:\n%s", want, prompt)
		}
	}
	if strings.Contains(prompt, "candidate_action") {
		t.Fatalf("general analysis prompt must not include candidate decisions:\n%s", prompt)
	}
}

func TestCandidateActionUsesDedicatedPromptAndResponse(t *testing.T) {
	prompt := buildTextInputCandidateActionPrompt(textInputScreenAnalysisRequest{
		TargetText:             "前缀目标词",
		CandidateTargetText:    "目标词",
		CandidateCommittedText: "目",
	})
	for _, want := range []string{
		"single next keyboard action",
		`Current IME part: "目标词"`,
		`Text already selected within the current IME part: "目"`,
		`{"action":"select","offset":0`,
		`{"action":"expand"}`,
		`{"action":"none"}`,
		"remaining target",
		"including a single character",
		"similar pronunciation",
		"active preedit/composition is not proof",
		"expand/disclosure control",
		"no exact-prefix candidate is visible",
		"use none",
	} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("candidate prompt missing %q:\n%s", want, prompt)
		}
	}

	model := &textInputVisionRecordingModel{content: `{"action":"select","offset":0,"text":"你好"}`}
	vision := &llmTextInputVision{models: model}
	action, err := vision.DecideCandidateAction(context.Background(), screenshotResult{Data: "ZmFrZQ=="}, textInputScreenAnalysisRequest{TargetText: "你好"})
	if err != nil {
		t.Fatalf("DecideCandidateAction() error = %v", err)
	}
	if action.Action != textInputCandidateActionSelect || action.Offset != 0 || action.Text != "你好" {
		t.Fatalf("candidate action = %+v", action)
	}
}

func TestTextInputProbeUsesDedicatedPromptAndResponse(t *testing.T) {
	prompt := buildTextInputProbePrompt("ios", focusPointArgs{X: 500, Y: 300})
	for _, want := range []string{
		`"candidate_popup_visible":true`,
		`"cjk_candidate_visible":true`,
		`Chinese candidates such as "啊", "爱"`,
		`Candidate text does not need to match "a"`,
		"input-mode probe only",
		"on-screen keyboard may be visible or completely absent",
		"inline preedit text",
		"candidate row or popup near the cursor or input field",
		"Never return unknown merely because an on-screen keyboard is absent",
		"HIGHEST PRIORITY",
		`"1 啊"`,
		"visible text cursor beside",
		"absence of an underline does not prove ASCII mode",
	} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("probe prompt missing %q:\n%s", want, prompt)
		}
	}

	model := &textInputVisionRecordingModel{content: `{"mode":"composition","evidence":"candidate 啊 is visible"}`}
	vision := &llmTextInputVision{models: model}
	analysis, err := vision.ProbeInputMode(context.Background(), screenshotResult{Data: "ZmFrZQ=="}, "ios", focusPointArgs{X: 500, Y: 300})
	if err != nil {
		t.Fatalf("ProbeInputMode() error = %v", err)
	}
	if analysis.Mode != textInputModeComposition || analysis.Evidence != "candidate 啊 is visible" {
		t.Fatalf("analysis = %+v", analysis)
	}
}

func TestTextInputProbeCJKCandidatePopupOverridesASCIIClassification(t *testing.T) {
	model := &textInputVisionRecordingModel{content: `{
		"typed_a_visible":true,
		"inline_preedit_visible":false,
		"candidate_popup_visible":true,
		"cjk_candidate_visible":true,
		"onscreen_keyboard_visible":false,
		"mode":"ascii",
		"evidence":"a has a cursor but numbered Chinese candidates are visible below it"
	}`}
	vision := &llmTextInputVision{models: model}
	analysis, err := vision.ProbeInputMode(context.Background(), screenshotResult{Data: "ZmFrZQ=="}, "ios", focusPointArgs{})
	if err != nil {
		t.Fatalf("ProbeInputMode() error = %v", err)
	}
	if analysis.Mode != textInputModeComposition {
		t.Fatalf("mode = %s, want composition for visible CJK candidate popup", analysis.Mode)
	}
}

func TestAnalyzeScreenRetriesTruncatedVisionJSON(t *testing.T) {
	vision := &retryingTextInputVision{}
	engine := newTextInputEngine(textInputHardwareDeps{
		screenshot: textInputStubTool{name: "screenshot", out: `{"format":"jpeg","width":100,"height":100,"data":"abc"}`},
	}, vision)

	analysis, calls, _, err := engine.analyzeScreen(context.Background(), "ios", enterTextInFieldArgs{
		Text: "你好",
	}, nil)
	if err != nil {
		t.Fatalf("analyzeScreen() error = %v", err)
	}
	if !analysis.TargetMatched {
		t.Fatal("analyzeScreen() did not return the successful retry")
	}
	if calls != 2 || vision.calls != 2 {
		t.Fatalf("calls = %d, vision calls = %d; want 2", calls, vision.calls)
	}
}

func TestPlanCompositionReordersPerCharacterMappingsByTargetIndex(t *testing.T) {
	model := &textInputVisionRecordingModel{content: `{
		"mappings":[
			{"index":4,"text":"率","input":"lv"},
			{"index":0,"text":"我","input":"wo"},
			{"index":3,"text":"概","input":"gai"},
			{"index":2,"text":"大","input":"da"},
			{"index":1,"text":"们","input":"men"}
		]
	}`}
	vision := &llmTextInputVision{models: model}
	segments, err := vision.PlanComposition(context.Background(), "我们大概率")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"wo", "men", "da", "gai", "lv"}
	if len(segments) != len(want) {
		t.Fatalf("segments=%v, want %v", segments, want)
	}
	for index := range want {
		if segments[index] != want[index] {
			t.Fatalf("segments=%v, want %v", segments, want)
		}
	}
}

func TestPlanCompositionRejectsCharacterIndexMismatch(t *testing.T) {
	model := &textInputVisionRecordingModel{content: `{
		"mappings":[
			{"index":0,"text":"们","input":"men"},
			{"index":1,"text":"我","input":"wo"}
		]
	}`}
	vision := &llmTextInputVision{models: model}
	if _, err := vision.PlanComposition(context.Background(), "我们"); err == nil || !strings.Contains(err.Error(), "want") {
		t.Fatalf("PlanComposition() error=%v, want character mismatch", err)
	}
}

func TestPlanCompositionRetriesTruncatedJSONWithLargerTokenBudget(t *testing.T) {
	model := &textInputVisionRecordingModel{contents: []string{
		`{"mappings":[{"index":0,"text":"我","input":"wo"}`,
		`{"mappings":[{"index":0,"text":"我","input":"wo"},{"index":1,"text":"们","input":"men"}]}`,
	}}
	vision := &llmTextInputVision{models: model}
	segments, err := vision.PlanComposition(context.Background(), "我们")
	if err != nil {
		t.Fatal(err)
	}
	if model.calls != 2 {
		t.Fatalf("model calls=%d, want 2", model.calls)
	}
	if len(segments) != 2 || segments[0] != "wo" || segments[1] != "men" {
		t.Fatalf("segments=%v, want [wo men]", segments)
	}
	options := llms.CallOptions{}
	for _, option := range model.options {
		option(&options)
	}
	if options.MaxTokens != textInputPlanMaxTokens {
		t.Fatalf("planner MaxTokens=%d, want %d", options.MaxTokens, textInputPlanMaxTokens)
	}
	if options.MaxTokens < 4096 {
		t.Fatalf("planner MaxTokens=%d, want at least 4096", options.MaxTokens)
	}
}
