package agent

import (
	"context"
	"errors"
	"fmt"
	"io"
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

type Runtime struct {
	config       Config
	models       ModelResolver
	memories     *MemoryManager
	tools        *ToolSet
	skills       *SkillManager
	skillsLoaded bool
	logger       *Logger
}

type RunRequest struct {
	Input        string
	Skills       []string
	StreamWriter io.Writer
	EventHandler func(RunEvent)
}

type RunResult struct {
	Output  string          `json:"output"`
	Skills  []string        `json:"skills"`
	Memory  []MessageRecord `json:"memory,omitempty"`
	Metrics *RunMetrics     `json:"metrics,omitempty"`
}

type RunMetrics struct {
	TotalDuration    float64 `json:"total_duration_ms"`
	PromptTokens     int     `json:"prompt_tokens,omitempty"`
	CompletionTokens int     `json:"completion_tokens,omitempty"`
	TotalTokens      int     `json:"total_tokens,omitempty"`
	FirstTokenTime   float64 `json:"first_token_time_ms,omitempty"`
}

type RunEvent struct {
	Type      string    `json:"type"`
	ToolName  string    `json:"tool_name,omitempty"`
	ToolInput string    `json:"tool_input,omitempty"`
	Content   string    `json:"content,omitempty"`
	Timestamp time.Time `json:"timestamp"`
	IsError   bool      `json:"is_error,omitempty"`
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

	rt := NewRuntimeWithDeps(cfg, NewModelManager(cfg.Model), NewMemoryManager(memoryDir), NewBuiltinToolSet(cfg.HID, cfg.Audio), skillIndex)
	rt.logger = logger
	return rt, nil
}

func NewRuntimeWithDeps(cfg Config, models ModelResolver, memories *MemoryManager, tools *ToolSet, skillIndex *SkillIndex) *Runtime {
	return &Runtime{
		config:       cfg,
		models:       models,
		memories:     memories,
		tools:        tools,
		skills:       NewSkillManager(skillIndex),
		skillsLoaded: skillIndex != nil && len(skillIndex.Names()) > 0,
	}
}

func (r *Runtime) Run(ctx context.Context, req RunRequest) (RunResult, error) {
	startTime := time.Now()
	metrics := &RunMetrics{}

	if r.logger != nil {
		r.logger.Info("Starting agent run: input=%q", req.Input)
	}

	if req.Input == "" {
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

	memoryHandle, err := r.memories.Get("default", MemoryConfig{Type: "window", WindowSize: 10})
	if err != nil {
		return RunResult{}, err
	}

	availableTools := r.resolveTools(resolvedSkills)

	maxIterations := r.config.MaxIterations
	if maxIterations <= 0 {
		maxIterations = 6
	}

	callOptions := r.models.CallOptions()
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

	agent := r.buildAgent(model, resolvedSkills, availableTools, agentCallbackHandler)
	executorOptions := []agents.Option{
		agents.WithMemory(memoryHandle.Memory),
		agents.WithMaxIterations(maxIterations),
	}
	if streamCallbackHandler != nil {
		executorOptions = append(executorOptions, agents.WithCallbacksHandler(streamCallbackHandler))
	}
	executor := agents.NewExecutor(agent, executorOptions...)

	output, err := chains.Run(ctx, executor, req.Input, callOptions...)
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

	memorySnapshot, err := r.memories.Snapshot(ctx, "default")
	if err != nil {
		return RunResult{}, err
	}
	if err := r.memories.Save(ctx, "default"); err != nil {
		return RunResult{}, err
	}

	return RunResult{
		Output:  output,
		Skills:  r.skills.GetActivatedSkills(),
		Memory:  memorySnapshot,
		Metrics: metrics,
	}, nil
}

func (r *Runtime) ClearMemory(ctx context.Context) error {
	return r.memories.Clear(ctx, "default")
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

	return available
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
	callbackHandler callbacks.Handler,
) agents.Agent {
	return NewFunctionAgent(
		model,
		availableTools,
		buildFunctionAgentSystemMessage(
			AgentConfig{Instruction: r.config.Instruction},
			skills,
			availableTools,
		),
		[]prompts.MessageFormatter{
			prompts.NewSystemMessagePromptTemplate(
				"Conversation history:\n{{.history}}",
				[]string{"history"},
			),
		},
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
	if res != nil && h.metrics != nil {
		// Extract token usage from response
		if res.Choices != nil && len(res.Choices) > 0 {
			// Try to get token usage from the response
			// Note: This depends on the LLM provider returning usage info
		}
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

func (h *runtimeCallbackHandler) HandleAgentAction(ctx context.Context, action schema.AgentAction) {
	if h.logger != nil {
		h.logger.Info("Tool call: name=%s input=%s",
			action.Tool, truncateForLog(action.ToolInput, 240))
	}
	if h.eventHandler != nil {
		h.pushPendingAction(action)
		h.eventHandler(RunEvent{
			Type:      "tool_call",
			ToolName:  action.Tool,
			ToolInput: action.ToolInput,
			Timestamp: time.Now(),
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

// Close releases resources held by the runtime
func (r *Runtime) Close() error {
	if r.logger != nil {
		r.logger.Info("Shutting down agent runtime")
		return r.logger.Close()
	}
	return nil
}
