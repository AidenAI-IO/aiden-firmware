package agent

import (
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
	TodoSourceImplicitSimple TodoSource = "implicit_simple"
)

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
		return s.CurrentSpeech()
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

func (s *roleLoopState) ensureImplicitSimpleTodo(spec ToolSpec) (TodoState, bool) {
	if s == nil || len(s.Todo.Items) > 0 || s.Todo.Mode == TodoModePlanned {
		return TodoState{}, false
	}
	text := implicitSimpleTodoText(spec)
	if text == "" {
		return TodoState{}, false
	}
	revision := s.Todo.Revision + 1
	if revision <= 0 {
		revision = 1
	}
	item := TodoItem{
		ID:     fmt.Sprintf("todo-r%d-simple", revision),
		Text:   text,
		Speech: deriveTodoSpeech(text),
		Status: TodoInProgress,
		Source: TodoSourceImplicitSimple,
	}
	s.Todo = TodoState{
		Mode:      TodoModeSimple,
		Objective: strings.TrimSpace(s.Objective),
		Revision:  revision,
		CurrentID: item.ID,
		Items:     []TodoItem{item},
	}
	return s.Todo.Clone(), true
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

func implicitSimpleTodoText(spec ToolSpec) string {
	switch strings.ToLower(strings.TrimSpace(spec.Category)) {
	case "observation":
		return "检查当前界面"
	case "web":
		return "查找相关信息"
	case "memory":
		return "回看已有记录"
	case "audio", "system":
		return "读取当前状态"
	default:
		return ""
	}
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
