package agent

import (
	"aiden-agent/internal/agent/langfuse"
	"aiden-agent/internal/util"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"gopkg.in/yaml.v3"
)

const (
	langfuseBatchSize          = 10
	langfuseTraceIngestReserve = 5 * time.Second
)

type langfuseClient = langfuse.Client
type langfuseIngestionEvent = langfuse.IngestionEvent

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
	ResultDurationMs    *int64
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
		cfg: cfg,
		client: langfuse.NewClient(langfuse.Config{
			BaseURL:       cfg.BaseURL,
			PublicKey:     cfg.PublicKey,
			SecretKey:     cfg.SecretKey,
			UploadTimeout: cfg.UploadTimeoutOrDefault(),
		}),
		logger: logger,
	}
}

func (e *EpisodeExporter) ExportEpisodeDir(ctx context.Context, episodeDir string, episode TaskEpisode, promptCalls ...[]telemetryPromptCall) error {
	if e == nil || !e.cfg.EnabledOrDefault() {
		return nil
	}
	if !e.client.Configured() {
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
			if err := e.client.Ingest(ctx, batch[i:end]); err != nil {
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

	var prompts []telemetryPromptCall
	if len(promptCalls) > 0 {
		prompts = promptCalls[0]
	}

	traceBody := map[string]interface{}{
		"id":          traceID,
		"timestamp":   langfuseRFC3339(startTime),
		"name":        "aiden-episode",
		"input":       episode.UserGoal,
		"output":      episode.Outcome.FinalAnswer,
		"tags":        e.traceTags(episode),
		"metadata":    e.traceMetadata(episode, prompts),
		"environment": e.cfg.EnvironmentOrDefault(),
		"public":      false,
	}
	if userID := traceUserID(episode); userID != "" {
		traceBody["userId"] = userID
	}
	if runtimeID := traceRuntimeID(episode); runtimeID != "" {
		traceBody["sessionId"] = runtimeID
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
	phaseSpanID := ""
	phaseSpanBatchIdx := -1
	phaseWindowIndex := 0
	currentPhase := "default"
	phaseWindows := langfusePhaseWindows(episode.Events, startTime, endTime)
	iterationSpansCreated := map[string]bool{}
	hasIterationTiming := langfuseHasIterationTimingEvents(episode.Events)

	openPhaseSpan := func(phase string, spanStart time.Time, spanEnd time.Time, metadata map[string]interface{}) error {
		if phaseSpanBatchIdx >= 0 {
			langfuseUpdateSpanEndTime(batch, phaseSpanBatchIdx, spanStart)
		}
		phase = strings.TrimSpace(phase)
		if phase == "" {
			phase = "unknown"
		}
		currentPhase = phase
		if phaseWindowIndex < len(phaseWindows) {
			phaseSpanID = phaseWindows[phaseWindowIndex].ID
			if spanEnd.IsZero() {
				spanEnd = phaseWindows[phaseWindowIndex].End
			}
		} else {
			phaseSpanID = uuid.NewString()
			if spanEnd.IsZero() {
				spanEnd = endTime
			}
		}
		body := map[string]interface{}{
			"id":          phaseSpanID,
			"traceId":     traceID,
			"name":        "phase/" + phase,
			"startTime":   langfuseRFC3339(spanStart),
			"endTime":     langfuseRFC3339(spanEnd),
			"environment": e.cfg.EnvironmentOrDefault(),
			"metadata":    metadata,
		}
		if version != "" {
			body["version"] = version
		}
		phaseEvent, err := newLangfuseEvent("span-create", spanStart, body)
		if err != nil {
			return err
		}
		batch = append(batch, phaseEvent)
		phaseSpanBatchIdx = len(batch) - 1
		phaseWindowIndex++
		return nil
	}
	if err := openPhaseSpan("default", startTime, time.Time{}, map[string]interface{}{
		"phase": "default",
	}); err != nil {
		return nil, err
	}
	openIterationSpan := func(iteration langfuseIterationWindow, metadata map[string]interface{}) error {
		if iteration.ID == "" || iterationSpansCreated[iteration.ID] {
			return nil
		}
		if metadata == nil {
			metadata = map[string]interface{}{}
		}
		metadata["iteration"] = iteration.Index
		body := map[string]interface{}{
			"id":          iteration.ID,
			"traceId":     traceID,
			"name":        fmt.Sprintf("iteration_%d", iteration.Index),
			"startTime":   langfuseRFC3339(iteration.Start),
			"endTime":     langfuseRFC3339(iteration.End),
			"environment": e.cfg.EnvironmentOrDefault(),
			"metadata":    metadata,
		}
		if phaseSpanID != "" {
			body["parentObservationId"] = phaseSpanID
		}
		if version != "" {
			body["version"] = version
		}
		iterationEvent, err := newLangfuseEvent("span-create", iteration.Start, body)
		if err != nil {
			return err
		}
		batch = append(batch, iterationEvent)
		iterationSpansCreated[iteration.ID] = true
		return nil
	}
	appendTimedVoiceSpan := func(event TaskEpisodeEvent, eventTime time.Time, spanName string) error {
		parentID := phaseSpanID
		duration := int64(0)
		if event.DurationMs != nil && *event.DurationMs >= 0 {
			duration = *event.DurationMs
		}
		metadata := map[string]interface{}{
			"event_id": event.EventID,
			"role":     event.Role,
			"phase":    currentPhase,
		}
		for key, value := range event.Metadata {
			metadata[key] = value
		}
		body := map[string]interface{}{
			"id":                  uuid.NewString(),
			"traceId":             traceID,
			"parentObservationId": parentID,
			"name":                spanName,
			"startTime":           langfuseRFC3339(eventTime),
			"endTime":             langfuseRFC3339(eventTime.Add(time.Duration(duration) * time.Millisecond)),
			"output":              event.Content,
			"environment":         e.cfg.EnvironmentOrDefault(),
			"metadata":            metadata,
		}
		if strings.TrimSpace(event.Reason) != "" {
			body["statusMessage"] = event.Reason
		}
		if event.IsError {
			body["level"] = "ERROR"
		}
		if version != "" {
			body["version"] = version
		}
		evt, err := newLangfuseEvent("span-create", eventTime, body)
		if err != nil {
			return err
		}
		batch = append(batch, evt)
		return nil
	}
	appendSTTDetailSpans := func(event TaskEpisodeEvent, eventTime time.Time, parentDurationMs int64, parentID string) error {
		if parentID == "" {
			return nil
		}
		parentEnd := eventTime.Add(time.Duration(parentDurationMs) * time.Millisecond)
		if parentEnd.Before(eventTime) {
			parentEnd = eventTime
		}
		oneShotMS, hasOneShotMS := metadataDurationMS(event.Metadata, "one_shot_ms")
		oneShotErr := metadataString(event.Metadata, "one_shot_error")
		oneShotStart := parentEnd
		if hasOneShotMS {
			oneShotStart = parentEnd.Add(-time.Duration(oneShotMS) * time.Millisecond)
			if oneShotStart.Before(eventTime) {
				oneShotStart = eventTime
			}
		}
		finalizeMS, hasFinalizeMS := metadataDurationMS(event.Metadata, "streaming_finalize_ms")
		finalizeErr := metadataString(event.Metadata, "streaming_finalize_error")
		finalizeStart := oneShotStart
		if !hasOneShotMS {
			finalizeStart = parentEnd
		}
		if hasFinalizeMS {
			finalizeStart = finalizeStart.Add(-time.Duration(finalizeMS) * time.Millisecond)
			if finalizeStart.Before(eventTime) {
				finalizeStart = eventTime
			}
		}
		audioCaptureEnd := finalizeStart
		if !hasFinalizeMS && !hasOneShotMS {
			audioCaptureEnd = parentEnd
		}

		appendChild := func(name string, spanStart, spanEnd time.Time, extra map[string]interface{}, statusMessage string) error {
			if spanEnd.Before(spanStart) {
				spanEnd = spanStart
			}
			metadata := map[string]interface{}{
				"event_id": event.EventID,
				"role":     event.Role,
				"phase":    currentPhase,
			}
			for key, value := range event.Metadata {
				metadata[key] = value
			}
			for key, value := range extra {
				metadata[key] = value
			}
			body := map[string]interface{}{
				"id":                  uuid.NewString(),
				"traceId":             traceID,
				"parentObservationId": parentID,
				"name":                name,
				"startTime":           langfuseRFC3339(spanStart),
				"endTime":             langfuseRFC3339(spanEnd),
				"environment":         e.cfg.EnvironmentOrDefault(),
				"metadata":            metadata,
			}
			if event.Content != "" {
				body["output"] = event.Content
			}
			if statusMessage != "" {
				body["level"] = "ERROR"
				body["statusMessage"] = statusMessage
			}
			if version != "" {
				body["version"] = version
			}
			evt, err := newLangfuseEvent("span-create", spanStart, body)
			if err != nil {
				return err
			}
			batch = append(batch, evt)
			return nil
		}

		if audioDurationMS, ok := metadataDurationMS(event.Metadata, "audio_duration_ms"); ok {
			spanEnd := audioCaptureEnd
			if spanEnd.Before(eventTime) || spanEnd.After(parentEnd) {
				spanEnd = parentEnd
			}
			spanStart := spanEnd.Add(-time.Duration(audioDurationMS) * time.Millisecond)
			if spanStart.Before(eventTime) {
				spanStart = eventTime
			}
			if spanStart.After(eventTime) {
				residualMS := spanStart.Sub(eventTime).Milliseconds()
				if err := appendChild("stt/listening_overhead", eventTime, spanStart, map[string]interface{}{
					"step":        "listening_overhead",
					"duration_ms": residualMS,
					"estimated":   true,
				}, ""); err != nil {
					return err
				}
			}
			if err := appendChild("stt/audio_capture", spanStart, spanEnd, map[string]interface{}{
				"step":        "audio_capture",
				"duration_ms": audioDurationMS,
			}, ""); err != nil {
				return err
			}
		}

		if readyMS, ok := metadataDurationMS(event.Metadata, "streaming_ready_ms"); ok || metadataString(event.Metadata, "streaming_unavailable_error") != "" {
			spanEnd := eventTime
			if ok {
				spanEnd = eventTime.Add(time.Duration(readyMS) * time.Millisecond)
				if spanEnd.After(parentEnd) {
					spanEnd = parentEnd
				}
			}
			if err := appendChild("stt/streaming_setup", eventTime, spanEnd, map[string]interface{}{
				"step":        "streaming_setup",
				"duration_ms": readyMS,
			}, metadataString(event.Metadata, "streaming_unavailable_error")); err != nil {
				return err
			}
		}

		if hasFinalizeMS || finalizeErr != "" {
			spanEnd := parentEnd
			if hasOneShotMS {
				spanEnd = oneShotStart
			}
			spanStart := spanEnd
			if hasFinalizeMS {
				spanStart = spanEnd.Add(-time.Duration(finalizeMS) * time.Millisecond)
				if spanStart.Before(eventTime) {
					spanStart = eventTime
				}
			}
			if err := appendChild("stt/streaming_finalize", spanStart, spanEnd, map[string]interface{}{
				"step":        "streaming_finalize",
				"duration_ms": finalizeMS,
			}, finalizeErr); err != nil {
				return err
			}
		}

		if hasOneShotMS || oneShotErr != "" {
			spanStart := oneShotStart
			if !hasOneShotMS {
				spanStart = parentEnd
			}
			if err := appendChild("stt/one_shot", spanStart, parentEnd, map[string]interface{}{
				"step":        "one_shot",
				"duration_ms": oneShotMS,
			}, oneShotErr); err != nil {
				return err
			}
		}

		if uploadErr := metadataString(event.Metadata, "streaming_upload_error"); uploadErr != "" {
			if err := appendChild("stt/streaming_upload", eventTime, eventTime, map[string]interface{}{
				"step": "streaming_upload",
			}, uploadErr); err != nil {
				return err
			}
		}

		return nil
	}

	for eventIndex, event := range episode.Events {
		eventTime := parseEpisodeTime(event.Ts, startTime)
		switch event.Type {
		case "loop_phase":
			if err := openPhaseSpan(event.Content, eventTime, time.Time{}, map[string]interface{}{
				"phase":      strings.TrimSpace(event.Content),
				"reason":     strings.TrimSpace(event.Reason),
				"event_id":   event.EventID,
				"event_type": event.Type,
				"role":       event.Role,
			}); err != nil {
				return nil, err
			}

		case "default_finish":
			parentID := iterationSpanID
			if parentID == "" {
				parentID = phaseSpanID
			}
			body := map[string]interface{}{
				"id":          uuid.NewString(),
				"traceId":     traceID,
				"name":        "agent/default_finish",
				"startTime":   langfuseRFC3339(eventTime),
				"endTime":     langfuseRFC3339(eventTime),
				"output":      event.Content,
				"environment": e.cfg.EnvironmentOrDefault(),
				"metadata": map[string]interface{}{
					"event_id": event.EventID,
					"role":     event.Role,
					"phase":    currentPhase,
				},
			}
			if parentID != "" {
				body["parentObservationId"] = parentID
			}
			if version != "" {
				body["version"] = version
			}
			finishEvent, err := newLangfuseEvent("span-create", eventTime, body)
			if err != nil {
				return nil, err
			}
			batch = append(batch, finishEvent)

		case runEventMemoryRetrieve:
			parentID := phaseSpanID
			duration := int64(0)
			if event.DurationMs != nil {
				duration = *event.DurationMs
			}
			body := map[string]interface{}{
				"id":                  uuid.NewString(),
				"traceId":             traceID,
				"parentObservationId": parentID,
				"name":                "memory/retrieve",
				"startTime":           langfuseRFC3339(eventTime),
				"endTime":             langfuseRFC3339(eventTime.Add(time.Duration(duration) * time.Millisecond)),
				"environment":         e.cfg.EnvironmentOrDefault(),
				"metadata":            event.Metadata,
			}
			if version != "" {
				body["version"] = version
			}
			evt, err := newLangfuseEvent("span-create", eventTime, body)
			if err != nil {
				return nil, err
			}
			batch = append(batch, evt)

		case runEventSessionBegin:
			parentID := phaseSpanID
			duration := int64(0)
			if event.DurationMs != nil {
				duration = *event.DurationMs
			}
			body := map[string]interface{}{
				"id":                  uuid.NewString(),
				"traceId":             traceID,
				"parentObservationId": parentID,
				"name":                "session/begin",
				"startTime":           langfuseRFC3339(eventTime),
				"endTime":             langfuseRFC3339(eventTime.Add(time.Duration(duration) * time.Millisecond)),
				"environment":         e.cfg.EnvironmentOrDefault(),
				"metadata":            event.Metadata,
			}
			if version != "" {
				body["version"] = version
			}
			evt, err := newLangfuseEvent("span-create", eventTime, body)
			if err != nil {
				return nil, err
			}
			batch = append(batch, evt)

		case runEventSTTTranscription:
			parentID := phaseSpanID
			duration := int64(0)
			if event.DurationMs != nil && *event.DurationMs >= 0 {
				duration = *event.DurationMs
			}
			metadata := map[string]interface{}{
				"event_id": event.EventID,
				"role":     event.Role,
				"phase":    currentPhase,
			}
			for key, value := range event.Metadata {
				metadata[key] = value
			}
			sttSpanID := uuid.NewString()
			body := map[string]interface{}{
				"id":                  sttSpanID,
				"traceId":             traceID,
				"parentObservationId": parentID,
				"name":                "stt/transcription",
				"startTime":           langfuseRFC3339(eventTime),
				"endTime":             langfuseRFC3339(eventTime.Add(time.Duration(duration) * time.Millisecond)),
				"output":              event.Content,
				"environment":         e.cfg.EnvironmentOrDefault(),
				"metadata":            metadata,
			}
			if strings.TrimSpace(event.Reason) != "" {
				body["statusMessage"] = event.Reason
			}
			if event.IsError {
				body["level"] = "ERROR"
			}
			if version != "" {
				body["version"] = version
			}
			sttEvent, err := newLangfuseEvent("span-create", eventTime, body)
			if err != nil {
				return nil, err
			}
			batch = append(batch, sttEvent)
			if err := appendSTTDetailSpans(event, eventTime, duration, sttSpanID); err != nil {
				return nil, err
			}

		case runEventVoicePromptSound:
			if err := appendTimedVoiceSpan(event, eventTime, "voice/prompt_sound_agent_send"); err != nil {
				return nil, err
			}

		case runEventTTSStreamPreopen:
			if err := appendTimedVoiceSpan(event, eventTime, "voice/preopen_tts_stream"); err != nil {
				return nil, err
			}

		case runEventIterationStart:
			iteration, ok := iterationWindowForEvent(iterations, eventTime)
			if !ok {
				continue
			}
			iterationSpanID = iteration.ID
			iterationIndex = iteration.Index
			metadata := map[string]interface{}{
				"event_id":   event.EventID,
				"event_type": event.Type,
			}
			for key, value := range event.Metadata {
				metadata[key] = value
			}
			if err := openIterationSpan(iteration, metadata); err != nil {
				return nil, err
			}

		case runEventIterationEnd:
			if iteration, ok := iterationWindowForEvent(iterations, eventTime); ok && iteration.ID == iterationSpanID {
				iterationSpanID = ""
			}

		case "planner_decision":
			if hasIterationTiming {
				iteration, ok := iterationWindowForEvent(iterations, eventTime)
				if !ok {
					iteration = iterationWindowForIndex(iterations, iterationIndex, eventTime)
				}
				iterationSpanID = iteration.ID
				iterationIndex = iteration.Index
				if err := openIterationSpan(iteration, map[string]interface{}{
					"event_id":   event.EventID,
					"event_type": event.Type,
				}); err != nil {
					return nil, err
				}
			} else {
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
			}

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

		case runEventToolCall:
			parentID := langfuseToolParentSpan(iterationSpanID, phaseSpanID)
			if parentID == "" {
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
			spanStart := eventTime
			end := eventTime
			if paired && pair.HasResult {
				if pair.ResultDurationMs != nil && *pair.ResultDurationMs >= 0 {
					end = pair.ResultTime
					spanStart = end.Add(-time.Duration(*pair.ResultDurationMs) * time.Millisecond)
				} else {
					end = pair.ResultTime
				}
			}
			metadata := map[string]interface{}{
				"event_id":        event.EventID,
				"tool_name":       event.ToolName,
				"has_tool_result": paired && pair.HasResult,
				"role":            event.Role,
				"phase":           currentPhase,
			}
			if paired && pair.ResultEventID != "" {
				metadata["result_event_id"] = pair.ResultEventID
				metadata["result_observation_id"] = pair.ResultObservationID
			}
			if paired && pair.ResultDurationMs != nil {
				metadata["duration_ms"] = *pair.ResultDurationMs
			}
			body := map[string]interface{}{
				"id":                  toolSpanID,
				"traceId":             traceID,
				"parentObservationId": parentID,
				"name":                langfuseToolSpanName(toolName),
				"startTime":           langfuseRFC3339(spanStart),
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
			var output interface{} = event.Content
			if e.cfg.UploadScreenshotsOrDefault() && strings.TrimSpace(event.ScreenshotRef) != "" {
				screenshotCtx, cancel, ok := langfuseScreenshotUploadContext(ctx, e.cfg.UploadTimeoutOrDefault())
				if ok {
					mediaRef, err := e.uploadScreenshot(screenshotCtx, traceID, resultSpanID, episodeDir, event.ScreenshotRef)
					cancel()
					if err != nil && e.logger != nil {
						e.logger.Warn("[telemetry] screenshot upload failed (%s): %v", event.ScreenshotRef, err)
					} else if mediaRef != "" {
						output = map[string]interface{}{
							"observation": event.Content,
							"screenshot":  mediaRef,
						}
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
				"role":      event.Role,
				"phase":     currentPhase,
			}
			if paired && pair.CallEventID != "" {
				metadata["tool_call_event_id"] = pair.CallEventID
				metadata["tool_call_observation_id"] = pair.CallObservationID
			}
			if event.DurationMs != nil {
				metadata["duration_ms"] = *event.DurationMs
			}
			body := map[string]interface{}{
				"id":          resultSpanID,
				"traceId":     traceID,
				"name":        langfuseToolResultSpanName(toolName),
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
			} else if parentID := langfuseToolParentSpan(iterationSpanID, phaseSpanID); parentID != "" {
				body["parentObservationId"] = parentID
			}
			if event.IsError {
				body["level"] = "ERROR"
				body["statusMessage"] = truncateForLog(event.Content, 500)
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
	if phaseSpanBatchIdx >= 0 {
		langfuseUpdateSpanEndTime(batch, phaseSpanBatchIdx, endTime)
	}

	if len(prompts) > 0 {
		for index := range prompts {
			if prompts[index].ID == "" {
				prompts[index].ID = uuid.NewString()
			}
			prompts[index] = e.uploadPromptMedia(ctx, traceID, prompts[index])
			call := prompts[index]
			parentID := promptParentObservationID(call, iterations, phaseWindows)
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

func (e *EpisodeExporter) uploadPromptMedia(ctx context.Context, traceID string, call telemetryPromptCall) telemetryPromptCall {
	for _, media := range call.Media {
		if !e.cfg.UploadScreenshotsOrDefault() {
			replaceTelemetryMediaPlaceholder(call.Input, media.Placeholder, "[media omitted: upload disabled]")
			continue
		}
		replacement := "[media omitted: upload unavailable]"
		uploadCtx, cancel, ok := langfuseScreenshotUploadContext(ctx, e.cfg.UploadTimeoutOrDefault())
		if ok {
			mediaID, err := e.client.UploadMedia(uploadCtx, traceID, call.ID, media.ContentType, media.Data, "input")
			cancel()
			if err != nil {
				if e.logger != nil {
					e.logger.Warn("[telemetry] prompt media upload failed (%s, %d bytes): %v", media.ContentType, len(media.Data), err)
				}
			} else if mediaID != "" {
				replacement = langfuse.MediaToken(media.ContentType, mediaID)
			}
		}
		replaceTelemetryMediaPlaceholder(call.Input, media.Placeholder, replacement)
	}
	call.Media = nil
	return call
}

func replaceTelemetryMediaPlaceholder(value interface{}, placeholder, replacement string) {
	switch typed := value.(type) {
	case []map[string]interface{}:
		for _, item := range typed {
			replaceTelemetryMediaPlaceholder(item, placeholder, replacement)
		}
	case []interface{}:
		for _, item := range typed {
			replaceTelemetryMediaPlaceholder(item, placeholder, replacement)
		}
	case map[string]interface{}:
		for key, item := range typed {
			if text, ok := item.(string); ok && text == placeholder {
				typed[key] = replacement
				continue
			}
			replaceTelemetryMediaPlaceholder(item, placeholder, replacement)
		}
	}
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
	if len(call.Metadata) > 0 {
		if metadata, ok := body["metadata"].(map[string]interface{}); ok {
			for key, value := range call.Metadata {
				metadata[key] = value
			}
		}
	}
	if parentObservationID != "" {
		body["parentObservationId"] = parentObservationID
	}
	if call.ID == "" {
		body["id"] = uuid.NewString()
	}
	if len(call.UsageDetails) > 0 {
		body["usageDetails"] = call.UsageDetails

		// Add cache hit rate to metadata if we have cached tokens
		if inputTokens, hasInput := call.UsageDetails["input"]; hasInput && inputTokens > 0 {
			if cachedTokens, hasCached := call.UsageDetails["cached"]; hasCached && cachedTokens > 0 {
				if metadata, ok := body["metadata"].(map[string]interface{}); ok {
					metadata["cache_hit_rate"] = float64(cachedTokens) / float64(inputTokens)
				}
			}
		}
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

func langfuseScreenshotUploadContext(ctx context.Context, timeout time.Duration) (context.Context, context.CancelFunc, bool) {
	if ctx == nil {
		ctx = context.Background()
	}
	if timeout <= 0 {
		return nil, nil, false
	}
	if deadline, ok := ctx.Deadline(); ok {
		remaining := time.Until(deadline)
		if remaining <= langfuseTraceIngestReserve {
			return nil, nil, false
		}
		if available := remaining - langfuseTraceIngestReserve; available < timeout {
			timeout = available
		}
	}
	if timeout <= 0 {
		return nil, nil, false
	}
	screenshotCtx, cancel := context.WithTimeout(ctx, timeout)
	return screenshotCtx, cancel, true
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
	if v, found := util.UsageMetricInt(episode.Extra["prompt_tokens"]); found {
		promptTokens = v
	}
	if v, found := util.UsageMetricInt(episode.Extra["completion_tokens"]); found {
		completionTokens = v
	}
	if v, found := util.UsageMetricInt(episode.Extra["total_tokens"]); found {
		totalTokens = v
	}
	if totalTokens == 0 && (promptTokens > 0 || completionTokens > 0) {
		totalTokens = promptTokens + completionTokens
	}
	return promptTokens, completionTokens, totalTokens, promptTokens > 0 || completionTokens > 0 || totalTokens > 0
}

func langfuseToolParentSpan(iterationSpanID, phaseSpanID string) string {
	if iterationSpanID != "" {
		return iterationSpanID
	}
	if phaseSpanID != "" {
		return phaseSpanID
	}
	return iterationSpanID
}

func langfuseToolSpanName(toolName string) string {
	return "tool/" + toolName
}

func langfuseToolResultSpanName(toolName string) string {
	return "tool_result/" + toolName
}

func langfuseUpdateSpanEndTime(batch []langfuseIngestionEvent, index int, endTime time.Time) {
	if index < 0 || index >= len(batch) {
		return
	}
	var body map[string]interface{}
	if err := json.Unmarshal(batch[index].Body, &body); err != nil {
		return
	}
	body["endTime"] = langfuseRFC3339(endTime)
	if raw, err := json.Marshal(body); err == nil {
		batch[index].Body = raw
	}
}

func metadataDurationMS(metadata map[string]interface{}, key string) (int64, bool) {
	if len(metadata) == 0 {
		return 0, false
	}
	value, ok := metadata[key]
	if !ok || value == nil {
		return 0, false
	}
	var duration int64
	switch v := value.(type) {
	case int:
		duration = int64(v)
	case int8:
		duration = int64(v)
	case int16:
		duration = int64(v)
	case int32:
		duration = int64(v)
	case int64:
		duration = v
	case uint:
		duration = int64(v)
	case uint8:
		duration = int64(v)
	case uint16:
		duration = int64(v)
	case uint32:
		duration = int64(v)
	case uint64:
		if v > uint64(1<<63-1) {
			return 0, false
		}
		duration = int64(v)
	case float32:
		duration = int64(v)
	case float64:
		duration = int64(v)
	case json.Number:
		parsed, err := v.Int64()
		if err != nil {
			floatValue, floatErr := v.Float64()
			if floatErr != nil {
				return 0, false
			}
			parsed = int64(floatValue)
		}
		duration = parsed
	case string:
		trimmed := strings.TrimSpace(v)
		if trimmed == "" {
			return 0, false
		}
		parsed, err := strconv.ParseInt(trimmed, 10, 64)
		if err != nil {
			floatValue, floatErr := strconv.ParseFloat(trimmed, 64)
			if floatErr != nil {
				return 0, false
			}
			parsed = int64(floatValue)
		}
		duration = parsed
	default:
		return 0, false
	}
	if duration < 0 {
		return 0, false
	}
	return duration, true
}

func metadataString(metadata map[string]interface{}, key string) string {
	if len(metadata) == 0 {
		return ""
	}
	value, ok := metadata[key]
	if !ok || value == nil {
		return ""
	}
	if s, ok := value.(string); ok {
		return strings.TrimSpace(s)
	}
	return strings.TrimSpace(fmt.Sprint(value))
}

func langfusePhaseWindows(events []TaskEpisodeEvent, startTime, endTime time.Time) []langfuseIterationWindow {
	windows := []langfuseIterationWindow{{
		Index: 1,
		ID:    uuid.NewString(),
		Start: startTime,
		End:   endTime,
	}}
	for _, event := range events {
		if event.Type != "loop_phase" {
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

func langfuseIterationWindows(events []TaskEpisodeEvent, startTime, endTime time.Time) []langfuseIterationWindow {
	if langfuseHasIterationStartEvents(events) {
		return langfuseIterationTimingWindows(events, startTime, endTime)
	}
	return langfusePlannerIterationWindows(events, startTime, endTime)
}

func langfusePlannerIterationWindows(events []TaskEpisodeEvent, startTime, endTime time.Time) []langfuseIterationWindow {
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

func langfuseIterationTimingWindows(events []TaskEpisodeEvent, startTime, endTime time.Time) []langfuseIterationWindow {
	windows := []langfuseIterationWindow{}
	for _, event := range events {
		eventTime := parseEpisodeTime(event.Ts, startTime)
		switch event.Type {
		case runEventIterationStart:
			if len(windows) > 0 && windows[len(windows)-1].End.IsZero() {
				windows[len(windows)-1].End = eventTime
			}
			windows = append(windows, langfuseIterationWindow{
				Index: taskEpisodeEventIterationIndex(event, len(windows)+1),
				ID:    uuid.NewString(),
				Start: eventTime,
			})
		case runEventIterationEnd:
			if len(windows) == 0 {
				continue
			}
			match := -1
			if index := taskEpisodeEventIterationIndex(event, 0); index > 0 {
				for i := len(windows) - 1; i >= 0; i-- {
					if windows[i].Index == index {
						match = i
						break
					}
				}
			}
			if match < 0 {
				match = len(windows) - 1
			}
			windows[match].End = eventTime
		}
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

func langfuseHasIterationTimingEvents(events []TaskEpisodeEvent) bool {
	for _, event := range events {
		if event.Type == runEventIterationStart || event.Type == runEventIterationEnd {
			return true
		}
	}
	return false
}

func langfuseHasIterationStartEvents(events []TaskEpisodeEvent) bool {
	for _, event := range events {
		if event.Type == runEventIterationStart {
			return true
		}
	}
	return false
}

func iterationWindowForIndex(windows []langfuseIterationWindow, index int, fallback time.Time) langfuseIterationWindow {
	if index > 0 && index <= len(windows) {
		return windows[index-1]
	}
	return langfuseIterationWindow{Index: index, ID: uuid.NewString(), Start: fallback, End: fallback}
}

func iterationWindowForEvent(windows []langfuseIterationWindow, eventTime time.Time) (langfuseIterationWindow, bool) {
	for i := len(windows) - 1; i >= 0; i-- {
		window := windows[i]
		if timeWithinWindow(eventTime, window) {
			return window, true
		}
	}
	return langfuseIterationWindow{}, false
}

func iterationWindowForEventMetadata(windows []langfuseIterationWindow, event TaskEpisodeEvent, eventTime time.Time) (langfuseIterationWindow, bool) {
	if index := taskEpisodeEventIterationIndex(event, 0); index > 0 {
		for _, window := range windows {
			if window.Index == index {
				return window, true
			}
		}
	}
	return iterationWindowForEvent(windows, eventTime)
}

func taskEpisodeEventIterationIndex(event TaskEpisodeEvent, fallback int) int {
	if event.Metadata != nil {
		if value, ok := util.UsageMetricInt(event.Metadata["iteration"]); ok && value > 0 {
			return value
		}
	}
	return fallback
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
		case runEventToolCall:
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
					ResultDurationMs:    cloneInt64Ptr(event.DurationMs),
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
					ResultDurationMs:    cloneInt64Ptr(event.DurationMs),
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

func cloneInt64Ptr(value *int64) *int64 {
	if value == nil {
		return nil
	}
	cloned := *value
	return &cloned
}

func promptParentObservationID(call telemetryPromptCall, iterations, phases []langfuseIterationWindow) string {
	if id := promptParentFromWindows(call, iterations); id != "" {
		return id
	}
	return promptParentFromWindows(call, phases)
}

func promptParentFromWindows(call telemetryPromptCall, windows []langfuseIterationWindow) string {
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

func traceRuntimeID(episode TaskEpisode) string {
	if runtimeID := extraString(episode.Extra, "runtime_id"); runtimeID != "" {
		return runtimeID
	}
	if legacySessionID := extraString(episode.Extra, "session_id"); legacySessionID != "" {
		return legacySessionID
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
	if v, ok := util.UsageMetricInt(episode.Extra["max_tokens"]); ok && v > 0 {
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
	mediaID, err := e.client.UploadMedia(ctx, traceID, observationID, contentType, data, "output")
	if err != nil {
		return "", err
	}
	return langfuse.MediaToken(contentType, mediaID), nil
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
	tags = append(tags, episodeLoopTags(episode.Events)...)
	return uniqueNonEmpty(tags)
}

func (e *EpisodeExporter) traceMetadata(episode TaskEpisode, prompts []telemetryPromptCall) map[string]interface{} {
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

	// Add LLM call statistics by role
	if len(prompts) > 0 {
		byRole := make(map[string][]int64)
		for _, call := range prompts {
			role := strings.TrimSpace(call.Role)
			if role == "" {
				role = "unknown"
			}
			duration := call.EndedAt.Sub(call.StartedAt).Milliseconds()
			byRole[role] = append(byRole[role], duration)
		}

		for role, durations := range byRole {
			if len(durations) == 0 {
				continue
			}
			sorted := make([]int64, len(durations))
			copy(sorted, durations)
			sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })

			meta[role+"_call_count"] = len(durations)
			meta[role+"_call_ms_avg"] = avgInt64(durations)
			meta[role+"_call_ms_p50"] = percentileInt64(sorted, 0.5)
			meta[role+"_call_ms_p95"] = percentileInt64(sorted, 0.95)
		}
	}

	// Add prompt cache hit rate from episode.Extra
	if episode.Extra != nil {
		if promptTokens, ok := util.UsageMetricInt(episode.Extra["prompt_tokens"]); ok && promptTokens > 0 {
			if cachedTokens, ok := util.UsageMetricInt(episode.Extra["cached_prompt_tokens"]); ok && cachedTokens > 0 {
				meta["prompt_cache_hit_rate"] = float64(cachedTokens) / float64(promptTokens)
				meta["cached_prompt_tokens"] = cachedTokens
			}
		}
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
	phaseTransitions := []string{}
	finalPhase := "default"
	for index, event := range events {
		switch event.Type {
		case "loop_phase":
			metrics["loop_phase_count"] = intMetric(metrics, "loop_phase_count") + 1
			phase := strings.TrimSpace(event.Content)
			reason := strings.TrimSpace(event.Reason)
			if phase != "" {
				finalPhase = phase
				if reason != "" {
					phaseTransitions = append(phaseTransitions, phase+":"+reason)
				} else {
					phaseTransitions = append(phaseTransitions, phase)
				}
			}
			switch reason {
			case "enter_plan_mode":
				metrics["enter_plan_mode_count"] = intMetric(metrics, "enter_plan_mode_count") + 1
			case "commit_plan":
				metrics["commit_plan_count"] = intMetric(metrics, "commit_plan_count") + 1
			case "cancel_plan":
				metrics["cancel_plan_count"] = intMetric(metrics, "cancel_plan_count") + 1
			case "plan_exhausted":
				metrics["plan_exhausted_count"] = intMetric(metrics, "plan_exhausted_count") + 1
			}
		case "default_finish":
			metrics["default_finish"] = true
			metrics["loop_mode"] = "default"
		case "planner_decision":
			metrics["iteration_count"] = intMetric(metrics, "iteration_count") + 1
			metrics["loop_mode"] = "committed"
		case "candidate_answer":
			metrics["candidate_answer_count"] = intMetric(metrics, "candidate_answer_count") + 1
		case runEventToolCall:
			metrics["tool_call_count"] = intMetric(metrics, "tool_call_count") + 1
			if strings.EqualFold(strings.TrimSpace(event.Role), "agent") {
				metrics["planner_tool_call_count"] = intMetric(metrics, "planner_tool_call_count") + 1
			} else if strings.EqualFold(strings.TrimSpace(event.Role), "executor") {
				metrics["executor_tool_call_count"] = intMetric(metrics, "executor_tool_call_count") + 1
			}
			toolName := strings.TrimSpace(event.ToolName)
			if toolName == "" {
				toolName = "tool"
			}
			toolCounts[toolName]++
			if pair, ok := toolPairsByCall[index]; ok && pair.HasResult {
				if latency, ok := toolPairLatencyMs(event, pair, startTime); ok {
					toolLatencies = append(toolLatencies, latency)
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

		// Add percentiles
		sorted := make([]int64, len(toolLatencies))
		copy(sorted, toolLatencies)
		sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
		metrics["tool_latency_ms_p50"] = percentileInt64(sorted, 0.5)
		metrics["tool_latency_ms_p95"] = percentileInt64(sorted, 0.95)
		metrics["tool_latency_ms_p99"] = percentileInt64(sorted, 0.99)
	}

	// Add tool latency by type
	toolLatenciesByType := make(map[string][]int64)
	for index, event := range events {
		if event.Type == runEventToolCall {
			if pair, ok := toolPairsByCall[index]; ok && pair.HasResult {
				toolName := strings.TrimSpace(event.ToolName)
				if toolName == "" {
					toolName = "unknown"
				}
				if latency, ok := toolPairLatencyMs(event, pair, startTime); ok {
					toolLatenciesByType[toolName] = append(toolLatenciesByType[toolName], latency)
				}
			}
		}
	}
	if len(toolLatenciesByType) > 0 {
		toolStats := make(map[string]interface{})
		for toolName, latencies := range toolLatenciesByType {
			sorted := make([]int64, len(latencies))
			copy(sorted, latencies)
			sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })

			toolStats[toolName] = map[string]interface{}{
				"count": len(latencies),
				"avg":   avgInt64(latencies),
				"p50":   percentileInt64(sorted, 0.5),
				"p95":   percentileInt64(sorted, 0.95),
				"max":   sorted[len(sorted)-1],
			}
		}
		metrics["tool_latency_by_type"] = toolStats
	}

	// Add memory retrieve timing
	for _, event := range events {
		if event.Type == runEventMemoryRetrieve && event.DurationMs != nil {
			metrics["memory_retrieve_ms"] = *event.DurationMs
			break
		}
	}

	// Add session begin timing
	for _, event := range events {
		if event.Type == runEventSessionBegin && event.DurationMs != nil {
			metrics["session_begin_ms"] = *event.DurationMs
			break
		}
	}

	// Add iteration timing statistics
	var iterationDurations []int64
	for _, event := range events {
		if event.Type == runEventIterationEnd && event.DurationMs != nil {
			iterationDurations = append(iterationDurations, *event.DurationMs)
		}
	}
	if len(iterationDurations) > 0 {
		sorted := make([]int64, len(iterationDurations))
		copy(sorted, iterationDurations)
		sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })

		metrics["iteration_durations_ms"] = iterationDurations
		metrics["iteration_ms_avg"] = avgInt64(iterationDurations)
		metrics["iteration_ms_p50"] = percentileInt64(sorted, 0.5)
		metrics["iteration_ms_p95"] = percentileInt64(sorted, 0.95)
		metrics["iteration_ms_p99"] = percentileInt64(sorted, 0.99)
	}

	if len(phaseTransitions) > 0 {
		metrics["phase_transitions"] = phaseTransitions
	}
	metrics["final_phase"] = finalPhase
	return metrics
}

func toolPairLatencyMs(callEvent TaskEpisodeEvent, pair langfuseToolPair, startTime time.Time) (int64, bool) {
	if pair.ResultDurationMs != nil && *pair.ResultDurationMs >= 0 {
		return *pair.ResultDurationMs, true
	}
	start := parseEpisodeTime(callEvent.Ts, startTime)
	if pair.ResultTime.Before(start) {
		return 0, false
	}
	return pair.ResultTime.Sub(start).Milliseconds(), true
}

func episodeLoopTags(events []TaskEpisodeEvent) []string {
	tags := []string{}
	hasDefaultFinish := false
	hasCommit := false
	hasReplan := false
	for _, event := range events {
		switch event.Type {
		case "default_finish":
			hasDefaultFinish = true
		case "planner_decision":
			hasCommit = true
		case "loop_phase":
			switch strings.TrimSpace(event.Reason) {
			case "enter_plan_mode":
				tags = append(tags, "loop:plan")
			case "commit_plan":
				tags = append(tags, "loop:execution")
			case "cancel_plan":
				tags = append(tags, "loop:cancelled")
			case "plan_exhausted":
				tags = append(tags, "loop:exhausted")
			}
		case "verifier_decision":
			if event.NeedsReplan {
				hasReplan = true
			}
		}
	}
	if hasDefaultFinish {
		tags = append(tags, "loop:default_finish")
	}
	if hasCommit {
		tags = append(tags, "loop:committed")
	}
	if hasReplan {
		tags = append(tags, "loop:replan")
	}
	return tags
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

func langfuseRFC3339(t time.Time) string {
	return langfuse.RFC3339(t)
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
	if strings.TrimSpace(event.Content) != "" {
		input["content"] = event.Content
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

func avgInt64(values []int64) float64 {
	if len(values) == 0 {
		return 0
	}
	var sum int64
	for _, v := range values {
		sum += v
	}
	return float64(sum) / float64(len(values))
}

func percentileInt64(sortedValues []int64, p float64) int64 {
	if len(sortedValues) == 0 {
		return 0
	}
	if p <= 0 {
		return sortedValues[0]
	}
	if p >= 1 {
		return sortedValues[len(sortedValues)-1]
	}
	index := int(float64(len(sortedValues)-1) * p)
	return sortedValues[index]
}
