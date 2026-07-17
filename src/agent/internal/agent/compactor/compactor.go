package compactor

import (
	"aiden-agent/internal/agent/contextmanager"
	"aiden-agent/internal/agent/model"
	"aiden-agent/internal/agent/tokencounter"
	"context"
	"fmt"
	"log"
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
	Models      model.ModelResolver
}

func NewCompactor(protectRule ProtectRule, models model.ModelResolver) *Compactor {
	validateProtectRule(protectRule)
	return &Compactor{
		ProtectRule: protectRule,
		Models:      models,
	}
}

func validateProtectRule(protectRule ProtectRule) {
	if protectRule.HeadN <= 0 || protectRule.TailN <= 0 {
		log.Fatalf("headN and tailN must be greater than 0")
	}
}

func (c *Compactor) EstimateTokenUsage(session *contextmanager.ContextManager) int {
	messageList := session.CloneMessageList()
	tokenUsage := 0
	for _, msg := range messageList {
		switch msg.Role {
		case contextmanager.MessageRoleToolCall:
			tokenUsage += tokencounter.EstimateTextTokens(msg.ToolCalls[0].Arguments)
		case contextmanager.MessageRoleToolResult:
			tokenUsage += tokencounter.EstimateTextTokens(msg.ToolResults[0].Content)
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
	HeadN := c.ProtectRule.HeadN
	TailN := c.ProtectRule.TailN
	messageList := session.CloneMessageList()

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
	summary := c.generateSummary(ctx, mids)
	if summary == "" {
		return nil, false, fmt.Errorf("failed to generate summary")
	}
	// assemble
	newMessageList := append(heads, contextmanager.Message{
		Role:    contextmanager.MessageRoleUser,
		Content: c.formatSummary(summary),
	})
	newMessageList = append(newMessageList, tails...)
	// create new context manager
	newManager, err := contextmanager.NewContextManagerFromMessageList(session.GetSessionFolder(), newMessageList)
	if err != nil {
		return nil, false, err
	}
	return newManager, true, nil
}

func (c *Compactor) generateSummary(ctx context.Context, messageList []contextmanager.Message) string {
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
		return ""
	}

	model, err := c.Models.Get()
	if err != nil {
		return ""
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
	result, err := llms.GenerateFromSinglePrompt(ctx, model, prompt, llms.WithMaxTokens(800))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(result)
}

func (c *Compactor) formatSummary(summary string) string {
	// wrap summary into <summary>...</summary>, make sure llm understand the summary is a summary of the conversation
	return fmt.Sprintf("<summary>\n%s\n</summary>\n", summary)
}
