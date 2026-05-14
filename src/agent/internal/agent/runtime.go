package agent

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/tmc/langchaingo/agents"
	"github.com/tmc/langchaingo/chains"
	"github.com/tmc/langchaingo/llms"
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

	rt := NewRuntimeWithDeps(cfg, NewModelManager(cfg.Model), NewMemoryManager(), NewBuiltinToolSet(cfg.HID), skillIndex)
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

	prompt := buildPrompt("agent", AgentConfig{Instruction: r.config.Instruction}, resolvedSkills, availableTools)
	agent := agents.NewOneShotAgent(model, availableTools, agents.WithPrompt(prompt))

	maxIterations := r.config.MaxIterations
	if maxIterations <= 0 {
		maxIterations = 6
	}

	executor := agents.NewExecutor(
		agent,
		agents.WithMemory(memoryHandle.Memory),
		agents.WithMaxIterations(maxIterations),
	)

	callOptions := r.models.CallOptions()

	if req.StreamWriter != nil {
		streamHandler := &streamCallbackHandler{
			writer:    req.StreamWriter,
			metrics:   metrics,
			startTime: startTime,
		}
		callOptions = append(callOptions, chains.WithCallback(streamHandler))
	}

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

// streamCallbackHandler implements callbacks.Handler for streaming output
type streamCallbackHandler struct {
	writer         io.Writer
	metrics        *RunMetrics
	startTime      time.Time
	firstTokenSeen bool
}

func (h *streamCallbackHandler) HandleText(ctx context.Context, text string) {
	if h.writer != nil {
		h.writer.Write([]byte(text))
	}
}

func (h *streamCallbackHandler) HandleLLMStart(ctx context.Context, prompts []string) {}

func (h *streamCallbackHandler) HandleLLMGenerateContentStart(ctx context.Context, ms []llms.MessageContent) {}

func (h *streamCallbackHandler) HandleLLMGenerateContentEnd(ctx context.Context, res *llms.ContentResponse) {
	if res != nil && h.metrics != nil {
		// Extract token usage from response
		if res.Choices != nil && len(res.Choices) > 0 {
			// Try to get token usage from the response
			// Note: This depends on the LLM provider returning usage info
		}
	}
}

func (h *streamCallbackHandler) HandleLLMError(ctx context.Context, err error) {}

func (h *streamCallbackHandler) HandleChainStart(ctx context.Context, inputs map[string]any) {}

func (h *streamCallbackHandler) HandleChainEnd(ctx context.Context, outputs map[string]any) {}

func (h *streamCallbackHandler) HandleChainError(ctx context.Context, err error) {}

func (h *streamCallbackHandler) HandleToolStart(ctx context.Context, input string) {}

func (h *streamCallbackHandler) HandleToolEnd(ctx context.Context, output string) {}

func (h *streamCallbackHandler) HandleToolError(ctx context.Context, err error) {}

func (h *streamCallbackHandler) HandleAgentAction(ctx context.Context, action schema.AgentAction) {}

func (h *streamCallbackHandler) HandleAgentFinish(ctx context.Context, finish schema.AgentFinish) {}

func (h *streamCallbackHandler) HandleRetrieverStart(ctx context.Context, query string) {}

func (h *streamCallbackHandler) HandleRetrieverEnd(ctx context.Context, query string, documents []schema.Document) {}

func (h *streamCallbackHandler) HandleStreamingFunc(ctx context.Context, chunk []byte) {
	if h.writer != nil {
		h.writer.Write(chunk)
	}

	// Record first token time
	if !h.firstTokenSeen && h.metrics != nil {
		h.firstTokenSeen = true
		h.metrics.FirstTokenTime = float64(time.Since(h.startTime).Milliseconds())
	}
}

// Close releases resources held by the runtime
func (r *Runtime) Close() error {
	if r.logger != nil {
		r.logger.Info("Shutting down agent runtime")
		return r.logger.Close()
	}
	return nil
}
