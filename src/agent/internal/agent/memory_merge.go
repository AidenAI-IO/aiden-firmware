package agent

import (
	"context"
	"fmt"
	"strings"
	"time"

	"aiden-agent/internal/agent/model"
	"github.com/tmc/langchaingo/llms"
)

// MemoryMergeReference is the normalized top-k context passed to the model.
// The scenario processor chooses how to serialize its raw source records, but
// the engine owns collecting related memories and making the model call.
type MemoryMergeReference struct {
	Scope        string            `json:"scope"`
	ID           string            `json:"id"`
	Type         string            `json:"type,omitempty"`
	Status       string            `json:"status,omitempty"`
	Title        string            `json:"title,omitempty"`
	Summary      string            `json:"summary,omitempty"`
	Content      string            `json:"content,omitempty"`
	Tags         []string          `json:"tags,omitempty"`
	Entities     []string          `json:"entities,omitempty"`
	Revision     int               `json:"revision,omitempty"`
	ExpiresAt    string            `json:"expires_at,omitempty"`
	Priority     int               `json:"priority,omitempty"`
	Confidence   float64           `json:"confidence,omitempty"`
	SourceRefs   []MemorySourceRef `json:"source_refs,omitempty"`
	EvidenceRefs []MemorySourceRef `json:"evidence_refs,omitempty"`
}

// MemoryMergeRequest is the scenario seam. Search and BuildMessages are
// supplied by a Processor; retrieval, LLM invocation, timeout and response
// extraction are owned by MemoryMergeEngine.
type MemoryMergeRequest struct {
	Search        func(context.Context) ([]MemoryMergeReference, error)
	BuildMessages func([]MemoryMergeReference) ([]llms.MessageContent, error)
	MaxTokens     int
	Timeout       time.Duration
}

type MemoryMergeEngine struct {
	model model.Model
}

func NewMemoryMergeEngine(m model.Model) *MemoryMergeEngine {
	return &MemoryMergeEngine{model: m}
}

func (e *MemoryMergeEngine) Extract(ctx context.Context, req MemoryMergeRequest) ([]MemoryMergeReference, string, error) {
	if e == nil || e.model == nil {
		return nil, "", fmt.Errorf("memory merge model is not configured")
	}
	if req.Search == nil || req.BuildMessages == nil {
		return nil, "", fmt.Errorf("memory merge request is incomplete")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	references, err := req.Search(ctx)
	if err != nil {
		return nil, "", err
	}
	messages, err := req.BuildMessages(references)
	if err != nil {
		return nil, "", err
	}
	if len(messages) == 0 {
		return references, "", fmt.Errorf("memory merge request has no messages")
	}
	timeout := req.Timeout
	if timeout <= 0 {
		timeout = 60 * time.Second
	}
	callCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	maxTokens := req.MaxTokens
	if maxTokens <= 0 {
		maxTokens = 2200
	}
	response, err := e.model.GenerateContent(callCtx, messages, llms.WithJSONMode(), llms.WithMaxTokens(maxTokens))
	if err != nil {
		return references, "", fmt.Errorf("memory merge model call: %w", err)
	}
	if response == nil || len(response.Choices) == 0 || strings.TrimSpace(response.Choices[0].Content) == "" {
		return references, "", fmt.Errorf("memory merge model returned an empty response")
	}
	return references, stripJSONFences(response.Choices[0].Content), nil
}
