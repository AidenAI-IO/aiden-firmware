package agent

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/tmc/langchaingo/callbacks"
	"github.com/tmc/langchaingo/chains"
	"github.com/tmc/langchaingo/llms"
	"github.com/tmc/langchaingo/prompts"
	"github.com/tmc/langchaingo/schema"
	langtools "github.com/tmc/langchaingo/tools"
)

type FunctionAgent struct {
	LLM              llms.Model
	Prompt           prompts.FormatPrompter
	Tools            []langtools.Tool
	OutputKey        string
	CallbacksHandler callbacks.Handler
}

func NewFunctionAgent(
	llm llms.Model,
	tools []langtools.Tool,
	systemMessage string,
	extraMessages []prompts.MessageFormatter,
	callbackHandler callbacks.Handler,
) *FunctionAgent {
	messageFormatters := []prompts.MessageFormatter{
		prompts.NewSystemMessagePromptTemplate(systemMessage, nil),
	}
	messageFormatters = append(messageFormatters, extraMessages...)
	messageFormatters = append(messageFormatters, prompts.NewHumanMessagePromptTemplate("{{.input}}", []string{"input"}))

	return &FunctionAgent{
		LLM:              llm,
		Prompt:           prompts.NewChatPromptTemplate(messageFormatters),
		Tools:            tools,
		OutputKey:        "output",
		CallbacksHandler: callbackHandler,
	}
}

func (a *FunctionAgent) Plan(
	ctx context.Context,
	intermediateSteps []schema.AgentStep,
	inputs map[string]string,
	options ...chains.ChainCallOption,
) ([]schema.AgentAction, *schema.AgentFinish, error) {
	fullInputs := make(map[string]any, len(inputs))
	for key, value := range inputs {
		fullInputs[key] = value
	}

	var stream func(ctx context.Context, chunk []byte) error
	if a.CallbacksHandler != nil {
		stream = func(ctx context.Context, chunk []byte) error {
			a.CallbacksHandler.HandleStreamingFunc(ctx, chunk)
			return nil
		}
	}

	prompt, err := a.Prompt.FormatPrompt(fullInputs)
	if err != nil {
		return nil, nil, err
	}

	messages := make([]llms.MessageContent, 0, len(prompt.Messages())+len(intermediateSteps)*3)
	for _, msg := range prompt.Messages() {
		messages = append(messages, chatMessageToContent(msg))
	}
	messages = append(messages, constructFunctionScratchPad(intermediateSteps)...)

	llmOptions := []llms.CallOption{
		llms.WithTools(a.toolsAsLLM()),
		llms.WithStreamingFunc(stream),
	}
	llmOptions = append(llmOptions, chains.GetLLMCallOptions(options...)...)

	result, err := a.LLM.GenerateContent(ctx, messages, llmOptions...)
	if err != nil {
		return nil, nil, err
	}

	return a.ParseOutput(result)
}

func (a *FunctionAgent) GetInputKeys() []string {
	return a.Prompt.GetInputVariables()
}

func (a *FunctionAgent) GetOutputKeys() []string {
	return []string{a.OutputKey}
}

func (a *FunctionAgent) GetTools() []langtools.Tool {
	return a.Tools
}

func (a *FunctionAgent) ParseOutput(contentResp *llms.ContentResponse) ([]schema.AgentAction, *schema.AgentFinish, error) {
	if contentResp == nil || len(contentResp.Choices) == 0 {
		return nil, nil, fmt.Errorf("no choices in response")
	}
	choice := contentResp.Choices[0]

	if len(choice.ToolCalls) > 0 {
		actions := make([]schema.AgentAction, 0, len(choice.ToolCalls))
		for _, toolCall := range choice.ToolCalls {
			functionName := toolCall.FunctionCall.Name
			toolInputStr := toolCall.FunctionCall.Arguments
			toolInput := extractToolInput(toolInputStr)

			contentMsg := "\n"
			if choice.Content != "" {
				contentMsg = fmt.Sprintf("responded: %s\n", choice.Content)
			}

			actions = append(actions, schema.AgentAction{
				Tool:      functionName,
				ToolInput: toolInput,
				Log:       fmt.Sprintf("Invoking: %s with %s %s", functionName, toolInputStr, contentMsg),
				ToolID:    toolCall.ID,
			})
		}
		return actions, nil, nil
	}

	if choice.FuncCall != nil {
		functionName := choice.FuncCall.Name
		toolInputStr := choice.FuncCall.Arguments
		toolInput := extractToolInput(toolInputStr)

		contentMsg := "\n"
		if choice.Content != "" {
			contentMsg = fmt.Sprintf("responded: %s\n", choice.Content)
		}

		return []schema.AgentAction{{
			Tool:      functionName,
			ToolInput: toolInput,
			Log:       fmt.Sprintf("Invoking: %s with %s %s", functionName, toolInputStr, contentMsg),
		}}, nil, nil
	}

	return nil, &schema.AgentFinish{
		ReturnValues: map[string]any{
			a.OutputKey: extractFinalAnswer(choice.Content),
		},
		Log: choice.Content,
	}, nil
}

func (a *FunctionAgent) toolsAsLLM() []llms.Tool {
	result := make([]llms.Tool, 0, len(a.Tools))
	for _, tool := range a.Tools {
		result = append(result, llms.Tool{
			Type: "function",
			Function: &llms.FunctionDefinition{
				Name:        tool.Name(),
				Description: tool.Description(),
				Parameters: map[string]any{
					"type": "object",
					"properties": map[string]any{
						"__arg1": map[string]string{
							"title": "__arg1",
							"type":  "string",
						},
					},
					"required": []string{"__arg1"},
				},
			},
		})
	}
	return result
}

func chatMessageToContent(msg llms.ChatMessage) llms.MessageContent {
	return llms.MessageContent{
		Role:  msg.GetType(),
		Parts: []llms.ContentPart{llms.TextPart(msg.GetContent())},
	}
}

func constructFunctionScratchPad(steps []schema.AgentStep) []llms.MessageContent {
	if len(steps) == 0 {
		return nil
	}

	messages := make([]llms.MessageContent, 0, len(steps)*3)

	for i := 0; i < len(steps); {
		groupEnd := i + 1
		for groupEnd < len(steps) && steps[groupEnd].Action.Log == steps[i].Action.Log {
			groupEnd++
		}

		toolCallParts := make([]llms.ContentPart, 0, groupEnd-i)
		for j := i; j < groupEnd; j++ {
			toolCallParts = append(toolCallParts, llms.ToolCall{
				ID:   steps[j].Action.ToolID,
				Type: "function",
				FunctionCall: &llms.FunctionCall{
					Name:      steps[j].Action.Tool,
					Arguments: encodeToolArguments(steps[j].Action.ToolInput),
				},
			})
		}
		messages = append(messages, llms.MessageContent{
			Role:  llms.ChatMessageTypeAI,
			Parts: toolCallParts,
		})

		for j := i; j < groupEnd; j++ {
			toolContent, followups := observationMessagesForStep(steps[j])
			if steps[j].Action.ToolID != "" {
				messages = append(messages, llms.MessageContent{
					Role: llms.ChatMessageTypeTool,
					Parts: []llms.ContentPart{llms.ToolCallResponse{
						ToolCallID: steps[j].Action.ToolID,
						Content:    toolContent,
					}},
				})
			} else {
				messages = append(messages, llms.MessageContent{
					Role: llms.ChatMessageTypeFunction,
					Parts: []llms.ContentPart{llms.ToolCallResponse{
						Name:    steps[j].Action.Tool,
						Content: toolContent,
					}},
				})
			}
			messages = append(messages, followups...)
		}

		i = groupEnd
	}

	return messages
}

func observationMessagesForStep(step schema.AgentStep) (string, []llms.MessageContent) {
	if step.Action.Tool != "screenshot" {
		return step.Observation, nil
	}

	var result screenshotResult
	if err := json.Unmarshal([]byte(step.Observation), &result); err != nil {
		return step.Observation, nil
	}

	imageBytes, err := base64.StdEncoding.DecodeString(result.Data)
	if err != nil {
		return step.Observation, nil
	}

	mimeType := "image/jpeg"
	toolContent := fmt.Sprintf(
		"screenshot captured successfully: format=%s width=%d height=%d size=%d bytes. The image is attached in the next message.",
		result.Format,
		result.Width,
		result.Height,
		result.Size,
	)
	if result.Format != "" && result.Format != "jpeg" {
		mimeType = "image/" + result.Format
	}

	return toolContent, []llms.MessageContent{{
		Role: llms.ChatMessageTypeHuman,
		Parts: []llms.ContentPart{
			llms.TextPart("This image is the screenshot returned by the screenshot tool. Use it when answering the original request."),
			llms.BinaryPart(mimeType, imageBytes),
		},
	}}
}

func extractToolInput(raw string) string {
	var toolInputMap map[string]any
	if err := json.Unmarshal([]byte(raw), &toolInputMap); err != nil {
		return raw
	}
	if arg1, ok := toolInputMap["__arg1"].(string); ok {
		return arg1
	}
	return raw
}

func encodeToolArguments(input string) string {
	encoded, err := json.Marshal(map[string]string{"__arg1": input})
	if err != nil {
		return input
	}
	return string(encoded)
}

func extractFinalAnswer(content string) string {
	if idx := strings.LastIndex(content, "Final Answer:"); idx >= 0 {
		return strings.TrimSpace(content[idx+len("Final Answer:"):])
	}
	return content
}
