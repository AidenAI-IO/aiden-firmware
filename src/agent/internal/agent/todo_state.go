package agent

import (
	"encoding/json"
	"fmt"
	"strings"
)

type TodoMode string

const (
	TodoModeNone    TodoMode = "none"
	TodoModeSimple  TodoMode = "simple"
	TodoModePlanned TodoMode = "planned"
)

type TodoStatus string

const (
	TodoPending    TodoStatus = "pending"
	TodoInProgress TodoStatus = "in_progress"
	TodoDone       TodoStatus = "done"
	TodoBlocked    TodoStatus = "blocked"
)

type TodoSource string

const (
	TodoSourceCommittedPlan  TodoSource = "committed_plan"
	TodoSourceExplicitSimple TodoSource = "explicit_simple"
)

type simpleTodoUpdate struct {
	Objective        string
	Items            []string
	CurrentIndex     int
	CompletedIndices []int
	BlockedIndices   []int
	Reason           string
}

type TodoItem struct {
	ID        string     `json:"id"`
	Text      string     `json:"text"`
	Speech    string     `json:"speech,omitempty"`
	Status    TodoStatus `json:"status"`
	Source    TodoSource `json:"source"`
	StepIndex int        `json:"step_index,omitempty"`
}

type TodoState struct {
	Mode      TodoMode   `json:"mode"`
	Objective string     `json:"objective,omitempty"`
	Revision  int        `json:"revision"`
	CurrentID string     `json:"current_id,omitempty"`
	Items     []TodoItem `json:"items,omitempty"`
}

const todoSpeechMaxRunes = 40
const defaultTodoReminderToolCalls = 4

func (s TodoState) Clone() TodoState {
	cloned := s
	if len(s.Items) > 0 {
		cloned.Items = append([]TodoItem(nil), s.Items...)
	}
	return cloned
}

func (s TodoState) CurrentItem() (TodoItem, bool) {
	currentID := strings.TrimSpace(s.CurrentID)
	if currentID == "" {
		return TodoItem{}, false
	}
	for _, item := range s.Items {
		if item.ID == currentID {
			return item, true
		}
	}
	return TodoItem{}, false
}

func (s TodoState) CurrentSpeech() string {
	item, ok := s.CurrentItem()
	if !ok {
		return ""
	}
	if speech := strings.TrimSpace(item.Speech); speech != "" {
		return speech
	}
	return deriveTodoSpeech(item.Text)
}

func (s TodoState) SummaryText() string {
	switch s.Mode {
	case TodoModePlanned:
		if len(s.Items) == 0 {
			return ""
		}
		return fmt.Sprintf("Plan committed: %d steps", len(s.Items))
	case TodoModeSimple:
		if len(s.Items) == 0 {
			return ""
		}
		return fmt.Sprintf("Todo updated: %d items", len(s.Items))
	default:
		return ""
	}
}

func (s *roleLoopState) applyCommittedPlanTodo(decision plannerDecision) {
	steps := uniqueNonEmpty(decision.Plan)
	revision := s.Todo.Revision + 1
	if revision <= 0 {
		revision = 1
	}
	items := make([]TodoItem, 0, len(steps))
	for i, step := range steps {
		text := strings.TrimSpace(step)
		items = append(items, TodoItem{
			ID:        fmt.Sprintf("todo-r%d-step%d", revision, i+1),
			Text:      text,
			Speech:    deriveTodoSpeech(text),
			Status:    TodoPending,
			Source:    TodoSourceCommittedPlan,
			StepIndex: i + 1,
		})
	}
	objective := strings.TrimSpace(decision.Objective)
	if objective == "" {
		objective = strings.TrimSpace(s.Objective)
	}
	s.Todo = TodoState{
		Mode:      TodoModePlanned,
		Objective: objective,
		Revision:  revision,
		Items:     items,
	}
}

func (s *roleLoopState) startCurrentTodoStep() (TodoState, bool) {
	if s == nil || s.Todo.Mode != TodoModePlanned || len(s.Todo.Items) == 0 {
		return TodoState{}, false
	}
	stepIndex := s.PlanStepIndex + 1
	for i := range s.Todo.Items {
		if s.Todo.Items[i].StepIndex != stepIndex {
			continue
		}
		if s.Todo.Items[i].Status == TodoInProgress && s.Todo.CurrentID == s.Todo.Items[i].ID {
			return TodoState{}, false
		}
		s.Todo.Items[i].Status = TodoInProgress
		s.Todo.CurrentID = s.Todo.Items[i].ID
		return s.Todo.Clone(), true
	}
	return TodoState{}, false
}

func (s *roleLoopState) finishCurrentTodoStep() (TodoState, bool) {
	return s.setCurrentTodoStatus(TodoDone)
}

func (s *roleLoopState) blockCurrentTodoStep(_ string) (TodoState, bool) {
	return s.setCurrentTodoStatus(TodoBlocked)
}

func (s *roleLoopState) applySimpleTodoUpdate(update simpleTodoUpdate) (TodoState, bool) {
	revision := s.Todo.Revision + 1
	if revision <= 0 {
		revision = 1
	}
	completed := indexSet(update.CompletedIndices)
	blocked := indexSet(update.BlockedIndices)
	items := make([]TodoItem, 0, len(update.Items))
	for i, itemText := range update.Items {
		stepIndex := i + 1
		text := strings.TrimSpace(itemText)
		status := TodoPending
		if completed[stepIndex] {
			status = TodoDone
		}
		if blocked[stepIndex] {
			status = TodoBlocked
		}
		if stepIndex == update.CurrentIndex {
			status = TodoInProgress
		}
		items = append(items, TodoItem{
			ID:        fmt.Sprintf("todo-r%d-simple%d", revision, stepIndex),
			Text:      text,
			Speech:    deriveTodoSpeech(text),
			Status:    status,
			Source:    TodoSourceExplicitSimple,
			StepIndex: stepIndex,
		})
	}
	objective := strings.TrimSpace(update.Objective)
	if objective == "" {
		objective = strings.TrimSpace(s.Objective)
	}
	currentID := ""
	if update.CurrentIndex >= 1 && update.CurrentIndex <= len(items) {
		currentID = items[update.CurrentIndex-1].ID
	}
	speechEligible := false
	if currentID != "" {
		current := items[update.CurrentIndex-1]
		previousStatus := s.Todo.statusForText(current.Text)
		speechEligible = current.Status == TodoInProgress && (previousStatus == "" || previousStatus == TodoPending)
	}
	s.Todo = TodoState{
		Mode:      TodoModeSimple,
		Objective: objective,
		Revision:  revision,
		CurrentID: currentID,
		Items:     items,
	}
	s.DefaultToolCallsSinceTodoTouch = 0
	s.PendingTodoReminder = ""
	return s.Todo.Clone(), speechEligible
}

func (s *roleLoopState) setCurrentTodoStatus(status TodoStatus) (TodoState, bool) {
	if s == nil || len(s.Todo.Items) == 0 {
		return TodoState{}, false
	}
	index := -1
	currentID := strings.TrimSpace(s.Todo.CurrentID)
	if currentID != "" {
		for i := range s.Todo.Items {
			if s.Todo.Items[i].ID == currentID {
				index = i
				break
			}
		}
	}
	if index < 0 && s.Todo.Mode == TodoModePlanned {
		stepIndex := s.PlanStepIndex + 1
		for i := range s.Todo.Items {
			if s.Todo.Items[i].StepIndex == stepIndex {
				index = i
				break
			}
		}
	}
	if index < 0 {
		return TodoState{}, false
	}
	if s.Todo.Items[index].Status == status && s.Todo.CurrentID == s.Todo.Items[index].ID {
		return TodoState{}, false
	}
	s.Todo.Items[index].Status = status
	s.Todo.CurrentID = s.Todo.Items[index].ID
	return s.Todo.Clone(), true
}

func (s TodoState) statusForText(text string) TodoStatus {
	text = strings.TrimSpace(text)
	for _, item := range s.Items {
		if strings.TrimSpace(item.Text) == text {
			return item.Status
		}
	}
	return ""
}

func parseSetTodoInput(raw string) (simpleTodoUpdate, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return simpleTodoUpdate{}, fmt.Errorf("set_todo requires a JSON payload")
	}
	var payload struct {
		Objective        string          `json:"objective"`
		Items            json.RawMessage `json:"items"`
		CurrentIndex     int             `json:"current_index"`
		CompletedIndices []int           `json:"completed_indices"`
		BlockedIndices   []int           `json:"blocked_indices"`
		Reason           string          `json:"reason"`
	}
	if err := json.Unmarshal([]byte(raw), &payload); err != nil {
		return simpleTodoUpdate{}, fmt.Errorf("set_todo payload must be valid JSON: %w", err)
	}
	items := uniqueNonEmpty(parseStructuredStringList(payload.Items))
	if len(items) == 0 {
		return simpleTodoUpdate{}, fmt.Errorf("set_todo requires at least one item")
	}
	if payload.CurrentIndex < 1 || payload.CurrentIndex > len(items) {
		return simpleTodoUpdate{}, fmt.Errorf("current_index must be between 1 and %d", len(items))
	}
	completedIndices, err := validatedTodoIndices(payload.CompletedIndices, len(items), "completed_indices")
	if err != nil {
		return simpleTodoUpdate{}, err
	}
	blockedIndices, err := validatedTodoIndices(payload.BlockedIndices, len(items), "blocked_indices")
	if err != nil {
		return simpleTodoUpdate{}, err
	}
	completed := indexSet(completedIndices)
	blocked := indexSet(blockedIndices)
	if completed[payload.CurrentIndex] || blocked[payload.CurrentIndex] {
		return simpleTodoUpdate{}, fmt.Errorf("current_index cannot also be completed or blocked")
	}
	return simpleTodoUpdate{
		Objective:        strings.TrimSpace(payload.Objective),
		Items:            items,
		CurrentIndex:     payload.CurrentIndex,
		CompletedIndices: completedIndices,
		BlockedIndices:   blockedIndices,
		Reason:           strings.TrimSpace(payload.Reason),
	}, nil
}

func todoCurrentStepIndex(todo TodoState) int {
	currentID := strings.TrimSpace(todo.CurrentID)
	if currentID == "" {
		return 0
	}
	for _, item := range todo.Items {
		if item.ID == currentID {
			return item.StepIndex
		}
	}
	return 0
}

func validatedTodoIndices(values []int, max int, field string) ([]int, error) {
	if len(values) == 0 {
		return nil, nil
	}
	seen := map[int]bool{}
	var out []int
	for _, value := range values {
		if value < 1 || value > max {
			return nil, fmt.Errorf("%s contains index %d outside 1..%d", field, value, max)
		}
		if seen[value] {
			continue
		}
		seen[value] = true
		out = append(out, value)
	}
	return out, nil
}

func indexSet(values []int) map[int]bool {
	set := map[int]bool{}
	for _, value := range values {
		if value > 0 {
			set[value] = true
		}
	}
	return set
}

func (s *roleLoopState) noteDefaultToolCallAndMaybeTodoReminder() bool {
	if s == nil || s.Phase != phaseDefault {
		return false
	}
	s.DefaultToolCallsSinceTodoTouch++
	hasTodo := s.Todo.Mode == TodoModeSimple && len(s.Todo.Items) > 0
	if s.DefaultToolCallsSinceTodoTouch < defaultTodoReminderToolCalls {
		return false
	}
	s.DefaultToolCallsSinceTodoTouch = 0
	message := "This single-agent loop has used several tool calls without a todo. If the task has become multi-step, call set_todo with the current item; otherwise continue normally."
	if hasTodo {
		message = "Several tool calls have run since the todo was updated. If the current todo is stale, completed, blocked, or you moved to another subtask, call set_todo; otherwise continue normally."
	}
	s.PendingTodoReminder = message
	return true
}

func deriveTodoSpeech(text string) string {
	text = strings.Join(strings.Fields(strings.TrimSpace(text)), " ")
	return truncateTodoRunes(text, todoSpeechMaxRunes)
}

func truncateTodoRunes(text string, max int) string {
	if max <= 0 {
		return strings.TrimSpace(text)
	}
	runes := []rune(strings.TrimSpace(text))
	if len(runes) <= max {
		return string(runes)
	}
	return string(runes[:max])
}
