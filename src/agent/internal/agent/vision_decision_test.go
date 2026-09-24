package agent

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"github.com/tmc/langchaingo/llms"
)

func TestVisionDecisionRetriesInvalidContractOnSameObservation(t *testing.T) {
	for _, invalid := range []string{
		`{"type":"json_object"}`, `{}`, `null`, `[]`, `{"found":`,
		`{"found":null}`, `{"found":"true"}`, `{"found":true}`, `{"found":true,"tap_point":{"x":1,"y":1001}}`,
		`{"found":false} {"found":true}`,
	} {
		t.Run(invalid, func(t *testing.T) {
			model := &textInputVisionRecordingModel{contents: []string{invalid, `{"found":false}`}}
			ctx, metrics := withTextInputMetrics(context.Background())
			result, calls, err := requestVisionDecision(ctx, &llmTextInputVision{models: model}, "app_search", buildAppSearchResultPrompt("Test"), parseAppSearchResult, screenshotResult{Data: "abc"})
			if err != nil || result.Found || calls != 2 || metrics.vllmCalls.Load() != 2 {
				t.Fatalf("result=%+v calls=%d err=%v", result, calls, err)
			}
			if !reflect.DeepEqual(model.optionHistory[0], model.optionHistory[1]) || !model.optionHistory[1].JSONMode {
				t.Fatal("validation must not silently change provider options")
			}
			first, second := model.messageHistory[0][1].Parts, model.messageHistory[1][1].Parts
			if !reflect.DeepEqual(first[1:], second[1:]) {
				t.Fatal("validation retry changed screenshots")
			}
			if !strings.Contains(second[0].(llms.TextContent).Text, "failed validation") {
				t.Fatal("retry did not explain the contract violation")
			}
		})
	}
}

func TestVisionDecisionAcceptsNegativeAndAdditionalFields(t *testing.T) {
	for _, raw := range []string{`{"found":false}`, `{"found":false,"metadata":{"version":2}}`, "```json\n{\"found\":false}\n```"} {
		model := &textInputVisionRecordingModel{content: raw}
		result, calls, err := requestVisionDecision(context.Background(), &llmTextInputVision{models: model}, "app_search", "Return JSON", parseAppSearchResult, screenshotResult{Data: "abc"})
		if err != nil || result.Found || calls != 1 {
			t.Fatalf("result=%+v calls=%d err=%v", result, calls, err)
		}
	}
}

func TestVisionDecisionExhaustionDoesNotBecomeNegativeObservation(t *testing.T) {
	model := &textInputVisionRecordingModel{content: `{"opened":null}`}
	vision := &llmTextInputVision{models: model}
	hw := textInputHardwareDeps{screenshot: textInputStubTool{name: "screenshot", out: `{"data":"abc"}`}}
	_, calls, err := confirmSearchOpenApp(context.Background(), appSearchOpenFlowConfig{vision: vision}, newFastTextInputEngine(hw, vision), "Test")
	var contractErr *visionDecisionResponseError
	if !errors.As(err, &contractErr) || contractErr.Operation != "app_open_confirmation" || calls != textInputVisionParseAttempts || model.calls != calls {
		t.Fatalf("calls=%d model calls=%d err=%v", calls, model.calls, err)
	}
}

func TestVisionDecisionDoesNotRetryTransportErrorOrCancellation(t *testing.T) {
	wantErr := errors.New("HTTP request failed")
	model := &textInputVisionRecordingModel{err: wantErr}
	vision := &llmTextInputVision{models: model}
	_, calls, err := requestVisionDecision(context.Background(), vision, "app_search", "Return JSON", parseAppSearchResult, screenshotResult{Data: "abc"})
	if !errors.Is(err, wantErr) || calls != 1 || model.calls != 1 {
		t.Fatalf("calls=%d err=%v", calls, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, calls, err = requestVisionDecision(ctx, vision, "app_search", "Return JSON", parseAppSearchResult, screenshotResult{Data: "abc"})
	if !errors.Is(err, context.Canceled) || calls != 0 || model.calls != 1 {
		t.Fatalf("cancelled calls=%d err=%v", calls, err)
	}
}

func TestVisionDecisionRejectsIncompleteEnvelope(t *testing.T) {
	for _, response := range []*llms.ContentResponse{
		nil, {}, {Choices: []*llms.ContentChoice{nil}},
		{Choices: []*llms.ContentChoice{{Content: ""}}},
		{Choices: []*llms.ContentChoice{{Content: `{"found":false}`, StopReason: "length"}}},
		{Choices: []*llms.ContentChoice{{Content: `{"found":false}`, StopReason: "content_filter"}}},
		{Choices: []*llms.ContentChoice{{Content: `{"found":false}`, ToolCalls: []llms.ToolCall{{ID: "call"}}}}},
	} {
		if _, err := visionDecisionContent(response); err == nil {
			t.Fatalf("accepted invalid response: %+v", response)
		}
	}
}

func TestVisionDecisionRequiresExecutableFields(t *testing.T) {
	for _, raw := range []string{`{}`, `{"action":"typo"}`, `{"action":"select","text":"Test"}`, `{"action":"select","offset":null,"text":"Test","completes_part":true}`, `{"action":"select","offset":21,"text":"Test","completes_part":true}`} {
		if _, err := parseTextInputCandidateAction(raw); err == nil {
			t.Fatalf("accepted candidate: %s", raw)
		}
	}
	if action, err := parseTextInputCandidateAction(`{"action":"none"}`); err != nil || action.Action != textInputCandidateActionNone {
		t.Fatalf("explicit none = %+v, %v", action, err)
	}
	if _, err := parseTextInputProbeAnalysis(`{"mode":"ascii"}`); err == nil {
		t.Fatal("missing composition evidence must not default to false")
	}
	if _, err := parseTextInputScreenAnalysis(`{"observed_mode":"ascii","field_text":"","target_matched":true}`); err == nil {
		t.Fatal("missing composition decision must not authorize completion")
	}
	if _, err := parseTextInputProbeCleanup(`{"probe_character_visible":true}`); err == nil {
		t.Fatal("missing cleanup safety decision")
	}
}

// Exercise the actual provider serializer and response extraction. API format
// configuration belongs outside messages; assistant content must be preserved
// even when it fails the application's decision contract.
func TestVisionDecisionProviderWireBoundary(t *testing.T) {
	const content = `{"type":"json_object"}`
	client := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		var body struct {
			ResponseFormat map[string]string `json:"response_format"`
			Messages       json.RawMessage   `json:"messages"`
		}
		if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if body.ResponseFormat["type"] != "json_object" || strings.Contains(string(body.Messages), "json_object") || !strings.Contains(string(body.Messages), "data:image/jpeg;base64,abc") || !strings.Contains(string(body.Messages), "found") {
			t.Fatalf("incorrect wire boundary: %+v", body)
		}
		response, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{"finish_reason": "stop", "message": map[string]any{"role": "assistant", "content": content}}}})
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(string(response)))}, nil
	})}
	provider, err := buildDeepSeekModel(ModelBuildContext{HTTPClient: client}, ModelConfig{Provider: "deepseek", Model: "deepseek-flash", ReasoningEffort: "none"})
	if err != nil {
		t.Fatal(err)
	}
	response, err := provider.GenerateContent(context.Background(), []llms.MessageContent{
		llms.TextParts(llms.ChatMessageTypeSystem, "Return JSON only."),
		{Role: llms.ChatMessageTypeHuman, Parts: []llms.ContentPart{llms.TextPart(buildAppSearchResultPrompt("Test")), llms.ImageURLPart("data:image/jpeg;base64,abc")}},
	}, llms.WithJSONMode())
	if err != nil {
		t.Fatal(err)
	}
	raw, err := visionDecisionContent(response)
	if err != nil || raw != content {
		t.Fatalf("provider content was replaced: %q, %v", raw, err)
	}
	if _, err := parseAppSearchResult(raw); err == nil {
		t.Fatal("valid JSON syntax must not substitute for a valid search decision")
	}
}
