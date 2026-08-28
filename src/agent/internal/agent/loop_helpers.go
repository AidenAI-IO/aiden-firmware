package agent

import (
	"encoding/json"
	"strings"
)

func steerHumanMessageContent(steer RunSteerMessage) string {
	content := strings.TrimSpace(steer.Content)
	if content == "" {
		content = "(empty steering message)"
	}
	return content
}

func compactStringList(values []string, max int) string {
	values = uniqueNonEmpty(values)
	if len(values) == 0 {
		return "[]"
	}
	parts := make([]string, 0, len(values))
	for _, value := range values {
		if line := compactPromptLine(value, 160); line != "" {
			parts = append(parts, line)
		}
	}
	return "[" + truncateForLog(strings.Join(parts, " | "), max) + "]"
}

func compactPromptLine(value string, max int) string {
	value = strings.ReplaceAll(strings.ReplaceAll(value, "\r\n", " "), "\n", " ")
	return truncateForLog(value, max)
}

func actionOutputFromScreenshotObservation(observation string) (string, bool) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal([]byte(observation), &fields); err != nil {
		return "", false
	}
	raw, ok := fields["action_output"]
	if !ok {
		return "", false
	}
	var output string
	if err := json.Unmarshal(raw, &output); err != nil {
		return "", false
	}
	return output, true
}

func compactScreenshotObservation(_ string, observation string) (string, bool) {
	var result postActionScreenshotResult
	if err := json.Unmarshal([]byte(observation), &result); err != nil {
		return "", false
	}
	if (result.Data == "" && strings.TrimSpace(result.ScreenshotRef) == "") || result.Width <= 0 || result.Height <= 0 {
		return "", false
	}
	if actionOutput, ok := actionOutputFromScreenshotObservation(observation); ok {
		return actionOutput, true
	}
	return observation, true
}

func (o observedWorldState) IsEmpty() bool {
	return strings.TrimSpace(o.AppName) == "" &&
		strings.TrimSpace(o.PageName) == "" &&
		strings.TrimSpace(o.Platform) == "" &&
		len(uniqueNonEmpty(o.VisibleText)) == 0 &&
		len(uniqueNonEmpty(o.Dialogs)) == 0
}

func normalizeObservedWorldState(observed observedWorldState) observedWorldState {
	observed.AppName = strings.TrimSpace(observed.AppName)
	observed.PageName = strings.TrimSpace(observed.PageName)
	if platform, err := normalizeQuickActionPlatform(observed.Platform); err == nil {
		observed.Platform = platform
	} else {
		observed.Platform = ""
	}
	observed.VisibleText = uniqueNonEmpty(observed.VisibleText)
	observed.Dialogs = uniqueNonEmpty(observed.Dialogs)
	if observed.Confidence < 0 {
		observed.Confidence = 0
	}
	if observed.Confidence > 1 {
		observed.Confidence = 1
	}
	return observed
}

func parseOptionalReasonInput(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	var payload struct {
		Reason string `json:"reason"`
	}
	if err := json.Unmarshal([]byte(raw), &payload); err == nil {
		return strings.TrimSpace(payload.Reason)
	}
	return raw
}

func parseStructuredStringList(raw json.RawMessage) []string {
	return uniqueNonEmpty(parseStringList(raw))
}

func parseStringList(raw json.RawMessage) []string {
	if len(raw) == 0 {
		return nil
	}
	var values []string
	if err := json.Unmarshal(raw, &values); err == nil {
		return values
	}
	var value string
	if err := json.Unmarshal(raw, &value); err == nil {
		return uniqueNonEmpty([]string{value})
	}
	return nil
}
