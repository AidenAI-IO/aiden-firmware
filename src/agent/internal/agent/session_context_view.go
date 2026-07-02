package agent

import (
	"context"
	"fmt"
	"strings"

	"github.com/tmc/langchaingo/schema"
)

const (
	sessionContextInputKey = "session_context"
	rootRequestInputKey    = "root_request"
	latestUserInputKey     = "latest_user_message"
)

const maxSessionContextEvents = 200

type sessionContextView struct {
	RootUserRequest   string
	LatestUserMessage string
}

type sessionContextPlannerMemory struct {
	inner     schema.Memory
	manager   *MemoryManager
	agentName string
}

func newSessionContextPlannerMemory(inner schema.Memory, manager *MemoryManager, agentName string) schema.Memory {
	if inner == nil || manager == nil || strings.TrimSpace(manager.storageDir) == "" {
		return inner
	}
	if strings.TrimSpace(agentName) == "" {
		agentName = "default"
	}
	return &sessionContextPlannerMemory{inner: inner, manager: manager, agentName: agentName}
}

func (m *sessionContextPlannerMemory) GetMemoryKey(ctx context.Context) string {
	return m.inner.GetMemoryKey(ctx)
}

func (m *sessionContextPlannerMemory) MemoryVariables(ctx context.Context) []string {
	variables := append([]string(nil), m.inner.MemoryVariables(ctx)...)
	for _, key := range []string{sessionContextInputKey, rootRequestInputKey, latestUserInputKey} {
		if !slicesContainsString(variables, key) {
			variables = append(variables, key)
		}
	}
	return variables
}

func (m *sessionContextPlannerMemory) LoadMemoryVariables(ctx context.Context, inputs map[string]any) (map[string]any, error) {
	values, err := m.inner.LoadMemoryVariables(ctx, inputs)
	if err != nil {
		return nil, err
	}
	events, err := m.manager.LoadActiveSessionEvents(ctx, maxSessionContextEvents)
	if err != nil {
		return nil, err
	}
	view := BuildSessionContextView(events, currentInputFromMemoryInputs(inputs))
	rendered := formatSessionContextView(view)
	if strings.TrimSpace(rendered) == "" {
		return values, nil
	}
	if values == nil {
		values = map[string]any{}
	}
	values[sessionContextInputKey] = rendered
	if view.RootUserRequest != "" {
		values[rootRequestInputKey] = view.RootUserRequest
	}
	if view.LatestUserMessage != "" {
		values[latestUserInputKey] = view.LatestUserMessage
	}
	return values, nil
}

func (m *sessionContextPlannerMemory) SaveContext(ctx context.Context, inputs map[string]any, outputs map[string]any) error {
	return m.inner.SaveContext(ctx, inputs, outputs)
}

func (m *sessionContextPlannerMemory) Clear(ctx context.Context) error {
	return m.inner.Clear(ctx)
}

func currentInputFromMemoryInputs(inputs map[string]any) string {
	if inputs == nil {
		return ""
	}
	value, ok := inputs["input"]
	if !ok {
		return ""
	}
	switch typed := value.(type) {
	case string:
		return typed
	default:
		return fmt.Sprint(typed)
	}
}

func BuildSessionContextView(events []SessionEvent, currentInput string) sessionContextView {
	currentInput = strings.TrimSpace(currentInput)
	events = runtimeSessionContextEvents(events)
	if len(events) == 0 && currentInput == "" {
		return sessionContextView{}
	}
	latestUserIndex := latestUserEventIndex(events, currentInput)
	var latestUser SessionEvent
	if latestUserIndex >= 0 {
		latestUser = events[latestUserIndex]
	}
	latestContent := strings.TrimSpace(latestUser.Content)
	if latestContent == "" {
		latestContent = currentInput
	}

	root := latestContent
	if sessionRoot, _ := activeSessionRootUserInput(events, latestUserIndex); sessionRoot != "" {
		root = sessionRoot
	}
	return sessionContextView{
		RootUserRequest:   root,
		LatestUserMessage: latestContent,
	}
}

func runtimeSessionContextEvents(events []SessionEvent) []SessionEvent {
	hasRuntimeEvents := false
	for _, event := range events {
		if strings.TrimSpace(event.RunID) != "" {
			hasRuntimeEvents = true
			break
		}
	}
	if !hasRuntimeEvents {
		return events
	}
	scoped := make([]SessionEvent, 0, len(events))
	for _, event := range events {
		if strings.TrimSpace(event.RunID) != "" {
			scoped = append(scoped, event)
		}
	}
	return scoped
}

func latestUserEventIndex(events []SessionEvent, currentInput string) int {
	currentInput = strings.TrimSpace(currentInput)
	if currentInput != "" {
		for i := len(events) - 1; i >= 0; i-- {
			if events[i].Type == "user_input" && strings.TrimSpace(events[i].Content) == currentInput {
				return i
			}
		}
	}
	for i := len(events) - 1; i >= 0; i-- {
		if events[i].Type == "user_input" {
			return i
		}
	}
	return -1
}

func activeSessionRootUserInput(events []SessionEvent, latestUserIndex int) (string, int) {
	limit := len(events)
	if latestUserIndex >= 0 && latestUserIndex < limit {
		limit = latestUserIndex + 1
	}
	for i := 0; i < limit; i++ {
		event := events[i]
		if event.Type != "user_input" {
			continue
		}
		content := strings.TrimSpace(event.Content)
		if content != "" {
			return content, i
		}
	}
	return "", -1
}

func formatSessionContextView(view sessionContextView) string {
	if strings.TrimSpace(view.LatestUserMessage) == "" {
		return ""
	}
	var builder strings.Builder
	builder.WriteString("Session context view:\n")
	builder.WriteString("- Latest user message: ")
	builder.WriteString(singleLineHistoryText(strings.TrimSpace(view.LatestUserMessage)))
	builder.WriteByte('\n')
	return strings.TrimSpace(builder.String())
}

func slicesContainsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
