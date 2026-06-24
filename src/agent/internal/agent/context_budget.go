package agent

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/tmc/langchaingo/llms"
)

type contextWindowModel interface {
	contextWindow() int
}

type toolResponseBudgetCandidate struct {
	MessageIndex  int
	PartIndex     int
	ToolCallID    string
	ToolName      string
	Content       string
	ContentTokens int
}

type toolResponseBudgetCandidateKey struct {
	MessageIndex int
	PartIndex    int
}

func (c toolResponseBudgetCandidate) key() toolResponseBudgetCandidateKey {
	return toolResponseBudgetCandidateKey{
		MessageIndex: c.MessageIndex,
		PartIndex:    c.PartIndex,
	}
}

func (e *roleCollaborativeExecutor) guardMessagesWithinContextWindow(messages []llms.MessageContent, options []llms.CallOption) []llms.MessageContent {
	if e == nil || e.Model == nil {
		return messages
	}
	windowProvider, ok := e.Model.(contextWindowModel)
	if !ok {
		return messages
	}
	contextWindow := windowProvider.contextWindow()
	if contextWindow <= 0 {
		return messages
	}

	var callOptions llms.CallOptions
	for _, option := range options {
		option(&callOptions)
	}
	inputBudget := inputContextBudget(contextWindow, callOptions.MaxTokens)
	if inputBudget <= 0 {
		return messages
	}

	toolSchemaTokens := estimateToolSchemaTokens(callOptions)
	if estimateMessagesTokens(messages)+toolSchemaTokens <= inputBudget {
		return messages
	}

	candidates := collectToolResponseBudgetCandidates(messages)
	if len(candidates) == 0 {
		return messages
	}

	sanitized := cloneMessageContents(messages)
	omitted := make(map[toolResponseBudgetCandidateKey]struct{})
	for _, candidate := range candidates {
		singlePromptTokens := estimateSingleToolResponsePromptTokens(messages, candidate) + toolSchemaTokens
		if singlePromptTokens <= inputBudget {
			continue
		}
		replaceToolResponsePart(sanitized, candidate, omittedToolResultMessage(candidate))
		omitted[candidate.key()] = struct{}{}
	}

	if estimateMessagesTokens(sanitized)+toolSchemaTokens <= inputBudget {
		return sanitized
	}

	sort.Slice(candidates, func(i, j int) bool {
		return candidates[i].ContentTokens > candidates[j].ContentTokens
	})
	for _, candidate := range candidates {
		if isToolResponseAlreadyOmitted(omitted, candidate) {
			continue
		}
		replaceToolResponsePart(sanitized, candidate, omittedToolResultMessage(candidate))
		omitted[candidate.key()] = struct{}{}
		if estimateMessagesTokens(sanitized)+toolSchemaTokens <= inputBudget {
			break
		}
	}
	return sanitized
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

func collectToolResponseBudgetCandidates(messages []llms.MessageContent) []toolResponseBudgetCandidate {
	var candidates []toolResponseBudgetCandidate
	for messageIndex, message := range messages {
		for partIndex, part := range message.Parts {
			response, ok := part.(llms.ToolCallResponse)
			if !ok {
				continue
			}
			content := strings.TrimSpace(response.Content)
			candidates = append(candidates, toolResponseBudgetCandidate{
				MessageIndex:  messageIndex,
				PartIndex:     partIndex,
				ToolCallID:    response.ToolCallID,
				ToolName:      response.Name,
				Content:       response.Content,
				ContentTokens: estimateTextTokens(content),
			})
		}
	}
	return candidates
}

func estimateSingleToolResponsePromptTokens(messages []llms.MessageContent, candidate toolResponseBudgetCandidate) int {
	matchingCallIndex := findMatchingToolCallMessageIndex(messages, candidate.ToolCallID, candidate.MessageIndex)
	total := 0
	for i, message := range messages {
		switch {
		case i == candidate.MessageIndex:
			total += estimateMessageTokens(llms.MessageContent{
				Role:  message.Role,
				Parts: []llms.ContentPart{message.Parts[candidate.PartIndex]},
			})
		case i == matchingCallIndex:
			total += estimateMessageTokens(message)
		case message.Role == llms.ChatMessageTypeSystem || message.Role == llms.ChatMessageTypeHuman:
			total += estimateMessageTokens(message)
		}
	}
	return total
}

func findMatchingToolCallMessageIndex(messages []llms.MessageContent, toolCallID string, before int) int {
	if strings.TrimSpace(toolCallID) == "" {
		return -1
	}
	for i := before - 1; i >= 0; i-- {
		for _, part := range messages[i].Parts {
			call, ok := part.(llms.ToolCall)
			if ok && call.ID == toolCallID {
				return i
			}
		}
	}
	return -1
}

func cloneMessageContents(messages []llms.MessageContent) []llms.MessageContent {
	cloned := append([]llms.MessageContent(nil), messages...)
	for i := range cloned {
		cloned[i].Parts = append([]llms.ContentPart(nil), messages[i].Parts...)
	}
	return cloned
}

func replaceToolResponsePart(messages []llms.MessageContent, candidate toolResponseBudgetCandidate, content string) {
	if candidate.MessageIndex < 0 || candidate.MessageIndex >= len(messages) {
		return
	}
	parts := messages[candidate.MessageIndex].Parts
	if candidate.PartIndex < 0 || candidate.PartIndex >= len(parts) {
		return
	}
	response, ok := parts[candidate.PartIndex].(llms.ToolCallResponse)
	if !ok {
		return
	}
	response.Content = content
	parts[candidate.PartIndex] = response
}

func isToolResponseAlreadyOmitted(omitted map[toolResponseBudgetCandidateKey]struct{}, candidate toolResponseBudgetCandidate) bool {
	_, ok := omitted[candidate.key()]
	return ok
}

func omittedToolResultMessage(candidate toolResponseBudgetCandidate) string {
	toolName := strings.TrimSpace(candidate.ToolName)
	if toolName == "" {
		toolName = "tool"
	}
	return fmt.Sprintf(
		"note: %s tool result omitted because including it would exceed the model context window. The tool call already completed, but its raw output was not inserted into the next model call.",
		toolName,
	)
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
