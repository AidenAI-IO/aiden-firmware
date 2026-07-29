package compactor

import (
	"aiden-agent/internal/agent/contextmanager"
	"aiden-agent/internal/agent/executor"
	"aiden-agent/internal/agent/model"
	"aiden-agent/internal/agent/tokencounter"
	"context"
	"fmt"
	"log"
	"strings"
	"unicode/utf8"

	"github.com/tmc/langchaingo/llms"
)

type ProtectRule struct {
	HeadN int
	TailN int
}

var DefaultProtectRule = ProtectRule{
	HeadN: 2,
	TailN: 3,
}

type Compactor struct {
	ProtectRule                ProtectRule
	Model                      model.Model
	HistoricalToolResultTarget int
}

func (c *Compactor) SetHistoricalToolResultTarget(target int) {
	if c == nil {
		return
	}
	c.HistoricalToolResultTarget = max(0, target)
}

func NewCompactor(protectRule ProtectRule, model model.Model) *Compactor {
	validateProtectRule(protectRule)
	return &Compactor{
		ProtectRule: protectRule,
		Model:       model,
	}
}

func validateProtectRule(protectRule ProtectRule) {
	if protectRule.HeadN <= 0 || protectRule.TailN <= 0 {
		log.Fatalf("headN and tailN must be greater than 0")
	}
}

func (c *Compactor) EstimateTokenUsage(session *contextmanager.ContextManager) int {
	if session == nil {
		return 0
	}
	return estimateMessageListTokenUsage(session.CloneMessageList())
}

func estimateMessageListTokenUsage(messageList []contextmanager.Message) int {
	tokenUsage := 0
	for _, msg := range messageList {
		switch msg.Role {
		case contextmanager.MessageRoleToolCall:
			for _, call := range msg.ToolCalls {
				tokenUsage += tokencounter.EstimateTextTokens(call.Arguments)
			}
		case contextmanager.MessageRoleToolResult:
			for _, result := range msg.ToolResults {
				tokenUsage += tokencounter.EstimateTextTokens(result.Content)
			}
		default:
			tokenUsage += tokencounter.EstimateTextTokens(msg.Content)
		}
		if len(msg.Attachments) > 0 {
			tokenUsage += len(msg.Attachments) * tokencounter.EstimateImageTokens
		}
	}
	return tokenUsage
}

func (c *Compactor) Compact(ctx context.Context, session *contextmanager.ContextManager) (*contextmanager.ContextManager, bool, error) {
	if session == nil {
		return nil, false, nil
	}
	messageList := session.CloneMessageList()
	if c.HistoricalToolResultTarget > 0 {
		var replaced int
		messageList, replaced = compactHistoricalToolResults(messageList, c.HistoricalToolResultTarget)
		if replaced > 0 && estimateMessageListTokenUsage(messageList) <= c.HistoricalToolResultTarget {
			newManager, err := contextmanager.NewContextManagerRevisionFromMessageList(session, messageList)
			if err != nil {
				return nil, false, err
			}
			return newManager, true, nil
		}
	}

	HeadN := c.ProtectRule.HeadN
	TailN := c.ProtectRule.TailN

	// adjust headN so that headN is assitant, tool_result or user_message, make sure headN not breaking tool_call pairs
	for i := HeadN - 1; i < len(messageList); i++ {
		if messageList[i].Role == contextmanager.MessageRoleAssistant || messageList[i].Role == contextmanager.MessageRoleToolResult || messageList[i].Role == contextmanager.MessageRoleUser {
			break
		}
		HeadN++
	}

	// adjust tailN so that N - tailN - 1 is tool_result or user_message
	for i := len(messageList) - TailN - 1; i >= 0; i-- {
		if messageList[i].Role == contextmanager.MessageRoleAssistant || messageList[i].Role == contextmanager.MessageRoleToolResult || messageList[i].Role == contextmanager.MessageRoleUser {
			break
		}
		TailN++
	}

	if HeadN+TailN >= len(messageList) {
		return nil, false, nil
	}

	heads := messageList[:HeadN]
	tails := messageList[len(messageList)-TailN:]
	mids := messageList[HeadN : len(messageList)-TailN]
	// compact mids into one single user message
	summary, err := c.generateSummary(ctx, mids)
	if err != nil {
		return nil, false, err
	}
	if summary == "" {
		return nil, false, fmt.Errorf("failed to generate summary")
	}
	// assemble
	newMessageList := append(append([]contextmanager.Message(nil), heads...), contextmanager.Message{
		Role:    contextmanager.MessageRoleUser,
		Content: c.formatSummary(summary),
	})
	newMessageList = append(newMessageList, tails...)
	// create new context manager
	newManager, err := contextmanager.NewContextManagerRevisionFromMessageList(session, newMessageList)
	if err != nil {
		return nil, false, err
	}
	return newManager, true, nil
}

func compactHistoricalToolResults(messageList []contextmanager.Message, targetTokens int) ([]contextmanager.Message, int) {
	if targetTokens <= 0 || len(messageList) == 0 {
		return messageList, 0
	}
	latestUserIndex := -1
	for i := len(messageList) - 1; i >= 0; i-- {
		if messageList[i].Role == contextmanager.MessageRoleUser {
			latestUserIndex = i
			break
		}
	}
	if latestUserIndex <= 0 {
		return messageList, 0
	}

	replaced := 0
	for messageIndex := 0; messageIndex < latestUserIndex; messageIndex++ {
		message := &messageList[messageIndex]
		if message.Role != contextmanager.MessageRoleToolResult {
			continue
		}
		for resultIndex := range message.ToolResults {
			result := &message.ToolResults[resultIndex]
			if result.Meta != nil && result.Meta.Reason == "historical_compaction" {
				continue
			}
			call, ok := findHistoricalToolCall(messageList, result.ToolCallID, messageIndex)
			if !ok {
				continue
			}
			meta := result.Meta
			if meta == nil {
				meta = &contextmanager.ToolResultMeta{
					OriginalBytes:   int64(len(result.Content)),
					OriginalChars:   utf8.RuneCountInString(result.Content),
					EstimatedTokens: tokencounter.EstimateTextTokens(result.Content),
					Complete:        true,
					Summary:         compactHistoricalSummary(result.Content),
				}
				result.Meta = meta
			}
			result.Content = historicalToolResultPlaceholder(*result, call)
			meta.Complete = false
			meta.Reason = "historical_compaction"
			replaced++
			if estimateMessageListTokenUsage(messageList) <= targetTokens {
				return messageList, replaced
			}
		}
	}
	return messageList, replaced
}

func findHistoricalToolCall(messageList []contextmanager.Message, toolCallID string, before int) (contextmanager.ToolCall, bool) {
	toolCallID = strings.TrimSpace(toolCallID)
	if toolCallID == "" {
		return contextmanager.ToolCall{}, false
	}
	for i := before - 1; i >= 0; i-- {
		for _, call := range messageList[i].ToolCalls {
			if strings.TrimSpace(call.ID) == toolCallID {
				return call, true
			}
		}
	}
	return contextmanager.ToolCall{}, false
}

func historicalToolResultPlaceholder(result contextmanager.ToolResult, call contextmanager.ToolCall) string {
	toolName := strings.TrimSpace(result.Name)
	if toolName == "" {
		toolName = strings.TrimSpace(call.Name)
	}
	if toolName == "" {
		toolName = "tool"
	}
	summary := ""
	if result.Meta != nil {
		summary = strings.TrimSpace(result.Meta.Summary)
	}
	if summary == "" {
		summary = compactHistoricalSummary(result.Content)
	}
	var builder strings.Builder
	fmt.Fprintf(&builder, "[%s] historical result compressed.\n", toolName)
	if summary != "" {
		builder.WriteString(summary)
		if !strings.HasSuffix(summary, "\n") {
			builder.WriteByte('\n')
		}
	}
	if result.Meta != nil && strings.TrimSpace(result.Meta.ArtifactRef) != "" {
		if result.Meta.ArtifactComplete {
			fmt.Fprintf(&builder, "Full result: %s", result.Meta.ArtifactRef)
		} else {
			fmt.Fprintf(&builder, "Saved partial result: %s", result.Meta.ArtifactRef)
		}
	}
	return strings.TrimSpace(builder.String())
}

func compactHistoricalSummary(content string) string {
	runes := []rune(strings.TrimSpace(content))
	const maxRunes = 512
	if len(runes) <= maxRunes {
		return string(runes)
	}
	head := maxRunes / 2
	tail := maxRunes - head
	return fmt.Sprintf("%s\n... %d chars omitted ...\n%s", string(runes[:head]), len(runes)-maxRunes, string(runes[len(runes)-tail:]))
}

func (c *Compactor) generateSummary(ctx context.Context, messageList []contextmanager.Message) (string, error) {
	var transcript strings.Builder
	for _, msg := range messageList {
		switch msg.Role {
		case contextmanager.MessageRoleToolCall:
			fmt.Fprintf(&transcript, "[%s] tool_call_id: %s\n tool_call_name: %s\n tool_call_arguments: %s\n", msg.Role, msg.ToolCalls[0].ID, msg.ToolCalls[0].Name, msg.ToolCalls[0].Arguments)
		case contextmanager.MessageRoleToolResult:
			fmt.Fprintf(&transcript, "[%s] tool_call_id: %s\n tool_call_name: %s\n tool_call_result: %s\n", msg.Role, msg.ToolResults[0].ToolCallID, msg.ToolResults[0].Name, msg.ToolResults[0].Content)
		default:
			fmt.Fprintf(&transcript, "[%s] %s\n", msg.Role, msg.Content)
		}
	}
	if transcript.Len() == 0 {
		return "", nil
	}

	prompt := `
Please summarize the conversation in a concised summary. Template:
## Goal
[What the user is trying to accomplish]
 
## Progress
### Done
[Completed work — specific file paths, commands run, results]
### In Progress
[Work currently underway]
### Blocked
[Any blockers or issues encountered]
 
## Key Decisions
[Important technical decisions and why]
 
## Next Steps
[What needs to happen next]
 
## Critical Context
[Specific values, error messages, configuration details]

And here are the conversation details:

` + transcript.String()
	// TODO: dynamic token budget for summary
	result, err := llms.GenerateFromSinglePrompt(ctx, c.Model, prompt, llms.WithMaxTokens(800))
	if err != nil {
		return "", executor.MarkLLMCallError(err)
	}
	result = strings.TrimSpace(result)
	if result == "" {
		return "", executor.MarkLLMCallError(fmt.Errorf("LLM returned an empty summary"))
	}
	return result, nil
}

func (c *Compactor) formatSummary(summary string) string {
	// wrap summary into <summary>...</summary>, make sure llm understand the summary is a summary of the conversation
	return fmt.Sprintf("<summary>\n%s\n</summary>\n", summary)
}
