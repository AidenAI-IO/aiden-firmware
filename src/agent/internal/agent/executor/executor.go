package executor

import (
	"aiden-agent/internal/agent/context_manager"
	"context"
	"fmt"

	"github.com/tmc/langchaingo/llms"
)

type LLMExecutor struct {
	model          llms.Model
	contextManager *context_manager.ContextManager
}

func NewLLMExecutor(model llms.Model, contextManager *context_manager.ContextManager) *LLMExecutor {
	return &LLMExecutor{
		model:          model,
		contextManager: contextManager,
	}
}

func (e *LLMExecutor) ContextManager() *context_manager.ContextManager {
	return e.contextManager
}

func (e *LLMExecutor) AppendMessage(message context_manager.Message) error {
	return e.contextManager.AppendMessage(message)
}

func (e *LLMExecutor) GenerateContent(ctx context.Context, options ...llms.CallOption) (*llms.ContentResponse, error) {
	messages := e.contextManager.TakeStandardMessageListForModel()
	contentResponse, err := e.model.GenerateContent(ctx, messages, options...)
	if err != nil {
		return nil, err
	}
	if len(contentResponse.Choices) == 0 || contentResponse.Choices[0] == nil {
		return contentResponse, fmt.Errorf("model returned no choices")
	}
	return contentResponse, nil
}

func (e *LLMExecutor) Generate(ctx context.Context, options ...llms.CallOption) (context_manager.Message, *llms.ContentResponse, error) {
	contentResponse, err := e.GenerateContent(ctx, options...)
	if err != nil {
		return context_manager.Message{}, contentResponse, err
	}
	response := context_manager.ConvertChoiceToContextManagerMessage(*contentResponse.Choices[0])
	if err := e.contextManager.AppendMessage(response); err != nil {
		return context_manager.Message{}, contentResponse, err
	}
	return response, contentResponse, nil
}

func (e *LLMExecutor) Execute(ctx context.Context, message context_manager.Message, options ...llms.CallOption) (context_manager.Message, *llms.ContentResponse, error) {
	if err := e.contextManager.AppendMessage(message); err != nil {
		return context_manager.Message{}, nil, err
	}
	return e.Generate(ctx, options...)
}
