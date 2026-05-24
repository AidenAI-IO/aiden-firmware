package agent

import (
	"context"
	"errors"
	"fmt"
	"io"
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
	logger           *Logger
	profileDebouncer *ProfileDebouncer
	sleep            *SleepController
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
	toolSet := NewBuiltinToolSet(cfg.HID, cfg.Audio, cfg.Search, cfg.Proxy, WithSleepController(sleepController))
	extractionCfg := LoadMemoryExtractionConfig(cfg.ConfigDir)
	modelManager := NewModelManager(cfg.Model, cfg.Proxy)
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
	rt.logger = logger
	rt.profileDebouncer = debouncer
	rt.sleep = sleepController
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
	return &Runtime{
		config:       cfg,
		models:       models,
		memories:     memories,
		tools:        tools,
		skills:       NewSkillManager(skillIndex),
		skillsLoaded: skillIndex != nil && len(skillIndex.Names()) > 0,
		sleep:        sleepController,
	}
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

	// Activate skills
	skillNames := uniqueNonEmpty(req.Skills)
	for _, skillName := range skillNames {
		if err := r.skills.Activate(ctx, skillName); err != nil {
			if r.logger != nil {
				r.logger.Error("Failed to activate skill %q: %v", skillName, err)
			}
			return RunResult{}, fmt.Errorf("activate skill %q: %w", skillName, err)
		}
	}

	if r.logger != nil && len(skillNames) > 0 {
		r.logger.Info("Activated skills: %v", skillNames)
	}

	resolvedSkills, err := r.skills.Resolve(skillNames)
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

	maxIterations := effectiveMaxIterations(r.config.MaxIterations)

	callOptions := r.models.CallOptions()
	if req.MaxTokens > 0 {
		callOptions = append(callOptions, chains.WithMaxTokens(req.MaxTokens))
	}
	var streamCallbackHandler *runtimeCallbackHandler
	var agentCallbackHandler callbacks.Handler
	if req.StreamWriter != nil || req.EventHandler != nil || r.logger != nil {
		streamCallbackHandler = &runtimeCallbackHandler{
			writer:       req.StreamWriter,
			metrics:      metrics,
			startTime:    startTime,
			logger:       r.logger,
			eventHandler: req.EventHandler,
		}
	}
	if req.StreamWriter != nil {
		agentCallbackHandler = streamCallbackHandler
		callOptions = append(callOptions, chains.WithStreamingFunc(func(ctx context.Context, chunk []byte) error {
			streamCallbackHandler.HandleStreamingFunc(ctx, chunk)
			return nil
		}))
	}
	if streamCallbackHandler != nil {
		availableTools = wrapToolsWithCallbacks(availableTools, streamCallbackHandler)
	}

	agent := r.buildAgent(model, resolvedSkills, availableTools, req.Attachments, agentCallbackHandler)
	var executorHandler callbacks.Handler
	if streamCallbackHandler != nil {
		executorHandler = streamCallbackHandler
	}
	executor := newParallelToolExecutor(agent, memoryHandle.Memory, maxIterations, executorHandler)

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
			return RunResult{}, err
		}
	}

	metrics.TotalDuration = float64(time.Since(startTime).Milliseconds())
	r.memories.SetLastPromptTokens(metrics.PromptTokens)
	if err := r.memories.AppendExchange(ctx, "default", normalizedInput, output); err != nil {
		return RunResult{}, err
	}

	memorySnapshot, err := r.memories.Snapshot(ctx, "default")
	if err != nil {
		return RunResult{}, err
	}
	if err := r.memories.SaveSnapshot(ctx, "default", memorySnapshot); err != nil {
		return RunResult{}, err
	}
	r.memories.RequestMaintenance()

	sleepRequested, sleepReason := r.sleep.Consume()
	return RunResult{
		Output:         output,
		Skills:         r.skills.GetActivatedSkills(),
		Memory:         memorySnapshot,
		Metrics:        metrics,
		SleepRequested: sleepRequested,
		SleepReason:    sleepReason,
	}, nil
}

func (r *Runtime) ClearMemory(ctx context.Context) error {
	return r.memories.ClearSession(ctx, "default")
}

func (r *Runtime) ClearAllMemory(ctx context.Context) error {
	return r.memories.ClearAll(ctx, "default")
}

func (r *Runtime) resolveTools(skills ResolvedSkills) []langtools.Tool {
	available := make([]langtools.Tool, 0)

	if r.skillsLoaded {
		available = append(available, NewActivateSkillTool(r.skills))
	}

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

	memoryTools := []string{"recall_session_chunks", "recall_memory", "save_memory", "forget_memory"}
	for _, name := range memoryTools {
		if skills.HasToolRestriction {
			if _, allowed := skills.AllowedTools[name]; !allowed {
				continue
			}
		}
		if tool, ok := r.tools.Get(name); ok {
			if !toolAlreadyIncluded(available, name) {
				available = append(available, tool)
			}
		}
	}

	return available
}

func toolAlreadyIncluded(tools []langtools.Tool, name string) bool {
	for _, t := range tools {
		if t.Name() == name {
			return true
		}
	}
	return false
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
				ToolInput: action.ToolInput,
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
				ToolInput: action.ToolInput,
				Content:   "error: " + err.Error(),
				Timestamp: time.Now(),
				IsError:   true,
			})
		}
	}
}

func (h *runtimeCallbackHandler) HandleNamedToolStart(ctx context.Context, name, input string) {}

func (h *runtimeCallbackHandler) HandleNamedToolEnd(ctx context.Context, name, input, output string) {
	if h.logger != nil {
		h.logger.Info("Tool result: name=%s output=%s", name, truncateForLog(output, 240))
	}
	if h.eventHandler != nil {
		h.eventHandler(RunEvent{
			Type:      "tool_result",
			ToolName:  name,
			ToolInput: input,
			Content:   output,
			Timestamp: time.Now(),
		})
	}
}

func (h *runtimeCallbackHandler) HandleNamedToolError(ctx context.Context, name, input string, err error) {
	if h.logger != nil {
		h.logger.Error("Tool error: name=%s err=%v", name, err)
	}
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
