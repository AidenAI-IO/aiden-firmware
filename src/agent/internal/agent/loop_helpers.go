package agent

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
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
	return truncateForLog(singleLineHistoryText(value), max)
}

func compactScreenshotObservation(toolName, observation string) (string, bool) {
	var result postActionScreenshotResult
	if err := json.Unmarshal([]byte(observation), &result); err != nil {
		return "", false
	}
	if (result.Data == "" && strings.TrimSpace(result.ScreenshotRef) == "") || result.Width <= 0 || result.Height <= 0 {
		return "", false
	}
	if strings.TrimSpace(toolName) == "" {
		toolName = "tool"
	}
	format := strings.TrimSpace(result.Format)
	if format == "" {
		format = "jpeg"
	}
	size := result.Size
	if size <= 0 {
		if imageBytes, err := base64.StdEncoding.DecodeString(result.Data); err == nil {
			size = len(imageBytes)
		}
	}
	if strings.TrimSpace(result.ActionOutput) != "" {
		return fmt.Sprintf(
			"%s completed with output %q, then returned a screenshot observation after the action settled: format=%s width=%d height=%d size=%d bytes. Image data omitted from text summary.",
			toolName,
			compactToolObservation(result.ActionOutput),
			format,
			result.Width,
			result.Height,
			size,
		), true
	}
	return fmt.Sprintf(
		"%s returned a screenshot observation: format=%s width=%d height=%d size=%d bytes. Image data omitted from text summary.",
		toolName,
		format,
		result.Width,
		result.Height,
		size,
	), true
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
