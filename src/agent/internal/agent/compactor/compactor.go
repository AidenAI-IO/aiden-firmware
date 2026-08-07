package compactor

import (
	"aiden-agent/internal/agent/contextmanager"
	"aiden-agent/internal/agent/executor"
	"aiden-agent/internal/agent/messages"
	"aiden-agent/internal/agent/model"
	"aiden-agent/internal/agent/tokencounter"
	"context"
	"fmt"
	"log"
	"slices"
	"strings"

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
	ProtectRule ProtectRule
	Model       model.Model
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

func estimateMessageListTokenUsage(messageList []messages.Message) int {
	tokenUsage := 0
	for _, msg := range messageList {
		switch msg.Role {
		case messages.MessageRoleToolCall:
			for _, call := range msg.ToolCalls {
				tokenUsage += tokencounter.EstimateTextTokens(call.Arguments)
			}
		case messages.MessageRoleToolResult:
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

	HeadN := c.ProtectRule.HeadN
	TailN := c.ProtectRule.TailN
	for i, m := range slices.Backward(messageList) {
		if m.Role == messages.MessageRoleUser {
			currentTurnLength := len(messageList) - i
			if currentTurnLength > TailN {
				TailN = currentTurnLength
			}
			break
		}
	}

	// adjust headN so that headN is assitant, tool_result or user_message, make sure headN not breaking tool_call pairs
	for i := HeadN - 1; i < len(messageList); i++ {
		if messageList[i].Role == messages.MessageRoleAssistant || messageList[i].Role == messages.MessageRoleToolResult || messageList[i].Role == messages.MessageRoleUser {
			break
		}
		HeadN++
	}

	// adjust tailN so that N - tailN - 1 is tool_result or user_message
	for i := len(messageList) - TailN - 1; i >= 0; i-- {
		if messageList[i].Role == messages.MessageRoleAssistant || messageList[i].Role == messages.MessageRoleToolResult || messageList[i].Role == messages.MessageRoleUser {
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
	formattedSummary := c.formatSummary(summary)
	newMessageList := append(append([]messages.Message(nil), heads...), messages.Message{
		Role:    messages.MessageRoleUser,
		Content: formattedSummary,
	})
	newMessageList = append(newMessageList, tails...)
	// create new context manager
	newManager, err := contextmanager.NewContextManagerRevisionFromMessageList(session, newMessageList)
	if err != nil {
		return nil, false, err
	}
	return newManager, true, nil
}

func (c *Compactor) generateSummary(ctx context.Context, messageList []messages.Message) (string, error) {
	var transcript strings.Builder
	for _, msg := range messageList {
		switch msg.Role {
		case messages.MessageRoleToolCall:
			for _, call := range msg.ToolCalls {
				fmt.Fprintf(&transcript, "[%s] tool_call_id: %s\n tool_call_name: %s\n tool_call_arguments: %s\n", msg.Role, call.ID, call.Name, call.Arguments)
			}
		case messages.MessageRoleToolResult:
			for _, result := range msg.ToolResults {
				fmt.Fprintf(&transcript, "[%s] tool_call_id: %s\n tool_call_name: %s\n tool_call_result: %s\n", msg.Role, result.ToolCallID, result.Name, result.Content)
			}
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
	formatted := fmt.Sprintf("<summary>\n%s\n</summary>\n", summary)
	return formatted
}
