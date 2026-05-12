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

const maxDelegateDepth = 4

type delegateDepthKey struct{}

type Runtime struct {
	config       Config
	models       ModelResolver
	memories     *MemoryManager
	tools        *ToolSet
	skills       *SkillManager
	skillsLoaded bool
}

type RunRequest struct {
	AgentName    string
	Input        string
	Skills       []string
	StreamWriter io.Writer
}

type RunResult struct {
	AgentName string          `json:"agent_name"`
	Output    string          `json:"output"`
	Skills    []string        `json:"skills"`
	Memory    []MessageRecord `json:"memory,omitempty"`
	Metrics   *RunMetrics     `json:"metrics,omitempty"`
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

	return NewRuntimeWithDeps(cfg, NewModelManager(cfg.Model), NewMemoryManager(), NewBuiltinToolSet(cfg.HID), skillIndex), nil
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

	agentName := req.AgentName
	if agentName == "" {
		agentName = r.config.DefaultAgent
	}

	cfg, ok := r.config.Agents[agentName]
	if !ok {
		return RunResult{}, fmt.Errorf("unknown agent %q", agentName)
	}
	if req.Input == "" {
		return RunResult{}, errors.New("input is required")
	}

	// Activate default skills
	skillNames := uniqueNonEmpty(append(append([]string{}, cfg.DefaultSkills...), req.Skills...))
	for _, skillName := range skillNames {
		if err := r.skills.Activate(ctx, skillName); err != nil {
			return RunResult{}, fmt.Errorf("activate skill %q: %w", skillName, err)
		}
	}

	// Resolve activated skills
	resolvedSkills, err := r.skills.Resolve(skillNames)
	if err != nil {
		return RunResult{}, err
	}

	model, err := r.models.Get()
	if err != nil {
		return RunResult{}, err
	}

	memoryHandle, err := r.memories.Get(agentName, cfg.Memory)
	if err != nil {
		return RunResult{}, err
	}

	availableTools, err := r.resolveTools(agentName, cfg, resolvedSkills)
	if err != nil {
		return RunResult{}, err
	}

	prompt := buildPrompt(agentName, cfg, resolvedSkills, availableTools)
	agent := agents.NewOneShotAgent(model, availableTools, agents.WithPrompt(prompt))

	maxIterations := cfg.MaxIterations
	if maxIterations <= 0 {
		maxIterations = 6
	}

	executor := agents.NewExecutor(
		agent,
		agents.WithMemory(memoryHandle.Memory),
		agents.WithMaxIterations(maxIterations),
	)

	// Build call options
	callOptions := r.models.CallOptions()

	// Add streaming callback if StreamWriter is provided
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
		return RunResult{}, err
	}

	// Calculate total duration
	metrics.TotalDuration = float64(time.Since(startTime).Milliseconds())

	memorySnapshot, err := r.memories.Snapshot(ctx, agentName)
	if err != nil {
		return RunResult{}, err
	}

	return RunResult{
		AgentName: agentName,
		Output:    output,
		Skills:    r.skills.GetActivatedSkills(),
		Memory:    memorySnapshot,
		Metrics:   metrics,
	}, nil
}

func (r *Runtime) ClearMemory(ctx context.Context, agentName string) error {
	if agentName == "" {
		agentName = r.config.DefaultAgent
	}
	return r.memories.Clear(ctx, agentName)
}

func (r *Runtime) resolveTools(agentName string, cfg AgentConfig, skills ResolvedSkills) ([]langtools.Tool, error) {
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
			if !ok {
				return nil, fmt.Errorf("skill references unknown tool %q", toolName)
			}
			available = append(available, tool)
		}
	} else {
		available = append(available, r.tools.All()...)
	}

	// Add child agent delegate tools
	for _, child := range cfg.Children {
		if skills.HasChildRestriction {
			if _, ok := skills.AllowedChildren[child]; !ok {
				continue
			}
		}

		toolName := DelegateToolName(child)
		if skills.HasToolRestriction {
			if _, ok := skills.AllowedTools[toolName]; !ok {
				continue
			}
		}

		childCfg := r.config.Agents[child]
		available = append(available, &DelegateTool{
			name: toolName,
			description: fmt.Sprintf(
				"Delegate a focused sub-task to child agent %q. Child description: %s",
				child,
				childCfg.Description,
			),
			run: func(ctx context.Context, input string) (string, error) {
				return r.runChild(ctx, agentName, child, input)
			},
		})
	}

	return available, nil
}

func (r *Runtime) runChild(ctx context.Context, parentName string, childName string, input string) (string, error) {
	depth, _ := ctx.Value(delegateDepthKey{}).(int)
	if depth >= maxDelegateDepth {
		return "", fmt.Errorf("delegate depth exceeded while %q delegates to %q", parentName, childName)
	}

	result, err := r.Run(context.WithValue(ctx, delegateDepthKey{}, depth+1), RunRequest{
		AgentName: childName,
		Input:     input,
	})
	if err != nil {
		return "", err
	}
	return result.Output, nil
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
