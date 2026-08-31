package compactor

import (
	"aiden-agent/internal/agent/contextmanager"
	"aiden-agent/internal/agent/executor"
	"aiden-agent/internal/agent/messages"
	"aiden-agent/internal/agent/model"
	"aiden-agent/internal/agent/tokencounter"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"slices"
	"strings"
	"time"
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

const historicalToolResultPruneReason = "historical_prune"

type CompactionOptions struct {
	TargetTokens int
	SkipSummary  bool
	ForceSummary bool
}

type CompactionStats struct {
	HistoricalStatesDropped     int
	HistoricalToolResultsPruned int
	TokensBefore                int
	TokensAfter                 int
	ConversationSummaryRequired bool
}

type Compactor struct {
	ProtectRule ProtectRule
	Model       model.Model
	lastStats   CompactionStats
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

func (c *Compactor) LastStats() CompactionStats {
	if c == nil {
		return CompactionStats{}
	}
	return c.lastStats
}

func estimateMessageListTokenUsage(messageList []messages.Message) int {
	return tokencounter.EstimateMessagesTokens(messageList)
}

func (c *Compactor) Compact(ctx context.Context, session *contextmanager.ContextManager) (*contextmanager.ContextManager, bool, error) {
	return c.CompactWithOptions(ctx, session, CompactionOptions{})
}

func (c *Compactor) CompactWithOptions(ctx context.Context, session *contextmanager.ContextManager, options CompactionOptions) (*contextmanager.ContextManager, bool, error) {
	if session == nil {
		return nil, false, nil
	}
	messageList := session.CloneMessageList()
	tokensBefore := estimateMessageListTokenUsage(messageList)
	c.lastStats = CompactionStats{TokensBefore: tokensBefore, TokensAfter: tokensBefore}

	deterministicChanged := false
	if options.TargetTokens > 0 {
		var dropped int
		messageList, dropped = pruneHistoricalStates(messageList)
		c.lastStats.HistoricalStatesDropped = dropped
		deterministicChanged = dropped > 0

		var pruned int
		messageList, pruned = compactHistoricalToolResults(messageList, options.TargetTokens)
		c.lastStats.HistoricalToolResultsPruned = pruned
		deterministicChanged = deterministicChanged || pruned > 0
		c.lastStats.TokensAfter = estimateMessageListTokenUsage(messageList)
		if c.lastStats.TokensAfter <= options.TargetTokens && !options.ForceSummary {
			if deterministicChanged {
				return newContextRevision(session, messageList)
			}
			return nil, false, nil
		}
		if deterministicChanged && options.SkipSummary {
			return newContextRevision(session, messageList)
		}
	}
	if options.SkipSummary {
		return nil, false, nil
	}

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
		if deterministicChanged {
			return newContextRevision(session, messageList)
		}
		return nil, false, nil
	}

	heads := messageList[:HeadN]
	tails := messageList[len(messageList)-TailN:]
	mids := messageList[HeadN : len(messageList)-TailN]
	// compact mids into one single user message
	c.lastStats.ConversationSummaryRequired = true
	summary, err := c.generateSummary(ctx, mids)
	if err != nil {
		if deterministicChanged {
			return newContextRevision(session, messageList)
		}
		return nil, false, err
	}
	if summary == "" {
		if deterministicChanged {
			return newContextRevision(session, messageList)
		}
		return nil, false, fmt.Errorf("failed to generate summary")
	}
	// assemble
	formattedSummary := c.formatSummary(summary)
	newMessageList := append(append([]messages.Message(nil), heads...), messages.Message{
		Role:    messages.MessageRoleUser,
		Content: formattedSummary,
	})
	newMessageList = append(newMessageList, tails...)
	c.lastStats.TokensAfter = estimateMessageListTokenUsage(newMessageList)
	return newContextRevision(session, newMessageList)
}

func newContextRevision(session *contextmanager.ContextManager, messageList []messages.Message) (*contextmanager.ContextManager, bool, error) {
	// A revision starts a fresh provider conversation. Retaining a Responses
	// response ID would chain the rewritten local transcript onto stale provider
	// state. The original session remains on disk for audit and recovery.
	for i := range messageList {
		messageList[i].ResponsesResponseID = ""
	}
	newManager, err := contextmanager.NewContextManagerRevisionFromMessageList(session, messageList)
	if err != nil {
		return nil, false, err
	}
	return newManager, true, nil
}

func pruneHistoricalStates(messageList []messages.Message) ([]messages.Message, int) {
	latestUserIndex := lastMessageIndexWithRole(messageList, messages.MessageRoleUser)
	if latestUserIndex < 0 {
		return messageList, 0
	}

	// State messages injected immediately before the current user input belong
	// to the active turn. State observations after that input do too. Every
	// earlier state is an expired snapshot and must not enter a later summary.
	currentTurnStart := latestUserIndex
	for currentTurnStart > 0 && messageList[currentTurnStart-1].Role == messages.MessageRoleState {
		currentTurnStart--
	}

	dropped := 0
	pruned := make([]messages.Message, 0, len(messageList))
	for i, message := range messageList {
		if i < currentTurnStart && message.Role == messages.MessageRoleState {
			dropped++
			continue
		}
		pruned = append(pruned, message)
	}
	if dropped == 0 {
		return messageList, 0
	}
	return pruned, dropped
}

func compactHistoricalToolResults(messageList []messages.Message, targetTokens int) ([]messages.Message, int) {
	if targetTokens <= 0 || estimateMessageListTokenUsage(messageList) <= targetTokens {
		return messageList, 0
	}
	latestUserIndex := lastMessageIndexWithRole(messageList, messages.MessageRoleUser)
	if latestUserIndex <= 0 {
		return messageList, 0
	}

	currentTokens := estimateMessageListTokenUsage(messageList)
	pruned := 0
	for messageIndex := 0; messageIndex < latestUserIndex; messageIndex++ {
		message := &messageList[messageIndex]
		if message.Role != messages.MessageRoleToolResult {
			continue
		}
		for resultIndex := range message.ToolResults {
			result := &message.ToolResults[resultIndex]
			if result.Meta != nil && result.Meta.Reason == historicalToolResultPruneReason {
				continue
			}
			call, _ := findHistoricalToolCall(messageList, result.ToolCallID, messageIndex)
			content, summary := historicalToolResultPlaceholder(*result, call)
			beforeTokens := tokencounter.EstimateTextTokens(result.Content)
			afterTokens := tokencounter.EstimateTextTokens(content)
			if afterTokens >= beforeTokens {
				continue
			}

			meta := result.Meta
			if meta == nil {
				meta = &messages.ToolResultMeta{Complete: true, ObservationComplete: true}
				result.Meta = meta
			}
			if meta.OriginalBytes == 0 {
				meta.OriginalBytes = int64(len(result.Content))
			}
			if meta.OriginalChars == 0 {
				meta.OriginalChars = utf8.RuneCountInString(result.Content)
			}
			if meta.EstimatedTokens == 0 {
				meta.EstimatedTokens = beforeTokens
			}
			if strings.TrimSpace(meta.Summary) == "" {
				meta.Summary = summary
			}
			meta.Complete = false
			meta.Reason = historicalToolResultPruneReason
			result.Content = content
			currentTokens += afterTokens - beforeTokens
			pruned++
			if currentTokens <= targetTokens {
				return messageList, pruned
			}
		}
	}
	return messageList, pruned
}

func lastMessageIndexWithRole(messageList []messages.Message, role messages.MessageRole) int {
	for i := len(messageList) - 1; i >= 0; i-- {
		if messageList[i].Role == role {
			return i
		}
	}
	return -1
}

func findHistoricalToolCall(messageList []messages.Message, toolCallID string, before int) (messages.ToolCall, bool) {
	toolCallID = strings.TrimSpace(toolCallID)
	if toolCallID == "" {
		return messages.ToolCall{}, false
	}
	for i := before - 1; i >= 0; i-- {
		for _, call := range messageList[i].ToolCalls {
			if strings.TrimSpace(call.ID) == toolCallID {
				return call, true
			}
		}
	}
	return messages.ToolCall{}, false
}

type historicalToolResultReference struct {
	Status           string `json:"status"`
	Tool             string `json:"tool"`
	Summary          string `json:"summary,omitempty"`
	ArtifactPath     string `json:"artifact_path,omitempty"`
	ArtifactComplete bool   `json:"artifact_complete,omitempty"`
}

func historicalToolResultPlaceholder(result messages.ToolResult, call messages.ToolCall) (string, string) {
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
	} else {
		summary = truncateRunes(summary, 512)
	}

	reference := historicalToolResultReference{
		Status:  historicalToolResultPruneReason,
		Tool:    toolName,
		Summary: summary,
	}
	if result.Meta != nil && contextmanager.ArtifactPathRecoverable(result.Meta.ArtifactPath, time.Now()) {
		reference.ArtifactPath = strings.TrimSpace(result.Meta.ArtifactPath)
		reference.ArtifactComplete = result.Meta.ArtifactComplete
	}
	data, _ := json.Marshal(reference)
	return string(data), summary
}

func compactHistoricalSummary(content string) string {
	const maxRunes = 512
	runes := []rune(strings.TrimSpace(content))
	if len(runes) <= maxRunes {
		return string(runes)
	}
	head := maxRunes / 2
	tail := maxRunes - head
	return fmt.Sprintf("%s\n... %d chars omitted ...\n%s", string(runes[:head]), len(runes)-maxRunes, string(runes[len(runes)-tail:]))
}

func truncateRunes(value string, maxRunes int) string {
	runes := []rune(strings.TrimSpace(value))
	if maxRunes <= 0 || len(runes) <= maxRunes {
		return string(runes)
	}
	return string(runes[:maxRunes])
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
