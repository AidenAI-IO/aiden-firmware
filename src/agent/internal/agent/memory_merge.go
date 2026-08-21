package agent

import (
	"context"
	"fmt"
	"strings"
	"sync"
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
	Apply         func(context.Context, string, []MemoryMergeReference) error
	MaxTokens     int
	Timeout       time.Duration
}

// MemoryRunGate serializes background Memory model calls across Worker
// instances. It does not guard local retrieval or Store writes.
type MemoryRunGate struct {
	once sync.Once
	sem  chan struct{}
}

func NewMemoryRunGate() *MemoryRunGate {
	return &MemoryRunGate{sem: make(chan struct{}, 1)}
}

func (g *MemoryRunGate) acquire(ctx context.Context) error {
	if g == nil {
		return nil
	}
	// Initialise lazily so the zero value is safe. This matters in tests and
	// for callers that embed the gate without going through the constructor.
	g.once.Do(func() {
		g.sem = make(chan struct{}, 1)
		g.sem <- struct{}{}
	})
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-g.sem:
		return nil
	}
}

func (g *MemoryRunGate) release() {
	if g != nil {
		// release is only called after acquire, but keeping the initialisation
		// here makes the zero-value contract explicit and avoids a nil send if a
		// future caller changes that ordering.
		g.once.Do(func() {
			g.sem = make(chan struct{}, 1)
		})
		g.sem <- struct{}{}
	}
}

// Merge runs the complete common orchestration. A caller that needs to
// persist a proposal before applying it (for crash recovery) may call Extract
// directly; normal processors should use Merge so the engine owns the whole
// raw -> top-k -> LLM -> apply sequence.
func (e *MemoryMergeEngine) Merge(ctx context.Context, req MemoryMergeRequest) error {
	refs, raw, err := e.Extract(ctx, req)
	if err != nil {
		return err
	}
	if req.Apply == nil {
		return fmt.Errorf("memory merge request has no apply callback")
	}
	return req.Apply(ctx, raw, refs)
}

type MemoryMergeEngine struct {
	model model.Model
	gate  *MemoryRunGate
}

func NewMemoryMergeEngine(m model.Model) *MemoryMergeEngine {
	return &MemoryMergeEngine{model: m, gate: NewMemoryRunGate()}
}

func NewMemoryMergeEngineWithGate(m model.Model, gate *MemoryRunGate) *MemoryMergeEngine {
	if gate == nil {
		gate = NewMemoryRunGate()
	}
	return &MemoryMergeEngine{model: m, gate: gate}
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
	if err := e.gate.acquire(callCtx); err != nil {
		return references, "", err
	}
	defer e.gate.release()
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
