package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"unicode/utf8"

	"aiden-agent/internal/agent/contextmanager"
	"aiden-agent/internal/agent/model"

	"github.com/tmc/langchaingo/llms"
)

const (
	ToolResultReasonInline         = "inline"
	ToolResultReasonIntrinsicLarge = "intrinsic_large"
	ToolResultReasonContextLarge   = "context_large"

	toolResultInlineMaxBytes     = 8 * 1024
	toolResultInlineMaxTokens    = 2_000
	toolResultPreviewTargetToken = 1_200
	toolResultMinimumObservation = 96
	toolResultSoftLimitPercent   = 80
	toolResultProjectionTopK     = 3
	toolResultProjectionFields   = 24
	toolResultProjectionDepth    = 5
	toolResultProjectionRunes    = 256
)

type ToolResultPrepareInput struct {
	Call           ToolCall
	Result         ToolResult
	ContextManager *contextmanager.ContextManager
	ModelSpec      model.ModelSpec
	CallOptions    []llms.CallOption
}

type PreparedToolResult struct {
	Content          string
	ArtifactRef      string
	OriginalBytes    int64
	OriginalChars    int
	EstimatedTokens  int
	Complete         bool
	ArtifactComplete bool
	Reason           string
	Summary          string
}

type ToolResultPolicy interface {
	Prepare(context.Context, ToolResultPrepareInput) (PreparedToolResult, error)
}

type defaultToolResultPolicy struct{}

func NewToolResultPolicy() ToolResultPolicy {
	return defaultToolResultPolicy{}
}

func (defaultToolResultPolicy) Prepare(_ context.Context, input ToolResultPrepareInput) (PreparedToolResult, error) {
	output := input.Result.Output
	prepared := PreparedToolResult{
		Content:         output,
		OriginalBytes:   int64(len(output)),
		OriginalChars:   utf8.RuneCountInString(output),
		EstimatedTokens: estimateTextTokens(output),
		Complete:        true,
		Reason:          ToolResultReasonInline,
	}
	recoveryRead := toolResultIsArtifactRead(input.Call)
	intrinsicLarge := !recoveryRead && (len(output) > toolResultInlineMaxBytes || prepared.EstimatedTokens > toolResultInlineMaxTokens)
	availableTokens := availableToolResultTokens(input)
	contextLarge := availableTokens >= 0 && prepared.EstimatedTokens > availableTokens
	if !intrinsicLarge && !contextLarge {
		prepared.Summary = projectToolResult(input.Call, output, 256)
		return prepared, nil
	}

	prepared.Complete = false
	prepared.Reason = ToolResultReasonContextLarge
	contentBudget := toolResultInlineMaxTokens
	if availableTokens >= 0 {
		contentBudget = min(toolResultInlineMaxTokens, max(toolResultMinimumObservation, availableTokens))
	}
	if intrinsicLarge {
		prepared.Reason = ToolResultReasonIntrinsicLarge
	}
	previewBudget := min(toolResultPreviewTargetToken, contentBudget)
	preview, structured := projectToolResultWithKind(input.Call, output, previewBudget)
	prepared.Summary = boundTextToTokens(preview, 256)
	if input.ContextManager != nil && !recoveryRead {
		mimeType := "text/plain"
		if structured {
			mimeType = "application/json"
		}
		stored, err := input.ContextManager.StoreArtifact(mimeType, []byte(output), contextmanager.ArtifactMetadata{
			ToolName:   input.Call.Spec.Name,
			ToolCallID: input.Call.Action.ToolID,
			Sensitive:  toolResultIsSensitive(input.Call.Spec.Name),
		})
		if err == nil {
			prepared.ArtifactRef = stored.Ref
			prepared.ArtifactComplete = stored.Complete
		}
	}
	prepared.Content = boundedToolResultObservation(input.Call, prepared, preview, contentBudget)
	return prepared, nil
}

func toolResultIsArtifactRead(call ToolCall) bool {
	toolName := strings.TrimSpace(call.Spec.Name)
	if toolName == "" {
		toolName = strings.TrimSpace(call.Action.Tool)
	}
	return toolName == "artifact_read"
}

func toolResultIsSensitive(toolName string) bool {
	return strings.TrimSpace(toolName) == toolBridgeClipboard
}

func availableToolResultTokens(input ToolResultPrepareInput) int {
	contextWindow := input.ModelSpec.ContextWindow
	if contextWindow <= 0 {
		return -1
	}
	maxResponseTokens := input.ModelSpec.MaxOutput
	var options llms.CallOptions
	for _, option := range input.CallOptions {
		if option != nil {
			option(&options)
		}
	}
	if options.MaxTokens > 0 {
		maxResponseTokens = options.MaxTokens
	}
	softLimit := toolResultSoftInputLimit(contextWindow, maxResponseTokens)
	if softLimit <= 0 {
		return 0
	}
	currentTokens := estimateToolSchemaTokens(options)
	if input.ContextManager != nil {
		currentTokens += estimateMessagesTokens(contextmanager.ConvertMessageList(input.ContextManager.CloneMessageList()))
	}
	return max(0, softLimit-currentTokens)
}

func toolResultUsableInputBudget(contextWindow, maxResponseTokens int) int {
	inputBudget := inputContextBudget(contextWindow, maxResponseTokens)
	if inputBudget <= 0 {
		return 0
	}
	safetyMargin := max(1_024, contextWindow*2/100)
	return max(0, inputBudget-safetyMargin)
}

func toolResultSoftInputLimit(contextWindow, maxResponseTokens int) int {
	return toolResultUsableInputBudget(contextWindow, maxResponseTokens) * toolResultSoftLimitPercent / 100
}

func boundedToolResultObservation(call ToolCall, prepared PreparedToolResult, preview string, contentTokens int) string {
	if contentTokens <= 0 {
		contentTokens = toolResultMinimumObservation
	}
	toolName := strings.TrimSpace(call.Spec.Name)
	if toolName == "" {
		toolName = strings.TrimSpace(call.Action.Tool)
	}
	if toolName == "" {
		toolName = "tool"
	}
	action := summarizeToolAction(call)
	if action == "" {
		action = "completed"
	}

	var builder strings.Builder
	fmt.Fprintf(&builder, "[%s] %s\n", toolName, action)
	fmt.Fprintf(
		&builder,
		"Tool action completed; model-visible result is partial (%d chars, %d bytes).\n",
		prepared.OriginalChars,
		prepared.OriginalBytes,
	)
	if prepared.ArtifactRef != "" {
		if prepared.ArtifactComplete {
			fmt.Fprintf(&builder, "Full result: %s\n", prepared.ArtifactRef)
		} else {
			fmt.Fprintf(&builder, "Saved partial result: %s\n", prepared.ArtifactRef)
		}
	} else {
		builder.WriteString("Full result is unavailable; output was bounded before entering active context.\n")
	}
	if strings.TrimSpace(preview) != "" {
		builder.WriteString(preview)
		if !strings.HasSuffix(preview, "\n") {
			builder.WriteByte('\n')
		}
	}
	return strings.TrimSpace(boundTextToTokens(builder.String(), contentTokens))
}

func summarizeToolAction(call ToolCall) string {
	toolName := strings.TrimSpace(call.Spec.Name)
	if toolName == "" {
		toolName = strings.TrimSpace(call.Action.Tool)
	}
	var input map[string]any
	_ = json.Unmarshal([]byte(strings.TrimSpace(call.Input)), &input)
	switch toolName {
	case "shell":
		if command, _ := input["command"].(string); strings.TrimSpace(command) != "" {
			return truncateToolResultRunes(strings.TrimSpace(command), 160)
		}
		if action, _ := input["action"].(string); strings.TrimSpace(action) != "" {
			if sessionID, _ := input["session_id"].(string); strings.TrimSpace(sessionID) != "" {
				return truncateToolResultRunes(strings.TrimSpace(action)+" "+strings.TrimSpace(sessionID), 160)
			}
			return truncateToolResultRunes(strings.TrimSpace(action), 160)
		}
	case "skill_read":
		if name, _ := input["name"].(string); strings.TrimSpace(name) != "" {
			return "read " + truncateToolResultRunes(strings.TrimSpace(name), 150)
		}
	}
	if action, _ := input["action"].(string); strings.TrimSpace(action) != "" {
		return truncateToolResultRunes(strings.TrimSpace(action), 160)
	}
	return "completed"
}

func projectToolResult(call ToolCall, output string, maxTokens int) string {
	projected, _ := projectToolResultWithKind(call, output, maxTokens)
	return projected
}

func projectToolResultWithKind(call ToolCall, output string, maxTokens int) (string, bool) {
	if maxTokens <= 0 || output == "" {
		return "", false
	}
	toolName := strings.TrimSpace(call.Spec.Name)
	if toolName == "" {
		toolName = strings.TrimSpace(call.Action.Tool)
	}
	if toolName == toolBridgeClipboard {
		if projected, ok := projectClipboardToolResult(output, maxTokens); ok {
			return projected, true
		}
	}
	if toolName == "shell" {
		if projected, ok := projectShellTextToolResult(output, maxTokens); ok {
			return projected, false
		}
	}
	if projected, ok := projectJSONToolResult(output, maxTokens); ok {
		return projected, true
	}
	maxRunes := maxTokens * 3
	if maxRunes < 96 {
		maxRunes = 96
	}
	preview := truncateHeadTail(output, maxRunes)
	return boundTextToTokens(preview, maxTokens), false
}

type shellTextSection struct {
	label   string
	start   int
	content string
}

func projectShellTextToolResult(output string, maxTokens int) (string, bool) {
	sections := splitShellTextSections(output)
	if len(sections) == 0 {
		return "", false
	}
	maxRunes := max(96, maxTokens*3)
	prefix := strings.TrimSpace(output[:sections[0].start])
	weight := 0
	if prefix != "" {
		weight++
	}
	for _, section := range sections {
		if section.label == "Stderr" {
			weight += 3
		} else {
			weight += 2
		}
	}
	if weight <= 0 {
		return "", false
	}

	var builder strings.Builder
	if prefix != "" {
		builder.WriteString(truncateHeadTail(prefix, max(64, maxRunes/weight)))
		builder.WriteByte('\n')
	}
	for _, section := range sections {
		sectionWeight := 2
		if section.label == "Stderr" {
			sectionWeight = 3
		}
		budget := max(96, maxRunes*sectionWeight/weight)
		fmt.Fprintf(&builder, "%s:\n", section.label)
		builder.WriteString(truncateHeadTail(strings.TrimSpace(section.content), budget))
		builder.WriteByte('\n')
	}
	return boundTextToTokens(strings.TrimSpace(builder.String()), maxTokens), true
}

func splitShellTextSections(output string) []shellTextSection {
	sections := make([]shellTextSection, 0, 2)
	for _, label := range []string{"Stderr", "Stdout"} {
		marker := label + ":\n"
		for offset := 0; offset < len(output); {
			index := strings.Index(output[offset:], marker)
			if index < 0 {
				break
			}
			index += offset
			if index == 0 || output[index-1] == '\n' {
				sections = append(sections, shellTextSection{
					label: label,
					start: index,
				})
				break
			}
			offset = index + len(marker)
		}
	}
	if len(sections) == 0 {
		return nil
	}
	sort.Slice(sections, func(i, j int) bool { return sections[i].start < sections[j].start })
	for i := range sections {
		contentStart := sections[i].start + len(sections[i].label) + len(":\n")
		contentEnd := len(output)
		if i+1 < len(sections) {
			contentEnd = sections[i+1].start
		}
		sections[i].content = output[contentStart:contentEnd]
	}
	return sections
}

func projectJSONToolResult(output string, maxTokens int) (string, bool) {
	data := []byte(strings.TrimSpace(output))
	if !json.Valid(data) {
		return "", false
	}
	var payload any
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	decoder.UseNumber()
	if err := decoder.Decode(&payload); err != nil {
		return "", false
	}
	projected := projectJSONValue(payload, 0)
	if values, ok := payload.([]any); ok {
		projected = map[string]any{
			"items":       projected,
			"items_total": len(values),
		}
	}
	encoded, err := json.Marshal(projected)
	if err != nil {
		return "", false
	}
	return boundTextToTokens(string(encoded), maxTokens), true
}

func projectJSONValue(value any, depth int) any {
	if depth >= toolResultProjectionDepth {
		return "[nested value omitted]"
	}
	switch value := value.(type) {
	case map[string]any:
		keys := make([]string, 0, len(value))
		for key := range value {
			keys = append(keys, key)
		}
		sort.Slice(keys, func(i, j int) bool {
			left, right := jsonProjectionPriority(keys[i]), jsonProjectionPriority(keys[j])
			if left != right {
				return left < right
			}
			return keys[i] < keys[j]
		})
		if len(keys) > toolResultProjectionFields {
			keys = keys[:toolResultProjectionFields]
		}

		projected := make(map[string]any, len(keys)+1)
		for _, key := range keys {
			field := value[key]
			projected[key] = projectJSONValue(field, depth+1)
			if values, ok := field.([]any); ok {
				totalKey := key + "_total"
				if _, exists := value[totalKey]; !exists {
					projected[totalKey] = len(values)
				}
			}
		}
		if len(value) > len(keys) {
			projected["fields_total"] = len(value)
		}
		return projected
	case []any:
		limit := min(len(value), toolResultProjectionTopK)
		projected := make([]any, 0, limit)
		for _, item := range value[:limit] {
			projected = append(projected, projectJSONValue(item, depth+1))
		}
		return projected
	case string:
		return truncateToolResultRunes(value, toolResultProjectionRunes)
	default:
		return value
	}
}

func jsonProjectionPriority(key string) int {
	switch key {
	case "ok":
		return 0
	case "status":
		return 1
	case "error":
		return 2
	case "code":
		return 3
	case "exit_code":
		return 4
	case "exit_error":
		return 5
	case "id":
		return 6
	case "path":
		return 7
	case "url":
		return 8
	case "cursor":
		return 9
	case "has_more":
		return 10
	case "continuation":
		return 11
	case "results", "items", "entries", "data":
		return 12
	default:
		return 100
	}
}

func projectClipboardToolResult(output string, maxTokens int) (string, bool) {
	var payload struct {
		OK     *bool  `json:"ok"`
		Status string `json:"status"`
		Error  any    `json:"error"`
		Text   string `json:"text"`
	}
	if err := json.Unmarshal([]byte(output), &payload); err != nil {
		return "", false
	}
	projection := map[string]any{
		"text_chars": utf8.RuneCountInString(payload.Text),
	}
	if payload.OK != nil {
		projection["ok"] = *payload.OK
	}
	if strings.TrimSpace(payload.Status) != "" {
		projection["status"] = payload.Status
	}
	if payload.Error != nil {
		projection["error"] = payload.Error
	}
	if payload.Text != "" {
		projection["text_preview"] = truncateToolResultRunes(payload.Text, 256)
	}
	data, err := json.Marshal(projection)
	if err != nil {
		return "", false
	}
	return boundTextToTokens(string(data), maxTokens), true
}

func truncateHeadTail(text string, maxRunes int) string {
	runes := []rune(text)
	if maxRunes <= 0 || len(runes) <= maxRunes {
		return text
	}
	marker := fmt.Sprintf("\n... %d chars omitted ...\n", len(runes)-maxRunes)
	head := maxRunes / 2
	tail := maxRunes - head
	return string(runes[:head]) + marker + string(runes[len(runes)-tail:])
}

func truncateToolResultRunes(text string, maxRunes int) string {
	runes := []rune(text)
	if maxRunes <= 0 || len(runes) <= maxRunes {
		return text
	}
	return string(runes[:maxRunes]) + "…"
}

func boundTextToTokens(text string, maxTokens int) string {
	if maxTokens <= 0 || estimateTextTokens(text) <= maxTokens {
		return text
	}
	runes := []rune(text)
	low, high := 0, len(runes)
	for low < high {
		mid := (low + high + 1) / 2
		candidate := string(runes[:mid])
		if estimateTextTokens(candidate) <= maxTokens {
			low = mid
		} else {
			high = mid - 1
		}
	}
	return string(runes[:low])
}
