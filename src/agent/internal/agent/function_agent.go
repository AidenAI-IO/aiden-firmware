package agent

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/tmc/langchaingo/llms"
	"github.com/tmc/langchaingo/schema"
	langtools "github.com/tmc/langchaingo/tools"
)

type FunctionAgent struct {
	Tools             []langtools.Tool
	OutputKey         string
	ScreenshotPruning ScreenshotPruningConfig
	ToolCallSpeech    bool
}

type visualObservationTool interface {
	ReturnsVisualObservation() bool
}

type structuredInputTool interface {
	ArgsSchema() map[string]any
}

const toolActionLogVersion = 1
const maxToolObservationRunes = 4000
const toolCallSpeechField = "speech"
const toolCallDescriptionField = "description"

type ScreenshotPruningConfig struct {
	KeepN    int
	Interval int
}

func (c ScreenshotPruningConfig) WithDefaults() ScreenshotPruningConfig {
	if c.KeepN <= 0 {
		c.KeepN = defaultScreenshotKeepN
	}
	if c.Interval <= 0 {
		c.Interval = defaultScreenshotPruneInterval
	}
	return c
}

func (c ScreenshotPruningConfig) PrunedCount(total int) int {
	c = c.WithDefaults()
	if total <= c.KeepN+c.Interval {
		return 0
	}
	pruned := ((total - c.KeepN - 1) / c.Interval) * c.Interval
	if maxPruned := total - c.KeepN; pruned > maxPruned {
		return maxPruned
	}
	return pruned
}

type toolActionLog struct {
	Version         int    `json:"aiden_action_log_version"`
	Message         string `json:"message"`
	ToolDescription string `json:"tool_description,omitempty"`
	ToolSpeech      string `json:"tool_speech,omitempty"`
}

type toolInvocation struct {
	Input       string
	Description string
	Speech      string
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
			invocation.Description = toolCallDescriptionText(choice.Content, invocation.Description)

			contentMsg := "\n"
			if choice.Content != "" {
				contentMsg = fmt.Sprintf("responded: %s\n", choice.Content)
			}

			actions = append(actions, schema.AgentAction{
				Tool:      functionName,
				ToolInput: invocation.Input,
				Log:       formatToolActionLog(functionName, toolInputStr, invocation.Description, invocation.Speech, contentMsg),
				ToolID:    ensureToolCallID(toolCall.ID, len(actions)),
			})
		}
		return actions, nil, nil
	}

	if choice.FuncCall != nil {
		functionName := choice.FuncCall.Name
		toolInputStr := choice.FuncCall.Arguments
		invocation := extractToolInvocation(toolInputStr)
		invocation.Description = toolCallDescriptionText(choice.Content, invocation.Description)

		contentMsg := "\n"
		if choice.Content != "" {
			contentMsg = fmt.Sprintf("responded: %s\n", choice.Content)
		}

		return []schema.AgentAction{{
			Tool:      functionName,
			ToolInput: invocation.Input,
			Log:       formatToolActionLog(functionName, toolInputStr, invocation.Description, invocation.Speech, contentMsg),
			ToolID:    ensureToolCallID("", 0),
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
		if tool == nil {
			continue
		}
		result = append(result, NewToolSpec(tool).LLMToolWithSpeech(a.ToolCallSpeech))
	}
	return result
}

func genericToolParameters(includeSpeech bool) map[string]any {
	return addToolSpeechParameter(map[string]any{
		"type": "object",
		"properties": map[string]any{
			"input": map[string]string{
				"title":       "input",
				"type":        "string",
				"description": `The plain string input for the selected tool. Prefer tools with typed parameters when available.`,
			},
		},
		"required": []string{"input"},
	}, includeSpeech)
}

func toolParametersSchema(schema map[string]any, includeSpeech bool) map[string]any {
	parameters := make(map[string]any, len(schema))
	for key, value := range schema {
		parameters[key] = value
	}
	parameters["type"] = "object"
	return addToolSpeechParameter(parameters, includeSpeech)
}

func addToolSpeechParameter(parameters map[string]any, includeSpeech bool) map[string]any {
	if parameters == nil {
		parameters = map[string]any{}
	}
	if !includeSpeech {
		return parameters
	}
	properties, _ := parameters["properties"].(map[string]any)
	copiedProperties := make(map[string]any, len(properties)+1)
	for key, value := range properties {
		copiedProperties[key] = value
	}
	if _, exists := copiedProperties[toolCallSpeechField]; !exists {
		copiedProperties[toolCallSpeechField] = map[string]string{
			"title":       "speech",
			"type":        "string",
			"description": "Optional short spoken status before this tool call. Write natural TTS text directly; use the user's language, avoid implementation details, and keep it under 20 Chinese characters or 8 English words.",
		}
	}
	parameters["properties"] = copiedProperties
	return parameters
}

func (a *FunctionAgent) constructFunctionScratchPad(steps []schema.AgentStep) []llms.MessageContent {
	if len(steps) == 0 {
		return nil
	}

	messages := make([]llms.MessageContent, 0, len(steps)*3)
	visualObservationCount := a.countVisualObservations(steps)
	prunedVisualObservationCount := a.ScreenshotPruning.PrunedCount(visualObservationCount)
	visualObservationIndex := 0

	for i := 0; i < len(steps); {
		if strings.TrimSpace(steps[i].Action.Tool) == "" {
			if observation := strings.TrimSpace(steps[i].Observation); observation != "" {
				messages = append(messages, llms.MessageContent{
					Role:  llms.ChatMessageTypeAI,
					Parts: []llms.ContentPart{llms.TextPart(observation)},
				})
			}
			i++
			continue
		}

		groupEnd := i + 1
		for groupEnd < len(steps) &&
			strings.TrimSpace(steps[groupEnd].Action.Tool) != "" &&
			steps[groupEnd].Action.Log == steps[i].Action.Log {
			groupEnd++
		}

		toolCallParts := make([]llms.ContentPart, 0, groupEnd-i+1)
		if description := toolDescriptionFromAction(steps[i].Action); description != "" {
			toolCallParts = append(toolCallParts, llms.TextPart(description))
		}
		for j := i; j < groupEnd; j++ {
			toolCallParts = append(toolCallParts, llms.ToolCall{
				ID:   scratchpadToolCallID(steps[j].Action, i, j),
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
			includeVisual := true
			if a.hasVisualObservation(steps[j]) {
				visualObservationIndex++
				includeVisual = visualObservationIndex > prunedVisualObservationCount
			}
			toolContent, followups := a.observationMessagesForStep(steps[j], includeVisual)
			toolCallID := scratchpadToolCallID(steps[j].Action, i, j)
			messages = append(messages, llms.MessageContent{
				Role: llms.ChatMessageTypeTool,
				Parts: []llms.ContentPart{llms.ToolCallResponse{
					ToolCallID: toolCallID,
					Content:    toolContent,
				}},
			})
			messages = append(messages, followups...)
		}

		i = groupEnd
	}

	return messages
}

func ensureToolCallID(id string, index int) string {
	if id = strings.TrimSpace(id); id != "" {
		return id
	}
	return fmt.Sprintf("call_%d", index+1)
}

func scratchpadToolCallID(action schema.AgentAction, groupStart, index int) string {
	if id := strings.TrimSpace(action.ToolID); id != "" {
		return id
	}
	return fmt.Sprintf("scratchpad_%d_%d", groupStart, index)
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
		imageAvailability = "The image is replaced with a placeholder in the next message."
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
	caption := fmt.Sprintf("This image is the screenshot observation returned by the %s tool. Use it when answering the original request.", step.Action.Tool)
	if !includeVisual {
		return toolContent, []llms.MessageContent{{
			Role: llms.ChatMessageTypeHuman,
			Parts: []llms.ContentPart{
				llms.TextPart(caption),
				llms.TextPart("[Image omitted]"),
			},
		}}
	}

	return toolContent, []llms.MessageContent{{
		Role: llms.ChatMessageTypeHuman,
		Parts: []llms.ContentPart{
			llms.TextPart(caption),
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
	_, hasLegacyArg := toolInputMap["__arg1"]
	hasGenericInput := false
	if speech, ok := toolInputMap[toolCallSpeechField].(string); ok {
		invocation.Speech = strings.TrimSpace(speech)
	}
	if arg1, ok := toolInputMap["__arg1"].(string); ok {
		invocation.Input = arg1
	} else if input, ok := toolInputMap["input"].(string); ok && isGenericStringInputWrapper(toolInputMap) {
		invocation.Input = input
		hasGenericInput = true
	}
	if description, ok := toolInputMap[toolCallDescriptionField].(string); ok {
		invocation.Description = strings.TrimSpace(description)
	}
	if !hasLegacyArg && !hasGenericInput {
		delete(toolInputMap, toolCallDescriptionField)
		delete(toolInputMap, toolCallSpeechField)
		if encoded, err := json.Marshal(toolInputMap); err == nil {
			invocation.Input = string(encoded)
		}
	}
	return invocation
}

func isGenericStringInputWrapper(fields map[string]any) bool {
	if _, ok := fields["input"]; !ok {
		return false
	}
	for key := range fields {
		switch key {
		case "input", toolCallDescriptionField, toolCallSpeechField:
		default:
			return false
		}
	}
	return true
}

func extractToolInput(raw string) string {
	return extractToolInvocation(raw).Input
}

func encodeToolArguments(input string) string {
	args := map[string]any{}
	trimmed := strings.TrimSpace(input)
	if strings.HasPrefix(trimmed, "{") {
		var fields map[string]any
		if err := json.Unmarshal([]byte(trimmed), &fields); err == nil {
			for key, value := range fields {
				args[key] = value
			}
		}
	}
	if len(args) == 0 && trimmed != "" {
		args["input"] = input
	}
	encoded, err := json.Marshal(args)
	if err != nil {
		return input
	}
	return string(encoded)
}

func formatToolActionLog(name, arguments, description, speech, contentMsg string) string {
	message := fmt.Sprintf("Invoking: %s with %s %s", name, arguments, contentMsg)
	metadata := toolActionLog{
		Version:         toolActionLogVersion,
		Message:         message,
		ToolDescription: strings.TrimSpace(description),
		ToolSpeech:      strings.TrimSpace(speech),
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

func toolSpeechFromAction(action schema.AgentAction) string {
	var metadata toolActionLog
	if err := json.Unmarshal([]byte(action.Log), &metadata); err != nil {
		return ""
	}
	if metadata.Version != toolActionLogVersion {
		return ""
	}
	return strings.TrimSpace(metadata.ToolSpeech)
}

func toolCallDescriptionText(content, legacyDescription string) string {
	if content = strings.TrimSpace(content); content != "" {
		return content
	}
	return strings.TrimSpace(legacyDescription)
}

func extractFinalAnswer(content string) string {
	if idx := strings.LastIndex(content, "Final Answer:"); idx >= 0 {
		return strings.TrimSpace(content[idx+len("Final Answer:"):])
	}
	return content
}
