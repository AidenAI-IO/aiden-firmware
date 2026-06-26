package agent

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
	"unicode"
)

type textInputMode string

type textInputInteractionMode string

const (
	textInputModeASCII       textInputMode = "ascii"
	textInputModeComposition textInputMode = "composition"
	textInputModeUnknown     textInputMode = "unknown"

	textInputModeForm   textInputInteractionMode = "form"
	textInputModeSearch textInputInteractionMode = "search"

	textInputKeystrokeGap         = 60 * time.Millisecond
	textInputFocusRestoreDelay    = 250 * time.Millisecond
	textInputIMESwitchSettleDelay = time.Second
	textInputClearBackspaceRepeats  = 32
	textInputClearBackspaceFallback = 16
	textInputMaxAttempts            = 3
	textInputCandidatePageMax      = 5
	textInputCandidatePageDelay    = 300 * time.Millisecond
)

func normalizeTextInputInteractionMode(raw string) textInputInteractionMode {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "", string(textInputModeForm):
		return textInputModeForm
	case string(textInputModeSearch):
		return textInputModeSearch
	default:
		return textInputModeForm
	}
}

func requiredTextInputMode(text string) textInputMode {
	if needsCompositionInput(text) {
		return textInputModeComposition
	}
	return textInputModeASCII
}

func needsCompositionInput(text string) bool {
	for _, r := range text {
		if r > 127 {
			return true
		}
	}
	return false
}

func containsHanRunes(text string) bool {
	for _, r := range text {
		if unicode.Is(unicode.Han, r) {
			return true
		}
	}
	return false
}

func compositionSegmentsForText(text string, segments []string) ([]string, error) {
	segs := normalizeCompositionSegments(segments)
	if len(segs) == 0 {
		return nil, fmt.Errorf("composition input requires segments (IME romanization syllables); none provided for %q", text)
	}
	return segs, nil
}

func fieldTextExactlyMatches(fieldText, targetText string) bool {
	return strings.TrimSpace(fieldText) == strings.TrimSpace(targetText)
}

func evaluateFieldCommit(analysis textInputScreenAnalysis, targetText string) (committed bool, fieldText string) {
	fieldText = strings.TrimSpace(analysis.FieldText)
	targetText = strings.TrimSpace(targetText)
	if needsCompositionInput(targetText) {
		if analysis.CompositionPending {
			return false, fieldText
		}
		return fieldTextExactlyMatches(fieldText, targetText), fieldText
	}
	// ASCII: committed text in the field wins even if VLM wrongly sets composition_pending.
	if fieldTextExactlyMatches(fieldText, targetText) || strings.EqualFold(fieldText, targetText) {
		return true, fieldText
	}
	return false, fieldText
}

func shouldSuspectWrongIME(analysis textInputScreenAnalysis, fieldText string, segments []string, requiredMode textInputMode) bool {
	if analysis.WrongIMESuspected || analysis.SuggestSwitchIME {
		return true
	}
	if requiredMode == textInputModeASCII {
		// ASCII text but IME is active (composition pending or candidates visible)
		if analysis.CompositionPending || len(analysis.Candidates) > 0 {
			return true
		}
		if analysis.ObservedMode == textInputModeComposition {
			return true
		}
		return false
	}
	// Composition mode checks below
	if analysis.CompositionPending || len(analysis.Candidates) > 0 {
		return false
	}
	if isRomanizationOnlyField(fieldText, segments) {
		return true
	}
	return analysis.ObservedMode == textInputModeASCII && !containsHanRunes(fieldText)
}

func normalizeCompositionSegments(segments []string) []string {
	out := make([]string, 0, len(segments))
	for _, segment := range segments {
		segment = strings.TrimSpace(segment)
		if segment == "" {
			continue
		}
		out = append(out, segment)
	}
	return out
}

func isRomanizationOnlyField(fieldText string, segments []string) bool {
	fieldText = strings.TrimSpace(fieldText)
	if fieldText == "" || containsHanRunes(fieldText) {
		return false
	}
	normalized := normalizeRomanizationField(fieldText)
	joined := strings.ToLower(strings.Join(segments, ""))
	if joined != "" && (normalized == joined || strings.Contains(normalized, joined)) {
		return true
	}
	return looksLikeSpacedRomanizationBlob(fieldText)
}

func normalizeRomanizationField(fieldText string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(fieldText) {
		if r >= 'a' && r <= 'z' {
			b.WriteRune(r)
		}
	}
	return b.String()
}

func looksLikeSpacedRomanizationBlob(text string) bool {
	text = strings.TrimSpace(text)
	if text == "" || !strings.Contains(text, " ") {
		return false
	}
	parts := strings.Fields(text)
	if len(parts) < 2 {
		return false
	}
	for _, part := range parts {
		if part != strings.ToLower(part) {
			return false
		}
		for _, r := range part {
			if r < 'a' || r > 'z' {
				return false
			}
		}
	}
	return true
}

func parseObservedTextInputMode(raw string) (textInputMode, error) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case string(textInputModeASCII), "english", "direct", "latin":
		return textInputModeASCII, nil
	case string(textInputModeComposition), "chinese", "pinyin", "ime", "cjk", "han":
		return textInputModeComposition, nil
	case string(textInputModeUnknown), "unclear", "unsure":
		return textInputModeUnknown, nil
	case "":
		return "", fmt.Errorf("observed_mode is required")
	default:
		return "", fmt.Errorf("unsupported observed_mode %q", raw)
	}
}

func focusPointArgSchema(description string) map[string]any {
	return map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"description":          description,
		"properties": map[string]any{
			"x":           map[string]any{"type": "number", "description": "X coordinate."},
			"y":           map[string]any{"type": "number", "description": "Y coordinate."},
			"coord_space": stringEnumArgSchema("Coordinate space.", "normalized", "pixel"),
		},
		"required": []string{"x", "y"},
	}
}

type focusPointArgs struct {
	X          float64 `json:"x"`
	Y          float64 `json:"y"`
	CoordSpace string  `json:"coord_space,omitempty"`
}

func textInputKeyboardKeysForIMESwitch(platform string) ([]string, error) {
	switch strings.ToLower(strings.TrimSpace(platform)) {
	case "android":
		return []string{"ctrl", "shift"}, nil
	case "ios":
		return []string{"capslock"}, nil
	case "mac":
		return []string{"ctrl", "space"}, nil
	default:
		return nil, fmt.Errorf("unsupported platform %q for IME switch", platform)
	}
}

func textInputKeyboardKeysForSelectAll(platform string) ([]string, error) {
	switch strings.ToLower(strings.TrimSpace(platform)) {
	case "android":
		return []string{"ctrl", "a"}, nil
	case "ios", "mac":
		return []string{"meta", "a"}, nil
	default:
		return nil, fmt.Errorf("unsupported platform %q for select all", platform)
	}
}

func interpretTextInputToolOutput(out string) error {
	out = strings.TrimSpace(out)
	if out == "" {
		return fmt.Errorf("empty tool output")
	}
	if strings.HasPrefix(out, "error:") {
		return fmt.Errorf("%s", out)
	}
	if strings.HasPrefix(out, "{") {
		var payload struct {
			OK      *bool  `json:"ok"`
			Status  string `json:"status"`
			Message string `json:"message"`
		}
		if err := json.Unmarshal([]byte(out), &payload); err == nil && payload.OK != nil && !*payload.OK {
			msg := strings.TrimSpace(payload.Message)
			if msg == "" {
				msg = strings.TrimSpace(payload.Status)
			}
			if msg == "" {
				msg = "tool returned ok=false"
			}
			return fmt.Errorf("%s", msg)
		}
	}
	return nil
}
