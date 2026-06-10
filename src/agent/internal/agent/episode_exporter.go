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

type langfuseIterationWindow struct {
	Index int
	ID    string
	Start time.Time
	End   time.Time
}

type langfuseToolPair struct {
	CallObservationID   string
	ResultObservationID string
	CallEventID         string
	ResultEventID       string
	ResultTime          time.Time
	HasCall             bool
	HasResult           bool
}

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
	stored = mergeEpisodeForExport(stored, episode)
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

func mergeEpisodeForExport(stored TaskEpisode, supplied TaskEpisode) TaskEpisode {
	if strings.TrimSpace(stored.ID) == "" {
		stored.ID = supplied.ID
	}
	if strings.TrimSpace(stored.Status) == "" {
		stored.Status = supplied.Status
	}
	if strings.TrimSpace(stored.StartedAt) == "" {
		stored.StartedAt = supplied.StartedAt
	}
	if strings.TrimSpace(stored.EndedAt) == "" {
		stored.EndedAt = supplied.EndedAt
	}
	if strings.TrimSpace(stored.UserGoal) == "" {
		stored.UserGoal = supplied.UserGoal
	}
	if len(stored.NormalizedGoal) == 0 {
		stored.NormalizedGoal = cloneStringMap(supplied.NormalizedGoal)
	}
	if len(stored.DeviceScope) == 0 {
		stored.DeviceScope = cloneStringMap(supplied.DeviceScope)
	}
	if len(stored.Tags) == 0 {
		stored.Tags = append([]string(nil), supplied.Tags...)
	} else if len(supplied.Tags) > 0 {
		stored.Tags = uniqueNonEmpty(append(stored.Tags, supplied.Tags...))
	}
	if len(stored.Entities) == 0 {
		stored.Entities = append([]string(nil), supplied.Entities...)
	} else if len(supplied.Entities) > 0 {
		stored.Entities = uniqueNonEmpty(append(stored.Entities, supplied.Entities...))
	}
	if len(stored.Events) == 0 {
		stored.Events = append([]TaskEpisodeEvent(nil), supplied.Events...)
	}
	if stored.Extra == nil && supplied.Extra != nil {
		stored.Extra = map[string]interface{}{}
	}
	for key, value := range supplied.Extra {
		stored.Extra[key] = value
	}
	return stored
}

func (e *EpisodeExporter) buildLangfuseBatch(ctx context.Context, episode TaskEpisode, episodeDir string, promptCalls ...[]telemetryPromptCall) ([]langfuseIngestionEvent, error) {
	traceID := episode.ID
	if _, err := uuid.Parse(traceID); err != nil {
		traceID = uuid.NewSHA1(uuid.NameSpaceURL, []byte(episode.ID)).String()
	}
	startTime := parseEpisodeTime(episode.StartedAt, time.Now().UTC())
	endTime := parseEpisodeTime(episode.EndedAt, startTime)
	version := traceVersionFromEpisode(episode)
	iterations := langfuseIterationWindows(episode.Events, startTime, endTime)
	toolPairsByCall, toolPairsByResult := langfuseToolPairs(episode.Events, startTime)

	traceBody := map[string]interface{}{
		"id":          traceID,
		"timestamp":   langfuseRFC3339(startTime),
		"name":        "aiden-episode",
		"input":       episode.UserGoal,
		"output":      episode.Outcome.FinalAnswer,
		"tags":        e.traceTags(episode),
		"metadata":    e.traceMetadata(episode),
		"environment": e.cfg.EnvironmentOrDefault(),
		"public":      false,
	}
	if userID := traceUserID(episode); userID != "" {
		traceBody["userId"] = userID
	}
	if sessionID := traceSessionID(episode); sessionID != "" {
		traceBody["sessionId"] = sessionID
	}
	if release := traceReleaseFromEpisode(episode); release != "" {
		traceBody["release"] = release
	}
	if version != "" {
		traceBody["version"] = version
	}
	traceEvent, err := newLangfuseEvent("trace-create", startTime, traceBody)
	if err != nil {
		return nil, err
	}

	batch := []langfuseIngestionEvent{traceEvent}

	iterationSpanID := ""
	iterationIndex := 0

	for eventIndex, event := range episode.Events {
		eventTime := parseEpisodeTime(event.Ts, startTime)
		switch event.Type {
		case "planner_decision":
			iterationIndex++
			iteration := iterationWindowForIndex(iterations, iterationIndex, eventTime)
			iterationSpanID = iteration.ID
			iterBody := map[string]interface{}{
				"id":          iterationSpanID,
				"traceId":     traceID,
				"name":        fmt.Sprintf("iteration_%d", iterationIndex),
				"startTime":   langfuseRFC3339(iteration.Start),
				"endTime":     langfuseRFC3339(iteration.End),
				"environment": e.cfg.EnvironmentOrDefault(),
				"metadata": map[string]interface{}{
					"iteration":  iterationIndex,
					"event_id":   event.EventID,
					"event_type": event.Type,
				},
			}
			if version != "" {
				iterBody["version"] = version
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
				"environment":         e.cfg.EnvironmentOrDefault(),
				"metadata": map[string]interface{}{
					"event_id": event.EventID,
					"role":     event.Role,
					"reason":   event.Reason,
				},
			}
			if version != "" {
				plannerBody["version"] = version
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
				"environment":         e.cfg.EnvironmentOrDefault(),
				"metadata": map[string]interface{}{
					"event_id": event.EventID,
					"role":     event.Role,
				},
			}
			if version != "" {
				body["version"] = version
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
			pair, paired := toolPairsByCall[eventIndex]
			toolSpanID := uuid.NewString()
			if paired && pair.CallObservationID != "" {
				toolSpanID = pair.CallObservationID
			}
			end := eventTime
			if paired && pair.HasResult {
				end = pair.ResultTime
			}
			metadata := map[string]interface{}{
				"event_id":         event.EventID,
				"tool_name":        event.ToolName,
				"tool_description": event.ToolDescription,
				"has_tool_result":  paired && pair.HasResult,
			}
			if paired && pair.ResultEventID != "" {
				metadata["result_event_id"] = pair.ResultEventID
				metadata["result_observation_id"] = pair.ResultObservationID
			}
			body := map[string]interface{}{
				"id":                  toolSpanID,
				"traceId":             traceID,
				"parentObservationId": iterationSpanID,
				"name":                "tool/" + toolName,
				"startTime":           langfuseRFC3339(eventTime),
				"endTime":             langfuseRFC3339(end),
				"input":               toolCallInput(event),
				"environment":         e.cfg.EnvironmentOrDefault(),
				"metadata":            metadata,
			}
			if version != "" {
				body["version"] = version
			}
			toolEvent, err := newLangfuseEvent("span-create", eventTime, body)
			if err != nil {
				return nil, err
			}
			batch = append(batch, toolEvent)

		case "tool_result":
			pair, paired := toolPairsByResult[eventIndex]
			resultSpanID := uuid.NewString()
			if paired && pair.ResultObservationID != "" {
				resultSpanID = pair.ResultObservationID
			}
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
			toolName := strings.TrimSpace(event.ToolName)
			if toolName == "" {
				toolName = "tool"
			}
			metadata := map[string]interface{}{
				"event_id":  event.EventID,
				"tool_name": event.ToolName,
				"is_error":  event.IsError,
			}
			if paired && pair.CallEventID != "" {
				metadata["tool_call_event_id"] = pair.CallEventID
				metadata["tool_call_observation_id"] = pair.CallObservationID
			}
			body := map[string]interface{}{
				"id":          resultSpanID,
				"traceId":     traceID,
				"name":        "tool_result/" + toolName,
				"startTime":   langfuseRFC3339(eventTime),
				"endTime":     langfuseRFC3339(eventTime),
				"output":      output,
				"environment": e.cfg.EnvironmentOrDefault(),
				"metadata":    metadata,
			}
			if version != "" {
				body["version"] = version
			}
			if paired && pair.CallObservationID != "" {
				body["parentObservationId"] = pair.CallObservationID
			} else if iterationSpanID != "" {
				body["parentObservationId"] = iterationSpanID
			}
			if event.IsError {
				body["level"] = "ERROR"
				body["statusMessage"] = truncateForLog(event.Observation, 500)
			}
			resultEvent, err := newLangfuseEvent("span-create", eventTime, body)
			if err != nil {
				return nil, err
			}
			batch = append(batch, resultEvent)

		case "verifier_decision":
			parentID := iterationSpanID
			body := map[string]interface{}{
				"id":          uuid.NewString(),
				"traceId":     traceID,
				"name":        "verifier",
				"startTime":   langfuseRFC3339(eventTime),
				"endTime":     langfuseRFC3339(eventTime),
				"output":      verifierOutput(event),
				"environment": e.cfg.EnvironmentOrDefault(),
				"metadata": map[string]interface{}{
					"event_id":     event.EventID,
					"can_finish":   event.CanFinish,
					"needs_replan": event.NeedsReplan,
					"reason":       event.Reason,
				},
			}
			if parentID != "" {
				body["parentObservationId"] = parentID
			}
			if version != "" {
				body["version"] = version
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

	var prompts []telemetryPromptCall
	if len(promptCalls) > 0 {
		prompts = promptCalls[0]
	}
	if len(prompts) > 0 {
		for index, call := range prompts {
			parentID := promptParentObservationID(call, iterations)
			usageEvent, err := newLangfuseEvent("generation-create", call.StartedAt, e.promptGenerationBody(episode, traceID, call, index, parentID))
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

	scoreBody := map[string]interface{}{
		"id":          uuid.NewString(),
		"traceId":     traceID,
		"name":        "success",
		"value":       0,
		"dataType":    "BOOLEAN",
		"environment": e.cfg.EnvironmentOrDefault(),
		"comment":     successScoreComment(episode),
		"metadata": map[string]interface{}{
			"failure_reason":  episode.Outcome.FailureReason,
			"verifier_reason": episode.Outcome.VerifierReason,
			"final_state":     episode.Outcome.FinalState,
		},
	}
	if episode.Outcome.Success {
		scoreBody["value"] = 1
	}
	scoreEvent, err := newLangfuseEvent("score-create", endTime, scoreBody)
	if err != nil {
		return nil, err
	}
	batch = append(batch, scoreEvent)
	return batch, nil
}

func (e *EpisodeExporter) promptGenerationBody(episode TaskEpisode, traceID string, call telemetryPromptCall, index int, parentObservationID string) map[string]interface{} {
	name := strings.TrimSpace(call.Role)
	if name == "" {
		name = "llm"
	}
	if call.EndedAt.IsZero() {
		call.EndedAt = call.StartedAt
	}
	body := map[string]interface{}{
		"id":          call.ID,
		"traceId":     traceID,
		"name":        fmt.Sprintf("%s_prompt_%d", name, index+1),
		"startTime":   langfuseRFC3339(call.StartedAt),
		"endTime":     langfuseRFC3339(call.EndedAt),
		"input":       call.Input,
		"output":      call.Output,
		"environment": e.cfg.EnvironmentOrDefault(),
		"metadata": map[string]interface{}{
			"role":         call.Role,
			"prompt_index": index + 1,
		},
	}
	if parentObservationID != "" {
		body["parentObservationId"] = parentObservationID
	}
	if call.ID == "" {
		body["id"] = uuid.NewString()
	}
	if len(call.UsageDetails) > 0 {
		body["usageDetails"] = call.UsageDetails
	}
	if len(call.CostDetails) > 0 {
		body["costDetails"] = call.CostDetails
	}
	if len(call.ModelParameters) > 0 {
		body["modelParameters"] = call.ModelParameters
	} else if params := episodeModelParameters(episode); len(params) > 0 {
		body["modelParameters"] = params
	}
	if call.Error != "" {
		body["level"] = "ERROR"
		body["statusMessage"] = call.Error
	}
	if model := extraString(episode.Extra, "model"); model != "" {
		body["model"] = model
	}
	if version := traceVersionFromEpisode(episode); version != "" {
		body["version"] = version
	}
	return body
}

func (e *EpisodeExporter) traceUsageGenerationBody(episode TaskEpisode, traceID string, startTime, endTime time.Time) map[string]interface{} {
	promptTokens, completionTokens, totalTokens, ok := episodeTokenUsage(episode)
	if !ok {
		return nil
	}
	body := map[string]interface{}{
		"id":          uuid.NewString(),
		"traceId":     traceID,
		"name":        "aiden-run-usage",
		"startTime":   langfuseRFC3339(startTime),
		"endTime":     langfuseRFC3339(endTime),
		"input":       episode.UserGoal,
		"output":      episode.Outcome.FinalAnswer,
		"environment": e.cfg.EnvironmentOrDefault(),
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
	if params := episodeModelParameters(episode); len(params) > 0 {
		body["modelParameters"] = params
	}
	if costs := episodeCostDetails(episode); len(costs) > 0 {
		body["costDetails"] = costs
	}
	if version := traceVersionFromEpisode(episode); version != "" {
		body["version"] = version
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

func langfuseIterationWindows(events []TaskEpisodeEvent, startTime, endTime time.Time) []langfuseIterationWindow {
	windows := []langfuseIterationWindow{}
	for _, event := range events {
		if event.Type != "planner_decision" {
			continue
		}
		eventTime := parseEpisodeTime(event.Ts, startTime)
		if len(windows) > 0 {
			windows[len(windows)-1].End = eventTime
		}
		windows = append(windows, langfuseIterationWindow{
			Index: len(windows) + 1,
			ID:    uuid.NewString(),
			Start: eventTime,
			End:   endTime,
		})
	}
	for i := range windows {
		if windows[i].End.IsZero() {
			windows[i].End = endTime
		}
		if windows[i].End.Before(windows[i].Start) {
			windows[i].End = windows[i].Start
		}
	}
	return windows
}

func iterationWindowForIndex(windows []langfuseIterationWindow, index int, fallback time.Time) langfuseIterationWindow {
	if index > 0 && index <= len(windows) {
		return windows[index-1]
	}
	return langfuseIterationWindow{Index: index, ID: uuid.NewString(), Start: fallback, End: fallback}
}

type pendingLangfuseToolCall struct {
	Index         int
	ToolName      string
	EventID       string
	ObservationID string
}

func langfuseToolPairs(events []TaskEpisodeEvent, startTime time.Time) (map[int]langfuseToolPair, map[int]langfuseToolPair) {
	byCall := map[int]langfuseToolPair{}
	byResult := map[int]langfuseToolPair{}
	pending := []pendingLangfuseToolCall{}
	for index, event := range events {
		switch event.Type {
		case "tool_call":
			pending = append(pending, pendingLangfuseToolCall{
				Index:         index,
				ToolName:      strings.TrimSpace(event.ToolName),
				EventID:       event.EventID,
				ObservationID: uuid.NewString(),
			})
		case "tool_result":
			resultID := uuid.NewString()
			matchIndex := -1
			toolName := strings.TrimSpace(event.ToolName)
			for i, call := range pending {
				if call.ToolName == toolName {
					matchIndex = i
					break
				}
			}
			if matchIndex < 0 && len(pending) > 0 {
				matchIndex = 0
			}
			if matchIndex >= 0 {
				call := pending[matchIndex]
				pending = append(pending[:matchIndex], pending[matchIndex+1:]...)
				pair := langfuseToolPair{
					CallObservationID:   call.ObservationID,
					ResultObservationID: resultID,
					CallEventID:         call.EventID,
					ResultEventID:       event.EventID,
					ResultTime:          parseEpisodeTime(event.Ts, startTime),
					HasCall:             true,
					HasResult:           true,
				}
				byCall[call.Index] = pair
				byResult[index] = pair
			} else {
				byResult[index] = langfuseToolPair{
					ResultObservationID: resultID,
					ResultEventID:       event.EventID,
					ResultTime:          parseEpisodeTime(event.Ts, startTime),
					HasResult:           true,
				}
			}
		}
	}
	for _, call := range pending {
		byCall[call.Index] = langfuseToolPair{
			CallObservationID: call.ObservationID,
			CallEventID:       call.EventID,
			HasCall:           true,
		}
	}
	return byCall, byResult
}

func promptParentObservationID(call telemetryPromptCall, windows []langfuseIterationWindow) string {
	if len(windows) == 0 {
		return ""
	}
	startedAt := call.StartedAt.UTC()
	endedAt := call.EndedAt.UTC()
	if endedAt.IsZero() {
		endedAt = startedAt
	}
	for _, window := range windows {
		if timeWithinWindow(startedAt, window) || timeWithinWindow(endedAt, window) {
			return window.ID
		}
	}
	best := windows[0]
	bestDistance := promptWindowDistance(endedAt, best)
	for _, window := range windows[1:] {
		if distance := promptWindowDistance(endedAt, window); distance < bestDistance {
			best = window
			bestDistance = distance
		}
	}
	return best.ID
}

func timeWithinWindow(ts time.Time, window langfuseIterationWindow) bool {
	if ts.IsZero() {
		return false
	}
	return (ts.Equal(window.Start) || ts.After(window.Start)) && (ts.Equal(window.End) || ts.Before(window.End))
}

func promptWindowDistance(ts time.Time, window langfuseIterationWindow) time.Duration {
	if ts.IsZero() {
		return 0
	}
	if timeWithinWindow(ts, window) {
		return 0
	}
	if ts.Before(window.Start) {
		return window.Start.Sub(ts)
	}
	return ts.Sub(window.End)
}

func successScoreComment(episode TaskEpisode) string {
	if episode.Outcome.Success {
		return firstNonEmptyString([]string{episode.Outcome.VerifierReason, "task completed"})
	}
	return firstNonEmptyString([]string{episode.Outcome.FailureReason, episode.Outcome.VerifierReason, "task failed"})
}

func traceUserID(episode TaskEpisode) string {
	if userID := extraString(episode.Extra, "user_id"); userID != "" {
		return userID
	}
	if episode.DeviceScope != nil {
		return strings.TrimSpace(episode.DeviceScope["device_id"])
	}
	return ""
}

func traceSessionID(episode TaskEpisode) string {
	if sessionID := extraString(episode.Extra, "session_id"); sessionID != "" {
		return sessionID
	}
	return extraString(episode.Extra, "telemetry_session_id")
}

func episodeModelParameters(episode TaskEpisode) map[string]interface{} {
	if episode.Extra == nil {
		return nil
	}
	if raw, ok := episode.Extra["model_parameters"]; ok {
		if params := normalizeModelParameters(raw); len(params) > 0 {
			return params
		}
	}
	params := map[string]interface{}{}
	if v, ok := costMetricFloat(episode.Extra["temperature"]); ok && v != 0 {
		params["temperature"] = v
	}
	if v, ok := usageMetricInt(episode.Extra["max_tokens"]); ok && v > 0 {
		params["max_tokens"] = v
	}
	if len(params) == 0 {
		return nil
	}
	return params
}

func normalizeModelParameters(raw interface{}) map[string]interface{} {
	switch typed := raw.(type) {
	case map[string]interface{}:
		return typed
	case map[string]string:
		out := make(map[string]interface{}, len(typed))
		for key, value := range typed {
			out[key] = value
		}
		return out
	default:
		return nil
	}
}

func episodeCostDetails(episode TaskEpisode) map[string]float64 {
	if episode.Extra == nil {
		return nil
	}
	if raw, ok := episode.Extra["cost_details"]; ok {
		if costs := normalizeCostDetails(raw); len(costs) > 0 {
			return costs
		}
	}
	return telemetryCostDetailsFromMap(episode.Extra)
}

func normalizeCostDetails(raw interface{}) map[string]float64 {
	switch typed := raw.(type) {
	case map[string]float64:
		return typed
	case map[string]interface{}:
		out := map[string]float64{}
		for key, value := range typed {
			if v, ok := costMetricFloat(value); ok {
				out[key] = v
			}
		}
		if len(out) == 0 {
			return nil
		}
		return out
	default:
		return nil
	}
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
	if status := strings.TrimSpace(episode.Status); status != "" {
		tags = append(tags, "status:"+status)
		if status == "interrupted" {
			tags = append(tags, "interrupted")
		}
	}
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
		"status":           episode.Status,
		"started_at":       episode.StartedAt,
		"ended_at":         episode.EndedAt,
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
	for key, value := range episodeDerivedMetrics(episode.Events) {
		meta[key] = value
	}
	if episode.Extra != nil {
		for key, value := range episode.Extra {
			meta[key] = value
		}
	}
	return meta
}

func episodeDerivedMetrics(events []TaskEpisodeEvent) map[string]interface{} {
	metrics := map[string]interface{}{}
	toolCounts := map[string]int{}
	var toolLatencies []int64
	var startTime time.Time
	if len(events) > 0 {
		startTime = parseEpisodeTime(events[0].Ts, time.Now().UTC())
	}
	toolPairsByCall, _ := langfuseToolPairs(events, startTime)
	for index, event := range events {
		switch event.Type {
		case "planner_decision":
			metrics["iteration_count"] = intMetric(metrics, "iteration_count") + 1
		case "candidate_answer":
			metrics["candidate_answer_count"] = intMetric(metrics, "candidate_answer_count") + 1
		case "tool_call":
			metrics["tool_call_count"] = intMetric(metrics, "tool_call_count") + 1
			toolName := strings.TrimSpace(event.ToolName)
			if toolName == "" {
				toolName = "tool"
			}
			toolCounts[toolName]++
			if pair, ok := toolPairsByCall[index]; ok && pair.HasResult {
				start := parseEpisodeTime(event.Ts, startTime)
				if !pair.ResultTime.Before(start) {
					toolLatencies = append(toolLatencies, pair.ResultTime.Sub(start).Milliseconds())
				}
			}
		case "tool_result":
			metrics["tool_result_count"] = intMetric(metrics, "tool_result_count") + 1
			if event.IsError {
				metrics["tool_error_count"] = intMetric(metrics, "tool_error_count") + 1
			}
			if strings.TrimSpace(event.ScreenshotRef) != "" {
				metrics["screenshot_count"] = intMetric(metrics, "screenshot_count") + 1
			}
		case "verifier_decision":
			metrics["verifier_decision_count"] = intMetric(metrics, "verifier_decision_count") + 1
			if event.NeedsReplan {
				metrics["replan_count"] = intMetric(metrics, "replan_count") + 1
			}
		}
	}
	if len(toolCounts) > 0 {
		metrics["tool_counts"] = toolCounts
	}
	if len(toolLatencies) > 0 {
		var total int64
		max := toolLatencies[0]
		for _, value := range toolLatencies {
			total += value
			if value > max {
				max = value
			}
		}
		metrics["tool_latency_ms_avg"] = float64(total) / float64(len(toolLatencies))
		metrics["tool_latency_ms_max"] = max
	}
	return metrics
}

func intMetric(values map[string]interface{}, key string) int {
	if raw, ok := values[key]; ok {
		if v, ok := raw.(int); ok {
			return v
		}
	}
	return 0
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
