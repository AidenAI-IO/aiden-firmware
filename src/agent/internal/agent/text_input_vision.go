package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/tmc/langchaingo/llms"
)

type textInputScreenPhase string

const (
	textInputPhaseBeforeType textInputScreenPhase = "before_type"
	textInputPhaseAfterType  textInputScreenPhase = "after_type"
)

type textInputScreenAnalysisRequest struct {
	Phase      textInputScreenPhase
	Platform   string
	TargetText string
	Focus      focusPointArgs
	Segments   []string
}

type textInputCandidateClick struct {
	X    float64 `json:"x"`
	Y    float64 `json:"y"`
	Text string  `json:"text,omitempty"`
}

type textInputScreenAnalysis struct {
	ObservedMode        textInputMode             `json:"observed_mode"`
	FieldText           string                    `json:"field_text"`
	CompositionPending  bool                      `json:"composition_pending"`
	WrongIMESuspected   bool                      `json:"wrong_ime_suspected"`
	SuggestSwitchIME    bool                      `json:"suggest_switch_ime"`
	Candidates          []textInputCandidateClick `json:"candidates"`
	Evidence            []string                  `json:"evidence,omitempty"`
}

type textInputVision interface {
	AnalyzeScreen(ctx context.Context, screenshot screenshotResult, req textInputScreenAnalysisRequest) (textInputScreenAnalysis, error)
}

type llmTextInputVision struct {
	models ModelResolver
}

func newLLMTextInputVision(models ModelResolver) textInputVision {
	if models == nil {
		return nil
	}
	return &llmTextInputVision{models: models}
}

func (v *llmTextInputVision) AnalyzeScreen(ctx context.Context, screenshot screenshotResult, req textInputScreenAnalysisRequest) (textInputScreenAnalysis, error) {
	prompt := buildTextInputAnalysisPrompt(req)
	raw, err := v.visionJSON(ctx, prompt, screenshot)
	if err != nil {
		return textInputScreenAnalysis{}, err
	}
	var parsed struct {
		ObservedMode       string                    `json:"observed_mode"`
		FieldText          string                    `json:"field_text"`
		CompositionPending bool                      `json:"composition_pending"`
		WrongIMESuspected  bool                      `json:"wrong_ime_suspected"`
		SuggestSwitchIME   bool                      `json:"suggest_switch_ime"`
		Candidates         []textInputCandidateClick `json:"candidates"`
		Evidence           []string                  `json:"evidence"`
	}
	if err := json.Unmarshal([]byte(raw), &parsed); err != nil {
		return textInputScreenAnalysis{}, fmt.Errorf("parse screen analysis: %w", err)
	}
	mode, err := parseObservedTextInputMode(parsed.ObservedMode)
	if err != nil {
		mode = textInputModeUnknown
	}
	return textInputScreenAnalysis{
		ObservedMode:       mode,
		FieldText:          strings.TrimSpace(parsed.FieldText),
		CompositionPending: parsed.CompositionPending,
		WrongIMESuspected:  parsed.WrongIMESuspected,
		SuggestSwitchIME:   parsed.SuggestSwitchIME,
		Candidates:         parsed.Candidates,
		Evidence:           uniqueNonEmpty(parsed.Evidence),
	}, nil
}

func buildTextInputAnalysisPrompt(req textInputScreenAnalysisRequest) string {
	segments := strings.Join(req.Segments, ", ")
	if segments == "" {
		segments = "(none yet)"
	}
	phaseHint := "Typing has NOT started yet. If IME mode is unclear, set observed_mode=unknown and suggest_switch_ime=false."
	if req.Phase == textInputPhaseAfterType {
		phaseHint = "Typing already happened. Read ONLY the focused target input field for field_text. Do NOT copy IME candidate bar, preedit strip, keyboard suggestions, or inline composition text into field_text. Set composition_pending=true when target text is visible only in IME candidates/preedit and is not yet fully committed inside the target input field. If field shows only latin/pinyin segments instead of target characters, set wrong_ime_suspected=true and suggest_switch_ime=true."
	}
	return strings.TrimSpace(fmt.Sprintf(`Analyze this device screenshot (Android/iOS/macOS) for text-input automation.
Platform: %q
Phase: %q
Target text: %q
Typed segments: %s
Focus (normalized 0-1000): (%.0f, %.0f)
%s

Return JSON only:
{
  "observed_mode": "ascii|composition|unknown",
  "field_text": "exact committed text visible ONLY inside the target input field",
  "composition_pending": false,
  "wrong_ime_suspected": false,
  "suggest_switch_ime": false,
  "candidates": [{"x":500,"y":800,"text":"你"}],
  "evidence": ["short reason"]
}

Rules:
- field_text: committed text inside the target input box only; exclude IME candidate rows, preedit, and keyboard suggestion chips
- composition_pending: true when target characters still need candidate selection or are only visible outside the target input field
- observed_mode: ascii=direct Latin entry; composition=IME with candidates/preedit; unknown=unclear
- candidates: normalized click points 0-1000 for visible IME candidates that would commit target text into the field; [] if none
- wrong_ime_suspected: true when field shows raw romanization instead of target script
- suggest_switch_ime: true only when confident the wrong keyboard/IME is active`, req.Platform, req.Phase, req.TargetText, segments, req.Focus.X, req.Focus.Y, phaseHint))
}

func (v *llmTextInputVision) visionJSON(ctx context.Context, prompt string, screenshot screenshotResult) (string, error) {
	model, err := v.models.Get()
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(screenshot.Data) == "" {
		return "", fmt.Errorf("screenshot data missing")
	}
	ctx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	imgURL := "data:image/jpeg;base64," + screenshot.Data
	msgs := []llms.MessageContent{
		llms.TextParts(llms.ChatMessageTypeSystem, "You analyze device screenshots for text input automation. Output JSON only."),
		{Role: llms.ChatMessageTypeHuman, Parts: []llms.ContentPart{
			llms.TextPart(prompt),
			llms.ImageURLPart(imgURL),
		}},
	}
	resp, err := model.GenerateContent(ctx, msgs, llms.WithTemperature(0), llms.WithJSONMode())
	if err != nil {
		return "", err
	}
	if len(resp.Choices) == 0 {
		return "", fmt.Errorf("empty vision response")
	}
	return stripJSONCodeFence(resp.Choices[0].Content), nil
}

func stripJSONCodeFence(raw string) string {
	raw = strings.TrimSpace(raw)
	raw = strings.TrimPrefix(raw, "```json")
	raw = strings.TrimPrefix(raw, "```")
	raw = strings.TrimSuffix(raw, "```")
	return strings.TrimSpace(raw)
}

type stubTextInputVision struct {
	analyses []textInputScreenAnalysis
}

func (s *stubTextInputVision) AnalyzeScreen(_ context.Context, _ screenshotResult, _ textInputScreenAnalysisRequest) (textInputScreenAnalysis, error) {
	if len(s.analyses) == 0 {
		return textInputScreenAnalysis{ObservedMode: textInputModeComposition}, nil
	}
	out := s.analyses[0]
	s.analyses = s.analyses[1:]
	return out, nil
}

func analysisToClicks(candidates []textInputCandidateClick) []focusPointArgs {
	out := make([]focusPointArgs, 0, len(candidates))
	for _, candidate := range candidates {
		out = append(out, focusPointArgs{X: candidate.X, Y: candidate.Y, CoordSpace: "normalized"})
	}
	return out
}
