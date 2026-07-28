package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"aiden-agent/internal/agent/model"

	"github.com/tmc/langchaingo/llms"
)

type textInputScreenPhase string

const (
	textInputPhaseBeforeType textInputScreenPhase = "before_type"
	textInputPhaseAfterType  textInputScreenPhase = "after_type"
	// Keep non-streaming vision analysis bounded. Some providers otherwise spend
	// an unbounded amount of time producing hidden reasoning before returning
	// the small JSON decision this workflow needs.
	textInputVisionMaxTokens = 200
)

type textInputScreenAnalysisRequest struct {
	Phase            textInputScreenPhase
	Platform         string
	TargetText       string
	Focus            focusPointArgs
	Segments         []string
	LastDirectInput  string
	LastDirectTarget string
}

type textInputCandidateSelection struct {
	Offset int    `json:"offset"`
	Text   string `json:"text,omitempty"`
}

type textInputScreenAnalysis struct {
	ObservedMode       textInputMode                 `json:"observed_mode"`
	FieldText          string                        `json:"field_text"`
	TargetMatched      bool                          `json:"target_matched"`
	CompositionPending bool                          `json:"composition_pending"`
	WrongIMESuspected  bool                          `json:"wrong_ime_suspected"`
	SuggestSwitchIME   bool                          `json:"suggest_switch_ime"`
	Candidates         []textInputCandidateSelection `json:"candidates"`
	CandidateExpand    bool                          `json:"candidate_expand,omitempty"`
}

type textInputVision interface {
	AnalyzeScreen(ctx context.Context, screenshot screenshotResult, req textInputScreenAnalysisRequest) (textInputScreenAnalysis, error)
}

type llmTextInputVision struct {
	models model.Model
}

func newLLMTextInputVision(models model.Model) textInputVision {
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
		ObservedMode       string                        `json:"observed_mode"`
		FieldText          string                        `json:"field_text"`
		TargetMatched      bool                          `json:"target_matched"`
		CompositionPending bool                          `json:"composition_pending"`
		WrongIMESuspected  bool                          `json:"wrong_ime_suspected"`
		SuggestSwitchIME   bool                          `json:"suggest_switch_ime"`
		Candidates         []textInputCandidateSelection `json:"candidates"`
		CandidateExpand    bool                          `json:"candidate_expand"`
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
		TargetMatched:      parsed.TargetMatched,
		CompositionPending: parsed.CompositionPending,
		WrongIMESuspected:  parsed.WrongIMESuspected,
		SuggestSwitchIME:   parsed.SuggestSwitchIME,
		Candidates:         parsed.Candidates,
		CandidateExpand:    parsed.CandidateExpand,
	}, nil
}

// PlanComposition turns one non-ASCII text run into the ASCII keystrokes that
// should be sent to the current IME. This keeps transliteration out of the
// public tool contract; enter_text callers supply only the original text.
func (v *llmTextInputVision) PlanComposition(ctx context.Context, target string) ([]string, error) {
	target = strings.TrimSpace(target)
	if target == "" {
		return nil, fmt.Errorf("IME target text is required")
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	prompt := fmt.Sprintf(`Convert each character of the following exact non-ASCII text into the ASCII keystrokes needed to enter it with a standard phone IME.
Target text: %q

Return JSON only:
{"mappings":[{"index":0,"text":"原","input":"yuan"}]}

Rules:
- Return exactly one mapping for every Unicode character in Target text.
- index is the zero-based character index in Target text; include every index exactly once.
- text must be exactly the one target character at index.
- input is that character's common context-appropriate romanization/IME input.
- input must contain only lowercase ASCII letters or digits, without spaces or tone marks.
- Mapping array order does not matter; the caller reorders it by index.
- Do not include explanations or candidate labels.`, target)
	var lastErr error
	for attempt := 1; attempt <= textInputPlanAttempts; attempt++ {
		attemptPrompt := prompt
		if lastErr != nil {
			attemptPrompt += fmt.Sprintf("\n\nThe previous response was invalid: %s. Return the complete JSON object again.", lastErr)
		}
		resp, err := v.models.GenerateContent(ctx, []llms.MessageContent{
			llms.TextParts(llms.ChatMessageTypeSystem, "You plan exact IME keystrokes for text-entry automation. Output JSON only."),
			llms.TextParts(llms.ChatMessageTypeHuman, attemptPrompt),
		}, llms.WithJSONMode(), llms.WithMaxTokens(textInputPlanMaxTokens))
		if err != nil {
			return nil, err
		}
		if len(resp.Choices) == 0 {
			lastErr = fmt.Errorf("empty IME planning response")
			continue
		}
		segments, err := parseTextInputCompositionPlan(target, resp.Choices[0].Content)
		if err == nil {
			return segments, nil
		}
		lastErr = err
	}
	return nil, fmt.Errorf("IME plan remained invalid after %d attempts: %w", textInputPlanAttempts, lastErr)
}

func parseTextInputCompositionPlan(target, raw string) ([]string, error) {
	var parsed struct {
		Mappings []struct {
			Index int    `json:"index"`
			Text  string `json:"text"`
			Input string `json:"input"`
		} `json:"mappings"`
	}
	if err := json.Unmarshal([]byte(stripJSONCodeFence(raw)), &parsed); err != nil {
		return nil, fmt.Errorf("parse IME plan: %w", err)
	}
	runes := []rune(target)
	if len(parsed.Mappings) != len(runes) {
		return nil, fmt.Errorf("IME plan returned %d character mappings for %d target characters", len(parsed.Mappings), len(runes))
	}
	segments := make([]string, len(runes))
	seen := make([]bool, len(runes))
	for _, mapping := range parsed.Mappings {
		if mapping.Index < 0 || mapping.Index >= len(runes) {
			return nil, fmt.Errorf("IME plan mapping index %d is out of range", mapping.Index)
		}
		if seen[mapping.Index] {
			return nil, fmt.Errorf("IME plan contains duplicate mapping index %d", mapping.Index)
		}
		mappedRunes := []rune(mapping.Text)
		if len(mappedRunes) != 1 || mappedRunes[0] != runes[mapping.Index] {
			return nil, fmt.Errorf("IME plan mapping index %d has text %q, want %q", mapping.Index, mapping.Text, string(runes[mapping.Index]))
		}
		input := strings.TrimSpace(mapping.Input)
		if input == "" {
			return nil, fmt.Errorf("IME plan mapping index %d has empty input", mapping.Index)
		}
		for _, r := range input {
			if (r < 'a' || r > 'z') && (r < '0' || r > '9') {
				return nil, fmt.Errorf("IME plan mapping index %d has invalid input %q", mapping.Index, input)
			}
		}
		seen[mapping.Index] = true
		segments[mapping.Index] = input
	}
	return segments, nil
}

func buildTextInputAnalysisPrompt(req textInputScreenAnalysisRequest) string {
	segments := strings.Join(req.Segments, ", ")
	if segments == "" {
		segments = "(none yet)"
	}
	phaseHint := "Typing has NOT started yet. If IME mode is unclear, set observed_mode=unknown and suggest_switch_ime=false."
	if req.Phase == textInputPhaseAfterType {
		phaseHint = "Typing already happened. Read ONLY the focused target input field for field_text. Do NOT copy IME candidate bar, preedit strip, keyboard suggestions, or inline composition text into field_text. Set composition_pending=true whenever the just-typed content is still in an IME preedit or candidate state and needs confirmation before more text is typed. If a Last direct HID input is given and any IME candidate/preedit box is visible for that ASCII part, observed_mode MUST be composition even when the raw ASCII already appears inside the field. If the direct ASCII part is committed with no active candidate/preedit box, set composition_pending=false and observed_mode=ascii. IME candidate selection is handled separately. If field shows only latin/pinyin segments instead of target characters, set wrong_ime_suspected=true and suggest_switch_ime=true."
	}
	return strings.TrimSpace(fmt.Sprintf(`Analyze this device screenshot (Android/iOS/macOS) for text-input automation.
Platform: %q
Phase: %q
Target text: %q
Typed segments: %s
Last direct HID input: %q
Expected rendered text for that direct part: %q
Focus (normalized 0-1000): (%.0f, %.0f)
%s

Return JSON only:
{
  "observed_mode": "ascii|composition|unknown",
  "field_text": "exact committed text visible ONLY inside the target input field",
  "target_matched": false,
  "composition_pending": false,
  "wrong_ime_suspected": false,
  "suggest_switch_ime": false,
  "candidates": [{"offset":2,"text":"你"}],
  "candidate_expand": false
}

Rules:
- field_text: committed text inside the target input box only; exclude IME candidate rows, preedit, and keyboard suggestion chips
- target_matched: directly judge whether the committed text visible in the focused field matches Target text. Use visual meaning, not a code-point comparison of your field_text transcription. Treat visually equivalent punctuation forms such as full-width versus half-width comma as matching when the screenshot cannot reliably distinguish them. Return false for missing, extra, reordered, or visibly wrong letters, digits, or words
- composition_pending: true whenever the just-typed content is still in an IME preedit/candidate state and requires confirmation, including an ASCII part shown with an IME candidate box
- observed_mode: ascii=direct Latin entry with no active candidate/preedit box; composition=any active IME candidate/preedit state, including a candidate box shown for an ASCII part such as "4k60"
- candidates: return at most one entry only when the exact intended pending word or phrase is visibly present and selecting it would make the committed field match Target text. offset is the signed number of keyboard moves from the currently highlighted candidate: positive means Right, negative means Left, zero means press Space immediately. Never return the first/default candidate merely because it is first or looks similar; [] if the intended candidate is not visibly present
- candidate_expand: true when the intended candidate is not visible in the collapsed candidate row and the candidate list should be expanded with the Down key; otherwise false. Do not set it when the intended candidate is already visible
- wrong_ime_suspected: true when field shows raw romanization instead of target script
- suggest_switch_ime: true only when confident the wrong keyboard/IME is active`, req.Platform, req.Phase, req.TargetText, segments, req.LastDirectInput, req.LastDirectTarget, req.Focus.X, req.Focus.Y, phaseHint))
}

func (v *llmTextInputVision) visionJSON(ctx context.Context, prompt string, screenshot screenshotResult) (string, error) {
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
	// Use the model's configured temperature for vision analysis. Previously
	// hardcoded to 0 for determinism, but that breaks kimi-k3 (requires temp=1)
	// and the temperature difference has minimal impact on vision text extraction.
	resp, err := v.models.GenerateContent(ctx, msgs,
		llms.WithJSONMode(),
		llms.WithMaxTokens(textInputVisionMaxTokens),
	)
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

func (s *stubTextInputVision) AnalyzeScreen(_ context.Context, _ screenshotResult, req textInputScreenAnalysisRequest) (textInputScreenAnalysis, error) {
	if len(s.analyses) == 0 {
		return textInputScreenAnalysis{ObservedMode: textInputModeComposition}, nil
	}
	out := s.analyses[0]
	s.analyses = s.analyses[1:]
	// Existing unit-test fixtures predate target_matched and describe successful
	// observations through field_text. Preserve that fixture shorthand without
	// reintroducing string comparison into the production verifier.
	if !out.TargetMatched && fieldTextExactlyMatches(out.FieldText, req.TargetText) {
		out.TargetMatched = true
	}
	return out, nil
}
