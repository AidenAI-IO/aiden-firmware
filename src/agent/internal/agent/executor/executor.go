package executor

import (
	"aiden-agent/internal/agent/context_manager"
	"context"
	"fmt"

	"github.com/tmc/langchaingo/llms"
)

type LLMExecutor struct {
	model llms.Model
	
	contextManager *context_manager.ContextManager
}

func NewLLMExecutor(model llms.Model, contextManager *context_manager.ContextManager) *LLMExecutor {
	return &LLMExecutor{
		model: model,
		contextManager: contextManager,
	}
}

func (e *LLMExecutor) Execute(ctx context.Context, message context_manager.Message) (context_manager.Message, error) {
	e.contextManager.AppendMessage(message)
	contentResponse, err := e.model.GenerateContent(ctx, e.contextManager.ConvertToStandardMessageList())
	if err != nil {
		return context_manager.Message{}, err
	}
	if len(contentResponse.Choices) == 0 || contentResponse.Choices[0] == nil {
		return context_manager.Message{}, fmt.Errorf("model returned no choices")
	}
	return context_manager.ConvertChoiceToContextManagerMessage(*contentResponse.Choices[0]), nil
}