package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"math"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/tmc/langchaingo/agents"
	"github.com/tmc/langchaingo/callbacks"
	"github.com/tmc/langchaingo/chains"
	"github.com/tmc/langchaingo/llms"
	"github.com/tmc/langchaingo/schema"
	langtools "github.com/tmc/langchaingo/tools"
)

func effectiveMaxIterations(configured int) int {
	if configured <= 0 {
		return math.MaxInt
	}
	return configured
}

const currentEnvironmentHintMaxAge = 10 * time.Minute

type Runtime struct {
	config             Config
	models             ModelResolver
	memories           *MemoryManager
	tools              *ToolSet
	skills             *SkillManager
	skillsLoaded       bool
	skillsReloadMu     sync.Mutex
	skillsDirty        bool
	runGateInit        sync.Once
	mergeWorker        *MergeWorker
	logger             *Logger
	profileDebouncer   *ProfileDebouncer
	sleep              *SleepController
	memoryPlane        MemoryPlane
	sessionManager     SessionManager
	telemetrySessionID string
	mobileGym          *mobileGymSessionStore
	runGate            chan struct{}
}

type RunRequest struct {
	Input             string
	Attachments       []InputAttachment
	Skills            []string
	EpisodeID         string
	DeviceEnvironment *PhoneEnvironment
	// RuntimeContext is dynamic per-turn system context, such as connected
	// hardware/app state. It is not persisted as user configuration.
	RuntimeContext string
	StreamWriter   io.Writer
	// StreamFinalChunks allows final-answer chunks to be written through
	// StreamWriter for audio paths. Non-final LLM calls must remain
	// non-streaming because they may be planner, tool-call, or verifier turns.
	StreamFinalChunks bool
	MaxTokens         int
	EventHandler      func(RunEvent)
	SteerProvider     func(context.Context) (RunSteerMessage, bool)
}

type RunResult struct {
	Output         string          `json:"output"`
	SpeechText     string          `json:"-"`
	Skills         []string        `json:"skills"`
	EpisodeID      string          `json:"episode_id,omitempty"`
	Memory         []MessageRecord `json:"memory,omitempty"`
	Metrics        *RunMetrics     `json:"metrics,omitempty"`
	SleepRequested bool            `json:"sleep_requested,omitempty"`
	SleepReason    string          `json:"sleep_reason,omitempty"`
	SpeechStreamed bool            `json:"-"`
}

func (r RunResult) SpokenText() string {
	if text := strings.TrimSpace(r.SpeechText); text != "" {
		return text
	}
	return strings.TrimSpace(r.Output)
}

func (r RunResult) SpokenTextForConfig(cfg Config) string {
	if !cfg.VoiceSpeechSummaryEnabledOrDefault() {
		return strings.TrimSpace(r.Output)
	}
	if text := strings.TrimSpace(r.SpeechText); text != "" {
		return text
	}
	text := BuildSpeechText(r.Output, cfg)
	if text != "" {
		return text
	}
	return r.SpokenText()
}

type RunSteerMessage struct {
	ID        string    `json:"id,omitempty"`
	RequestID string    `json:"request_id,omitempty"`
	Content   string    `json:"content"`
	Timestamp time.Time `json:"timestamp"`
}

type RunMetrics struct {
	TotalDuration    float64 `json:"total_duration_ms"`
	PromptTokens     int     `json:"prompt_tokens,omitempty"`
	CompletionTokens int     `json:"completion_tokens,omitempty"`
	TotalTokens      int     `json:"total_tokens,omitempty"`
	ContextWindow    int     `json:"context_window,omitempty"`
	FirstTokenTime   float64 `json:"first_token_time_ms,omitempty"`
	// LastPromptTokens holds the largest single prompt-token count in the run.
	// PromptTokens/CompletionTokens/TotalTokens accumulate across the multiple
	// planner/executor/verifier calls in a single run, but the compression
	// heuristic needs the size of one prompt relative to the context window, not
	// the cumulative sum. Using the largest single prompt keeps a small verifier
	// call from masking a much larger planner prompt.
	LastPromptTokens int `json:"-"`
}

const (
	runEventToolCall   = "tool_call"
	runEventTodoUpdate = "todo_update"
	runEventTodoClosed = "todo_closed"
)

type RunEvent struct {
	Type           string     `json:"type"`
	Role           string     `json:"role,omitempty"`
	EpisodeID      string     `json:"episode_id,omitempty"`
	ToolName       string     `json:"tool_name,omitempty"`
	ToolInput      string     `json:"tool_input,omitempty"`
	Description    string     `json:"description,omitempty"`
	Speech         string     `json:"speech,omitempty"`
	Content        string     `json:"content,omitempty"`
	Todo           *TodoState `json:"todo,omitempty"`
	SpeechEligible bool       `json:"speech_eligible,omitempty"`
	Timestamp      time.Time  `json:"timestamp"`
	IsError        bool       `json:"is_error,omitempty"`
}

type usageTrackingModel struct {
	inner           llms.Model
	metrics         *RunMetrics
	promptCapture   *telemetryPromptCapture
	contextWindowFn func() int
}

func (m *usageTrackingModel) GenerateContent(ctx context.Context, messages []llms.MessageContent, options ...llms.CallOption) (*llms.ContentResponse, error) {
	startedAt := time.Now()
	res, err := m.inner.GenerateContent(ctx, messages, options...)
	if err == nil {
		recordUsageMetrics(m.metrics, res)
	}
	if m.promptCapture != nil {
		m.promptCapture.Record(ctx, startedAt, time.Now(), messages, options, res, err, m.contextWindow())
	}
	return res, err
}

func (m *usageTrackingModel) Call(ctx context.Context, prompt string, options ...llms.CallOption) (string, error) {
	startedAt := time.Now()
	out, err := m.inner.Call(ctx, prompt, options...)
	if m.promptCapture != nil {
		res := &llms.ContentResponse{Choices: []*llms.ContentChoice{{Content: out}}}
		m.promptCapture.Record(ctx, startedAt, time.Now(), []llms.MessageContent{{
			Role:  llms.ChatMessageTypeHuman,
			Parts: []llms.ContentPart{llms.TextPart(prompt)},
		}}, options, res, err, m.contextWindow())
	}
	return out, err
}

func (m *usageTrackingModel) contextWindow() int {
	if m == nil {
		return 0
	}
	if m.contextWindowFn != nil {
		if v := m.contextWindowFn(); v > 0 {
			return v
		}
	}
	if m.metrics != nil {
		return m.metrics.ContextWindow
	}
	return 0
}

func NewRuntime(cfg Config) (*Runtime, error) {
	var mergeNeeded []SkillMergeJob

	// Sync bundled skills into user directory before loading
	if cfg.BundledSkillsDir != "" && cfg.ConfigDir != "" {
		report, err := SyncBundledSkills(context.Background(), SkillSyncOptions{
			ConfigDir:        cfg.ConfigDir,
			BundledSkillsDir: cfg.BundledSkillsDir,
			MergeModel:       cfg.SkillMergeModel,
			Quiet:            false,
		})
		if err != nil {
			log.Printf("[skill_sync] sync failed (non-fatal): %v", err)
		} else {
			mergeNeeded = report.MergeNeeded
		}
	}
	if cfg.ConfigDir != "" {
		skillsDir := filepath.Join(cfg.ConfigDir, "skills")
		if info, err := os.Stat(skillsDir); err == nil && info.IsDir() {
			cfg.SkillsDirs = []string{skillsDir}
		}
	}

	// Load skills from configured directories
	var skillIndex *SkillIndex
	var err error

	if len(cfg.SkillsDirs) > 0 {
		skillIndex, err = LoadSkillsFromDirs(cfg.SkillsDirs)
		if err != nil {
			return nil, fmt.Errorf("load skills: %w", err)
		}
	} else {
		skillIndex = NewSkillIndex()
	}

	// Create logger if ConfigDir is set
	var logger *Logger
	if cfg.ConfigDir != "" {
		logger, err = NewLogger(cfg.ConfigDir)
		if err != nil {
			return nil, fmt.Errorf("create logger: %w", err)
		}
		logger.Info("Agent runtime initialized with config from %s", cfg.ConfigDir)
	}

	memoryDir := ""
	if cfg.ConfigDir != "" {
		memoryDir = filepath.Join(cfg.ConfigDir, "memory")
	}

	sleepController := NewSleepController()
	proxy := ProxyConfigFromEnvironment()
	if err := proxy.Validate(); err != nil {
		return nil, fmt.Errorf("proxy environment: %w", err)
	}
	mobileGymStore := &mobileGymSessionStore{}
	toolSet := NewBuiltinToolSetFromConfig(
		cfg,
		proxy,
		mobileGymStore,
		WithSleepController(sleepController),
		WithScreenStableDefaults(cfg.ScreenStableDefaults()),
	)
	extractionCfg := LoadMemoryExtractionConfig(cfg.ConfigDir)
	modelManagerOptions := []ModelManagerOption{}
	if cfg.ConfigDir != "" {
		modelManagerOptions = append(modelManagerOptions, WithProviderModelMetadataCachePath(filepath.Join(cfg.ConfigDir, "cache", "provider_model_metadata.json")))
	}
	modelManager := NewModelManager(cfg.Model, proxy, modelManagerOptions...)
	modelManager.prefetchProviderModelSpecIfNeeded()
	summarizeFn := buildLLMSummarizeFn(modelManager)
	structuredSummarizeFn := buildLLMStructuredSummarizeFn(modelManager, logger)
	profileFn := buildLLMProfileFn(modelManager)
	contextWindowFn := func() int { return modelManager.Spec().ContextWindow }

	longTermDir := ""
	if memoryDir != "" {
		longTermDir = filepath.Join(memoryDir, "long_term")
	}
	var debouncer *ProfileDebouncer
	if longTermDir != "" {
		store := NewLongTermMemoryStore(longTermDir, WithLifecycleDir(filepath.Join(memoryDir, "lifecycle")), WithStoreProfileFn(profileFn))
		debouncer = NewProfileDebouncer(store.RegenerateProfileMD, 60*time.Second, logger)
	}

	toolSet.RegisterMemoryTools(memoryDir, profileFn, extractionCfg.SummaryMaxChunks, debouncer)

	rt := NewRuntimeWithDeps(cfg, modelManager, NewMemoryManager(memoryDir, WithExtractionConfig(extractionCfg), WithSummarizeFn(summarizeFn), WithStructuredSummarizeFn(structuredSummarizeFn), WithProfileFn(profileFn), WithContextWindowFn(contextWindowFn), WithMemoryProfileDebouncer(debouncer), WithMemoryLogger(logger)), toolSet, skillIndex)

	// Register skill tools after the runtime exists so skill_manage can mark the
	// skill index dirty. The updated index is reloaded at the start of the next run.
	if cfg.ConfigDir != "" {
		skillsDir := filepath.Join(cfg.ConfigDir, "skills")
		manifestPath := filepath.Join(cfg.ConfigDir, "skill-state", ".bundled_manifest.json")
		toolSet.RegisterSkillTools(skillsDir, manifestPath, rt.MarkSkillsDirty)
	}
	rt.logger = logger
	rt.profileDebouncer = debouncer
	rt.sleep = sleepController
	rt.mobileGym = mobileGymStore

	if len(mergeNeeded) > 0 && cfg.SkillMergeModel != nil {
		manifestPath := filepath.Join(cfg.ConfigDir, "skill-state", ".bundled_manifest.json")
		worker := NewMergeWorker(cfg.SkillMergeModel, manifestPath)
		worker.onSuccess = func(SkillMergeJob) {
			rt.MarkSkillsDirty()
		}
		worker.Enqueue(mergeNeeded)
		worker.Start(context.Background())
		rt.mergeWorker = worker
	}

	rt.memoryPlane = NewFilesystemMemoryPlane(memoryDir, extractionCfg, logger)
	rt.markInterruptedEpisodesBestEffort()
	return rt, nil
}

func NewRuntimeWithDeps(cfg Config, models ModelResolver, memories *MemoryManager, tools *ToolSet, skillIndex *SkillIndex) *Runtime {
	sleepController := NewSleepController()
	if tools != nil {
		if tool, ok := tools.Get("enter_sleep"); ok {
			if sleepTool, ok := tool.(*EnterSleepTool); ok && sleepTool.controller != nil {
				sleepController = sleepTool.controller
			}
		}
	}
	skillManager := NewSkillManager(skillIndex)
	if cfg.ConfigDir != "" {
		skillManager.SetUsagePath(filepath.Join(cfg.ConfigDir, "skill-state", "usage.json"))
	}
	rt := &Runtime{
		config:             cfg,
		models:             models,
		memories:           memories,
		tools:              tools,
		skills:             skillManager,
		skillsLoaded:       skillIndex != nil && len(skillIndex.Names()) > 0,
		sleep:              sleepController,
		telemetrySessionID: uuid.NewString(),
		mobileGym:          &mobileGymSessionStore{},
	}
	if cfg.ConfigDir != "" {
		rt.memoryPlane = NewFilesystemMemoryPlane(filepath.Join(cfg.ConfigDir, "memory"), LoadMemoryExtractionConfig(cfg.ConfigDir), nil)
		rt.markInterruptedEpisodesBestEffort()
	}
	rt.sessionManager = newMemoryManagerSessionManager(memories, func(now time.Time, activeMaxAge time.Duration) BoundaryEpisodeContext {
		return recentEpisodeContext(rt.memoryPlane, now, activeMaxAge)
	})
	rt.initRunGate()
	return rt
}

func (r *Runtime) initRunGate() {
	if r == nil {
		return
	}
	r.runGateInit.Do(func() {
		r.runGate = make(chan struct{}, 1)
		r.runGate <- struct{}{}
	})
}

func (r *Runtime) lockRun(ctx context.Context) (func(), error) {
	if r == nil {
		return func() {}, nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	r.initRunGate()
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-r.runGate:
		if err := ctx.Err(); err != nil {
			r.runGate <- struct{}{}
			return nil, err
		}
		return func() {
			r.runGate <- struct{}{}
		}, nil
	}
}

func (r *Runtime) NewEpisodeID() string {
	if r == nil || r.memoryPlane == nil {
		return ""
	}
	return newTaskEpisodeID(time.Now().UTC())
}

func (r *Runtime) markInterruptedEpisodesBestEffort() {
	plane, ok := r.memoryPlane.(*FilesystemMemoryPlane)
	if !ok || plane == nil || plane.episodes == nil {
		return
	}
	episodes, err := plane.episodes.MarkRunningEpisodesInterruptedWithDetails(context.Background(), "agent restarted before the task episode completed")
	if err != nil {
		if r.logger != nil {
			r.logger.Warn("[memory] mark interrupted episodes failed: %v", err)
		}
		return
	}
	if len(episodes) == 0 {
		return
	}
	r.persistInterruptedEpisodeHistoryBestEffort(plane, episodes)
	r.exportInterruptedEpisodesBestEffort(episodes)
	if r.logger != nil {
		r.logger.Warn("[memory] marked %d running task episode(s) as interrupted", len(episodes))
	}
}

func (r *Runtime) persistInterruptedEpisodeHistoryBestEffort(plane *FilesystemMemoryPlane, episodes []TaskEpisode) {
	if plane == nil || strings.TrimSpace(plane.memoryDir) == "" || len(episodes) == 0 {
		return
	}
	store := NewChatHistoryStore(filepath.Join(plane.memoryDir, "chat_history"))
	for _, episode := range episodes {
		if err := store.Append(context.Background(), interruptedEpisodeStatusMessage(episode)); err != nil && r.logger != nil {
			r.logger.Warn("[memory] persist interrupted episode history failed: episode_id=%s error=%v", episode.ID, err)
		}
	}
}

func (r *Runtime) exportInterruptedEpisodesBestEffort(episodes []TaskEpisode) {
	if len(episodes) == 0 {
		return
	}
	for _, episode := range episodes {
		enrichEpisodeTelemetry(&episode, r.config)
		r.enrichEpisodeRuntimeTelemetry(&episode)
		if episode.Extra == nil {
			episode.Extra = map[string]interface{}{}
		}
		episode.Extra["interruption_source"] = "agent_restart"
		r.exportEpisodeBestEffort(episode, nil)
	}
}

func (r *Runtime) Run(ctx context.Context, req RunRequest) (RunResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	unlockRun, err := r.lockRun(ctx)
	if err != nil {
		return RunResult{}, err
	}
	defer unlockRun()

	startTime := time.Now()
	metrics := &RunMetrics{}
	normalizedInput := normalizeRunInput(req.Input, req.Attachments)
	if r.sleep != nil {
		r.sleep.Consume()
	}

	if r.logger != nil {
		r.logger.Info("Starting agent run: input=%q attachments=%d", normalizedInput, len(req.Attachments))
	}

	if normalizedInput == "" {
		return RunResult{}, errors.New("input is required")
	}

	if err := r.reloadSkillsIfDirty(); err != nil {
		return RunResult{}, err
	}
	runSkills := r.skills.Snapshot()

	// Activate skills
	skillNames := uniqueNonEmpty(req.Skills)
	for _, skillName := range skillNames {
		if err := runSkills.Activate(ctx, skillName); err != nil {
			if r.logger != nil {
				r.logger.Error("Failed to activate skill %q: %v", skillName, err)
			}
			return RunResult{}, fmt.Errorf("activate skill %q: %w", skillName, err)
		}
	}

	if r.logger != nil && len(skillNames) > 0 {
		r.logger.Info("Activated skills: %v", skillNames)
	}

	resolvedSkills, err := runSkills.Resolve(skillNames)
	if err != nil {
		return RunResult{}, err
	}

	model, err := r.models.Get()
	if err != nil {
		return RunResult{}, err
	}
	contextWindow := r.effectiveContextWindow()
	if contextWindow > 0 {
		metrics.ContextWindow = contextWindow
		if r.logger != nil {
			r.logger.Info("Resolved model context window: context_window=%d", contextWindow)
		}
	}
	promptCapture := newTelemetryPromptCapture(r.config.Telemetry.EnabledOrDefault())
	model = &usageTrackingModel{inner: model, metrics: metrics, promptCapture: promptCapture, contextWindowFn: r.effectiveContextWindow}

	currentHints := r.currentEnvironmentHints()
	beginResult, err := r.beginSession(ctx, SessionBeginRequest{
		Input:        normalizedInput,
		CurrentHints: currentHints,
	})
	if err != nil {
		return RunResult{}, err
	}
	boundaryTelemetry := beginResult.Boundary
	if boundaryTelemetry.PendingRecallCounter == nil {
		boundaryTelemetry.PendingRecallCounter = &atomic.Int64{}
	}

	memoryHandle, err := r.memories.Get("default", MemoryConfig{Type: "window", WindowSize: 10})
	if err != nil {
		return RunResult{}, err
	}

	availableTools := r.resolveTools(resolvedSkills)
	availableTools = wrapSessionRecallTelemetry(availableTools, boundaryTelemetry.PendingRecallCounter)
	retrieveReq := MemoryRetrieveRequest{
		Input:        normalizedInput,
		Attachments:  req.Attachments,
		Skills:       skillNames,
		ToolNames:    toolNamesFromTools(availableTools),
		EpisodeID:    req.EpisodeID,
		DeviceID:     defaultMemoryDeviceID,
		CurrentHints: currentHints,
	}
	memoryContext := MemoryContext{}
	if r.memoryPlane != nil {
		retrieved, retrieveErr := r.memoryPlane.Retrieve(ctx, retrieveReq)
		if retrieveErr != nil {
			if r.logger != nil {
				r.logger.Warn("[memory] retrieve failed: %v", retrieveErr)
			}
		} else {
			memoryContext = retrieved
		}
	}
	var episodeRecorder *EpisodeRecorder
	if r.memoryPlane != nil {
		episodeRecorder = r.memoryPlane.NewEpisodeRecorder(retrieveReq, memoryContext)
		if episodeRecorder != nil {
			episodeRecorder.ToolCallSpeech = r.config.VoiceToolCallSpeechOrDefault()
			retrieveReq.EpisodeID = episodeRecorder.ID()
			if err := episodeRecorder.Start(ctx); err != nil && r.logger != nil {
				r.logger.Warn("[memory] start episode failed: %v", err)
			}
		}
	}
	episodeID := ""
	if episodeRecorder != nil {
		episodeID = episodeRecorder.ID()
	}

	maxIterations := effectiveMaxIterations(r.config.MaxIterations)

	callOptions := r.models.CallOptions()
	if req.MaxTokens > 0 {
		callOptions = append(callOptions, chains.WithMaxTokens(req.MaxTokens))
	}
	var streamCallbackHandler *runtimeCallbackHandler
	if req.StreamWriter != nil || req.EventHandler != nil || req.SteerProvider != nil || r.logger != nil {
		streamCallbackHandler = &runtimeCallbackHandler{
			writer:                 req.StreamWriter,
			providerFinalStreaming: req.StreamWriter != nil && req.StreamFinalChunks,
			metrics:                metrics,
			startTime:              startTime,
			logger:                 r.logger,
			eventHandler:           req.EventHandler,
			episodeID:              episodeID,
			toolCallSpeechEnabled:  r.config.VoiceToolCallSpeechOrDefault(),
		}
	}
	var executorHandler callbacks.Handler
	if streamCallbackHandler != nil {
		executorHandler = streamCallbackHandler
	}
	profiles := r.buildRoleProfiles(resolvedSkills, availableTools, memoryContext, req.RuntimeContext)
	plannerMemory := memoryHandle.Memory
	if historyStore := chatHistoryStoreForConfigDir(r.config.ConfigDir); historyStore != nil {
		plannerMemory = newChatHistoryPlannerMemory(plannerMemory, historyStore)
	}
	var steerStatus steerConversationStatus
	if req.SteerProvider != nil {
		plannerMemory = newSteerConversationMemory(plannerMemory, memoryHandle.History)
		if status, ok := plannerMemory.(steerConversationStatus); ok {
			steerStatus = status
		}
	}
	executor := newRoleCollaborativeExecutor(model, profiles, availableTools, plannerMemory, maxIterations, req.Attachments, executorHandler, episodeRecorder, r.config.ScreenshotPruningOrDefault(), req.DeviceEnvironment, req.SteerProvider)
	executor.TodoReminderToolCalls = r.config.TodoReminderToolCallsOrDefault()
	executor.ToolCallSpeech = r.config.VoiceToolCallSpeechOrDefault()

	output, err := chains.Run(ctx, executor, normalizedInput, callOptions...)
	if err != nil {
		// If the agent couldn't parse the LLM output format, extract the raw
		// text and return it as the response instead of failing.
		if errors.Is(err, agents.ErrUnableToParseOutput) {
			raw := err.Error()
			const prefix = "unable to parse agent output: "
			if idx := strings.Index(raw, prefix); idx >= 0 {
				output = strings.TrimSpace(raw[idx+len(prefix):])
				err = nil
			}
		}
		if err != nil {
			r.commitEpisodeBestEffort(episodeRecorder, normalizedInput, output, metrics, err, promptCapture, boundaryTelemetry)
			return RunResult{}, err
		}
	}

	output, speechText := finalizeSpeechOutput(output, r.config)
	metrics.TotalDuration = float64(time.Since(startTime).Milliseconds())
	commitReq := SessionCommitRequest{
		AgentName: "default",
		Input:     normalizedInput,
		Output:    output,
		Metrics:   metrics,
	}
	if steerStatus != nil && steerStatus.HasSteerMessages() {
		commitReq.Steers = steerStatus.SteerMessages()
	}

	commitResult, err := r.commitSession(ctx, commitReq)
	if err != nil {
		r.commitEpisodeBestEffort(episodeRecorder, normalizedInput, output, metrics, err, promptCapture, boundaryTelemetry)
		return RunResult{}, err
	}
	r.commitEpisodeBestEffort(episodeRecorder, normalizedInput, output, metrics, nil, promptCapture, boundaryTelemetry)

	sleepRequested, sleepReason := false, ""
	if r.sleep != nil {
		sleepRequested, sleepReason = r.sleep.Consume()
	}
	return RunResult{
		Output:         output,
		SpeechText:     speechText,
		Skills:         runSkills.GetActivatedSkills(),
		EpisodeID:      episodeID,
		Memory:         commitResult.Memory,
		Metrics:        metrics,
		SleepRequested: sleepRequested,
		SleepReason:    sleepReason,
	}, nil
}

func (r *Runtime) beginSession(ctx context.Context, req SessionBeginRequest) (SessionBeginResult, error) {
	if r == nil || r.sessionManager == nil {
		return SessionBeginResult{}, nil
	}
	return r.sessionManager.BeginRun(ctx, req)
}

func (r *Runtime) commitSession(ctx context.Context, req SessionCommitRequest) (SessionCommitResult, error) {
	if r == nil || r.sessionManager == nil {
		return SessionCommitResult{}, nil
	}
	return r.sessionManager.CommitRun(ctx, req)
}

func steeredExchangeRecords(input string, steers []RunSteerMessage, output string) []MessageRecord {
	records := make([]MessageRecord, 0, len(steers)+2)
	records = append(records, MessageRecord{Role: string(llms.ChatMessageTypeHuman), Content: input})
	for _, steer := range steers {
		records = append(records, MessageRecord{Role: string(llms.ChatMessageTypeHuman), Content: steerHumanMessageContent(steer)})
	}
	records = append(records, MessageRecord{Role: string(llms.ChatMessageTypeAI), Content: output})
	return records
}

func (r *Runtime) currentEnvironmentHints() CurrentEnvironmentHints {
	if r.tools != nil {
		return r.tools.CurrentEnvironmentHints(currentEnvironmentHintMaxAge)
	}
	return CurrentEnvironmentHints{}
}

func (r *Runtime) effectiveContextWindow() int {
	if r == nil {
		return 0
	}
	if r.models != nil {
		if spec := r.models.Spec(); spec.ContextWindow > 0 {
			return spec.ContextWindow
		}
	}
	if r.memories != nil {
		return r.memories.effectiveContextWindow()
	}
	return 0
}

func (r *Runtime) ClearMemory(ctx context.Context) error {
	return r.memories.ClearSession(ctx, "default")
}

func (r *Runtime) MarkSkillsDirty() {
	r.skillsReloadMu.Lock()
	defer r.skillsReloadMu.Unlock()
	r.skillsDirty = true
}

func (r *Runtime) reloadSkillsIfDirty() error {
	r.skillsReloadMu.Lock()
	defer r.skillsReloadMu.Unlock()

	if !r.skillsDirty {
		return nil
	}
	if len(r.config.SkillsDirs) == 0 {
		r.skillsDirty = false
		return nil
	}

	index, err := LoadSkillsFromDirs(r.config.SkillsDirs)
	if err != nil {
		return fmt.Errorf("reload skills: %w", err)
	}
	r.skills.ReplaceIndex(index)
	r.skillsLoaded = len(index.Names()) > 0
	r.skillsDirty = false
	return nil
}

func (r *Runtime) hasLoadedSkills() bool {
	r.skillsReloadMu.Lock()
	defer r.skillsReloadMu.Unlock()
	return r.skillsLoaded
}

func (r *Runtime) ClearAllMemory(ctx context.Context) error {
	return r.memories.ClearAll(ctx, "default")
}

func (r *Runtime) resolveTools(skills ResolvedSkills) []langtools.Tool {
	available := make([]langtools.Tool, 0)

	if skills.HasToolRestriction {
		for toolName := range skills.AllowedTools {
			if strings.HasPrefix(toolName, "delegate_") {
				continue
			}
			if !isAgentToolExposed(toolName) {
				continue
			}
			tool, ok := r.tools.Get(toolName)
			if ok {
				available = append(available, tool)
			}
		}
	} else {
		for _, tool := range r.tools.All() {
			if isAgentToolExposed(tool.Name()) {
				available = append(available, tool)
			}
		}
	}

	memoryTools := []string{"recall_session_chunks", "recall_memory", "save_memory", "forget_memory", "recall_device_memory", "inspect_episode"}
	for _, name := range memoryTools {
		if skills.HasToolRestriction {
			if _, allowed := skills.AllowedTools[name]; !allowed {
				continue
			}
		}
		available = r.appendToolIfAvailable(available, name)
	}

	// Keep skill meta-tools available even when an active skill has allowed_tools
	// restrictions. Otherwise the Hermes-like flow breaks: the prompt can show
	// Available skills, but the model cannot read or maintain the matching skill.
	for _, name := range []string{"skill_list", "skill_read", "skill_manage", "skill_mark_used"} {
		available = r.appendToolIfAvailable(available, name)
	}

	return available
}

func (r *Runtime) appendToolIfAvailable(tools []langtools.Tool, name string) []langtools.Tool {
	if tool, ok := r.tools.Get(name); ok {
		if !toolAlreadyIncluded(tools, name) {
			return append(tools, tool)
		}
	}
	return tools
}

func toolAlreadyIncluded(tools []langtools.Tool, name string) bool {
	for _, t := range tools {
		if t.Name() == name {
			return true
		}
	}
	return false
}

func toolNamesFromTools(tools []langtools.Tool) []string {
	names := make([]string, 0, len(tools))
	for _, tool := range tools {
		if tool == nil {
			continue
		}
		names = append(names, tool.Name())
	}
	return uniqueNonEmpty(names)
}

func wrapSessionRecallTelemetry(tools []langtools.Tool, counter *atomic.Int64) []langtools.Tool {
	if counter == nil {
		return tools
	}
	wrapped := make([]langtools.Tool, 0, len(tools))
	for _, tool := range tools {
		if tool != nil && tool.Name() == "recall_session_chunks" {
			wrapped = append(wrapped, &sessionRecallTelemetryTool{inner: tool, counter: counter})
			continue
		}
		wrapped = append(wrapped, tool)
	}
	return wrapped
}

type sessionRecallTelemetryTool struct {
	inner   langtools.Tool
	counter *atomic.Int64
}

func (t *sessionRecallTelemetryTool) Name() string {
	return t.inner.Name()
}

func (t *sessionRecallTelemetryTool) Description() string {
	return t.inner.Description()
}

func (t *sessionRecallTelemetryTool) ArgsSchema() map[string]any {
	if structured, ok := t.inner.(structuredInputTool); ok {
		return structured.ArgsSchema()
	}
	return nil
}

func (t *sessionRecallTelemetryTool) Call(ctx context.Context, input string) (string, error) {
	output, err := t.inner.Call(ctx, input)
	if err != nil {
		return "", err
	}
	if t.counter != nil {
		t.counter.Add(int64(countPendingRecallResults(output)))
	}
	return output, nil
}

func (t *sessionRecallTelemetryTool) ReturnsVisualObservation() bool {
	if visual, ok := t.inner.(visualObservationTool); ok {
		return visual.ReturnsVisualObservation()
	}
	return false
}

func countPendingRecallResults(output string) int {
	var payload struct {
		Results []struct {
			Source string `json:"source"`
		} `json:"results"`
	}
	if err := json.Unmarshal([]byte(output), &payload); err != nil {
		return 0
	}
	count := 0
	for _, result := range payload.Results {
		if strings.EqualFold(strings.TrimSpace(result.Source), chunkRecallSourcePending) {
			count++
		}
	}
	return count
}

func (r *Runtime) commitEpisodeBestEffort(recorder *EpisodeRecorder, input string, output string, metrics *RunMetrics, runErr error, promptCapture *telemetryPromptCapture, boundary sessionBoundaryTelemetry) {
	if recorder == nil || r.memoryPlane == nil {
		return
	}
	cfg := DefaultMemoryExtractionConfig()
	if r.memories != nil {
		cfg = r.memories.extraction
	} else if r.config.ConfigDir != "" {
		cfg = LoadMemoryExtractionConfig(r.config.ConfigDir)
	}
	tags := cfg.extractTagsFromText(input)
	entities := cfg.extractEntitiesFromText(input)
	episode := recorder.Finish(output, metrics, runErr, tags, entities)
	enrichEpisodeTelemetry(&episode, r.config)
	r.enrichEpisodeRuntimeTelemetry(&episode)
	enrichEpisodeSessionBoundaryTelemetry(&episode, boundary)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := r.memoryPlane.CommitEpisode(ctx, episode); err != nil && r.logger != nil {
		r.logger.Warn("[memory] commit episode failed: %v", err)
		return
	}
	r.exportEpisodeBestEffort(episode, promptCapture)
}

func enrichEpisodeSessionBoundaryTelemetry(episode *TaskEpisode, boundary sessionBoundaryTelemetry) {
	if episode == nil || boundary.Decision == "" {
		return
	}
	if episode.Extra == nil {
		episode.Extra = map[string]interface{}{}
	}
	episode.Extra["session_boundary_decision"] = boundary.Decision
	episode.Extra["session_boundary_reason"] = boundary.Reason
	episode.Extra["session_rotated"] = boundary.Rotated
	pendingRecalled := int64(0)
	if boundary.PendingRecallCounter != nil {
		pendingRecalled = boundary.PendingRecallCounter.Load()
	}
	episode.Extra["pending_chunks_recalled"] = pendingRecalled
}

func (r *Runtime) enrichEpisodeRuntimeTelemetry(episode *TaskEpisode) {
	if r == nil || episode == nil {
		return
	}
	if episode.Extra == nil {
		episode.Extra = map[string]interface{}{}
	}
	if extraString(episode.Extra, "session_id") == "" && r.telemetrySessionID != "" {
		episode.Extra["session_id"] = r.telemetrySessionID
	}
	contextWindow := r.effectiveContextWindow()
	if contextWindow > 0 {
		episode.Extra["context_window"] = contextWindow
	}
	params, _ := episode.Extra["model_parameters"].(map[string]interface{})
	if len(params) == 0 {
		params = telemetryModelParametersFromModelConfig(r.config.Model)
	}
	if contextWindow > 0 {
		if params == nil {
			params = map[string]interface{}{}
		}
		params["context_window"] = contextWindow
	}
	if len(params) > 0 {
		episode.Extra["model_parameters"] = params
	}
}

func telemetryModelParametersFromModelConfig(cfg ModelConfig) map[string]interface{} {
	params := map[string]interface{}{}
	if cfg.Temperature != 0 {
		params["temperature"] = cfg.Temperature
	}
	if cfg.MaxResponseTokens > 0 {
		params["max_response_tokens"] = cfg.MaxResponseTokens
	}
	if len(params) == 0 {
		return nil
	}
	return params
}

func (r *Runtime) exportEpisodeBestEffort(episode TaskEpisode, promptCapture *telemetryPromptCapture) {
	if !r.config.Telemetry.EnabledOrDefault() || strings.TrimSpace(episode.UserGoal) == "" {
		return
	}
	if r.config.ConfigDir == "" {
		return
	}
	exporter := NewEpisodeExporter(r.config.Telemetry, r.logger)
	promptCalls := promptCapture.Snapshot()
	episodesRoot := filepath.Join(r.config.ConfigDir, "memory", "episodes")
	episodeDir := EpisodeDirectory(episodesRoot, episode)
	timeout := r.config.Telemetry.UploadTimeoutOrDefault()
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), timeout)
		defer cancel()
		if err := exporter.ExportEpisodeDir(ctx, episodeDir, episode, promptCalls); err != nil && r.logger != nil {
			r.logger.Warn("[telemetry] export episode failed: %v", err)
		}
	}()
}

func (r *Runtime) buildRoleProfiles(skills ResolvedSkills, availableTools []langtools.Tool, memoryContext MemoryContext, runtimeContext string) RoleProfiles {
	if memoryContext.IsEmpty() && r.config.ConfigDir != "" {
		memoryContext = normalizeMemoryContext(r.memoryContextForPrompt())
	}
	return buildRoleProfiles(
		AgentConfig{
			Instruction:               r.config.Instruction,
			AdditionalPrompt:          r.config.AdditionalPrompt,
			RuntimeContext:            runtimeContext,
			ForceSimpleLoop:           r.config.ForceSimpleLoop,
			VoiceSpeechSummaryEnabled: r.config.VoiceSpeechSummaryEnabled,
			VoiceToolCallSpeech:       r.config.VoiceToolCallSpeech,
		},
		skills,
		availableTools,
		memoryContext,
	)
}

func (r *Runtime) memoryContextForPrompt() string {
	if r.config.ConfigDir == "" {
		return ""
	}
	var parts []string
	sessionSummary, _ := os.ReadFile(filepath.Join(r.config.ConfigDir, "memory", "session", "summary.md"))
	if len(sessionSummary) > 0 {
		parts = append(parts, string(sessionSummary))
	}
	profile, _ := os.ReadFile(filepath.Join(r.config.ConfigDir, "memory", "long_term", "profile.md"))
	if len(profile) > 0 {
		parts = append(parts, string(profile))
	}
	return strings.Join(parts, "\n\n")
}

// runtimeCallbackHandler implements callbacks.Handler for streaming output and
// tool/agent observability.
type runtimeCallbackHandler struct {
	writer                 io.Writer
	providerFinalStreaming bool
	metrics                *RunMetrics
	startTime              time.Time
	firstTokenSeen         bool
	finalTokenSeen         bool
	finalStreamErr         error
	streamFinal            bool
	logger                 *Logger
	eventHandler           func(RunEvent)
	episodeID              string
	toolCallSpeechEnabled  bool
	mu                     sync.Mutex
	pendingActions         []schema.AgentAction
}

func (h *runtimeCallbackHandler) HandleText(ctx context.Context, text string) {
	if h.writer != nil {
		h.writer.Write([]byte(text))
	}
}

func (h *runtimeCallbackHandler) HandleLLMStart(ctx context.Context, prompts []string) {}

func (h *runtimeCallbackHandler) HandleLLMGenerateContentStart(ctx context.Context, ms []llms.MessageContent) {
}

func (h *runtimeCallbackHandler) HandleLLMGenerateContentEnd(ctx context.Context, res *llms.ContentResponse) {
	recordUsageMetrics(h.metrics, res)
}

func recordUsageMetrics(metrics *RunMetrics, res *llms.ContentResponse) {
	if res == nil || metrics == nil || len(res.Choices) == 0 {
		return
	}
	info := res.Choices[0].GenerationInfo
	if info == nil {
		return
	}

	// A single run makes several LLM calls (planner, executor, verifier, and any
	// tool-assisted iterations). Accumulate token counts across calls so the run
	// metrics reflect the whole run rather than only the last call. LastPromptTokens
	// keeps the largest single-call prompt size for the compression heuristic.
	if v, ok := usageMetricInt(info["prompt_tokens"]); ok {
		metrics.PromptTokens += v
		if v > metrics.LastPromptTokens {
			metrics.LastPromptTokens = v
		}
	}
	if v, ok := usageMetricInt(info["completion_tokens"]); ok {
		metrics.CompletionTokens += v
	}
	if v, ok := usageMetricInt(info["total_tokens"]); ok {
		metrics.TotalTokens += v
	}
}

func usageMetricInt(value any) (int, bool) {
	switch typed := value.(type) {
	case int:
		return typed, true
	case int32:
		return int(typed), true
	case int64:
		return int(typed), true
	case float64:
		return int(typed), true
	default:
		return 0, false
	}
}

func (h *runtimeCallbackHandler) HandleLLMError(ctx context.Context, err error) {}

func (h *runtimeCallbackHandler) HandleChainStart(ctx context.Context, inputs map[string]any) {}

func (h *runtimeCallbackHandler) HandleChainEnd(ctx context.Context, outputs map[string]any) {}

func (h *runtimeCallbackHandler) HandleChainError(ctx context.Context, err error) {}

func (h *runtimeCallbackHandler) HandleToolStart(ctx context.Context, input string) {}

func (h *runtimeCallbackHandler) HandleToolEnd(ctx context.Context, output string) {
	if h.logger != nil {
		h.logger.Info("Tool result: %s", truncateForLog(output, 240))
	}
	if h.eventHandler != nil {
		action, ok := h.popPendingAction()
		if ok {
			h.eventHandler(RunEvent{
				Type:      "tool_result",
				EpisodeID: h.episodeID,
				ToolName:  action.Tool,
				ToolInput: normalizeToolInput(action.ToolInput),
				Content:   output,
				Timestamp: time.Now(),
			})
		}
	}
}

func (h *runtimeCallbackHandler) HandleToolError(ctx context.Context, err error) {
	if h.logger != nil {
		h.logger.Error("Tool error: %v", err)
	}
	if h.eventHandler != nil {
		action, ok := h.popPendingAction()
		if ok {
			h.eventHandler(RunEvent{
				Type:      "tool_result",
				EpisodeID: h.episodeID,
				ToolName:  action.Tool,
				ToolInput: normalizeToolInput(action.ToolInput),
				Content:   "error: " + err.Error(),
				Timestamp: time.Now(),
				IsError:   true,
			})
		}
	}
}

func (h *runtimeCallbackHandler) HandleNamedToolStart(ctx context.Context, name, input string) {}

func (h *runtimeCallbackHandler) HandleNamedToolEnd(ctx context.Context, name, input, output string) {
	input = normalizeToolInput(input)
	if h.logger != nil {
		h.logger.Info("Tool result: name=%s output=%s", name, truncateForLog(output, 240))
	}
	h.removePendingAction(name, input)
	if h.eventHandler != nil {
		h.eventHandler(RunEvent{
			Type:      "tool_result",
			EpisodeID: h.episodeID,
			ToolName:  name,
			ToolInput: input,
			Content:   output,
			Timestamp: time.Now(),
			IsError:   toolOutputLooksLikeError(output),
		})
	}
}

func (h *runtimeCallbackHandler) HandleNamedToolError(ctx context.Context, name, input string, err error) {
	input = normalizeToolInput(input)
	if h.logger != nil {
		h.logger.Error("Tool error: name=%s err=%v", name, err)
	}
	h.removePendingAction(name, input)
	if h.eventHandler != nil {
		h.eventHandler(RunEvent{
			Type:      "tool_result",
			EpisodeID: h.episodeID,
			ToolName:  name,
			ToolInput: input,
			Content:   "error: " + err.Error(),
			Timestamp: time.Now(),
			IsError:   true,
		})
	}
}

func (h *runtimeCallbackHandler) HandleToolCallStart(ctx context.Context, call ToolCall) {
	description := call.Description
	speech := call.Speech
	if h.logger != nil {
		if description != "" && speech != "" {
			h.logger.Info("Tool call: name=%s input=%s description=%s speech=%s",
				call.Spec.Name, truncateForLog(call.Input, 240), truncateForLog(description, 240), truncateForLog(speech, 120))
		} else if description != "" {
			h.logger.Info("Tool call: name=%s input=%s description=%s",
				call.Spec.Name, truncateForLog(call.Input, 240), truncateForLog(description, 240))
		} else if speech != "" {
			h.logger.Info("Tool call: name=%s input=%s speech=%s",
				call.Spec.Name, truncateForLog(call.Input, 240), truncateForLog(speech, 120))
		} else {
			h.logger.Info("Tool call: name=%s input=%s",
				call.Spec.Name, truncateForLog(call.Input, 240))
		}
	}
	if h.eventHandler != nil {
		h.eventHandler(RunEvent{
			Type:        runEventToolCall,
			EpisodeID:   h.episodeID,
			ToolName:    call.Spec.Name,
			ToolInput:   call.Input,
			Description: description,
			Speech:      h.toolCallSpeech(speech),
			Content:     description,
			Timestamp:   time.Now(),
		})
	}
}

func (h *runtimeCallbackHandler) BeforeToolCall(ctx context.Context, call ToolCall) (ToolResult, bool) {
	return DefaultBeforeToolCall(ctx, call)
}

func (h *runtimeCallbackHandler) AfterToolCall(ctx context.Context, call ToolCall, result ToolResult) ToolResult {
	return DefaultAfterToolCall(ctx, call, result)
}

func (h *runtimeCallbackHandler) HandleToolCallResult(ctx context.Context, call ToolCall, result ToolResult) {
	output := result.EventOutput()
	if h.logger != nil {
		if result.IsError {
			h.logger.Error("Tool result: name=%s output=%s", call.Spec.Name, truncateForLog(output, 240))
		} else {
			h.logger.Info("Tool result: name=%s output=%s", call.Spec.Name, truncateForLog(output, 240))
		}
	}
	if h.eventHandler != nil {
		h.eventHandler(RunEvent{
			Type:      "tool_result",
			EpisodeID: h.episodeID,
			ToolName:  call.Spec.Name,
			ToolInput: call.Input,
			Content:   output,
			Timestamp: time.Now(),
			IsError:   result.IsError,
		})
	}
}

func (h *runtimeCallbackHandler) HandleAgentAction(ctx context.Context, action schema.AgentAction) {
	description := toolDescriptionFromAction(action)
	speech := toolSpeechFromAction(action)
	if h.logger != nil {
		if description != "" && speech != "" {
			h.logger.Info("Tool call: name=%s input=%s description=%s speech=%s",
				action.Tool, truncateForLog(action.ToolInput, 240), truncateForLog(description, 240), truncateForLog(speech, 120))
		} else if description != "" {
			h.logger.Info("Tool call: name=%s input=%s description=%s",
				action.Tool, truncateForLog(action.ToolInput, 240), truncateForLog(description, 240))
		} else if speech != "" {
			h.logger.Info("Tool call: name=%s input=%s speech=%s",
				action.Tool, truncateForLog(action.ToolInput, 240), truncateForLog(speech, 120))
		} else {
			h.logger.Info("Tool call: name=%s input=%s",
				action.Tool, truncateForLog(action.ToolInput, 240))
		}
	}
	if h.eventHandler != nil {
		h.pushPendingAction(action)
		h.eventHandler(RunEvent{
			Type:        runEventToolCall,
			EpisodeID:   h.episodeID,
			ToolName:    action.Tool,
			ToolInput:   action.ToolInput,
			Description: description,
			Speech:      h.toolCallSpeech(speech),
			Content:     description,
			Timestamp:   time.Now(),
		})
	}
}

func (h *runtimeCallbackHandler) HandleAgentFinish(ctx context.Context, finish schema.AgentFinish) {}

func (h *runtimeCallbackHandler) HandleTodoUpdate(ctx context.Context, todo TodoState, content string, speechEligible bool) {
	content = strings.TrimSpace(content)
	snapshot := todo.Clone()
	if h.logger != nil {
		h.logger.Info("Todo update: mode=%s revision=%d current_id=%s speech_eligible=%v content=%s",
			snapshot.Mode, snapshot.Revision, snapshot.CurrentID, speechEligible, truncateForLog(content, 240))
	}
	if h.eventHandler != nil {
		h.eventHandler(RunEvent{
			Type:           runEventTodoUpdate,
			EpisodeID:      h.episodeID,
			Content:        content,
			Todo:           &snapshot,
			SpeechEligible: speechEligible,
			Timestamp:      time.Now(),
		})
	}
}

func (h *runtimeCallbackHandler) HandleTodoClosed(ctx context.Context, todo TodoState, reason string) {
	reason = strings.TrimSpace(reason)
	snapshot := todo.Clone()
	if h.logger != nil {
		h.logger.Info("Todo closed: mode=%s revision=%d current_id=%s reason=%s",
			snapshot.Mode, snapshot.Revision, snapshot.CurrentID, truncateForLog(reason, 240))
	}
	if h.eventHandler != nil {
		h.eventHandler(RunEvent{
			Type:      runEventTodoClosed,
			EpisodeID: h.episodeID,
			Content:   reason,
			Todo:      &snapshot,
			Timestamp: time.Now(),
		})
	}
}

func (h *runtimeCallbackHandler) HandleRoleOutput(ctx context.Context, role, content string) {
	content = strings.TrimSpace(content)
	if h.logger != nil {
		h.logger.Info("Role output: role=%s content=%s", role, truncateForLog(content, 1000))
	}
	if h.eventHandler != nil {
		h.eventHandler(RunEvent{
			Type:      "role_output",
			Role:      role,
			EpisodeID: h.episodeID,
			Content:   content,
			Timestamp: time.Now(),
		})
	}
}

func (h *runtimeCallbackHandler) HandleSteerMessage(ctx context.Context, steer RunSteerMessage) {
	if h.eventHandler == nil {
		return
	}
	if steer.Timestamp.IsZero() {
		steer.Timestamp = time.Now()
	}
	h.eventHandler(RunEvent{
		Type:      "steer",
		EpisodeID: h.episodeID,
		Content:   steer.Content,
		Timestamp: steer.Timestamp,
	})
}

func (h *runtimeCallbackHandler) HandleRetrieverStart(ctx context.Context, query string) {}

func (h *runtimeCallbackHandler) HandleRetrieverEnd(ctx context.Context, query string, documents []schema.Document) {
}

func (h *runtimeCallbackHandler) HandleStreamingFunc(ctx context.Context, chunk []byte) {
	finalStreaming := h.finalStreamingEnabled()
	if h.writer != nil && finalStreaming && !h.finalStreamingFailed() {
		if _, err := h.writer.Write(chunk); err != nil {
			h.recordFinalStreamError(err)
		} else if streamWriterEmitted(h.writer) {
			h.recordFinalToken()
		}
	}

	// Record first token time
	h.recordFirstToken()
}

func (h *runtimeCallbackHandler) EnableFinalStreaming(ctx context.Context) {
	resetStreamWriterState(h.writer)
	h.mu.Lock()
	h.finalTokenSeen = false
	h.finalStreamErr = nil
	h.streamFinal = true
	h.mu.Unlock()
}

func (h *runtimeCallbackHandler) DisableFinalStreaming(ctx context.Context) {
	h.mu.Lock()
	h.streamFinal = false
	h.mu.Unlock()
}

func (h *runtimeCallbackHandler) finalStreamingEnabled() bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.streamFinal
}

func (h *runtimeCallbackHandler) ProviderFinalStreamingEnabled() bool {
	return h != nil && h.writer != nil && h.providerFinalStreaming
}

type streamStateResetter interface {
	ResetStreamState()
}

type streamOutputTracker interface {
	StreamEmitted() bool
}

func resetStreamWriterState(writer io.Writer) {
	resetter, ok := writer.(streamStateResetter)
	if !ok {
		return
	}
	resetter.ResetStreamState()
}

func streamWriterEmitted(writer io.Writer) bool {
	tracker, ok := writer.(streamOutputTracker)
	if !ok {
		return true
	}
	return tracker.StreamEmitted()
}

func (h *runtimeCallbackHandler) HasFinalStreamingToken(context.Context) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.finalTokenSeen && h.finalStreamErr == nil
}

func (h *runtimeCallbackHandler) recordFirstToken() {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.firstTokenSeen {
		return
	}
	h.firstTokenSeen = true
	if h.metrics != nil {
		h.metrics.FirstTokenTime = float64(time.Since(h.startTime).Milliseconds())
	}
}

func (h *runtimeCallbackHandler) recordFinalToken() {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.finalStreamErr != nil {
		return
	}
	h.finalTokenSeen = true
}

func (h *runtimeCallbackHandler) finalStreamingFailed() bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.finalStreamErr != nil
}

func (h *runtimeCallbackHandler) recordFinalStreamError(err error) {
	if err == nil {
		return
	}
	h.mu.Lock()
	firstErr := h.finalStreamErr == nil
	if firstErr {
		h.finalStreamErr = err
	}
	h.finalTokenSeen = false
	h.mu.Unlock()
	if firstErr && h.logger != nil {
		h.logger.Warn("Final stream writer failed; falling back to final answer: %v", err)
	}
}

var _ callbacks.Handler = (*runtimeCallbackHandler)(nil)

func (h *runtimeCallbackHandler) toolCallSpeech(speech string) string {
	if h == nil || !h.toolCallSpeechEnabled {
		return ""
	}
	return strings.TrimSpace(speech)
}

func (h *runtimeCallbackHandler) pushPendingAction(action schema.AgentAction) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.pendingActions = append(h.pendingActions, action)
}

func (h *runtimeCallbackHandler) popPendingAction() (schema.AgentAction, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if len(h.pendingActions) == 0 {
		return schema.AgentAction{}, false
	}
	action := h.pendingActions[0]
	h.pendingActions = h.pendingActions[1:]
	return action, true
}

func (h *runtimeCallbackHandler) removePendingAction(name, input string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	normalizedInput := normalizeToolInput(input)
	for i, action := range h.pendingActions {
		if strings.EqualFold(action.Tool, name) && normalizeToolInput(action.ToolInput) == normalizedInput {
			h.pendingActions = append(h.pendingActions[:i], h.pendingActions[i+1:]...)
			return
		}
	}
}

func normalizeToolInput(input string) string {
	return strings.TrimSuffix(input, "\nObservation:")
}

func truncateForLog(text string, max int) string {
	if max <= 0 || text == "" {
		return text
	}
	if utf8.RuneCountInString(text) <= max {
		return text
	}
	runes := []rune(text)
	return string(runes[:max]) + "..."
}

func buildLLMSummarizeFn(models ModelResolver) SummarizeFn {
	return func(ctx context.Context, events []SessionEvent) string {
		model, err := models.Get()
		if err != nil {
			return ""
		}
		var transcript strings.Builder
		for _, evt := range events {
			if evt.Content == "" {
				continue
			}
			transcript.WriteString(fmt.Sprintf("[%s] %s\n", evt.Role, evt.Content))
		}
		if transcript.Len() == 0 {
			return ""
		}
		prompt := "Summarize this conversation in 2-3 concise sentences. Focus on what was discussed, decided, or requested. Write in the same language as the conversation.\n\n" + transcript.String()
		result, err := llms.GenerateFromSinglePrompt(ctx, model, prompt, llms.WithMaxTokens(200))
		if err != nil {
			return ""
		}
		return strings.TrimSpace(result)
	}
}

const structuredSummarizerPrompt = `Summarize this conversation chunk as STRICT JSON only. Do not wrap in markdown.
Schema:
{
  "summary": "1 concise sentence in the same language as the conversation",
  "user_goals": [],
  "confirmed_facts": [],
  "decisions": [],
  "proposals": [],
  "open_tasks": [],
  "risks_or_pitfalls": [],
  "memory_candidates": []
}
Rules:
- Distinguish implemented/verified facts from proposals.
- Put assistant suggestions or unimplemented designs in proposals, not confirmed_facts.
- Put only explicit user-approved choices or clearly completed outcomes in decisions.
- Put unfinished work in open_tasks.
- memory_candidates must be durable user/project facts worth future recall; omit transient todos and assistant speculation.
- Keep each list item short. Empty lists are allowed.

Transcript:
`

const (
	structuredSummaryMaxItems       = 16
	structuredSummaryMaxItemRunes   = 240
	structuredSummaryMaxSummaryRune = 480
)

func buildLLMStructuredSummarizeFn(models ModelResolver, logger *Logger) StructuredSummarizeFn {
	return func(ctx context.Context, events []SessionEvent) ChunkStructuredSummary {
		model, err := models.Get()
		if err != nil {
			if logger != nil {
				logger.Warn("[memory] structured summary: failed to get model: %v", err)
			}
			return ChunkStructuredSummary{}
		}
		var transcript strings.Builder
		for _, evt := range events {
			content := strings.TrimSpace(evt.Content)
			if content == "" {
				continue
			}
			transcript.WriteString(fmt.Sprintf("[%s:%s] %s\n", evt.Role, evt.Type, content))
		}
		if transcript.Len() == 0 {
			return ChunkStructuredSummary{}
		}
		result, err := llms.GenerateFromSinglePrompt(ctx, model, structuredSummarizerPrompt+transcript.String(), llms.WithMaxTokens(800))
		if err != nil {
			if logger != nil {
				logger.Warn("[memory] structured summary: LLM generation failed: %v", err)
			}
			return ChunkStructuredSummary{}
		}
		structured, err := parseChunkStructuredSummaryJSON(result)
		if err != nil {
			if logger != nil {
				logger.Warn("[memory] structured summary: JSON parse failed: %v", err)
			}
			return ChunkStructuredSummary{}
		}
		return structured
	}
}

func parseChunkStructuredSummaryJSON(text string) (ChunkStructuredSummary, error) {
	jsonText := strings.TrimSpace(text)
	var structured ChunkStructuredSummary
	decoder := json.NewDecoder(strings.NewReader(jsonText))
	if err := decoder.Decode(&structured); err != nil {
		return ChunkStructuredSummary{}, err
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return ChunkStructuredSummary{}, fmt.Errorf("structured summary must contain a single JSON object")
	}
	structured.Summary = capRunes(strings.TrimSpace(structured.Summary), structuredSummaryMaxSummaryRune)
	structured.UserGoals = cleanStringList(structured.UserGoals)
	structured.ConfirmedFacts = cleanStringList(structured.ConfirmedFacts)
	structured.Decisions = cleanStringList(structured.Decisions)
	structured.Proposals = cleanStringList(structured.Proposals)
	structured.OpenTasks = cleanStringList(structured.OpenTasks)
	structured.RisksOrPitfalls = cleanStringList(structured.RisksOrPitfalls)
	structured.MemoryCandidates = cleanStringList(structured.MemoryCandidates)
	return structured, nil
}

func cleanStringList(values []string) []string {
	cleaned := make([]string, 0, len(values))
	for _, value := range values {
		value = capRunes(strings.TrimSpace(value), structuredSummaryMaxItemRunes)
		if value == "" {
			continue
		}
		cleaned = append(cleaned, value)
		if len(cleaned) >= structuredSummaryMaxItems {
			break
		}
	}
	return cleaned
}

func capRunes(text string, max int) string {
	if max <= 0 || text == "" {
		return text
	}
	if utf8.RuneCountInString(text) <= max {
		return text
	}
	runes := []rune(text)
	return string(runes[:max])
}

func buildLLMProfileFn(models ModelResolver) ProfileFn {
	return func(ctx context.Context, entries []ProfileEntry) string {
		model, err := models.Get()
		if err != nil {
			return ""
		}
		var input strings.Builder
		for _, e := range entries {
			input.WriteString(fmt.Sprintf("[%s] %s\n", e.Type, e.Content))
		}
		if input.Len() == 0 {
			return ""
		}
		prompt := `Based on the following memory entries about a user, synthesize a concise user profile.
Rules:
- Only include information directly about the user (identity, role, preferences, habits, rules they set).
- Discard transient facts, one-time events, or information not useful for future interactions.
- Group related information under clear headings.
- Keep it concise — no more than 10 lines total.
- Write in the same language as the entries.
- Output markdown starting with "# User Profile".

Memory entries:
` + input.String()
		result, err := llms.GenerateFromSinglePrompt(ctx, model, prompt, llms.WithMaxTokens(400))
		if err != nil {
			return ""
		}
		return strings.TrimSpace(result)
	}
}

// Close releases resources held by the runtime
func (r *Runtime) Close() error {
	if r.mergeWorker != nil {
		r.mergeWorker.Stop()
	}
	if r.memories != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		if err := r.memories.WaitMaintenance(ctx); err != nil && r.logger != nil {
			r.logger.Error("memory maintenance drain on close: %v", err)
		}
		cancel()
	}
	if r.profileDebouncer != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		if err := r.profileDebouncer.Flush(ctx); err != nil && r.logger != nil {
			r.logger.Error("profile debouncer flush on close: %v", err)
		}
		cancel()
	}
	if r.logger != nil {
		r.logger.Info("Shutting down agent runtime")
		return r.logger.Close()
	}
	return nil
}
