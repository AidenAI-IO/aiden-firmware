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
	InputAttachments []InputAttachment
	OutputKey        string
	CallbacksHandler callbacks.Handler
}

type visualObservationTool interface {
	ReturnsVisualObservation() bool
}

const toolActionLogVersion = 1
const maxToolObservationRunes = 4000
const maxScreenshotContextImages = 3

type toolActionLog struct {
	Version         int    `json:"aiden_action_log_version"`
	Message         string `json:"message"`
	ToolDescription string `json:"tool_description,omitempty"`
}

type toolInvocation struct {
	Input       string
	Description string
}

func NewFunctionAgent(
	llm llms.Model,
	tools []langtools.Tool,
	systemMessage string,
	extraMessages []prompts.MessageFormatter,
	inputAttachments []InputAttachment,
	callbackHandler callbacks.Handler,
) *FunctionAgent {
	messageFormatters := []prompts.MessageFormatter{
		prompts.NewSystemMessagePromptTemplate(systemMessage, nil),
	}
	messageFormatters = append(messageFormatters, extraMessages...)

	return &FunctionAgent{
		LLM:              llm,
		Prompt:           prompts.NewChatPromptTemplate(messageFormatters),
		Tools:            tools,
		InputAttachments: append([]InputAttachment{}, inputAttachments...),
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
	messages = append(messages, llms.MessageContent{
		Role:  llms.ChatMessageTypeHuman,
		Parts: buildUserMessageParts(inputs["input"], a.InputAttachments),
	})
	messages = append(messages, a.constructFunctionScratchPad(intermediateSteps)...)

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
	inputs := append([]string{}, a.Prompt.GetInputVariables()...)
	for _, key := range inputs {
		if key == "input" {
			return inputs
		}
	}
	return append(inputs, "input")
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
			if toolCall.FunctionCall == nil {
				continue
			}
			functionName := toolCall.FunctionCall.Name
			toolInputStr := toolCall.FunctionCall.Arguments
			invocation := extractToolInvocation(toolInputStr)
			invocation.Description = toolDescriptionOrFallback(functionName, invocation.Description)

			contentMsg := "\n"
			if choice.Content != "" {
				contentMsg = fmt.Sprintf("responded: %s\n", choice.Content)
			}

			actions = append(actions, schema.AgentAction{
				Tool:      functionName,
				ToolInput: invocation.Input,
				Log:       formatToolActionLog(functionName, toolInputStr, invocation.Description, contentMsg),
				ToolID:    toolCall.ID,
			})
		}
		return actions, nil, nil
	}

	if choice.FuncCall != nil {
		functionName := choice.FuncCall.Name
		toolInputStr := choice.FuncCall.Arguments
		invocation := extractToolInvocation(toolInputStr)
		invocation.Description = toolDescriptionOrFallback(functionName, invocation.Description)

		contentMsg := "\n"
		if choice.Content != "" {
			contentMsg = fmt.Sprintf("responded: %s\n", choice.Content)
		}

		return []schema.AgentAction{{
			Tool:      functionName,
			ToolInput: invocation.Input,
			Log:       formatToolActionLog(functionName, toolInputStr, invocation.Description, contentMsg),
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
							"title":       "__arg1",
							"type":        "string",
							"description": "The plain string input for the selected tool.",
						},
						"description": map[string]string{
							"title":       "description",
							"type":        "string",
							"description": "A short first-person sentence in the user's language that says what you are about to do with this tool. Voice clients may present it while the tool runs.",
						},
					},
					"required": []string{"__arg1", "description"},
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

func (a *FunctionAgent) constructFunctionScratchPad(steps []schema.AgentStep) []llms.MessageContent {
	if len(steps) == 0 {
		return nil
	}

	messages := make([]llms.MessageContent, 0, len(steps)*3)
	visualObservationCount := a.countVisualObservations(steps)
	visualObservationIndex := 0

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
					Name: steps[j].Action.Tool,
					Arguments: encodeToolArguments(
						steps[j].Action.ToolInput,
						toolDescriptionOrFallback(steps[j].Action.Tool, toolDescriptionFromAction(steps[j].Action)),
					),
				},
			})
		}
		messages = append(messages, llms.MessageContent{
			Role:  llms.ChatMessageTypeAI,
			Parts: toolCallParts,
		})

		for j := i; j < groupEnd; j++ {
			includeVisual := true
			if a.hasVisualObservation(steps[j]) {
				visualObservationIndex++
				includeVisual = visualObservationIndex > visualObservationCount-maxScreenshotContextImages
			}
			toolContent, followups := a.observationMessagesForStep(steps[j], includeVisual)
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

func (a *FunctionAgent) observationMessagesForStep(step schema.AgentStep, includeVisual bool) (string, []llms.MessageContent) {
	if !a.isVisualObservationTool(step.Action.Tool) {
		return compactToolObservation(step.Observation), nil
	}

	var result postActionScreenshotResult
	if err := json.Unmarshal([]byte(step.Observation), &result); err != nil {
		return compactToolObservation(step.Observation), nil
	}
	if result.Data == "" {
		return compactToolObservation(step.Observation), nil
	}

	imageBytes, err := base64.StdEncoding.DecodeString(result.Data)
	if err != nil {
		return compactToolObservation(step.Observation), nil
	}
	if len(imageBytes) == 0 {
		return compactToolObservation(step.Observation), nil
	}

	mimeType := "image/jpeg"
	imageAvailability := "The image is attached in the next message."
	if !includeVisual {
		imageAvailability = fmt.Sprintf("This older screenshot image is omitted from the current context; only the latest %d screenshot observations are attached.", maxScreenshotContextImages)
	}
	toolContent := fmt.Sprintf(
		"%s returned a screenshot observation: format=%s width=%d height=%d size=%d bytes. %s",
		step.Action.Tool,
		result.Format,
		result.Width,
		result.Height,
		result.Size,
		imageAvailability,
	)
	if strings.TrimSpace(result.ActionOutput) != "" {
		actionOutput := compactToolObservation(result.ActionOutput)
		toolContent = fmt.Sprintf(
			"%s completed with output %q, then returned a screenshot observation after the action settled: format=%s width=%d height=%d size=%d bytes. %s",
			step.Action.Tool,
			actionOutput,
			result.Format,
			result.Width,
			result.Height,
			result.Size,
			imageAvailability,
		)
	}
	if result.Format != "" && result.Format != "jpeg" {
		mimeType = "image/" + result.Format
	}
	if !includeVisual {
		return toolContent, nil
	}

	return toolContent, []llms.MessageContent{{
		Role: llms.ChatMessageTypeHuman,
		Parts: []llms.ContentPart{
			llms.TextPart(fmt.Sprintf("This image is the screenshot observation returned by the %s tool. Use it when answering the original request.", step.Action.Tool)),
			buildImagePart(mimeType, imageBytes),
		},
	}}
}

func (a *FunctionAgent) countVisualObservations(steps []schema.AgentStep) int {
	count := 0
	for _, step := range steps {
		if a.hasVisualObservation(step) {
			count++
		}
	}
	return count
}

func (a *FunctionAgent) hasVisualObservation(step schema.AgentStep) bool {
	if !a.isVisualObservationTool(step.Action.Tool) {
		return false
	}

	var result postActionScreenshotResult
	if err := json.Unmarshal([]byte(step.Observation), &result); err != nil {
		return false
	}
	if result.Data == "" {
		return false
	}
	imageBytes, err := base64.StdEncoding.DecodeString(result.Data)
	if err != nil || len(imageBytes) == 0 {
		return false
	}
	return true
}

func compactToolObservation(observation string) string {
	observation = strings.TrimSpace(observation)
	if observation == "" || len([]rune(observation)) <= maxToolObservationRunes {
		return observation
	}
	runes := []rune(observation)
	return string(runes[:maxToolObservationRunes]) + fmt.Sprintf("\n...[truncated %d chars]", len(runes)-maxToolObservationRunes)
}

func (a *FunctionAgent) isVisualObservationTool(name string) bool {
	for _, tool := range a.Tools {
		if tool.Name() != name {
			continue
		}
		visualTool, ok := tool.(visualObservationTool)
		return ok && visualTool.ReturnsVisualObservation()
	}
	return false
}

func buildUserMessageParts(input string, attachments []InputAttachment) []llms.ContentPart {
	text := normalizeRunInput(input, attachments)
	if len(attachments) == 0 {
		return []llms.ContentPart{llms.TextPart(text)}
	}

	descriptions := make([]string, 0, len(attachments))
	parts := []llms.ContentPart{llms.TextPart(attachmentAwarePrompt(text, attachments, descriptions))}
	for _, attachment := range attachments {
		if len(attachment.Data) == 0 {
			continue
		}
		if attachment.Kind == AttachmentKindImage {
			parts = append(parts, buildImagePart(attachment.MIMEType, attachment.Data))
			continue
		}
		parts = append(parts, llms.BinaryPart(attachment.MIMEType, attachment.Data))
	}
	return parts
}

func buildImagePart(mimeType string, data []byte) llms.ContentPart {
	if strings.TrimSpace(mimeType) == "" {
		mimeType = "image/png"
	}
	return llms.ImageURLPart("data:" + mimeType + ";base64," + base64.StdEncoding.EncodeToString(data))
}

func attachmentAwarePrompt(text string, attachments []InputAttachment, descriptions []string) string {
	if descriptions == nil {
		descriptions = make([]string, 0, len(attachments))
	}
	for _, attachment := range attachments {
		label := attachment.Kind
		if attachment.Name != "" {
			label += ": " + attachment.Name
		}
		if attachment.MIMEType != "" {
			label += " (" + attachment.MIMEType + ")"
		}
		descriptions = append(descriptions, label)
	}
	if len(descriptions) == 0 {
		return text
	}
	return text + "\n\nAttached content:\n- " + strings.Join(descriptions, "\n- ")
}

func extractToolInvocation(raw string) toolInvocation {
	var toolInputMap map[string]any
	if err := json.Unmarshal([]byte(raw), &toolInputMap); err != nil {
		return toolInvocation{Input: raw}
	}
	invocation := toolInvocation{Input: raw}
	if arg1, ok := toolInputMap["__arg1"].(string); ok {
		invocation.Input = arg1
	}
	if description, ok := toolInputMap["description"].(string); ok {
		invocation.Description = strings.TrimSpace(description)
	}
	return invocation
}

func extractToolInput(raw string) string {
	return extractToolInvocation(raw).Input
}

func encodeToolArguments(input string, descriptions ...string) string {
	args := map[string]string{"__arg1": input}
	if len(descriptions) > 0 {
		if description := strings.TrimSpace(descriptions[0]); description != "" {
			args["description"] = description
		}
	}
	encoded, err := json.Marshal(args)
	if err != nil {
		return input
	}
	return string(encoded)
}

func formatToolActionLog(name, arguments, description, contentMsg string) string {
	message := fmt.Sprintf("Invoking: %s with %s %s", name, arguments, contentMsg)
	metadata := toolActionLog{
		Version:         toolActionLogVersion,
		Message:         message,
		ToolDescription: toolDescriptionOrFallback(name, description),
	}
	encoded, err := json.Marshal(metadata)
	if err != nil {
		return message
	}
	return string(encoded)
}

func toolDescriptionFromAction(action schema.AgentAction) string {
	var metadata toolActionLog
	if err := json.Unmarshal([]byte(action.Log), &metadata); err != nil {
		return ""
	}
	if metadata.Version != toolActionLogVersion {
		return ""
	}
	return strings.TrimSpace(metadata.ToolDescription)
}

func toolDescriptionOrFallback(toolName, description string) string {
	if description = strings.TrimSpace(description); description != "" {
		return description
	}
	toolName = strings.TrimSpace(toolName)
	if toolName == "" {
		return "I will use a tool."
	}
	return fmt.Sprintf("I will use the %s tool.", toolName)
}

func extractFinalAnswer(content string) string {
	if idx := strings.LastIndex(content, "Final Answer:"); idx >= 0 {
		return strings.TrimSpace(content[idx+len("Final Answer:"):])
	}
	return content
}
