package agent

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"

	"aiden-agent/internal/agent/model"

	"github.com/tmc/langchaingo/llms"
)

const defaultContextWindowFallback = 8_192

type HardContextGuard struct {
	spec model.ModelSpec
}

type ContextBudgetExceededError struct {
	EstimatedPromptTokens int
	MessageTokens         int
	ToolSchemaTokens      int
	InputBudget           int
	ContextWindow         int
	MaxResponseTokens     int
}

func (e *ContextBudgetExceededError) Error() string {
	if e == nil {
		return "context budget exceeded"
	}
	return fmt.Sprintf(
		"context budget exceeded: estimated prompt tokens %d exceed input budget %d (context window %d, max response %d)",
		e.EstimatedPromptTokens,
		e.InputBudget,
		e.ContextWindow,
		e.MaxResponseTokens,
	)
}

func NewHardContextGuard(spec model.ModelSpec) HardContextGuard {
	return HardContextGuard{spec: spec}
}

func (g HardContextGuard) Apply(_ context.Context, messages []llms.MessageContent, options []llms.CallOption) ([]llms.MessageContent, error) {
	contextWindow := g.spec.ContextWindow
	if contextWindow <= 0 {
		return messages, nil
	}
	var callOptions llms.CallOptions
	for _, option := range options {
		if option != nil {
			option(&callOptions)
		}
	}
	maxResponseTokens := g.spec.MaxOutput
	if callOptions.MaxTokens > 0 {
		maxResponseTokens = callOptions.MaxTokens
	}
	inputBudget := inputContextBudget(contextWindow, maxResponseTokens)
	messageTokens := estimateMessagesTokens(messages)
	toolSchemaTokens := estimateToolSchemaTokens(callOptions)
	estimatedPromptTokens := messageTokens + toolSchemaTokens
	if estimatedPromptTokens <= inputBudget {
		return messages, nil
	}
	return nil, &ContextBudgetExceededError{
		EstimatedPromptTokens: estimatedPromptTokens,
		MessageTokens:         messageTokens,
		ToolSchemaTokens:      toolSchemaTokens,
		InputBudget:           inputBudget,
		ContextWindow:         contextWindow,
		MaxResponseTokens:     maxResponseTokens,
	}
}

func inputContextBudget(contextWindow, maxResponseTokens int) int {
	if contextWindow <= 0 {
		return 0
	}
	if maxResponseTokens <= 0 {
		return contextWindow
	}
	if maxResponseTokens >= contextWindow {
		return 1
	}
	return contextWindow - maxResponseTokens
}

func estimateMessagesTokens(messages []llms.MessageContent) int {
	total := 0
	for _, message := range messages {
		total += estimateMessageTokens(message)
	}
	return total
}

func estimateMessageTokens(message llms.MessageContent) int {
	total := 4 + estimateTextTokens(string(message.Role))
	for _, part := range message.Parts {
		total += estimateContentPartTokens(part)
	}
	return total
}

func estimateContentPartTokens(part llms.ContentPart) int {
	switch p := part.(type) {
	case llms.TextContent:
		return estimateTextTokens(p.Text)
	case llms.ToolCallResponse:
		return 8 + estimateTextTokens(p.ToolCallID) + estimateTextTokens(p.Name) + estimateTextTokens(p.Content)
	case llms.ToolCall:
		name := ""
		arguments := ""
		if p.FunctionCall != nil {
			name = p.FunctionCall.Name
			arguments = p.FunctionCall.Arguments
		}
		return 8 + estimateTextTokens(p.ID) + estimateTextTokens(name) + estimateTextTokens(arguments)
	case llms.ImageURLContent:
		return estimateImageURLTokens(p.URL)
	case llms.BinaryContent:
		return estimateBinaryTokens(p.Data)
	default:
		return estimateTextTokens(fmt.Sprint(part))
	}
}

func estimateImageURLTokens(url string) int {
	if strings.HasPrefix(url, "data:image/") {
		return 1024
	}
	return estimateTextTokens(url)
}

func estimateBinaryTokens(data []byte) int {
	if len(data) == 0 {
		return 0
	}
	encodedLen := base64.StdEncoding.EncodedLen(len(data))
	return (encodedLen + 3) / 4
}

func estimateToolSchemaTokens(options llms.CallOptions) int {
	total := 0
	if len(options.Tools) > 0 {
		total += estimateJSONTokens(options.Tools)
	}
	if len(options.Functions) > 0 {
		total += estimateJSONTokens(options.Functions)
	}
	if options.ToolChoice != nil {
		total += estimateJSONTokens(options.ToolChoice)
	}
	if options.FunctionCallBehavior != "" {
		total += estimateTextTokens(string(options.FunctionCallBehavior))
	}
	return total
}

func estimateJSONTokens(value any) int {
	data, err := json.Marshal(value)
	if err != nil {
		return estimateTextTokens(fmt.Sprint(value))
	}
	return estimateTextTokens(string(data))
}
