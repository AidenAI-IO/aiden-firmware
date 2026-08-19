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

	"aiden-agent/internal/agent/agentpath"
	"aiden-agent/internal/agent/compactor"
	"aiden-agent/internal/agent/contextmanager"
	"aiden-agent/internal/agent/messages"
	"aiden-agent/internal/agent/model"
	"aiden-agent/internal/agent/screen"
	"aiden-agent/internal/agent/speech"
	"aiden-agent/internal/agent/statemanager"
	"aiden-agent/internal/agent/tokencounter"
	"aiden-agent/internal/agent/tts"
	"aiden-agent/internal/util"

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

const (
	currentEnvironmentHintMaxAge      = 10 * time.Minute
	runtimeSessionEventPersistTimeout = 2 * time.Second
	runtimeTTSCloseTimeout            = 5 * time.Second
	runtimeEpisodeMaintenanceTimeout  = 10 * time.Second
	maxPublicToolResultRunes          = maxToolObservationRunes
)

type Runtime struct {
	config             Config
	models             model.Model
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
	waitForWakeup      *WaitForWakeupController
	voiceNotifications *VoiceNotificationManager
	memoryPlane        MemoryPlane
	sessionManager     SessionManager
	contextManager     *contextmanager.ContextManager
	stateManager       *statemanager.StateManager
	runtimeID          string
	telemetrySessionID string
	runGate            chan struct{}
	preemptMu          sync.Mutex
	activeCancel       context.CancelFunc
	preemptHooks       []func()
	lastPreemptTime    time.Time
	storage            *StorageManager
	screenState        *screen.ScreenState
	phoneBridge        *PhoneBridge
	storageMonitor     *StorageMonitor
	ttsManager         *tts.ProviderManager
	ttsManagerOnce     sync.Once
	episodeMaintenance asyncEpisodeMaintenance
}

type asyncEpisodeMaintenance struct {
	mu      sync.Mutex
	wg      sync.WaitGroup
	closing bool
	ctx     context.Context
	cancel  context.CancelFunc
}

type RunRequest struct {
	Input             string
	Attachments       []InputAttachment
	Turn              TurnInput
	EpisodeID         string
	RequestID         string
	RuntimeContext    string
	DeviceEnvironment *PhoneEnvironment
	StreamWriter      io.Writer
	MaxTokens         int
	EventHandler      func(RunEvent)
	// OnRunActive runs after the previous run and its resources have been
	// preempted and this run owns the runtime gate. Callers can register
	// request-scoped output here without that output being interrupted by this
	// run's own startup preemption.
	OnRunActive   func(context.Context)
	SteerProvider func(context.Context) (RunSteerMessage, bool)
	// FinalSteerProvider is called at terminal executor boundaries. It should
	// atomically consume one last pending steer if available; otherwise it should
	// close the request's steer acceptance window.
	FinalSteerProvider func(context.Context) (RunSteerMessage, bool)
	// SteerInterrupt signals that the current run should pause before scheduling
	// more model/tool work while an out-of-band steering input is being captured.
	SteerInterrupt func() <-chan struct{}
	// SteerWaiter waits for the out-of-band steering capture to resolve. ok=false
	// means the interruption produced no usable input and the run may continue.
	SteerWaiter func(context.Context) (RunSteerMessage, bool, error)
	// AsyncEpisodeMaintenance keeps the episode trace write synchronous but moves
	// lesson extraction and referenced-memory maintenance off the response path.
	AsyncEpisodeMaintenance bool
}

type RunResult struct {
	Output                 string          `json:"output"`
	EpisodeID              string          `json:"episode_id,omitempty"`
	Memory                 []MessageRecord `json:"memory,omitempty"`
	Metrics                *RunMetrics     `json:"metrics,omitempty"`
	WaitForWakeupRequested bool            `json:"wait_for_wakeup_requested,omitempty"`
	WaitForWakeupReason    string          `json:"wait_for_wakeup_reason,omitempty"`
	// Deprecated: use WaitForWakeupRequested.
	SleepRequested bool `json:"sleep_requested,omitempty"`
	// Deprecated: use WaitForWakeupReason.
	SleepReason    string       `json:"sleep_reason,omitempty"`
	SpeechStreamed bool         `json:"-"`
	Preempted      bool         `json:"preempted,omitempty"`
	TurnFailure    *TurnFailure `json:"-"`
}

func canonicalTurnInputFromRunRequest(req RunRequest) TurnInput {
	turn := normalizeTurnInput(req.Turn)
	if turn.InputText == "" && turn.Modality == TurnModalityText && len(turn.Attachments) == 0 && len(turn.Artifacts) == 0 && len(turn.TelemetryEvents) == 0 && turn.Transcript == "" && turn.OriginalText == "" {
		return NewTextTurnInput(req.Input, req.Attachments)
	}
	if len(turn.Attachments) == 0 && len(req.Attachments) > 0 {
		turn.Attachments = cloneInputAttachments(req.Attachments)
	}
	if turn.OriginalText == "" {
		turn.OriginalText = strings.TrimSpace(req.Input)
	}
	if turn.InputText == "" {
		turn.InputText = normalizeRunInput(req.Input, turn.Attachments)
	}
	return normalizeTurnInput(turn)
}

func episodeStartTimeWithEvents(fallback time.Time, events []TaskEpisodeEvent) time.Time {
	start := fallback
	for _, event := range events {
		eventTime, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(event.Ts))
		if err != nil || eventTime.IsZero() {
			continue
		}
		eventTime = eventTime.UTC()
		if start.IsZero() || eventTime.Before(start) {
			start = eventTime
		}
	}
	return start
}

func recordPreRunEpisodeEvents(recorder *EpisodeRecorder, events []TaskEpisodeEvent) {
	if recorder == nil {
		return
	}
	for _, event := range events {
		recorder.RecordEvent(event)
	}
}

func (r RunResult) SpokenText() string {
	return speech.BuildText(r.Output)
}

func (r RunResult) SpokenTextForConfig(cfg Config) string {
	if r.WaitForWakeupRequested {
		return ""
	}
	return speech.BuildText(r.Output)
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
	ReasoningTokens  int     `json:"reasoning_tokens,omitempty"`
	ContextWindow    int     `json:"context_window,omitempty"`
	FirstTokenTime   float64 `json:"first_token_time_ms,omitempty"`
	ToolCount        int     `json:"tool_count,omitempty"`
	// CachedPromptTokens accumulates provider-reported cached prompt tokens
	// (usage.prompt_tokens_details.cached_tokens) across the run. The prompt-cache
	// hit rate is CachedPromptTokens / PromptTokens.
	CachedPromptTokens int `json:"cached_prompt_tokens,omitempty"`
	// LastPromptTokens holds the largest single prompt-token count in the run.
	// PromptTokens/CompletionTokens/TotalTokens accumulate across model calls in
	// a single run, but the compression heuristic needs the size of one prompt
	// relative to the context window, not the cumulative sum.
	LastPromptTokens int `json:"-"`
}

// CacheHitRate returns the prompt-cache hit ratio in [0,1]: cached prompt tokens
// over total prompt tokens. It returns 0 when no prompt tokens were recorded.
func (m *RunMetrics) CacheHitRate() float64 {
	if m == nil || m.PromptTokens <= 0 {
		return 0
	}
	rate := float64(m.CachedPromptTokens) / float64(m.PromptTokens)
	if rate < 0 {
		return 0
	}
	if rate > 1 {
		return 1
	}
	return rate
}

const (
	runEventToolCall                       = "tool_call"
	runEventSTTTranscription               = "stt_transcription"
	runEventVoicePromptSound               = "voice_prompt_sound"
	runEventTTSStreamPreopen               = "tts_stream_preopen"
	runEventMemoryRetrieve                 = "memory_retrieve"
	runEventSessionBegin                   = "session_begin"
	runEventIterationStart                 = "iteration_start"
	runEventIterationEnd                   = "iteration_end"
	runEventLoopGuardStop                  = "loop_guard_stop"
	runEventToolResultContext              = "tool_result_context"
	runEventContextBudget                  = "context_budget"
	runEventHistoricalToolResultCompaction = "historical_tool_result_compaction"
	runEventModelRequestFailure            = "model_request_failure"
)

type RunEvent struct {
	Type           string     `json:"type"`
	Role           string     `json:"role,omitempty"`
	EpisodeID      string     `json:"episode_id,omitempty"`
	ToolCallID     string     `json:"tool_call_id,omitempty"`
	ToolName       string     `json:"tool_name,omitempty"`
	ToolInput      string     `json:"tool_input,omitempty"`
	Content        string     `json:"content,omitempty"`
	SpeechEligible bool       `json:"speech_eligible,omitempty"`
	Timestamp      time.Time  `json:"timestamp"`
	IsError        bool       `json:"is_error,omitempty"`
	ToolError      *ToolError `json:"tool_error,omitempty"`
}

type usageTrackingModel struct {
	inner           model.Model
	metrics         *RunMetrics
	promptCapture   *telemetryPromptCapture
	contextWindowFn ContextWindowFn
}

func (m *usageTrackingModel) CallOptions() []chains.ChainCallOption { return m.inner.CallOptions() }

func (m *usageTrackingModel) Spec() model.ModelSpec {
	if m == nil || m.inner == nil {
		return model.ModelSpec{}
	}
	spec := m.inner.Spec()
	if spec.ContextWindow <= 0 {
		spec.ContextWindow = m.contextWindow()
	}
	return spec
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

// GenerateContentFromMessageList forwards provider-specific persisted context
// through the usage wrapper. Responses models use this to replay opaque
// reasoning items when store=false; ordinary models retain the standard path.
func (m *usageTrackingModel) GenerateContentFromMessageList(ctx context.Context, messageList []messages.Message, options ...llms.CallOption) (*llms.ContentResponse, error) {
	inner, ok := m.inner.(interface {
		GenerateContentFromMessageList(context.Context, []messages.Message, ...llms.CallOption) (*llms.ContentResponse, error)
	})
	if !ok {
		return m.GenerateContent(ctx, messages.ConvertMessageList(messageList), options...)
	}

	startedAt := time.Now()
	res, err := inner.GenerateContentFromMessageList(ctx, messageList, options...)
	if err == nil {
		recordUsageMetrics(m.metrics, res)
	}
	if m.promptCapture != nil {
		m.promptCapture.Record(ctx, startedAt, time.Now(), messages.ConvertMessageList(messageList), options, res, err, m.contextWindow())
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
		if spec := m.contextWindowFn(); spec.ContextWindow > 0 {
			return spec.ContextWindow
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
		logger, err = NewLogger(cfg.ConfigDir, cfg.Log.LLMHTTPRetentionDaysOrDefault())
		if err != nil {
			return nil, fmt.Errorf("create logger: %w", err)
		}
		logger.Info("Agent runtime initialized with config from %s", cfg.ConfigDir)
	}

	memoryDir := ""
	if cfg.ConfigDir != "" {
		memoryDir = filepath.Join(cfg.ConfigDir, "memory")
	}

	waitForWakeupController := NewWaitForWakeupController()
	proxy := ProxyConfigFromEnvironment()
	if err := proxy.Validate(); err != nil {
		return nil, fmt.Errorf("proxy environment: %w", err)
	}
	screenState := &screen.ScreenState{}
	toolOptions := []BuiltinToolSetOption{
		WithScreenState(screenState),
		WithWaitForWakeupController(waitForWakeupController),
		WithScreenStableDefaults(cfg.ScreenStableDefaults()),
	}
	pythonErr := prepareManagedPythonPaths(managedPythonRoot, managedPythonTmp)
	if pythonErr != nil {
		if logger != nil {
			logger.Warn("managed python environment unavailable: %v", pythonErr)
		} else {
			log.Printf("managed python environment unavailable: %v", pythonErr)
		}
	} else {
		toolOptions = append(toolOptions, WithShellTemporaryDirectory(managedPythonTmp))
	}
	toolSet := NewBuiltinToolSetFromConfig(
		cfg,
		proxy,
		toolOptions...,
	)
	extractionCfg := LoadMemoryExtractionConfig(cfg.ConfigDir)
	modelManagerOptions := []ModelManagerOption{}
	if cfg.ConfigDir != "" {
		modelManagerOptions = append(modelManagerOptions, WithProviderModelMetadataCachePath(filepath.Join(cfg.ConfigDir, "cache", "provider_model_metadata.json")))
		if cfg.Model.LogRawHTTP {
			modelManagerOptions = append(modelManagerOptions, WithLLMRawHTTPLogDir(filepath.Join(cfg.ConfigDir, "log")))
		}
	}
	modelManager := NewModelManager(cfg.Model, proxy, modelManagerOptions...)
	modelManager.prefetchProviderModelSpecIfNeeded()
	summarizeFn := buildLLMSummarizeFn(modelManager)
	structuredSummarizeFn := buildLLMStructuredSummarizeFn(modelManager, logger)
	profileFn := buildLLMProfileFn(modelManager)
	contextWindowFn := func() model.ModelSpec { return modelManager.Spec() }

	longTermDir := ""
	if memoryDir != "" {
		longTermDir = filepath.Join(memoryDir, "long_term")
	}
	var debouncer *ProfileDebouncer
	var longTermStore *LongTermMemoryStore
	if longTermDir != "" {
		longTermStore = NewLongTermMemoryStore(longTermDir, WithLifecycleDir(filepath.Join(memoryDir, "lifecycle")), WithStoreProfileFn(profileFn))
		debouncer = NewProfileDebouncer(longTermStore.RegenerateProfileMD, 60*time.Second, logger)
		longTermStore.setProfileDebouncer(debouncer)
	}

	toolSet.RegisterMemoryTools(memoryDir, extractionCfg.SummaryMaxChunks, longTermStore)
	toolSet.RegisterEnterTextTool(modelManager, nil) // deviceTypeFn set after runtime construction

	rt := NewRuntimeWithDeps(cfg, modelManager, NewMemoryManager(memoryDir, WithExtractionConfig(extractionCfg), WithSummarizeFn(summarizeFn), WithStructuredSummarizeFn(structuredSummarizeFn), WithProfileFn(profileFn), WithContextWindowFn(contextWindowFn), WithMemoryProfileDebouncer(debouncer), WithLongTermMemoryStore(longTermStore), WithMemoryLogger(logger)), toolSet, skillIndex)

	// Register skill tools after the runtime exists so skill_manage can mark the
	// skill index dirty. The updated index is reloaded at the start of the next run.
	if cfg.ConfigDir != "" {
		skillsDir := filepath.Join(cfg.ConfigDir, "skills")
		manifestPath := filepath.Join(cfg.ConfigDir, "skill-state", ".bundled_manifest.json")
		toolSet.RegisterSkillToolsWithDeviceType(skillsDir, manifestPath, rt.deviceTypeFromState, rt.MarkSkillsDirty)
	}
	rt.logger = logger
	rt.profileDebouncer = debouncer
	rt.waitForWakeup = waitForWakeupController
	rt.storageMonitor = newRuntimeStorageMonitor(cfg, logger, rt.memories)
	rt.SetVoiceNotificationSink(rt.VoiceNotificationSink())
	modelManager.SetStorageMonitor(rt.storageMonitor)
	if rt.memories != nil {
		rt.memories.SetStorageMonitor(rt.storageMonitor)
	}

	// Log runtime start marker.
	if logger != nil {
		logger.Info("[runtime] started: runtime_id=%s", rt.runtimeID)
	}

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

	rt.memoryPlane = NewFilesystemMemoryPlane(memoryDir, extractionCfg, logger, WithMemoryPlaneLongTermStore(longTermStore))
	rt.markInterruptedEpisodesBestEffort()

	// Start the SD/eMMC storage manager on every device. Missing or unusable
	// card hardware degrades to eMMC-only operation.
	rt.storage = NewStorageManager(cfg.Storage, logger)
	rt.storage.Start()

	rt.screenState = screenState
	rt.phoneBridge = NewPhoneBridge(logger)
	rt.phoneBridge.SetConfiguredPlatform(cfg.DevicePlatformOrDefault())
	rt.stateManager.RegisterUpdater(screenState)
	rt.stateManager.RegisterUpdater(rt.phoneBridge)

	return rt, nil
}

// Storage returns the SD/eMMC storage manager, or nil when the runtime was
// built without one (NewRuntimeWithDeps).
func (r *Runtime) Storage() *StorageManager {
	return r.storage
}

func (r *Runtime) PhoneBridge() *PhoneBridge {
	return r.phoneBridge
}

// ttsProviderManager returns the process-wide provider manager shared by all
// TTS entrypoints. The stable manager exists even when TTS is not configured so
// a runtime provider switch is immediately visible to every consumer.
func (r *Runtime) ttsProviderManager() *tts.ProviderManager {
	if r == nil {
		return nil
	}
	r.ttsManagerOnce.Do(func() {
		if r.ttsManager != nil {
			return
		}
		manager, err := newTTSProviderManagerFromConfig(r.config, r.logger)
		if err != nil {
			if r.logger != nil {
				r.logger.Warn("TTS init failed: %v", err)
			} else {
				log.Printf("[tts] init failed, continuing without TTS: %v\n", err)
			}
		}
		if manager == nil {
			manager = tts.NewProviderManager(nil, &ttsLoggerAdapter{logger: r.logger})
		}
		r.ttsManager = manager
	})
	return r.ttsManager
}

func NewRuntimeWithDeps(cfg Config, models model.Model, memories *MemoryManager, tools *ToolSet, skillIndex *SkillIndex) *Runtime {
	waitForWakeupController := NewWaitForWakeupController()
	if tools != nil {
		if tool, ok := tools.Get(toolWaitForWakeup); ok {
			if waitTool, ok := tool.(*WaitForWakeupTool); ok && waitTool.controller != nil {
				waitForWakeupController = waitTool.controller
			}
		}
	}
	skillManager := NewSkillManager(skillIndex)
	if cfg.ConfigDir != "" {
		skillManager.SetUsagePath(filepath.Join(cfg.ConfigDir, "skill-state", "usage.json"))
	}

	var memoryDir string
	var longTermStore *LongTermMemoryStore
	if cfg.ConfigDir != "" {
		memoryDir = filepath.Join(cfg.ConfigDir, "memory")
		if memories != nil {
			// Use sync.Once to ensure exactly one goroutine creates the fallback store
			memories.longTermOnce.Do(func() {
				if memories.longTerm == nil {
					storeOpts := []LongTermMemoryOption{WithLifecycleDir(filepath.Join(memoryDir, "lifecycle"))}
					storeOpts = append(storeOpts, WithStoreProfileFn(memories.profileFn))
					store := NewLongTermMemoryStore(filepath.Join(memoryDir, "long_term"), storeOpts...)
					store.setProfileDebouncer(memories.profileDebouncer)
					memories.longTerm = store
				}
			})
			longTermStore = memories.longTerm
		} else {
			// No manager provided, create a standalone store
			storeOpts := []LongTermMemoryOption{WithLifecycleDir(filepath.Join(memoryDir, "lifecycle"))}
			longTermStore = NewLongTermMemoryStore(filepath.Join(memoryDir, "long_term"), storeOpts...)
		}
		if tools != nil {
			extractionCfg := LoadMemoryExtractionConfig(cfg.ConfigDir)
			tools.RegisterMemoryTools(memoryDir, extractionCfg.SummaryMaxChunks, longTermStore)
		}
	}

	rt := &Runtime{
		config:        cfg,
		models:        models,
		memories:      memories,
		tools:         tools,
		skills:        skillManager,
		skillsLoaded:  skillIndex != nil && len(skillIndex.Names()) > 0,
		waitForWakeup: waitForWakeupController,
		voiceNotifications: NewVoiceNotificationManager(
			cfg.VoiceNotifications,
			WithVoiceNotificationLocale(resolvedVoiceNotificationLocale(cfg)),
		),
		runtimeID:          uuid.NewString(),
		telemetrySessionID: uuid.NewString(),
		stateManager:       statemanager.NewStateManager(),
	}
	// Use the active memory session ID for raw HTTP log partitioning.
	if modelManager, ok := models.(*ModelManager); ok {
		modelManager.SetSessionIDProvider(func() string {
			if memories == nil {
				return ""
			}
			sessionID, err := memories.ActiveSessionID()
			if err != nil {
				return ""
			}
			return sessionID
		})
	}
	if cfg.ConfigDir != "" {
		rt.memoryPlane = NewFilesystemMemoryPlane(memoryDir, LoadMemoryExtractionConfig(cfg.ConfigDir), nil, WithMemoryPlaneLongTermStore(longTermStore))
		rt.markInterruptedEpisodesBestEffort()
	}
	rt.stateManager.RegisterUpdater(newDeviceStateUpdater(cfg))
	skillManager.SetDeviceTypeFunc(rt.deviceTypeFromState)
	rt.tools.SetRuntimeDeviceTypeFn(rt.deviceTypeFromState)
	rt.sessionManager = newMemoryManagerSessionManager(memories, func() BoundaryEpisodeContext {
		return recentEpisodeContext(rt.memoryPlane)
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

func (r *Runtime) deviceTypeFromState() string {
	if r != nil && r.stateManager != nil {
		for _, entry := range r.stateManager.GetAllStates() {
			if entry.Key != "device_type" {
				continue
			}
			if deviceType, ok := normalizeDeviceType(entry.Value); ok {
				return deviceType
			}
		}
	}
	if r != nil {
		return r.config.DeviceTypeOrDefault()
	}
	return defaultDeviceType
}

func (r *Runtime) devicePlatformFromState() string {
	return deviceTypePlatform(r.deviceTypeFromState())
}

func (r *Runtime) devicePointerModeFromState() string {
	return DeviceConfig{DeviceType: r.deviceTypeFromState()}.PointerModeOrDefault()
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

// Preempt terminates the currently active run and releases all associated
// resources (audio recording, TTS playback) via registered hooks.
func (r *Runtime) Preempt() {
	if r == nil {
		return
	}
	r.preemptMu.Lock()
	defer r.preemptMu.Unlock()
	if r.activeCancel != nil {
		r.activeCancel()
		r.lastPreemptTime = time.Now()
	}
	for i, hook := range r.preemptHooks {
		func() {
			defer func() {
				if recovered := recover(); recovered != nil {
					if r.logger != nil {
						r.logger.Warn("[preempt] hook %d panicked: %v", i, recovered)
					} else {
						log.Printf("[preempt] hook %d panicked: %v", i, recovered)
					}
				}
			}()
			hook()
		}()
	}
}

// RegisterPreemptHook adds a callback invoked when a new run preempts the
// current one. Used to release audio resources (stop recording, stop TTS).
func (r *Runtime) RegisterPreemptHook(hook func()) {
	if r == nil || hook == nil {
		return
	}
	r.preemptMu.Lock()
	defer r.preemptMu.Unlock()
	r.preemptHooks = append(r.preemptHooks, hook)
}

// WasPreempted reports whether a preemption occurred within the last duration.
func (r *Runtime) WasPreempted(within time.Duration) bool {
	if r == nil {
		return false
	}
	r.preemptMu.Lock()
	defer r.preemptMu.Unlock()
	return !r.lastPreemptTime.IsZero() && time.Since(r.lastPreemptTime) < within
}

func (r *Runtime) NewEpisodeID() string {
	if r == nil || r.memoryPlane == nil {
		return ""
	}
	return newTaskEpisodeID(time.Now().UTC())
}

func (r *Runtime) MemoryPlane() MemoryPlane {
	if r == nil {
		return nil
	}
	return r.memoryPlane
}

// Model returns the runtime's model manager for direct model calls outside the
// agent loop, such as the screen memory pipeline's vision call.
func (r *Runtime) Model() model.Model {
	if r == nil {
		return nil
	}
	return r.models
}

// ScreenState returns the runtime's screen coordinate mapping state for tools
// and pipelines that need to resolve the active area.
func (r *Runtime) ScreenState() *screen.ScreenState {
	if r == nil {
		return nil
	}
	return r.screenState
}

// LongTermMemoryStore returns the store for direct memory writes outside the
// agent loop, such as the screen memory pipeline's Screen Memory entries.
func (r *Runtime) LongTermMemoryStore() *LongTermMemoryStore {
	if r == nil || r.memories == nil {
		return nil
	}
	return r.memories.longTerm
}

// Logger returns the runtime's structured logger for components constructed
// outside the agent loop, such as the Quick Capture controller.
func (r *Runtime) Logger() *Logger {
	if r == nil {
		return nil
	}
	return r.logger
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

func (r *Runtime) Run(ctx context.Context, req RunRequest) (result RunResult, runErr error) {
	defer func() {
		if runErr != nil && isLLMTurnFailureSource(runErr) {
			result.TurnFailure = TurnFailureFromError(runErr)
		}
	}()
	if ctx == nil {
		ctx = context.Background()
	}

	// Preempt any currently active run and its resources.
	r.Preempt()

	unlockRun, err := r.lockRun(ctx)
	if err != nil {
		return RunResult{}, err
	}
	defer unlockRun()

	// Register this run's cancel so future callers can preempt us.
	runCtx, runCancel := context.WithCancel(ctx)
	r.preemptMu.Lock()
	r.activeCancel = runCancel
	r.preemptMu.Unlock()
	defer func() {
		r.preemptMu.Lock()
		r.activeCancel = nil
		r.preemptMu.Unlock()
		runCancel()
	}()

	// Check if we were preempted while waiting for runGate.
	if err := runCtx.Err(); err != nil {
		return RunResult{Preempted: true}, err
	}
	return r.withIOSKeyboardIsolationRun(runCtx, func(isolationCtx context.Context) (RunResult, error) {
		if req.OnRunActive != nil {
			req.OnRunActive(isolationCtx)
		}
		return r.run(isolationCtx, req)
	})
}

func (r *Runtime) withIOSKeyboardIsolationRun(
	ctx context.Context,
	action func(context.Context) (RunResult, error),
) (result RunResult, runErr error) {
	if r == nil || r.tools == nil || r.tools.iosKeyboardIsolation == nil {
		return action(ctx)
	}
	runErr = r.tools.iosKeyboardIsolation.withBatch(ctx, func(batchCtx context.Context) error {
		result, runErr = action(batchCtx)
		return runErr
	})
	return result, runErr
}

func (r *Runtime) run(ctx context.Context, req RunRequest) (result RunResult, runErr error) {
	var err error

	startTime := time.Now()
	metrics := &RunMetrics{}
	turnInput := canonicalTurnInputFromRunRequest(req)
	preRunEvents := cloneTaskEpisodeEvents(turnInput.TelemetryEvents)
	normalizedInput := turnInput.InputText
	if r.waitForWakeup != nil {
		r.waitForWakeup.Consume()
	}

	if r.logger != nil {
		r.logger.Info("Starting agent run: input=%q modality=%s attachments=%d", normalizedInput, turnInput.Modality, len(turnInput.Attachments))
	}

	if normalizedInput == "" {
		return RunResult{}, errors.New("input is required")
	}

	runID := "run_" + uuid.NewString()
	episodeID := strings.TrimSpace(req.EpisodeID)
	if episodeID == "" && r.memoryPlane != nil {
		episodeID = newTaskEpisodeID(startTime.UTC())
	}
	currentHints := r.currentEnvironmentHints()
	promptCapture := newTelemetryPromptCapture(r.config.Telemetry.EnabledOrDefault())
	var episodeRecorder *EpisodeRecorder
	var availableTools []langtools.Tool
	var boundaryTelemetry sessionBoundaryTelemetry
	var output string
	episodeCommitted := false
	defer func() {
		if runErr == nil || episodeCommitted {
			return
		}
		metrics.TotalDuration = float64(time.Since(startTime).Milliseconds())
		if episodeRecorder == nil && r.memoryPlane != nil {
			retrieveReq := MemoryRetrieveRequest{
				Input:        normalizedInput,
				Attachments:  turnInput.Attachments,
				ToolNames:    toolNamesFromTools(availableTools),
				EpisodeID:    episodeID,
				DeviceID:     defaultMemoryDeviceID,
				CurrentHints: currentHints,
			}
			episodeRecorder = r.memoryPlane.NewEpisodeRecorder(retrieveReq, MemoryContext{})
			if episodeRecorder != nil {
				episodeRecorder.setStartedAtIfEarlier(episodeStartTimeWithEvents(startTime.UTC(), preRunEvents))
				episodeID = episodeRecorder.ID()
				startCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
				if err := episodeRecorder.Start(startCtx); err != nil && r.logger != nil {
					r.logger.Warn("[memory] start failed episode trace failed: %v", err)
				}
				cancel()
				recordPreRunEpisodeEvents(episodeRecorder, preRunEvents)
			}
		}
		if episodeRecorder == nil {
			return
		}
		episodeID = episodeRecorder.ID()
		r.persistRunStatusBestEffort(episodeID, req.RequestID, runID, runErr)
		r.commitEpisodeBestEffort(episodeRecorder, normalizedInput, output, metrics, runErr, promptCapture, boundaryTelemetry, req.AsyncEpisodeMaintenance)
		episodeCommitted = true
	}()

	if err := r.reloadSkillsIfDirty(); err != nil {
		return RunResult{}, err
	}

	contextWindow := r.effectiveContextWindow()
	if contextWindow > 0 {
		metrics.ContextWindow = contextWindow
		if r.logger != nil {
			r.logger.Info("Resolved model context window: context_window=%d", contextWindow)
		}
	}
	m := &usageTrackingModel{inner: r.models, metrics: metrics, promptCapture: promptCapture, contextWindowFn: func() model.ModelSpec {
		if r.models != nil {
			return r.models.Spec()
		}
		return model.ModelSpec{}
	}}

	memoryCfg := MemoryConfig{Type: "buffer"}
	var memoryHandle *MemoryHandle
	if r.memories != nil {
		memoryHandle, err = r.memories.Get("default", memoryCfg)
	} else {
		memoryHandle, err = newMemoryHandle(memoryCfg)
	}
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return RunResult{}, ctxErr
		}
		if r.logger != nil {
			r.logger.Warn("[memory] load persisted memory failed; continuing with empty history: %v", err)
		}
		memoryHandle, err = newMemoryHandle(memoryCfg)
		if err != nil {
			return RunResult{}, err
		}
	}
	sessionBeginStart := time.Now()
	beginResult, err := r.beginSession(ctx, SessionBeginRequest{
		AgentName:    "default",
		Input:        normalizedInput,
		Turn:         turnInput,
		RuntimeID:    r.runtimeID,
		EpisodeID:    episodeID,
		RequestID:    req.RequestID,
		RunID:        runID,
		CurrentHints: currentHints,
	})
	sessionBeginDuration := time.Since(sessionBeginStart).Milliseconds()
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return RunResult{}, ctxErr
		}
		if r.logger != nil {
			r.logger.Warn("[memory] begin session failed; continuing without session update: %v", err)
		}
		beginResult = SessionBeginResult{}
	}
	boundaryTelemetry = beginResult.Boundary
	if boundaryTelemetry.Rotated {
		r.rotateContext()
	}
	if boundaryTelemetry.PendingRecallCounter == nil {
		boundaryTelemetry.PendingRecallCounter = &atomic.Int64{}
	}
	sessionBeginEvent := TaskEpisodeEvent{
		Type:       runEventSessionBegin,
		Ts:         sessionBeginStart.Format(time.RFC3339Nano),
		DurationMs: &sessionBeginDuration,
		Metadata: map[string]interface{}{
			"rotated":  beginResult.Boundary.Rotated,
			"decision": beginResult.Boundary.Decision,
			"reason":   beginResult.Boundary.Reason,
		},
	}

	availableTools = r.availableTools()
	availableTools = wrapSessionRecallTelemetry(availableTools, boundaryTelemetry.PendingRecallCounter)
	retrieveReq := MemoryRetrieveRequest{
		Input:        normalizedInput,
		Attachments:  turnInput.Attachments,
		ToolNames:    toolNamesFromTools(availableTools),
		EpisodeID:    episodeID,
		DeviceID:     defaultMemoryDeviceID,
		CurrentHints: currentHints,
	}
	// Memories are no longer retrieved up front. The agent pulls what it needs
	// on demand through the recall tools, which record the referenced IDs on the
	// episode recorder so outcome-based confidence updates only touch memories
	// the agent actually saw.
	if r.memoryPlane != nil {
		episodeRecorder = r.memoryPlane.NewEpisodeRecorder(retrieveReq, MemoryContext{})
		if episodeRecorder != nil {
			episodeRecorder.setStartedAtIfEarlier(episodeStartTimeWithEvents(startTime.UTC(), preRunEvents))
			if err := episodeRecorder.Start(ctx); err != nil && r.logger != nil {
				r.logger.Warn("[memory] start episode failed: %v", err)
			}
			recordPreRunEpisodeEvents(episodeRecorder, preRunEvents)
			episodeRecorder.RecordEvent(sessionBeginEvent)
		}
	}
	if episodeRecorder != nil {
		episodeID = episodeRecorder.ID()
	}

	maxIterations := effectiveMaxIterations(r.config.MaxIterations)

	// Record tool count in metrics for observability
	metrics.ToolCount = len(availableTools)

	callOptions := r.models.CallOptions()
	if req.MaxTokens > 0 {
		callOptions = append(callOptions, chains.WithMaxTokens(req.MaxTokens))
	}
	var streamCallbackHandler *runtimeCallbackHandler
	persistRuntimeSessionEvents := r.memories != nil && strings.TrimSpace(r.memories.storageDir) != ""
	if req.StreamWriter != nil || req.EventHandler != nil || req.SteerProvider != nil || r.logger != nil || persistRuntimeSessionEvents {
		streamCallbackHandler = &runtimeCallbackHandler{
			writer:       req.StreamWriter,
			metrics:      metrics,
			startTime:    startTime,
			logger:       r.logger,
			eventHandler: req.EventHandler,
			episodeID:    episodeID,
			runtimeID:    r.runtimeID,
			requestID:    req.RequestID,
			runID:        runID,
		}
		if persistRuntimeSessionEvents {
			streamCallbackHandler.sessionEventAppender = func(ctx context.Context, event SessionEvent) error {
				return r.appendRuntimeSessionEvent(ctx, "default", event, SessionEventMetadata{
					RuntimeID: r.runtimeID,
					EpisodeID: episodeID,
					RequestID: req.RequestID,
					RunID:     runID,
				})
			}
		}
	}
	if streamCallbackHandler != nil && req.StreamWriter != nil {
		streamCallbackHandler.ResetStreaming(ctx)
		callOptions = append(callOptions, chains.WithStreamingFunc(func(ctx context.Context, chunk []byte) error {
			streamCallbackHandler.HandleStreamingFunc(ctx, chunk)
			return nil
		}))
	}
	var executorHandler callbacks.Handler
	if streamCallbackHandler != nil {
		executorHandler = streamCallbackHandler
	}
	profile := r.buildAgentProfile(r.skills, availableTools)
	plannerMemory := memoryHandle.Memory
	var steerStatus steerConversationStatus
	if req.SteerProvider != nil {
		plannerMemory = newSteerConversationMemory(plannerMemory, memoryHandle.History)
		if status, ok := plannerMemory.(steerConversationStatus); ok {
			steerStatus = status
		}
	}

	// setup context manager if not initialized
	if r.contextManager == nil {
		r.contextManager, err = InitializeContextManager(profile.SystemPrompt, agentpath.ContextManagerSessionFolder(r.config.ConfigDir), []contextmanager.AppendMessageHook{r.getStateHook()})
		if err != nil {
			return RunResult{}, err
		}
	}
	// append runtime context as assistant message if present (e.g., voice interruption notification)
	if runtimeContext := strings.TrimSpace(req.RuntimeContext); runtimeContext != "" {
		if err := r.contextManager.AppendMessage(messages.Message{
			Role:    messages.MessageRoleAssistant,
			Content: runtimeContext,
		}); err != nil {
			return RunResult{}, err
		}
	}
	// append user message to context manager
	if err := r.contextManager.AppendMessage(userMessageFromInput(r.contextManager, turnInput.InputText, turnInput.Attachments)); err != nil {
		return RunResult{}, err
	}

	compactor := compactor.NewCompactor(compactor.DefaultProtectRule, r.models)
	budgetContextWindow := contextWindow
	if budgetContextWindow <= 0 {
		budgetContextWindow = r.models.Spec().ContextWindow
	}
	maxResponseTokens := r.models.Spec().MaxOutput
	if r.config.Model.MaxResponseTokens > 0 {
		maxResponseTokens = r.config.Model.MaxResponseTokens
	}
	if req.MaxTokens > 0 {
		maxResponseTokens = req.MaxTokens
	}
	usableInputBudget := toolResultUsableInputBudget(budgetContextWindow, maxResponseTokens)
	compactionTrigger, compactionTarget, compactionEnabled := toolResultCompactionBudgets(usableInputBudget)
	tokenUsage := tokencounter.EstimateMessagesTokens(r.contextManager.CloneMessageList())
	if compactionEnabled && tokenUsage > compactionTrigger {
		if r.logger != nil {
			r.logger.Info("Compaction: token usage reached the threshold, try to compact the context... tokenUsage: %d, trigger: %d, target: %d, contextWindow: %d", tokenUsage, compactionTrigger, compactionTarget, contextWindow)
		}
		newManager, compacted, err := compactor.Compact(ctx, r.contextManager)
		if episodeRecorder != nil {
			episodeRecorder.RecordEvent(TaskEpisodeEvent{
				Type: runEventHistoricalToolResultCompaction,
				Metadata: map[string]interface{}{
					"compacted": compacted,
					"success":   err == nil,
				},
			})
		}
		if err != nil {
			return RunResult{}, err
		}
		if compacted {
			// setup hooks
			newManager.AddAppendMessageHook(r.getStateHook())
			// switch to new session
			err = contextmanager.SwitchSession(newManager.GetSessionFolder(), newManager.GetSessionID())
			if err != nil {
				return RunResult{}, err
			}
			r.contextManager = newManager
		}
	}

	agentLoop := NewAgentLoop(m, profile, plannerMemory, maxIterations, executorHandler, episodeRecorder, r.config.ScreenshotPruningOrDefault(), r.contextManager)
	agentLoop.toolExecutionHookFactory = func() toolExecutionHookHandler {
		if r.tools == nil {
			return newWheelNudgeGuard(nil)
		}
		return newWheelNudgeGuard(r.tools.screen)
	}
	agentLoop.ToolResultObserver = newScreenToolResultObserver(r.screenState)
	agentLoop.SteerInterrupt = req.SteerInterrupt
	agentLoop.SteerProvider = req.SteerProvider
	agentLoop.SteerWaiter = req.SteerWaiter
	agentLoop.TerminationPolicy = NewTerminationPolicy(r.config.TerminationPolicy)
	agentLoop.DevicePlatform = r.devicePlatformFromState()
	agentLoop.PointerMode = r.devicePointerModeFromState()
	agentLoop.ContextOverflowRecovery = func(recoveryCtx context.Context, currentManager *contextmanager.ContextManager) (*contextmanager.ContextManager, bool, error) {
		if r.logger != nil {
			r.logger.Info("Compaction: provider rejected the request because the context window was exceeded; compacting and retrying")
		}
		newManager, compacted, compactErr := compactor.Compact(recoveryCtx, currentManager)
		if episodeRecorder != nil {
			episodeRecorder.RecordEvent(TaskEpisodeEvent{
				Type: runEventHistoricalToolResultCompaction,
				Metadata: map[string]interface{}{
					"compacted": compacted,
					"success":   compactErr == nil,
					"reason":    "provider_context_exceeded",
				},
			})
		}
		if compactErr != nil || !compacted {
			return newManager, compacted, compactErr
		}
		newManager.AddAppendMessageHook(r.getStateHook())
		if switchErr := contextmanager.SwitchSession(newManager.GetSessionFolder(), newManager.GetSessionID()); switchErr != nil {
			return nil, false, switchErr
		}
		r.contextManager = newManager
		return newManager, true, nil
	}

	output, err = agentLoop.Run(ctx, normalizedInput, callOptions...)
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
			return RunResult{}, err
		}
	}

	output = strings.TrimSpace(output)
	metrics.TotalDuration = float64(time.Since(startTime).Milliseconds())
	if r.logger != nil && metrics.PromptTokens > 0 {
		r.logger.Info("LLM usage: prompt_tokens=%d completion_tokens=%d total_tokens=%d cached_tokens=%d cache_hit_rate=%.1f%%",
			metrics.PromptTokens, metrics.CompletionTokens, metrics.TotalTokens,
			metrics.CachedPromptTokens, metrics.CacheHitRate()*100)
	}
	commitReq := SessionCommitRequest{
		AgentName: "default",
		Input:     normalizedInput,
		Output:    output,
		Metrics:   metrics,
		RuntimeID: r.runtimeID,
		EpisodeID: episodeID,
		RequestID: req.RequestID,
		RunID:     runID,
	}
	if steerStatus != nil && steerStatus.HasSteerMessages() {
		commitReq.Steers = steerStatus.SteerMessages()
	}

	commitResult, err := r.commitSession(ctx, commitReq)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return RunResult{}, ctxErr
		}
		if r.logger != nil {
			r.logger.Warn("[memory] commit session failed; returning model output without memory snapshot: %v", err)
		}
		commitResult = SessionCommitResult{}
	}
	if streamCallbackHandler != nil {
		streamCallbackHandler.HandleAssistantOutput(ctx, output)
	}
	r.commitEpisodeBestEffort(episodeRecorder, normalizedInput, output, metrics, nil, promptCapture, boundaryTelemetry, req.AsyncEpisodeMaintenance)
	episodeCommitted = true

	waitForWakeupRequested, waitForWakeupReason := false, ""
	if r.waitForWakeup != nil {
		waitForWakeupRequested, waitForWakeupReason = r.waitForWakeup.Consume()
	}
	return RunResult{
		Output:                 output,
		EpisodeID:              episodeID,
		Memory:                 commitResult.Memory,
		Metrics:                metrics,
		WaitForWakeupRequested: waitForWakeupRequested,
		WaitForWakeupReason:    waitForWakeupReason,
		SleepRequested:         waitForWakeupRequested,
		SleepReason:            waitForWakeupReason,
	}, nil
}

func (r *Runtime) getSystemPrompt() string {
	profile := r.buildAgentProfile(r.skills, r.availableTools())
	return profile.SystemPrompt
}

func (r *Runtime) getStateHook() contextmanager.AppendMessageHook {
	return func(message messages.Message) contextmanager.AppendMessageHookResult {
		// if not user message, just skip
		if message.Role != messages.MessageRoleUser {
			return contextmanager.AppendMessageHookResult{
				Message: &message,
			}
		}
		attachment := r.captureStateScreenshot()
		entries := r.stateManager.GetAllStates()
		// If neither runtime state nor a screenshot is available, just skip.
		if len(entries) == 0 && attachment == nil {
			return contextmanager.AppendMessageHookResult{
				Message: &message,
			}
		}
		// format state entries into a list
		// example:
		// key1: value1
		// key2: value2
		// ...
		var formated strings.Builder
		for _, entry := range entries {
			if strings.TrimSpace(entry.Value) == "" {
				continue
			}
			fmt.Fprintf(&formated, "%s: %s\n", entry.Key, entry.Value)
		}
		if formated.Len() == 0 && attachment == nil {
			return contextmanager.AppendMessageHookResult{
				Message: &message,
			}
		}
		tagged := ""
		if formated.Len() > 0 {
			tagged = util.STag("state", formated.String())
		}
		// create a new StateMessage
		stateMessage := messages.Message{
			Role:    messages.MessageRoleState,
			Content: tagged,
		}
		if attachment != nil {
			stateMessage.Attachments = []messages.Attachment{*attachment}
		}
		return contextmanager.AppendMessageHookResult{
			Before:  []messages.Message{stateMessage},
			Message: &message,
			After:   []messages.Message{},
		}
	}
}

func (r *Runtime) captureStateScreenshot() *messages.Attachment {
	if r == nil || r.tools == nil || r.contextManager == nil {
		return nil
	}
	screenshotTool, ok := r.tools.Get("screenshot")
	if !ok || screenshotTool == nil {
		return nil
	}

	output, err := screenshotTool.Call(context.Background(), "{}")
	if err != nil {
		if r.logger != nil {
			r.logger.Debug("[state] screenshot unavailable: %v", err)
		}
		return nil
	}
	observation, ok := parseScreenshotObservation(output)
	if !ok {
		if r.logger != nil {
			r.logger.Debug("[state] screenshot output is not a valid visual observation")
		}
		return nil
	}
	attachment, err := r.contextManager.StoreAttachment(observation.MIMEType, observation.ImageBytes)
	if err != nil {
		if r.logger != nil {
			r.logger.Debug("[state] failed to store screenshot attachment: %v", err)
		}
		return nil
	}
	attachment.Source = messages.AttachmentSourceScreenshotObservation
	return &attachment
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

func (r *Runtime) appendRuntimeSessionEvent(ctx context.Context, agentName string, event SessionEvent, meta SessionEventMetadata) error {
	if r == nil || r.memories == nil {
		return nil
	}
	return r.memories.AppendSessionEvent(ctx, agentName, event, meta)
}

func (r *Runtime) persistRunStatusBestEffort(episodeID, requestID, runID string, runErr error) {
	if r == nil || r.memories == nil || runErr == nil {
		return
	}
	status := "failed"
	if errors.Is(runErr, context.Canceled) {
		status = "interrupted"
	}
	content := "Agent run " + status + ": " + runErr.Error()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	err := r.memories.AppendSessionEvent(ctx, "default", SessionEvent{
		Type:      "episode_status",
		Role:      "system",
		Status:    status,
		Content:   content,
		IsError:   true,
		EpisodeID: episodeID,
		RequestID: requestID,
		RunID:     runID,
		RuntimeID: r.runtimeID,
	}, SessionEventMetadata{
		RuntimeID: r.runtimeID,
		EpisodeID: episodeID,
		RequestID: requestID,
		RunID:     runID,
	})
	if err != nil && r.logger != nil {
		r.logger.Warn("[memory] persist run status failed: %v", err)
	}
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

func (r *Runtime) activeConversationHistoryTokenBudget(contextWindow int) int {
	if contextWindow <= 0 {
		return 0
	}
	reserveTokens := defaultReserveTokens
	keepRecentTokens := defaultKeepRecentTokens
	if r != nil && r.memories != nil {
		reserveTokens = r.memories.reserveTokens()
		keepRecentTokens = r.memories.keepRecentTokens()
	}
	_, keepRecent := clampTokenBudgets(reserveTokens, keepRecentTokens, contextWindow)
	return keepRecent
}

func (r *Runtime) ClearMemory(ctx context.Context) error {
	if err := r.memories.ClearSession(ctx, "default"); err != nil {
		return err
	}
	r.rotateContext()
	return nil
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
	if err := r.memories.ClearAll(ctx, "default"); err != nil {
		return err
	}

	_ = contextmanager.ClearAllSessions(agentpath.ContextManagerSessionFolder(r.config.ConfigDir))
	r.rotateContext()

	return nil
}

func (r *Runtime) ContextDump() contextmanager.MessageListDump {
	contextManager := r.contextManager
	if contextManager == nil {
		return contextmanager.MessageListDump{}
	}
	return contextManager.MessageListDump()
}

func (r *Runtime) rotateContext() {
	newContextManager, err := contextmanager.NewContextManager(agentpath.ContextManagerSessionFolder(r.config.ConfigDir), r.getSystemPrompt())
	if err != nil {
		return
	}
	newContextManager.AddAppendMessageHooks([]contextmanager.AppendMessageHook{r.getStateHook()})
	r.contextManager = newContextManager
}

func (r *Runtime) availableTools() []langtools.Tool {
	if r == nil || r.tools == nil {
		return nil
	}
	tools := NewToolSpecs(r.tools.All()).AgentToolsForPlatform(r.config.LoadAllTools, r.devicePlatformFromState())
	return r.filterPhoneBridgeAgentTools(tools)
}

func (r *Runtime) filterPhoneBridgeAgentTools(tools []langtools.Tool) []langtools.Tool {
	if r == nil || r.tools == nil || r.tools.phoneBridge == nil {
		return tools
	}
	bridge := r.tools.phoneBridge
	bridge.SetConfiguredPlatform(r.devicePlatformFromState())
	status := bridge.getStatus()
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	capabilities := bridge.bleCapabilities(ctx)
	cancel()

	openURLAvailable := phoneBridgeReadyForCommand(status, "open_app") || phoneBridgeCanRestoreFromReturnEntry(status)
	clipboardAvailable := phoneBridgeCommandAvailable(status, "clipboard_read", capabilities.Wake) ||
		phoneBridgeCommandAvailable(status, "clipboard_write", capabilities.Wake)
	calendarAvailable := phoneBridgeCommandAvailable(status, "calendar_create", capabilities.Wake) ||
		phoneBridgeCommandAvailable(status, "calendar_query", capabilities.Wake) ||
		phoneBridgeCommandAvailable(status, "calendar_delete", capabilities.Wake)
	contactsQueryAvailable := phoneBridgeCommandAvailable(status, "contacts_query", capabilities.Wake)
	contactsCreateAvailable := phoneBridgeCommandAvailable(status, "contacts_create", capabilities.Wake)
	contactsUpdateAvailable := phoneBridgeCommandAvailable(status, "contacts_update", capabilities.Wake)
	contactsAvailable := contactsQueryAvailable || contactsCreateAvailable || contactsUpdateAvailable
	notificationSendAvailable := phoneBridgeCommandAvailable(status, "notification_send", capabilities.Wake)
	notificationQueryAvailable := capabilities.NotificationQuery
	notificationAvailable := notificationSendAvailable || notificationQueryAvailable

	filtered := make([]langtools.Tool, 0, len(tools))
	for _, tool := range tools {
		if tool == nil {
			continue
		}
		available := true
		switch tool.Name() {
		case toolOpenURL:
			available = openURLAvailable
		case toolBridgeClipboard:
			available = clipboardAvailable
		case toolBridgeCalendar:
			available = calendarAvailable
		case toolBridgeContacts:
			available = contactsAvailable
		case toolBridgeNotification:
			available = notificationAvailable
		}
		if available {
			switch tool.Name() {
			case toolBridgeContacts:
				tool = newContactsCapabilityTool(tool, contactsQueryAvailable, contactsCreateAvailable, contactsUpdateAvailable)
			case toolBridgeNotification:
				tool = newNotificationCapabilityTool(tool, notificationSendAvailable, notificationQueryAvailable)
			}
			filtered = append(filtered, tool)
		}
	}
	return filtered
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

type episodeMaintenancePlane interface {
	MemoryPlane
	commitEpisodeTrace(ctx context.Context, episode TaskEpisode) error
	commitEpisodeMaintenance(ctx context.Context, episode TaskEpisode)
}

func (r *Runtime) commitEpisodeBestEffort(recorder *EpisodeRecorder, input string, output string, metrics *RunMetrics, runErr error, promptCapture *telemetryPromptCapture, boundary sessionBoundaryTelemetry, asyncMaintenance bool) {
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
	if asyncMaintenance {
		if plane, ok := r.memoryPlane.(episodeMaintenancePlane); ok {
			if err := plane.commitEpisodeTrace(ctx, episode); err != nil && r.logger != nil {
				r.logger.Warn("[memory] commit episode trace failed: %v", err)
				return
			}
			r.exportEpisodeBestEffort(episode, promptCapture)
			maintenanceParentCtx, started := r.episodeMaintenance.begin()
			if !started {
				return
			}
			go func() {
				defer r.episodeMaintenance.done()
				maintenanceCtx, maintenanceCancel := context.WithTimeout(maintenanceParentCtx, 10*time.Second)
				defer maintenanceCancel()
				plane.commitEpisodeMaintenance(maintenanceCtx, episode)
			}()
			return
		}
	}
	if err := r.memoryPlane.CommitEpisode(ctx, episode); err != nil && r.logger != nil {
		r.logger.Warn("[memory] commit episode failed: %v", err)
		return
	}
	r.exportEpisodeBestEffort(episode, promptCapture)
}

func (m *asyncEpisodeMaintenance) begin() (context.Context, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closing {
		return nil, false
	}
	if m.ctx == nil {
		m.ctx, m.cancel = context.WithCancel(context.Background())
	}
	m.wg.Add(1)
	return m.ctx, true
}

func (m *asyncEpisodeMaintenance) done() {
	m.wg.Done()
}

func (m *asyncEpisodeMaintenance) closeAndWait(ctx context.Context) error {
	m.mu.Lock()
	m.closing = true
	cancel := m.cancel
	m.mu.Unlock()
	done := make(chan struct{})
	go func() {
		m.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
		if cancel != nil {
			cancel()
		}
		return nil
	case <-ctx.Done():
		if cancel != nil {
			cancel()
		}
		return ctx.Err()
	}
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
	if extraString(episode.Extra, "runtime_id") == "" && r.runtimeID != "" {
		episode.Extra["runtime_id"] = r.runtimeID
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
	if cfg.Temperature != nil {
		params["temperature"] = *cfg.Temperature
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

func (r *Runtime) buildAgentProfile(skills *SkillManager, availableTools []langtools.Tool) RoleProfile {
	return buildProfile(
		AgentConfig{
			Instruction:      r.config.Instruction,
			AdditionalPrompt: r.config.AdditionalPrompt,
			Locale:           r.config.LocaleOrDefault(),
		},
		skills,
		availableTools,
		agentRoleRules(),
	)
}

// runtimeCallbackHandler implements callbacks.Handler for streaming output and
// tool/agent observability.
type runtimeCallbackHandler struct {
	writer               io.Writer
	metrics              *RunMetrics
	startTime            time.Time
	firstTokenSeen       bool
	streamTokenSeen      bool
	streamLogEmitted     bool
	streamErr            error
	logger               *Logger
	eventHandler         func(RunEvent)
	episodeID            string
	runtimeID            string
	requestID            string
	runID                string
	sessionEventAppender func(context.Context, SessionEvent) error
	mu                   sync.Mutex
	pendingActions       []schema.AgentAction
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
	if v, ok := util.UsageMetricInt(info["prompt_tokens"]); ok {
		metrics.PromptTokens += v
		if v > metrics.LastPromptTokens {
			metrics.LastPromptTokens = v
		}
	}
	if v, ok := util.UsageMetricInt(info["completion_tokens"]); ok {
		metrics.CompletionTokens += v
	}
	if v, ok := util.UsageMetricInt(info["total_tokens"]); ok {
		metrics.TotalTokens += v
	}
	if v, ok := util.UsageMetricInt(info["cached_tokens"]); ok {
		metrics.CachedPromptTokens += v
	}
	if v, ok := util.UsageMetricInt(info["reasoning_tokens"]); ok {
		metrics.ReasoningTokens += v
	}
}

func (h *runtimeCallbackHandler) HandleLLMError(ctx context.Context, err error) {}

func (h *runtimeCallbackHandler) HandleChainStart(ctx context.Context, inputs map[string]any) {}

func (h *runtimeCallbackHandler) HandleChainEnd(ctx context.Context, outputs map[string]any) {}

func (h *runtimeCallbackHandler) HandleChainError(ctx context.Context, err error) {}

func (h *runtimeCallbackHandler) HandleToolStart(ctx context.Context, input string) {}

func (h *runtimeCallbackHandler) emitRunEvent(event RunEvent) {
	h.emitRunEventWithPersistence(event, true)
}

func (h *runtimeCallbackHandler) emitRunEventWithPersistence(event RunEvent, persist bool) {
	if event.Timestamp.IsZero() {
		event.Timestamp = time.Now()
	}
	if event.EpisodeID == "" {
		event.EpisodeID = h.episodeID
	}
	if persist {
		h.persistSessionEventBestEffort(sessionEventFromRunEvent(event, h), "[memory] persist runtime session event failed")
	}
	if h.eventHandler != nil {
		h.eventHandler(event)
	}
}

func (h *runtimeCallbackHandler) persistSessionEventBestEffort(event SessionEvent, warnMessage string) {
	if h.sessionEventAppender == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), runtimeSessionEventPersistTimeout)
	defer cancel()
	if err := h.sessionEventAppender(ctx, event); err != nil && h.logger != nil {
		h.logger.Warn("%s: %v", warnMessage, err)
	}
}

func (h *runtimeCallbackHandler) HandleAssistantOutput(ctx context.Context, content string) {
	content = strings.TrimSpace(content)
	if content == "" {
		return
	}
	if h.logger != nil {
		h.logger.Info("Assistant output: %s", truncateForLog(content, 1000))
	}
	h.emitRunEventWithPersistence(RunEvent{
		Type:      "assistant_output",
		Role:      "assistant",
		EpisodeID: h.episodeID,
		Content:   content,
		Timestamp: time.Now(),
	}, false)
}

func (h *runtimeCallbackHandler) HandleToolEnd(ctx context.Context, output string) {
	if h.logger != nil {
		h.logger.Info("Tool result: %s", truncateForLog(output, 240))
	}
	action, ok := h.popPendingAction()
	if ok {
		h.emitRunEvent(RunEvent{
			Type:       "tool_result",
			EpisodeID:  h.episodeID,
			ToolCallID: action.ToolID,
			ToolName:   action.Tool,
			ToolInput:  normalizeToolInput(action.ToolInput),
			Content:    output,
			Timestamp:  time.Now(),
		})
	}
}

func (h *runtimeCallbackHandler) HandleToolError(ctx context.Context, err error) {
	if h.logger != nil {
		h.logger.Error("Tool error: %v", err)
	}
	action, ok := h.popPendingAction()
	if ok {
		var toolErr *ToolError
		if !errors.As(err, &toolErr) {
			toolErr = NewToolError(CodeToolExecutionFailed, err.Error())
		}
		content := toolErr.Message
		h.emitRunEvent(RunEvent{
			Type:       "tool_result",
			EpisodeID:  h.episodeID,
			ToolCallID: action.ToolID,
			ToolName:   action.Tool,
			ToolInput:  normalizeToolInput(action.ToolInput),
			Content:    content,
			Timestamp:  time.Now(),
			IsError:    true,
			ToolError:  cloneToolError(toolErr),
		})
	}
}

func (h *runtimeCallbackHandler) HandleNamedToolStart(ctx context.Context, name, input string) {}

func (h *runtimeCallbackHandler) HandleNamedToolEnd(ctx context.Context, name, input, output string) {
	input = normalizeToolInput(input)
	if h.logger != nil {
		h.logger.Info("Tool result: name=%s output=%s", name, truncateForLog(output, 240))
	}
	h.removePendingAction(name, input)
	h.emitRunEvent(RunEvent{
		Type:      "tool_result",
		EpisodeID: h.episodeID,
		ToolName:  name,
		ToolInput: input,
		Content:   output,
		Timestamp: time.Now(),
	})
}

func (h *runtimeCallbackHandler) HandleNamedToolError(ctx context.Context, name, input string, err error) {
	input = normalizeToolInput(input)
	if h.logger != nil {
		h.logger.Error("Tool error: name=%s err=%v", name, err)
	}
	var toolErr *ToolError
	if !errors.As(err, &toolErr) {
		toolErr = NewToolError(CodeToolExecutionFailed, err.Error())
	}
	content := toolErr.Message
	h.removePendingAction(name, input)
	h.emitRunEvent(RunEvent{
		Type:      "tool_result",
		EpisodeID: h.episodeID,
		ToolName:  name,
		ToolInput: input,
		Content:   content,
		Timestamp: time.Now(),
		IsError:   true,
		ToolError: cloneToolError(toolErr),
	})
}

func (h *runtimeCallbackHandler) HandleToolCallStart(ctx context.Context, call ToolCall) {
	content := strings.TrimSpace(call.Content)
	if h.logger != nil {
		if content != "" {
			h.logger.Info("Tool call: name=%s input=%s content=%s",
				call.Spec.Name, truncateForLog(call.Input, 240), truncateForLog(content, 240))
		} else {
			h.logger.Info("Tool call: name=%s input=%s",
				call.Spec.Name, truncateForLog(call.Input, 240))
		}
	}
	h.emitRunEvent(RunEvent{
		Type:       runEventToolCall,
		EpisodeID:  h.episodeID,
		ToolCallID: call.Action.ToolID,
		ToolName:   call.Spec.Name,
		ToolInput:  call.Input,
		Content:    content,
		Timestamp:  time.Now(),
	})
}

func (h *runtimeCallbackHandler) BeforeToolCall(ctx context.Context, call ToolCall) (ToolResult, bool) {
	return DefaultBeforeToolCall(ctx, call)
}

func (h *runtimeCallbackHandler) AfterToolCall(ctx context.Context, call ToolCall, result ToolResult) ToolResult {
	return DefaultAfterToolCall(ctx, call, result)
}

func (h *runtimeCallbackHandler) HandleToolCallResult(ctx context.Context, call ToolCall, result ToolResult) {
	output := publicToolResultContent(result)
	logOutput := result.EventOutput()
	if h.logger != nil {
		if result.IsError() {
			h.logger.Error("Tool result: name=%s output=%s", call.Spec.Name, truncateForLog(logOutput, 240))
		} else {
			h.logger.Info("Tool result: name=%s output=%s", call.Spec.Name, truncateForLog(logOutput, 240))
		}
	}
	h.emitRunEvent(RunEvent{
		Type:       "tool_result",
		EpisodeID:  h.episodeID,
		ToolCallID: call.Action.ToolID,
		ToolName:   call.Spec.Name,
		ToolInput:  call.Input,
		Content:    output,
		Timestamp:  time.Now(),
		IsError:    result.IsError(),
		ToolError:  cloneToolError(result.Error),
	})
}

func publicToolResultContent(result ToolResult) string {
	originalChars := utf8.RuneCountInString(result.Output)
	if result.SummaryTruncated && originalChars > maxPublicToolResultRunes {
		return fmt.Sprintf("[Large tool result omitted from public history (%d chars)]", originalChars)
	}
	return result.EventOutput()
}

func (h *runtimeCallbackHandler) HandleAgentAction(ctx context.Context, action schema.AgentAction) {
	content := toolContentFromAction(action)
	if h.logger != nil {
		if content != "" {
			h.logger.Info("Tool call: name=%s input=%s content=%s",
				action.Tool, truncateForLog(action.ToolInput, 240), truncateForLog(content, 240))
		} else {
			h.logger.Info("Tool call: name=%s input=%s",
				action.Tool, truncateForLog(action.ToolInput, 240))
		}
	}
	h.pushPendingAction(action)
	h.emitRunEvent(RunEvent{
		Type:       runEventToolCall,
		EpisodeID:  h.episodeID,
		ToolCallID: action.ToolID,
		ToolName:   action.Tool,
		ToolInput:  action.ToolInput,
		Content:    content,
		Timestamp:  time.Now(),
	})
}

func (h *runtimeCallbackHandler) HandleAgentFinish(ctx context.Context, finish schema.AgentFinish) {}

func (h *runtimeCallbackHandler) HandleRoleOutput(ctx context.Context, role, content string) {
	content = strings.TrimSpace(content)
	if h.logger != nil {
		h.logger.Info("Role output: role=%s content=%s", role, truncateForLog(content, 1000))
	}
	h.emitRunEvent(RunEvent{
		Type:      "role_output",
		Role:      role,
		EpisodeID: h.episodeID,
		Content:   content,
		Timestamp: time.Now(),
	})
}

func (h *runtimeCallbackHandler) HandleSteerMessage(ctx context.Context, steer RunSteerMessage) {
	if steer.Timestamp.IsZero() {
		steer.Timestamp = time.Now()
	}
	h.emitRunEvent(RunEvent{
		Type:      "steer",
		EpisodeID: h.episodeID,
		Content:   steer.Content,
		Timestamp: steer.Timestamp,
	})
}

func sessionEventFromRunEvent(event RunEvent, h *runtimeCallbackHandler) SessionEvent {
	role := strings.TrimSpace(event.Role)
	if role == "" {
		switch event.Type {
		case "tool_result":
			role = "tool"
		case runEventToolCall:
			role = string(llms.ChatMessageTypeAI)
		case "steer":
			role = "user"
		default:
			role = "system"
		}
	}
	ts := event.Timestamp
	if ts.IsZero() {
		ts = time.Now()
	}
	sessionEvent := SessionEvent{
		Ts:         ts.UTC().Format(time.RFC3339Nano),
		Type:       event.Type,
		Role:       role,
		RuntimeID:  h.runtimeID,
		EpisodeID:  firstNonEmptyString([]string{event.EpisodeID, h.episodeID}),
		RequestID:  h.requestID,
		RunID:      h.runID,
		Content:    event.Content,
		ToolCallID: event.ToolCallID,
		ToolName:   event.ToolName,
		ToolInput:  event.ToolInput,
		IsError:    event.IsError,
		ToolError:  cloneToolError(event.ToolError),
	}
	return sessionEvent
}

func (h *runtimeCallbackHandler) HandleRetrieverStart(ctx context.Context, query string) {}

func (h *runtimeCallbackHandler) HandleRetrieverEnd(ctx context.Context, query string, documents []schema.Document) {
}

func (h *runtimeCallbackHandler) HandleStreamingFunc(ctx context.Context, chunk []byte) {
	if h.writer != nil && !h.streamingFailed() {
		if _, err := h.writer.Write(chunk); err != nil {
			h.recordStreamError(err)
		} else if streamWriterEmitted(h.writer) {
			if h.recordStreamToken() {
				h.logStreamEmitted(ctx, chunk)
			}
		}
	}

	// Record first token time
	h.recordFirstToken()
}

func (h *runtimeCallbackHandler) ResetStreaming(ctx context.Context) {
	resetStreamWriterState(h.writer)
	// Drop any text the previous turn streamed that the writer buffered but
	// never emitted, so residual content cannot leak into this turn.
	resetStreamBuffer(h.writer)
	h.mu.Lock()
	h.streamTokenSeen = false
	h.streamLogEmitted = false
	h.streamErr = nil
	h.mu.Unlock()
}

type streamStateResetter interface {
	ResetStreamState()
}

type streamOutputTracker interface {
	StreamEmitted() bool
}

type streamResponseFinisher interface {
	FinishResponse() bool
}

func resetStreamWriterState(writer io.Writer) {
	resetter, ok := writer.(streamStateResetter)
	if !ok {
		return
	}
	resetter.ResetStreamState()
}

func resetStreamBuffer(writer io.Writer) {
	if resetter, ok := writer.(ttsBufferResetter); ok {
		resetter.ResetBuffer()
	}
}

func finishStreamWriterResponse(writer io.Writer) bool {
	finisher, ok := writer.(streamResponseFinisher)
	if !ok {
		return false
	}
	return finisher.FinishResponse()
}

func streamWriterEmitted(writer io.Writer) bool {
	tracker, ok := writer.(streamOutputTracker)
	if !ok {
		return true
	}
	return tracker.StreamEmitted()
}

func (h *runtimeCallbackHandler) FinishStreamingResponse(context.Context) {
	finishStreamWriterResponse(h.writer)
}

func (h *runtimeCallbackHandler) AbortStreamingResponse(context.Context) {
	resetStreamBuffer(h.writer)
	resetStreamWriterState(h.writer)
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

func (h *runtimeCallbackHandler) recordStreamToken() bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.streamErr != nil {
		return false
	}
	first := !h.streamTokenSeen
	h.streamTokenSeen = true
	return first
}

func (h *runtimeCallbackHandler) streamingFailed() bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.streamErr != nil
}

func (h *runtimeCallbackHandler) recordStreamError(err error) {
	if err == nil {
		return
	}
	h.mu.Lock()
	firstErr := h.streamErr == nil
	if firstErr {
		h.streamErr = err
	}
	h.streamTokenSeen = false
	h.mu.Unlock()
	if firstErr && h.logger != nil {
		h.logger.Warn("Stream writer failed; continuing without streaming output: %v", err)
	}
}

func (h *runtimeCallbackHandler) logStreamEmitted(ctx context.Context, chunk []byte) {
	if h == nil || h.logger == nil {
		return
	}
	h.mu.Lock()
	if h.streamLogEmitted {
		h.mu.Unlock()
		return
	}
	h.streamLogEmitted = true
	requestID := h.requestID
	runID := h.runID
	h.mu.Unlock()

	role := telemetryRoleFromContext(ctx)
	if role == "" {
		role = "post_parse"
	}
	h.logger.Info("Stream emitted: role=%s chunk_len=%d chunk=%s request_id=%s run_id=%s",
		role, len(chunk), truncateForLog(string(chunk), 120), requestID, runID)
}

var _ callbacks.Handler = (*runtimeCallbackHandler)(nil)

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

func buildLLMSummarizeFn(models model.Model) SummarizeFn {
	return func(ctx context.Context, events []SessionEvent) string {
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
		result, err := llms.GenerateFromSinglePrompt(ctx, models, prompt, llms.WithMaxTokens(200))
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

func buildLLMStructuredSummarizeFn(models model.Model, logger *Logger) StructuredSummarizeFn {
	return func(ctx context.Context, events []SessionEvent) ChunkStructuredSummary {
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
		result, err := llms.GenerateFromSinglePrompt(ctx, models, structuredSummarizerPrompt+transcript.String(), llms.WithMaxTokens(800))
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

func buildLLMProfileFn(models model.Model) ProfileFn {
	return func(ctx context.Context, entries []ProfileEntry) string {
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
		result, err := llms.GenerateFromSinglePrompt(ctx, models, prompt, llms.WithMaxTokens(400))
		if err != nil {
			return ""
		}
		return strings.TrimSpace(result)
	}
}

// Close releases resources held by the runtime
func (r *Runtime) Close() error {
	maintenanceCtx, maintenanceCancel := context.WithTimeout(context.Background(), runtimeEpisodeMaintenanceTimeout)
	if err := r.episodeMaintenance.closeAndWait(maintenanceCtx); err != nil && r.logger != nil {
		r.logger.Error("episode maintenance drain on close: %v", err)
	}
	maintenanceCancel()
	if r.storageMonitor != nil {
		r.storageMonitor.Stop()
	}
	if r.storage != nil {
		r.storage.Stop()
	}
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
	if r.ttsManager != nil {
		ctx, cancel := context.WithTimeout(context.Background(), runtimeTTSCloseTimeout)
		if err := r.ttsManager.CloseContext(ctx); err != nil && r.logger != nil {
			r.logger.Warn("close TTS provider: %v", err)
		}
		cancel()
	}
	if r.logger != nil {
		r.logger.Info("Shutting down agent runtime")
		return r.logger.Close()
	}
	return nil
}
