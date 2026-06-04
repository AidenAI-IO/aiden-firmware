package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"
	"gopkg.in/yaml.v3"
)

const langfuseBatchSize = 40

type EpisodeExporter struct {
	cfg    TelemetryConfig
	client *langfuseClient
	logger *Logger
}

func NewEpisodeExporter(cfg TelemetryConfig, logger *Logger) *EpisodeExporter {
	return &EpisodeExporter{
		cfg:    cfg,
		client: newLangfuseClient(cfg),
		logger: logger,
	}
}

func (e *EpisodeExporter) ExportEpisodeDir(ctx context.Context, episodeDir string, episode TaskEpisode, promptCalls ...[]telemetryPromptCall) error {
	if e == nil || !e.cfg.EnabledOrDefault() {
		return nil
	}
	if !e.client.configured() {
		return fmt.Errorf("langfuse credentials or base_url missing")
	}
	metaPath := filepath.Join(episodeDir, "episode.yaml")
	eventsPath := filepath.Join(episodeDir, "events.jsonl")
	if _, err := os.Stat(metaPath); err != nil {
		return fmt.Errorf("episode metadata missing: %w", err)
	}
	data, err := os.ReadFile(metaPath)
	if err != nil {
		return fmt.Errorf("read episode metadata: %w", err)
	}
	var stored TaskEpisode
	if err := yaml.Unmarshal(data, &stored); err != nil {
		return fmt.Errorf("decode episode metadata: %w", err)
	}
	events, err := readEpisodeEvents(eventsPath)
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("read episode events: %w", err)
	}
	stored.Events = events
	if strings.TrimSpace(stored.ID) == "" {
		stored.ID = episode.ID
	}
	var prompts []telemetryPromptCall
	if len(promptCalls) > 0 {
		prompts = promptCalls[0]
	}
	batch, err := e.buildLangfuseBatch(ctx, stored, episodeDir, prompts)
	if err != nil {
		return err
	}
	return e.ingestWithRetry(ctx, batch)
}

func (e *EpisodeExporter) ingestWithRetry(ctx context.Context, batch []langfuseIngestionEvent) error {
	maxRetry := e.cfg.MaxRetryOrDefault()
	var lastErr error
	for attempt := 0; attempt <= maxRetry; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(time.Duration(attempt) * time.Second):
			}
		}
		for i := 0; i < len(batch); i += langfuseBatchSize {
			end := i + langfuseBatchSize
			if end > len(batch) {
				end = len(batch)
			}
			if err := e.client.ingest(ctx, batch[i:end]); err != nil {
				lastErr = err
				break
			}
			lastErr = nil
		}
		if lastErr == nil {
			return nil
		}
	}
	return lastErr
}

func (e *EpisodeExporter) buildLangfuseBatch(ctx context.Context, episode TaskEpisode, episodeDir string, promptCalls ...[]telemetryPromptCall) ([]langfuseIngestionEvent, error) {
	traceID := episode.ID
	if _, err := uuid.Parse(traceID); err != nil {
		traceID = uuid.NewSHA1(uuid.NameSpaceURL, []byte(episode.ID)).String()
	}
	startTime := parseEpisodeTime(episode.StartedAt, time.Now().UTC())
	endTime := parseEpisodeTime(episode.EndedAt, startTime)

	traceBody := map[string]interface{}{
		"id":          traceID,
		"timestamp":   langfuseRFC3339(startTime),
		"name":        "aiden-episode",
		"input":       episode.UserGoal,
		"output":      episode.Outcome.FinalAnswer,
		"tags":        e.traceTags(episode),
		"metadata":    e.traceMetadata(episode),
		"environment": e.cfg.EnvironmentOrDefault(),
	}
	if release := traceReleaseFromEpisode(episode); release != "" {
		traceBody["release"] = release
	}
	if version := traceVersionFromEpisode(episode); version != "" {
		traceBody["version"] = version
	}
	traceEvent, err := newLangfuseEvent("trace-create", startTime, traceBody)
	if err != nil {
		return nil, err
	}

	batch := []langfuseIngestionEvent{traceEvent}
	var prompts []telemetryPromptCall
	if len(promptCalls) > 0 {
		prompts = promptCalls[0]
	}
	if len(prompts) > 0 {
		for index, call := range prompts {
			usageEvent, err := newLangfuseEvent("generation-create", call.StartedAt, e.promptGenerationBody(episode, traceID, call, index))
			if err != nil {
				return nil, err
			}
			batch = append(batch, usageEvent)
		}
	} else if usageBody := e.traceUsageGenerationBody(episode, traceID, startTime, endTime); usageBody != nil {
		usageEvent, err := newLangfuseEvent("generation-create", startTime, usageBody)
		if err != nil {
			return nil, err
		}
		batch = append(batch, usageEvent)
	}
	if episode.Outcome.Success {
		scoreBody := map[string]interface{}{
			"id":       uuid.NewString(),
			"traceId":  traceID,
			"name":     "success",
			"value":    1,
			"dataType": "BOOLEAN",
		}
		scoreEvent, err := newLangfuseEvent("score-create", endTime, scoreBody)
		if err != nil {
			return nil, err
		}
		batch = append(batch, scoreEvent)
	}

	iterationSpanID := ""
	iterationIndex := 0
	parentByIteration := map[int]string{}

	for _, event := range episode.Events {
		eventTime := parseEpisodeTime(event.Ts, startTime)
		switch event.Type {
		case "planner_decision":
			iterationIndex++
			iterationSpanID = uuid.NewString()
			parentByIteration[iterationIndex] = iterationSpanID
			iterBody := map[string]interface{}{
				"id":        iterationSpanID,
				"traceId":   traceID,
				"name":      fmt.Sprintf("iteration_%d", iterationIndex),
				"startTime": langfuseRFC3339(eventTime),
				"endTime":   langfuseRFC3339(eventTime),
				"metadata": map[string]interface{}{
					"iteration": iterationIndex,
				},
			}
			iterEvent, err := newLangfuseEvent("span-create", eventTime, iterBody)
			if err != nil {
				return nil, err
			}
			batch = append(batch, iterEvent)

			plannerBody := map[string]interface{}{
				"id":                  uuid.NewString(),
				"traceId":             traceID,
				"parentObservationId": iterationSpanID,
				"name":                "planner",
				"startTime":           langfuseRFC3339(eventTime),
				"endTime":             langfuseRFC3339(eventTime),
				"input":               eventObjectiveInput(event),
				"output":              plannerOutput(event),
				"metadata": map[string]interface{}{
					"role":   event.Role,
					"reason": event.Reason,
				},
			}
			plannerEvent, err := newLangfuseEvent("span-create", eventTime, plannerBody)
			if err != nil {
				return nil, err
			}
			batch = append(batch, plannerEvent)

		case "candidate_answer":
			if iterationSpanID == "" {
				continue
			}
			body := map[string]interface{}{
				"id":                  uuid.NewString(),
				"traceId":             traceID,
				"parentObservationId": iterationSpanID,
				"name":                "candidate_answer",
				"startTime":           langfuseRFC3339(eventTime),
				"endTime":             langfuseRFC3339(eventTime),
				"output":              event.Content,
			}
			evt, err := newLangfuseEvent("event-create", eventTime, body)
			if err != nil {
				return nil, err
			}
			batch = append(batch, evt)

		case "tool_call":
			if iterationSpanID == "" {
				continue
			}
			toolName := strings.TrimSpace(event.ToolName)
			if toolName == "" {
				toolName = "tool"
			}
			body := map[string]interface{}{
				"id":                  uuid.NewString(),
				"traceId":             traceID,
				"parentObservationId": iterationSpanID,
				"name":                "tool/" + toolName,
				"startTime":           langfuseRFC3339(eventTime),
				"input":               toolCallInput(event),
				"metadata": map[string]interface{}{
					"tool_name":        event.ToolName,
					"tool_description": event.ToolDescription,
				},
			}
			toolEvent, err := newLangfuseEvent("span-create", eventTime, body)
			if err != nil {
				return nil, err
			}
			batch = append(batch, toolEvent)

		case "tool_result":
			resultSpanID := uuid.NewString()
			var output interface{} = event.Observation
			if e.cfg.UploadScreenshotsOrDefault() && strings.TrimSpace(event.ScreenshotRef) != "" {
				mediaRef, err := e.uploadScreenshot(ctx, traceID, resultSpanID, episodeDir, event.ScreenshotRef)
				if err != nil && e.logger != nil {
					e.logger.Warn("[telemetry] screenshot upload failed (%s): %v", event.ScreenshotRef, err)
				} else if mediaRef != "" {
					output = map[string]interface{}{
						"observation": event.Observation,
						"screenshot":  mediaRef,
					}
				}
			}
			body := map[string]interface{}{
				"id":        resultSpanID,
				"traceId":   traceID,
				"name":      "tool_result/" + strings.TrimSpace(event.ToolName),
				"startTime": langfuseRFC3339(eventTime),
				"endTime":   langfuseRFC3339(eventTime),
				"output":    output,
				"metadata": map[string]interface{}{
					"tool_name": event.ToolName,
					"is_error":  event.IsError,
				},
			}
			if iterationSpanID != "" {
				body["parentObservationId"] = iterationSpanID
			}
			if event.IsError {
				body["level"] = "ERROR"
			}
			resultEvent, err := newLangfuseEvent("span-create", eventTime, body)
			if err != nil {
				return nil, err
			}
			batch = append(batch, resultEvent)

		case "verifier_decision":
			parentID := iterationSpanID
			if parentID == "" {
				parentID = parentByIteration[iterationIndex]
			}
			body := map[string]interface{}{
				"id":                  uuid.NewString(),
				"traceId":             traceID,
				"parentObservationId": parentID,
				"name":                "verifier",
				"startTime":           langfuseRFC3339(eventTime),
				"endTime":             langfuseRFC3339(eventTime),
				"output":              verifierOutput(event),
				"metadata": map[string]interface{}{
					"can_finish":   event.CanFinish,
					"needs_replan": event.NeedsReplan,
					"reason":       event.Reason,
				},
			}
			verifierEvent, err := newLangfuseEvent("span-create", eventTime, body)
			if err != nil {
				return nil, err
			}
			batch = append(batch, verifierEvent)
			if event.CanFinish != nil && *event.CanFinish {
				iterationSpanID = ""
			}
		}
	}
	return batch, nil
}

func (e *EpisodeExporter) promptGenerationBody(episode TaskEpisode, traceID string, call telemetryPromptCall, index int) map[string]interface{} {
	name := strings.TrimSpace(call.Role)
	if name == "" {
		name = "llm"
	}
	if call.EndedAt.IsZero() {
		call.EndedAt = call.StartedAt
	}
	body := map[string]interface{}{
		"id":        call.ID,
		"traceId":   traceID,
		"name":      fmt.Sprintf("%s_prompt_%d", name, index+1),
		"startTime": langfuseRFC3339(call.StartedAt),
		"endTime":   langfuseRFC3339(call.EndedAt),
		"input":     call.Input,
		"output":    call.Output,
		"metadata": map[string]interface{}{
			"role":         call.Role,
			"prompt_index": index + 1,
		},
	}
	if call.ID == "" {
		body["id"] = uuid.NewString()
	}
	if len(call.UsageDetails) > 0 {
		body["usageDetails"] = call.UsageDetails
	}
	if call.Error != "" {
		body["level"] = "ERROR"
		body["statusMessage"] = call.Error
	}
	if model := extraString(episode.Extra, "model"); model != "" {
		body["model"] = model
	}
	return body
}

func (e *EpisodeExporter) traceUsageGenerationBody(episode TaskEpisode, traceID string, startTime, endTime time.Time) map[string]interface{} {
	promptTokens, completionTokens, totalTokens, ok := episodeTokenUsage(episode)
	if !ok {
		return nil
	}
	body := map[string]interface{}{
		"id":        uuid.NewString(),
		"traceId":   traceID,
		"name":      "aiden-run-usage",
		"startTime": langfuseRFC3339(startTime),
		"endTime":   langfuseRFC3339(endTime),
		"input":     episode.UserGoal,
		"output":    episode.Outcome.FinalAnswer,
		"usageDetails": map[string]interface{}{
			"input":  promptTokens,
			"output": completionTokens,
			"total":  totalTokens,
		},
		"metadata": map[string]interface{}{
			"usage_source": "episode.extra",
		},
	}
	if model := extraString(episode.Extra, "model"); model != "" {
		body["model"] = model
	}
	return body
}

func episodeTokenUsage(episode TaskEpisode) (promptTokens, completionTokens, totalTokens int, ok bool) {
	if episode.Extra == nil {
		return 0, 0, 0, false
	}
	if v, found := usageMetricInt(episode.Extra["prompt_tokens"]); found {
		promptTokens = v
	}
	if v, found := usageMetricInt(episode.Extra["completion_tokens"]); found {
		completionTokens = v
	}
	if v, found := usageMetricInt(episode.Extra["total_tokens"]); found {
		totalTokens = v
	}
	if totalTokens == 0 && (promptTokens > 0 || completionTokens > 0) {
		totalTokens = promptTokens + completionTokens
	}
	return promptTokens, completionTokens, totalTokens, promptTokens > 0 || completionTokens > 0 || totalTokens > 0
}

func (e *EpisodeExporter) uploadScreenshot(ctx context.Context, traceID, observationID, episodeDir, screenshotRef string) (string, error) {
	path := filepath.Join(episodeDir, filepath.FromSlash(screenshotRef))
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	contentType := screenshotContentType(path)
	mediaID, err := e.client.uploadMedia(ctx, traceID, observationID, contentType, data, "output")
	if err != nil {
		return "", err
	}
	return langfuseMediaToken(contentType, mediaID), nil
}

func (e *EpisodeExporter) traceTags(episode TaskEpisode) []string {
	tags := append([]string(nil), e.cfg.Tags...)
	tags = append(tags, episode.Tags...)
	if model := extraString(episode.Extra, "model"); model != "" {
		tags = append(tags, "model:"+model)
	}
	if episode.Outcome.Success {
		tags = append(tags, "success")
	} else {
		tags = append(tags, "failure")
	}
	return uniqueNonEmpty(tags)
}

func (e *EpisodeExporter) traceMetadata(episode TaskEpisode) map[string]interface{} {
	meta := map[string]interface{}{
		"episode_id":       episode.ID,
		"normalized_goal":  episode.NormalizedGoal,
		"device_scope":     episode.DeviceScope,
		"failure_reason":   episode.Outcome.FailureReason,
		"verifier_reason":  episode.Outcome.VerifierReason,
		"final_state":      episode.Outcome.FinalState,
		"entities":         episode.Entities,
		"reusable_lessons": episode.ReusableLessons,
		"failure_causes":   episode.FailureCauses,
	}
	if len(episode.RetrievedMemoryRefs) > 0 {
		meta["retrieved_memory_refs"] = episode.RetrievedMemoryRefs
	}
	if episode.Extra != nil {
		for key, value := range episode.Extra {
			meta[key] = value
		}
	}
	return meta
}

func traceReleaseFromEpisode(episode TaskEpisode) string {
	if v := extraString(episode.Extra, "agent_commit"); v != "" {
		return v
	}
	return extraString(episode.Extra, "firmware_version")
}

func traceVersionFromEpisode(episode TaskEpisode) string {
	if v := extraString(episode.Extra, "agent_build"); v != "" {
		return v
	}
	return extraString(episode.Extra, "firmware_version")
}

func extraString(extra map[string]interface{}, key string) string {
	if extra == nil {
		return ""
	}
	raw, ok := extra[key]
	if !ok || raw == nil {
		return ""
	}
	switch typed := raw.(type) {
	case string:
		return strings.TrimSpace(typed)
	default:
		return strings.TrimSpace(fmt.Sprint(typed))
	}
}

func newLangfuseEvent(eventType string, ts time.Time, body map[string]interface{}) (langfuseIngestionEvent, error) {
	raw, err := json.Marshal(body)
	if err != nil {
		return langfuseIngestionEvent{}, err
	}
	return langfuseIngestionEvent{
		ID:        uuid.NewString(),
		Timestamp: langfuseRFC3339(ts),
		Type:      eventType,
		Body:      raw,
	}, nil
}

func parseEpisodeTime(raw string, fallback time.Time) time.Time {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return fallback
	}
	if parsed, err := time.Parse(time.RFC3339Nano, raw); err == nil {
		return parsed.UTC()
	}
	if parsed, err := time.Parse(time.RFC3339, raw); err == nil {
		return parsed.UTC()
	}
	return fallback
}

func eventObjectiveInput(event TaskEpisodeEvent) map[string]interface{} {
	return map[string]interface{}{
		"objective":           event.Objective,
		"completion_criteria": event.CompletionCriteria,
		"observed_state":      event.ObservedState,
	}
}

func plannerOutput(event TaskEpisodeEvent) map[string]interface{} {
	return map[string]interface{}{
		"plan":      event.Plan,
		"next_step": event.NextStep,
		"reason":    event.Reason,
	}
}

func toolCallInput(event TaskEpisodeEvent) map[string]interface{} {
	input := map[string]interface{}{
		"tool_name": event.ToolName,
	}
	if strings.TrimSpace(event.ToolInput) != "" {
		input["tool_input"] = event.ToolInput
	}
	if strings.TrimSpace(event.ToolDescription) != "" {
		input["description"] = event.ToolDescription
	}
	return input
}

func verifierOutput(event TaskEpisodeEvent) map[string]interface{} {
	return map[string]interface{}{
		"content":        event.Content,
		"can_finish":     event.CanFinish,
		"needs_replan":   event.NeedsReplan,
		"reason":         event.Reason,
		"observed_state": event.ObservedState,
	}
}

func screenshotContentType(path string) string {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".png":
		return "image/png"
	case ".webp":
		return "image/webp"
	default:
		return "image/jpeg"
	}
}
