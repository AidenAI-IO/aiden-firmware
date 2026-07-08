package agent

import (
	"strings"
	"time"
)

func voicePreRunTelemetryEvent(eventType, content string, startedAt time.Time, duration time.Duration, metadata map[string]interface{}, err error) TaskEpisodeEvent {
	if startedAt.IsZero() {
		startedAt = time.Now().UTC()
	}
	startedAt = startedAt.UTC()
	durationMs := duration.Milliseconds()
	if durationMs < 0 {
		durationMs = 0
	}
	copyMetadata := make(map[string]interface{}, len(metadata)+1)
	for key, value := range metadata {
		copyMetadata[key] = value
	}
	copyMetadata["success"] = err == nil

	event := TaskEpisodeEvent{
		Type:       eventType,
		Role:       "system",
		Ts:         startedAt.Format(time.RFC3339Nano),
		Content:    strings.TrimSpace(content),
		DurationMs: &durationMs,
		Metadata:   copyMetadata,
	}
	if err != nil {
		event.IsError = true
		event.Reason = err.Error()
	}
	return event
}
