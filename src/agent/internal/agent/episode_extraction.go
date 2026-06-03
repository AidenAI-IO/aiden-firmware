package agent

import (
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// extractToolCallDescription 从工具调用的 ToolInput JSON 中尽力提取出可读的 description
// 字段。不同模型/工具会把它放在 top-level 或包在 __arg1 这样的字符串里，这里尝试两层。
func extractToolCallDescription(toolInput string) string {
	input := strings.TrimSpace(toolInput)
	if input == "" || input[0] != '{' {
		return ""
	}
	if desc := lookupStringField(input, "description"); desc != "" {
		return desc
	}
	// __arg1 通常是又一个 JSON 字符串 — 解一层再找。
	if arg := lookupStringField(input, "__arg1"); arg != "" {
		if strings.HasPrefix(strings.TrimSpace(arg), "{") {
			if desc := lookupStringField(arg, "description"); desc != "" {
				return desc
			}
		}
	}
	return ""
}

func lookupStringField(jsonText, field string) string {
	var probe map[string]json.RawMessage
	if err := json.Unmarshal([]byte(jsonText), &probe); err != nil {
		return ""
	}
	raw, ok := probe[field]
	if !ok {
		return ""
	}
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return ""
	}
	return strings.TrimSpace(s)
}

// extractToolCallCoords 解析一个 touch/swipe 类工具的输入，给出形如
// "x=0.50,y=0.85"（tap）或 "0.50,0.80→0.50,0.40"（swipe）的紧凑坐标摘要。
// 找不到坐标返回空串。
func extractToolCallCoords(toolInput string) string {
	input := strings.TrimSpace(toolInput)
	if input == "" || input[0] != '{' {
		return ""
	}
	target := input
	// 拆开 __arg1 包裹，如果存在的话。
	if arg := lookupStringField(input, "__arg1"); arg != "" && strings.HasPrefix(strings.TrimSpace(arg), "{") {
		target = arg
	}

	var probe struct {
		Type  string         `json:"type"`
		Point map[string]any `json:"point"`
		Start map[string]any `json:"start"`
		End   map[string]any `json:"end"`
	}
	if err := json.Unmarshal([]byte(target), &probe); err != nil {
		return ""
	}
	switch strings.ToLower(strings.TrimSpace(probe.Type)) {
	case "swipe":
		start := pointSummary(probe.Start)
		end := pointSummary(probe.End)
		if start != "" && end != "" {
			return start + "→" + end
		}
		return ""
	default:
		return pointSummary(probe.Point)
	}
}

func pointSummary(point map[string]any) string {
	if len(point) == 0 {
		return ""
	}
	x, okX := numericField(point, "x")
	y, okY := numericField(point, "y")
	if !okX || !okY {
		return ""
	}
	return fmt.Sprintf("x=%.2f,y=%.2f", x, y)
}

func numericField(m map[string]any, key string) (float64, bool) {
	v, ok := m[key]
	if !ok {
		return 0, false
	}
	switch typed := v.(type) {
	case float64:
		return typed, true
	case int:
		return float64(typed), true
	case json.Number:
		f, err := typed.Float64()
		return f, err == nil
	case string:
		f, err := strconv.ParseFloat(strings.TrimSpace(typed), 64)
		return f, err == nil
	}
	return 0, false
}

// extractToolCallText 提取常见的"输入文本"参数（text / query / keyword 等）。
func extractToolCallText(toolInput string) string {
	input := strings.TrimSpace(toolInput)
	if input == "" || input[0] != '{' {
		return ""
	}
	target := input
	if arg := lookupStringField(input, "__arg1"); arg != "" && strings.HasPrefix(strings.TrimSpace(arg), "{") {
		target = arg
	}
	for _, key := range []string{"text", "query", "keyword", "input", "value"} {
		if v := lookupStringField(target, key); v != "" {
			return v
		}
	}
	return ""
}

// episodeProcedureSteps 配对 episode 中的 tool_call 与紧随的 tool_result，
// 再吸收同窗口内的 verifier ObservedState（用作 OutcomeNote 与 PageName），
// 输出一份适合写入 procedure 的步骤列表。
func episodeProcedureSteps(events []TaskEpisodeEvent) []ProcedureStep {
	var steps []ProcedureStep
	pendingApp, pendingPage := "", ""
	for i := 0; i < len(events); i++ {
		evt := events[i]
		if evt.Type == "verifier_decision" && evt.ObservedState != nil {
			pendingApp, pendingPage = evt.ObservedState.AppName, evt.ObservedState.PageName
			continue
		}
		if evt.Type != "tool_call" || strings.TrimSpace(evt.ToolName) == "" {
			continue
		}
		step := ProcedureStep{
			Tool:        evt.ToolName,
			Description: evt.ToolDescription,
			Coords:      extractToolCallCoords(evt.ToolInput),
			Text:        extractToolCallText(evt.ToolInput),
			AppName:     pendingApp,
			PageName:    pendingPage,
		}
		// 找对应的 tool_result 与下一个 verifier 观察来填 OutcomeNote。
		var resultObs string
		var nextApp, nextPage string
		for j := i + 1; j < len(events); j++ {
			next := events[j]
			if next.Type == "tool_result" && next.ToolName == evt.ToolName && resultObs == "" {
				resultObs = next.Observation
			}
			if next.Type == "verifier_decision" && next.ObservedState != nil {
				nextApp, nextPage = next.ObservedState.AppName, next.ObservedState.PageName
				break
			}
			if next.Type == "tool_call" {
				break
			}
		}
		if step.AppName == "" {
			step.AppName = nextApp
		}
		if step.PageName == "" {
			step.PageName = nextPage
		}
		step.OutcomeNote = composeOutcomeNote(resultObs, nextApp, nextPage)
		steps = append(steps, step)
	}
	return steps
}

func composeOutcomeNote(observation, app, page string) string {
	parts := []string{}
	target := strings.TrimSpace(page)
	if app != "" {
		if target != "" {
			target = app + "/" + target
		} else {
			target = app
		}
	}
	if target != "" {
		parts = append(parts, "page="+target)
	}
	if obs := strings.TrimSpace(observation); obs != "" {
		parts = append(parts, "obs="+truncateForLog(obs, 80))
	}
	return strings.Join(parts, " ")
}

// summarizeProcedureSteps 把 procedure 步骤压成一段对 LLM 友好的多行文本。
// limit<=0 时不截断。
func summarizeProcedureSteps(steps []ProcedureStep, limit int) string {
	if len(steps) == 0 {
		return ""
	}
	var b strings.Builder
	for i, step := range steps {
		if limit > 0 && i >= limit {
			b.WriteString(fmt.Sprintf("  ... (+%d more steps)\n", len(steps)-limit))
			break
		}
		b.WriteString(fmt.Sprintf("  %d. %s", i+1, step.Tool))
		args := []string{}
		if step.Coords != "" {
			args = append(args, "@"+step.Coords)
		}
		if step.Text != "" {
			args = append(args, fmt.Sprintf("text=%q", truncateForLog(step.Text, 32)))
		}
		if step.Description != "" {
			args = append(args, "desc="+strconv.Quote(truncateForLog(step.Description, 60)))
		}
		if len(args) > 0 {
			b.WriteString("(")
			b.WriteString(strings.Join(args, ", "))
			b.WriteString(")")
		}
		if step.OutcomeNote != "" {
			b.WriteString(" → ")
			b.WriteString(step.OutcomeNote)
		}
		b.WriteString("\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

// observedPagesByApp 收集 episode 中每个 app 出现过的所有 page_name，结果按
// 出现顺序去重，便于写入 app_profile.PagesSeen。
func observedPagesByApp(events []TaskEpisodeEvent) map[string][]string {
	out := map[string][]string{}
	seen := map[string]map[string]bool{}
	add := func(app, page string) {
		app = strings.TrimSpace(app)
		page = strings.TrimSpace(page)
		if app == "" || page == "" {
			return
		}
		if seen[app] == nil {
			seen[app] = map[string]bool{}
		}
		if seen[app][page] {
			return
		}
		seen[app][page] = true
		out[app] = append(out[app], page)
	}
	for _, evt := range events {
		if evt.ObservedState == nil {
			continue
		}
		add(evt.ObservedState.AppName, evt.ObservedState.PageName)
	}
	return out
}

// observedToolsByApp 统计每个 app 在成功 tool_call 中用到的工具名。
// 工具调用归属到当前 app（从之前最近的 verifier 观察或之后最近的观察推断）。
func observedToolsByApp(events []TaskEpisodeEvent) map[string][]string {
	out := map[string][]string{}
	seen := map[string]map[string]bool{}

	// 预扫描：记录每个 verifier_decision 的位置和 app
	appAtIndex := make([]string, len(events))
	lastApp := ""
	for i := range events {
		if events[i].Type == "verifier_decision" && events[i].ObservedState != nil {
			lastApp = strings.TrimSpace(events[i].ObservedState.AppName)
		}
		appAtIndex[i] = lastApp
	}

	// 扫描 tool_call，归属到当前 app（如果为空，往后找最近的）
	for i, evt := range events {
		if evt.Type != "tool_call" || strings.TrimSpace(evt.ToolName) == "" {
			continue
		}
		app := appAtIndex[i]
		if app == "" {
			// 当前没有 app，往后找最近的 verifier
			for j := i + 1; j < len(events); j++ {
				if events[j].Type == "verifier_decision" && events[j].ObservedState != nil {
					app = strings.TrimSpace(events[j].ObservedState.AppName)
					break
				}
			}
		}
		if app == "" {
			continue
		}
		if seen[app] == nil {
			seen[app] = map[string]bool{}
		}
		if seen[app][evt.ToolName] {
			continue
		}
		seen[app][evt.ToolName] = true
		out[app] = append(out[app], evt.ToolName)
	}
	for app := range out {
		sort.Strings(out[app])
	}
	return out
}

// pageTransitions 抽取 episode 中所有「page_a → page_b 因为执行了 tool_call X」的
// 转移规则，便于写入 navigation 类型记忆。
type pageTransition struct {
	FromApp, FromPage string
	ToApp, ToPage     string
	Tool              string
	Description       string
	Coords            string
	Text              string
}

func pageTransitions(events []TaskEpisodeEvent) []pageTransition {
	var transitions []pageTransition
	curApp, curPage := "", ""
	var pending *TaskEpisodeEvent
	for i := range events {
		evt := events[i]
		switch evt.Type {
		case "verifier_decision":
			if evt.ObservedState == nil {
				continue
			}
			nextApp := strings.TrimSpace(evt.ObservedState.AppName)
			nextPage := strings.TrimSpace(evt.ObservedState.PageName)
			if pending != nil && (curApp != "" || curPage != "") && (nextApp != "" || nextPage != "") {
				if curApp != nextApp || curPage != nextPage {
					transitions = append(transitions, pageTransition{
						FromApp:     curApp,
						FromPage:    curPage,
						ToApp:       nextApp,
						ToPage:      nextPage,
						Tool:        pending.ToolName,
						Description: pending.ToolDescription,
						Coords:      extractToolCallCoords(pending.ToolInput),
						Text:        extractToolCallText(pending.ToolInput),
					})
				}
			}
			curApp, curPage = nextApp, nextPage
			pending = nil
		case "tool_call":
			cp := evt
			pending = &cp
		}
	}
	return transitions
}
