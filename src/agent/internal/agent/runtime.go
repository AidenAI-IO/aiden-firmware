package agent

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"math"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/tmc/langchaingo/agents"
	"github.com/tmc/langchaingo/callbacks"
	"github.com/tmc/langchaingo/chains"
	"github.com/tmc/langchaingo/llms"
	"github.com/tmc/langchaingo/prompts"
	"github.com/tmc/langchaingo/schema"
	langtools "github.com/tmc/langchaingo/tools"
)

func effectiveMaxIterations(configured int) int {
	if configured <= 0 {
		return math.MaxInt
	}
	return configured
}

type Runtime struct {
	config           Config
	models           ModelResolver
	memories         *MemoryManager
	tools            *ToolSet
	skills           *SkillManager
	skillsLoaded     bool
	skillsReloadMu   sync.Mutex
	skillsDirty      bool
	mergeWorker      *MergeWorker
	logger           *Logger
	profileDebouncer *ProfileDebouncer
	sleep            *SleepController
	memoryPlane      MemoryPlane
}

type RunRequest struct {
	Input        string
	Attachments  []InputAttachment
	Skills       []string
	StreamWriter io.Writer
	MaxTokens    int
	EventHandler func(RunEvent)
}

type RunResult struct {
	Output         string          `json:"output"`
	Skills         []string        `json:"skills"`
	Memory         []MessageRecord `json:"memory,omitempty"`
	Metrics        *RunMetrics     `json:"metrics,omitempty"`
	SleepRequested bool            `json:"sleep_requested,omitempty"`
	SleepReason    string          `json:"sleep_reason,omitempty"`
	SpeechStreamed bool            `json:"-"`
}

type RunMetrics struct {
	TotalDuration    float64 `json:"total_duration_ms"`
	PromptTokens     int     `json:"prompt_tokens,omitempty"`
	CompletionTokens int     `json:"completion_tokens,omitempty"`
	TotalTokens      int     `json:"total_tokens,omitempty"`
	FirstTokenTime   float64 `json:"first_token_time_ms,omitempty"`
}

type RunEvent struct {
	Type        string    `json:"type"`
	Role        string    `json:"role,omitempty"`
	ToolName    string    `json:"tool_name,omitempty"`
	ToolInput   string    `json:"tool_input,omitempty"`
	Description string    `json:"description,omitempty"`
	Content     string    `json:"content,omitempty"`
	Timestamp   time.Time `json:"timestamp"`
	IsError     bool      `json:"is_error,omitempty"`
}

type usageTrackingModel struct {
	inner   llms.Model
	metrics *RunMetrics
}

func (m *usageTrackingModel) GenerateContent(ctx context.Context, messages []llms.MessageContent, options ...llms.CallOption) (*llms.ContentResponse, error) {
	res, err := m.inner.GenerateContent(ctx, messages, options...)
	if err == nil {
		recordUsageMetrics(m.metrics, res)
	}
	return res, err
}

func (m *usageTrackingModel) Call(ctx context.Context, prompt string, options ...llms.CallOption) (string, error) {
	return m.inner.Call(ctx, prompt, options...)
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
	toolSet := NewBuiltinToolSet(cfg.HID, cfg.Audio, cfg.Search, proxy, WithSleepController(sleepController))
	extractionCfg := LoadMemoryExtractionConfig(cfg.ConfigDir)
	modelManager := NewModelManager(cfg.Model, proxy)
	summarizeFn := buildLLMSummarizeFn(modelManager)
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

	rt := NewRuntimeWithDeps(cfg, modelManager, NewMemoryManager(memoryDir, WithExtractionConfig(extractionCfg), WithSummarizeFn(summarizeFn), WithProfileFn(profileFn), WithContextWindowFn(contextWindowFn), WithMemoryProfileDebouncer(debouncer), WithMemoryLogger(logger)), toolSet, skillIndex)

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
		config:       cfg,
		models:       models,
		memories:     memories,
		tools:        tools,
		skills:       skillManager,
		skillsLoaded: skillIndex != nil && len(skillIndex.Names()) > 0,
		sleep:        sleepController,
	}
	if cfg.ConfigDir != "" {
		rt.memoryPlane = NewFilesystemMemoryPlane(filepath.Join(cfg.ConfigDir, "memory"), LoadMemoryExtractionConfig(cfg.ConfigDir), nil)
	}
	return rt
}

func (r *Runtime) Run(ctx context.Context, req RunRequest) (RunResult, error) {
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
	model = &usageTrackingModel{inner: model, metrics: metrics}

	memoryHandle, err := r.memories.Get("default", MemoryConfig{Type: "window", WindowSize: 10})
	if err != nil {
		return RunResult{}, err
	}

	availableTools := r.resolveTools(resolvedSkills)
	retrieveReq := MemoryRetrieveRequest{
		Input:       normalizedInput,
		Attachments: req.Attachments,
		Skills:      skillNames,
		ToolNames:   toolNamesFromTools(availableTools),
		DeviceID:    "default",
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
	}

	maxIterations := effectiveMaxIterations(r.config.MaxIterations)

	callOptions := r.models.CallOptions()
	if req.MaxTokens > 0 {
		callOptions = append(callOptions, chains.WithMaxTokens(req.MaxTokens))
	}
	var streamCallbackHandler *runtimeCallbackHandler
	if req.StreamWriter != nil || req.EventHandler != nil || r.logger != nil {
		streamCallbackHandler = &runtimeCallbackHandler{
			writer:       req.StreamWriter,
			metrics:      metrics,
			startTime:    startTime,
			logger:       r.logger,
			eventHandler: req.EventHandler,
		}
	}
	if streamCallbackHandler != nil {
		availableTools = wrapToolsWithCallbacks(availableTools, streamCallbackHandler)
	}

	var executorHandler callbacks.Handler
	if streamCallbackHandler != nil {
		executorHandler = streamCallbackHandler
	}
	profiles := r.buildRoleProfiles(resolvedSkills, availableTools, memoryContext)
	executor := newRoleCollaborativeExecutor(model, profiles, availableTools, memoryHandle.Memory, maxIterations, req.Attachments, executorHandler, episodeRecorder)

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
			r.commitEpisodeBestEffort(episodeRecorder, normalizedInput, output, metrics, err)
			return RunResult{}, err
		}
	}

	metrics.TotalDuration = float64(time.Since(startTime).Milliseconds())
	r.memories.SetLastPromptTokens(metrics.PromptTokens)
	if err := r.memories.AppendExchange(ctx, "default", normalizedInput, output); err != nil {
		r.commitEpisodeBestEffort(episodeRecorder, normalizedInput, output, metrics, err)
		return RunResult{}, err
	}

	memorySnapshot, err := r.memories.Snapshot(ctx, "default")
	if err != nil {
		r.commitEpisodeBestEffort(episodeRecorder, normalizedInput, output, metrics, err)
		return RunResult{}, err
	}
	if err := r.memories.SaveSnapshot(ctx, "default", memorySnapshot); err != nil {
		r.commitEpisodeBestEffort(episodeRecorder, normalizedInput, output, metrics, err)
		return RunResult{}, err
	}
	r.memories.RequestMaintenance()
	r.commitEpisodeBestEffort(episodeRecorder, normalizedInput, output, metrics, nil)

	sleepRequested, sleepReason := r.sleep.Consume()
	return RunResult{
		Output:         output,
		Skills:         runSkills.GetActivatedSkills(),
		Memory:         memorySnapshot,
		Metrics:        metrics,
		SleepRequested: sleepRequested,
		SleepReason:    sleepReason,
	}, nil
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
			tool, ok := r.tools.Get(toolName)
			if ok {
				available = append(available, tool)
			}
		}
	} else {
		available = append(available, r.tools.All()...)
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

func (r *Runtime) commitEpisodeBestEffort(recorder *EpisodeRecorder, input string, output string, metrics *RunMetrics, runErr error) {
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
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := r.memoryPlane.CommitEpisode(ctx, episode); err != nil && r.logger != nil {
		r.logger.Warn("[memory] commit episode failed: %v", err)
	}
}

func wrapToolsWithCallbacks(tools []langtools.Tool, handler callbacks.Handler) []langtools.Tool {
	if handler == nil {
		return tools
	}
	wrapped := make([]langtools.Tool, 0, len(tools))
	for _, tool := range tools {
		wrapped = append(wrapped, &callbackTool{
			inner:   tool,
			handler: handler,
		})
	}
	return wrapped
}

func (r *Runtime) buildAgent(
	model llms.Model,
	skills ResolvedSkills,
	availableTools []langtools.Tool,
	attachments []InputAttachment,
	callbackHandler callbacks.Handler,
) agents.Agent {
	systemMessage := buildFunctionAgentSystemMessage(
		AgentConfig{
			Instruction:      r.config.Instruction,
			AdditionalPrompt: r.config.AdditionalPrompt,
		},
		skills,
		availableTools,
	)
	if r.config.ConfigDir != "" {
		sessionSummary, _ := os.ReadFile(filepath.Join(r.config.ConfigDir, "memory", "session", "summary.md"))
		if len(sessionSummary) > 0 {
			systemMessage += "\n\n" + string(sessionSummary)
		}
		profile, _ := os.ReadFile(filepath.Join(r.config.ConfigDir, "memory", "long_term", "profile.md"))
		if len(profile) > 0 {
			systemMessage += "\n\n" + string(profile)
		}
	}
	return NewFunctionAgent(
		model,
		availableTools,
		systemMessage,
		[]prompts.MessageFormatter{
			prompts.NewSystemMessagePromptTemplate(
				"Conversation history:\n{{.history}}",
				[]string{"history"},
			),
		},
		attachments,
		callbackHandler,
	)
}

func (r *Runtime) buildRoleProfiles(skills ResolvedSkills, availableTools []langtools.Tool, memoryContext MemoryContext) RoleProfiles {
	if memoryContext.IsEmpty() && r.config.ConfigDir != "" {
		memoryContext = normalizeMemoryContext(r.memoryContextForPrompt())
	}
	return buildRoleProfiles(
		AgentConfig{
			Instruction:      r.config.Instruction,
			AdditionalPrompt: r.config.AdditionalPrompt,
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
	writer         io.Writer
	metrics        *RunMetrics
	startTime      time.Time
	firstTokenSeen bool
	logger         *Logger
	eventHandler   func(RunEvent)
	mu             sync.Mutex
	pendingActions []schema.AgentAction
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
	metrics.PromptTokens = 0
	metrics.CompletionTokens = 0
	metrics.TotalTokens = 0

	info := res.Choices[0].GenerationInfo
	if info == nil {
		return
	}

	if v, ok := usageMetricInt(info["prompt_tokens"]); ok {
		metrics.PromptTokens = v
	}
	if v, ok := usageMetricInt(info["completion_tokens"]); ok {
		metrics.CompletionTokens = v
	}
	if v, ok := usageMetricInt(info["total_tokens"]); ok {
		metrics.TotalTokens = v
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
			ToolName:  name,
			ToolInput: input,
			Content:   "error: " + err.Error(),
			Timestamp: time.Now(),
			IsError:   true,
		})
	}
}

func (h *runtimeCallbackHandler) HandleAgentAction(ctx context.Context, action schema.AgentAction) {
	description := toolDescriptionFromAction(action)
	if h.logger != nil {
		if description != "" {
			h.logger.Info("Tool call: name=%s input=%s description=%s",
				action.Tool, truncateForLog(action.ToolInput, 240), truncateForLog(description, 240))
		} else {
			h.logger.Info("Tool call: name=%s input=%s",
				action.Tool, truncateForLog(action.ToolInput, 240))
		}
	}
	if h.eventHandler != nil {
		h.pushPendingAction(action)
		h.eventHandler(RunEvent{
			Type:        "tool_call",
			ToolName:    action.Tool,
			ToolInput:   action.ToolInput,
			Description: description,
			Content:     description,
			Timestamp:   time.Now(),
		})
	}
}

func (h *runtimeCallbackHandler) HandleAgentFinish(ctx context.Context, finish schema.AgentFinish) {}

func (h *runtimeCallbackHandler) HandleRoleOutput(ctx context.Context, role, content string) {
	content = strings.TrimSpace(content)
	if h.logger != nil {
		h.logger.Info("Role output: role=%s content=%s", role, truncateForLog(content, 1000))
	}
	if h.eventHandler != nil {
		h.eventHandler(RunEvent{
			Type:      "role_output",
			Role:      role,
			Content:   content,
			Timestamp: time.Now(),
		})
	}
}

func (h *runtimeCallbackHandler) HandleRetrieverStart(ctx context.Context, query string) {}

func (h *runtimeCallbackHandler) HandleRetrieverEnd(ctx context.Context, query string, documents []schema.Document) {
}

func (h *runtimeCallbackHandler) HandleStreamingFunc(ctx context.Context, chunk []byte) {
	if h.writer != nil {
		h.writer.Write(chunk)
	}

	// Record first token time
	if !h.firstTokenSeen && h.metrics != nil {
		h.firstTokenSeen = true
		h.metrics.FirstTokenTime = float64(time.Since(h.startTime).Milliseconds())
	}
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
