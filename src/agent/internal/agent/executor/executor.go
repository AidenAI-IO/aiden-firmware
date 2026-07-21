package executor

import (
	"aiden-agent/internal/agent/contextmanager"
	"context"
	"errors"
	"fmt"

	"github.com/tmc/langchaingo/llms"
)

// LLMCallError marks failures that occurred while requesting or validating an
// LLM response. Callers can distinguish these from local runtime, persistence,
// skill, and configuration failures without matching error strings.
type LLMCallError struct {
	err error
}

func MarkLLMCallError(err error) error {
	if err == nil {
		return nil
	}
	var existing *LLMCallError
	if errors.As(err, &existing) {
		return err
	}
	return &LLMCallError{err: err}
}

func (e *LLMCallError) Error() string {
	if e == nil || e.err == nil {
		return ""
	}
	return e.err.Error()
}

func (e *LLMCallError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.err
}

func (e *LLMCallError) IsLLMTurnFailureSource() {}

type LLMExecutor struct {
	model          llms.Model
	contextManager *contextmanager.ContextManager
	transforms     []OutboundMessageTransform
}

func NewLLMExecutor(model llms.Model, contextManager *contextmanager.ContextManager, transforms ...OutboundMessageTransform) *LLMExecutor {
	return &LLMExecutor{
		model:          model,
		contextManager: contextManager,
		transforms:     transforms,
	}
}

func (e *LLMExecutor) ContextManager() *contextmanager.ContextManager {
	return e.contextManager
}

func (e *LLMExecutor) AppendMessage(message contextmanager.Message) error {
	return e.contextManager.AppendMessage(message)
}

func (e *LLMExecutor) GenerateContent(ctx context.Context, options ...llms.CallOption) (*llms.ContentResponse, error) {
	messages := e.contextManager.CloneMessageList()
	for _, transform := range e.transforms {
		if transform == nil {
			continue
		}
		messages = transform.Transform(messages)
	}
	standard := contextmanager.ConvertMessageList(messages)
	contentResponse, err := e.model.GenerateContent(ctx, standard, options...)
	if err != nil {
		return nil, MarkLLMCallError(err)
	}
	if len(contentResponse.Choices) == 0 || contentResponse.Choices[0] == nil {
		return contentResponse, MarkLLMCallError(fmt.Errorf("model returned no choices"))
	}
	return contentResponse, nil
}

func (e *LLMExecutor) Generate(ctx context.Context, options ...llms.CallOption) (contextmanager.Message, *llms.ContentResponse, error) {
	contentResponse, err := e.GenerateContent(ctx, options...)
	if err != nil {
		return contextmanager.Message{}, contentResponse, err
	}
	response := contextmanager.ConvertChoiceToContextManagerMessage(*contentResponse.Choices[0])
	if err := e.contextManager.AppendMessage(response); err != nil {
		return contextmanager.Message{}, contentResponse, err
	}
	return response, contentResponse, nil
}

func (e *LLMExecutor) Execute(ctx context.Context, message contextmanager.Message, options ...llms.CallOption) (contextmanager.Message, *llms.ContentResponse, error) {
	if err := e.contextManager.AppendMessage(message); err != nil {
		return contextmanager.Message{}, nil, err
	}
	return e.Generate(ctx, options...)
}
