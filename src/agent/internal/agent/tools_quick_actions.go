package agent

import (
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"unicode"
)

//go:embed quick_actions.json
var defaultQuickActionsJSON []byte

// QuickActionsFileName is the filename used to look up runtime overrides under
// configDir, and the bundled file shipped with the firmware.
const QuickActionsFileName = "quick_actions.json"

// BundledQuickActionsPath is the on-device install path, populated by
// _build_image.sh from src/agent/internal/agent/quick_actions.json.
const BundledQuickActionsPath = "/usr/share/aiden/" + QuickActionsFileName

const (
	quickActionStatusActive   = "active"
	quickActionStatusReserved = "reserved"
)

var supportedQuickActionPlatforms = []string{"ios", "android", "mac"}

type quickActionBinding struct {
	Status       string               `json:"status"`
	Tool         string               `json:"tool"`
	Input        json.RawMessage      `json:"input"`
	Note         string               `json:"note,omitempty"`
	Alternatives []quickActionBinding `json:"alternatives,omitempty"`
}

type quickActionDefinition struct {
	Label     string                        `json:"label"`
	Aliases   []string                      `json:"aliases,omitempty"`
	Category  string                        `json:"category"`
	Platforms map[string]quickActionBinding `json:"platforms"`
}

type quickActionsDocument struct {
	Version int                              `json:"version"`
	Actions map[string]quickActionDefinition `json:"actions"`
}

type quickActionsTable struct {
	mu       sync.RWMutex
	document quickActionsDocument
	aliasMap map[string]string
	matchMap map[string]string
	source   string
}

var globalQuickActions = newQuickActionsTable()

func newQuickActionsTable() *quickActionsTable {
	t := &quickActionsTable{}
	if err := t.loadFromBytes(defaultQuickActionsJSON, "embedded"); err != nil {
		t.document = quickActionsDocument{Actions: map[string]quickActionDefinition{}}
		t.aliasMap = map[string]string{}
		t.matchMap = map[string]string{}
		t.source = "embedded(invalid)"
	}
	return t
}

func (t *quickActionsTable) loadFromBytes(data []byte, source string) error {
	var doc quickActionsDocument
	if err := json.Unmarshal(data, &doc); err != nil {
		return err
	}
	if doc.Actions == nil {
		doc.Actions = map[string]quickActionDefinition{}
	}
	aliasMap := make(map[string]string)
	matchMap := make(map[string]string)
	for id, action := range doc.Actions {
		addQuickActionAlias(aliasMap, matchMap, id, id)
		addQuickActionAlias(aliasMap, matchMap, id, action.Label)
		for _, alias := range action.Aliases {
			addQuickActionAlias(aliasMap, matchMap, id, alias)
		}
	}
	t.mu.Lock()
	t.document = doc
	t.aliasMap = aliasMap
	t.matchMap = matchMap
	t.source = source
	t.mu.Unlock()
	return nil
}

func (t *quickActionsTable) loadFromFile(path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return t.loadFromBytes(data, path)
}

func normalizeQuickActionKey(key string) string {
	return strings.ToLower(strings.TrimSpace(key))
}

func normalizeQuickActionMatchKey(key string) string {
	key = strings.ToLower(strings.TrimSpace(key))
	var builder strings.Builder
	lastUnderscore := false
	for _, r := range key {
		switch {
		case unicode.IsLetter(r) || unicode.IsDigit(r):
			builder.WriteRune(r)
			lastUnderscore = false
		case unicode.IsSpace(r) || r == '_' || r == '-' || r == '/' || r == '.':
			if builder.Len() > 0 && !lastUnderscore {
				builder.WriteRune('_')
				lastUnderscore = true
			}
		}
	}
	return strings.Trim(builder.String(), "_")
}

func addQuickActionAlias(aliasMap, matchMap map[string]string, id, alias string) {
	alias = strings.TrimSpace(alias)
	if alias == "" {
		return
	}
	aliasMap[normalizeQuickActionKey(alias)] = id
	matchMap[normalizeQuickActionMatchKey(alias)] = id
}

func normalizeQuickActionPlatform(platform string) (string, error) {
	platform = normalizeQuickActionKey(platform)
	switch platform {
	case "ios", "iphone", "ipad", "ipados":
		return "ios", nil
	case "android":
		return "android", nil
	case "mac", "macos", "osx":
		return "mac", nil
	case "":
		return "", fmt.Errorf("platform is required (ios, android, mac)")
	default:
		return "", fmt.Errorf("unsupported platform %q (expected ios, android, mac)", platform)
	}
}

func (t *quickActionsTable) resolveActionID(action string) (string, bool) {
	t.mu.RLock()
	defer t.mu.RUnlock()
	id, ok := t.aliasMap[normalizeQuickActionKey(action)]
	if ok {
		return id, true
	}
	id, ok = t.matchMap[normalizeQuickActionMatchKey(action)]
	return id, ok
}

func (t *quickActionsTable) suggestActionIDs(action string, limit int) []string {
	query := normalizeQuickActionMatchKey(action)
	if query == "" || limit <= 0 {
		return nil
	}

	t.mu.RLock()
	defer t.mu.RUnlock()

	scores := map[string]int{}
	for alias, id := range t.matchMap {
		if alias == "" {
			continue
		}
		score := quickActionSuggestionScore(query, alias, id)
		if score <= 0 {
			continue
		}
		if score > scores[id] {
			scores[id] = score
		}
	}

	ids := make([]string, 0, len(scores))
	for id := range scores {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool {
		if scores[ids[i]] == scores[ids[j]] {
			return ids[i] < ids[j]
		}
		return scores[ids[i]] > scores[ids[j]]
	})
	if len(ids) > limit {
		ids = ids[:limit]
	}
	return ids
}

func quickActionSuggestionScore(query, alias, id string) int {
	switch {
	case query == alias || query == id:
		return 100
	case strings.Contains(alias, query):
		return 80 - len(alias) + len(query)
	case strings.Contains(query, alias):
		return 70 - len(query) + len(alias)
	}

	queryParts := strings.Split(query, "_")
	aliasParts := strings.Split(alias, "_")
	shared := 0
	for _, qp := range queryParts {
		if qp == "" {
			continue
		}
		for _, ap := range aliasParts {
			if qp == ap {
				shared++
				break
			}
		}
	}
	if shared == 0 {
		return 0
	}
	return 10 + shared*10
}

func (t *quickActionsTable) lookup(actionID, platform string) (quickActionDefinition, quickActionBinding, bool) {
	t.mu.RLock()
	defer t.mu.RUnlock()
	action, ok := t.document.Actions[actionID]
	if !ok {
		return quickActionDefinition{}, quickActionBinding{}, false
	}
	binding, ok := action.Platforms[platform]
	if !ok {
		return action, quickActionBinding{}, false
	}
	return action, binding, true
}

func (t *quickActionsTable) catalogSummary(platform string) string {
	t.mu.RLock()
	defer t.mu.RUnlock()

	ids := make([]string, 0, len(t.document.Actions))
	for id := range t.document.Actions {
		ids = append(ids, id)
	}
	sort.Strings(ids)

	var builder strings.Builder
	for _, id := range ids {
		action := t.document.Actions[id]
		binding, hasPlatform := action.Platforms[platform]
		if platform != "" && !hasPlatform {
			continue
		}
		status := "missing"
		tool := ""
		if hasPlatform {
			status = strings.TrimSpace(binding.Status)
			if status == "" {
				status = quickActionStatusReserved
			}
			tool = strings.TrimSpace(binding.Tool)
		}
		builder.WriteString(fmt.Sprintf("- %s (%s): %s", id, action.Label, status))
		if tool != "" {
			builder.WriteString(fmt.Sprintf(", tool=%s", tool))
		}
		builder.WriteString("\n")
	}
	return strings.TrimRight(builder.String(), "\n")
}

// loadQuickActionsForConfig picks the highest-priority mapping file available
// and loads it into the global table. Order: configDir override → bundled
// firmware file → embedded defaults (already loaded at init).
func loadQuickActionsForConfig(configDir string, logger *Logger) {
	candidates := make([]string, 0, 2)
	if configDir != "" {
		candidates = append(candidates, filepath.Join(configDir, QuickActionsFileName))
	}
	candidates = append(candidates, BundledQuickActionsPath)

	for _, path := range candidates {
		if _, err := os.Stat(path); err != nil {
			continue
		}
		if err := globalQuickActions.loadFromFile(path); err != nil {
			if logger != nil {
				logger.Warn("quick_actions: failed to load %s: %v (falling back)", path, err)
			}
			continue
		}
		if logger != nil {
			logger.Info("quick_actions: loaded %s", path)
		}
		return
	}
	if logger != nil {
		logger.Info("quick_actions: using embedded defaults")
	}
}

// QuickActionTool executes predefined platform-specific shortcuts and gestures.
type QuickActionTool struct {
	keyboard *KeyboardTapTool
	touch    *TouchGestureTool
}

func (t *QuickActionTool) Name() string { return "quick_action" }

func (t *QuickActionTool) Description() string {
	return strings.TrimSpace(`Execute a predefined platform shortcut or system gesture from quick_actions.json. ` +
		`Prefer this tool first when the requested operation matches a catalog entry. ` +
		`Input JSON examples: {"action":"back","platform":"ios"}, {"action":"copy","platform":"android"}, {"action":"spotlight_search","platform":"android"}. ` +
		`Supported platforms: ios, android, mac. ` +
		`To inspect available actions, pass exactly {"list":true,"platform":"android"}; do not pass {"action":"list"}. ` +
		`If quick_action returns ok=false, status=reserved, the screen did not change, or the outcome is wrong: do not retry the same binding more than once. ` +
		`Try alternative=true when alternatives are listed; otherwise fall back immediately to keyboard_tap, touch_gesture, or mouse tools and continue the task. ` +
		`Do not debate shortcut policy with the user—move on after one failed quick_action attempt unless an alternative binding exists. ` +
		quickActionBehaviorSummary())
}

type quickActionArgs struct {
	Action       string `json:"action"`
	Platform     string `json:"platform"`
	List         bool   `json:"list"`
	Alternative  bool   `json:"alternative"`
	AlternativeN int    `json:"alternative_index"`
}

func (t *QuickActionTool) Call(ctx context.Context, input string) (string, error) {
	var args quickActionArgs
	trimmed := strings.TrimSpace(input)
	if trimmed == "" {
		return t.errorJSON("action or list is required"), nil
	}
	if strings.HasPrefix(trimmed, "{") {
		if err := json.Unmarshal([]byte(trimmed), &args); err != nil {
			return fmt.Sprintf("error: invalid input: %v", err), nil
		}
	} else {
		args.Action = trimmed
	}

	platform, err := normalizeQuickActionPlatform(args.Platform)
	if err != nil {
		return t.errorJSON(err.Error()), nil
	}

	if strings.EqualFold(strings.TrimSpace(args.Action), "list") {
		args.List = true
	}
	if args.List {
		return t.listJSON(platform), nil
	}

	actionID, ok := globalQuickActions.resolveActionID(args.Action)
	if !ok {
		suggestions := globalQuickActions.suggestActionIDs(args.Action, 5)
		message := fmt.Sprintf("unknown action %q; use {\"list\":true,\"platform\":\"%s\"}", args.Action, platform)
		if len(suggestions) > 0 {
			message = fmt.Sprintf("%s; suggested actions: %s", message, strings.Join(suggestions, ", "))
		}
		return t.errorJSON(message), nil
	}

	action, binding, ok := globalQuickActions.lookup(actionID, platform)
	if !ok {
		return t.errorJSON(fmt.Sprintf("action %q is not defined for platform %q", actionID, platform)), nil
	}

	selected := binding
	selectedSource := "primary"
	if args.Alternative {
		idx := args.AlternativeN
		if idx <= 0 {
			idx = 1
		}
		if len(binding.Alternatives) < idx {
			return t.errorJSON(fmt.Sprintf("action %q has no alternative #%d on platform %q", actionID, idx, platform)), nil
		}
		selected = binding.Alternatives[idx-1]
		selectedSource = fmt.Sprintf("alternative_%d", idx)
	}

	status := strings.TrimSpace(selected.Status)
	if status == "" {
		status = quickActionStatusReserved
	}
	if status == quickActionStatusReserved {
		note := strings.TrimSpace(selected.Note)
		if note == "" {
			note = "reserved on this platform"
		}
		return jsonString(map[string]interface{}{
			"ok":       false,
			"status":   quickActionStatusReserved,
			"action":   actionID,
			"label":    action.Label,
			"platform": platform,
			"message":  note,
		}), nil
	}
	if status != quickActionStatusActive {
		return t.errorJSON(fmt.Sprintf("action %q has unsupported status %q on platform %q", actionID, status, platform)), nil
	}

	toolName := strings.TrimSpace(selected.Tool)
	if toolName == "" {
		note := strings.TrimSpace(selected.Note)
		if note == "" {
			note = "no tool binding configured"
		}
		return t.errorJSON(note), nil
	}

	payload, err := json.Marshal(selected.Input)
	if err != nil {
		return t.errorJSON(fmt.Sprintf("invalid action input: %v", err)), nil
	}

	output, err := t.delegate(ctx, toolName, string(payload))
	if err != nil {
		return fmt.Sprintf("error: %v", err), nil
	}
	if strings.HasPrefix(output, "error:") {
		return jsonString(map[string]interface{}{
			"ok":       false,
			"action":   actionID,
			"label":    action.Label,
			"platform": platform,
			"binding":  selectedSource,
			"tool":     toolName,
			"input":    json.RawMessage(payload),
			"output":   output,
		}), nil
	}

	result := map[string]interface{}{
		"ok":       true,
		"action":   actionID,
		"label":    action.Label,
		"platform": platform,
		"binding":  selectedSource,
		"tool":     toolName,
		"input":    json.RawMessage(payload),
		"output":   output,
	}
	if note := strings.TrimSpace(selected.Note); note != "" {
		result["note"] = note
	}
	if len(binding.Alternatives) > 0 {
		alts := make([]map[string]interface{}, 0, len(binding.Alternatives))
		for i, alt := range binding.Alternatives {
			alts = append(alts, map[string]interface{}{
				"index":  i + 1,
				"status": alt.Status,
				"tool":   alt.Tool,
				"note":   alt.Note,
			})
		}
		result["alternatives"] = alts
	}
	return jsonString(result), nil
}

func (t *QuickActionTool) delegate(ctx context.Context, toolName, payload string) (string, error) {
	switch toolName {
	case "keyboard_tap":
		if t.keyboard == nil {
			return "", fmt.Errorf("keyboard_tap is not available")
		}
		return t.keyboard.Call(ctx, payload)
	case "touch_gesture":
		if t.touch == nil {
			return "", fmt.Errorf("touch_gesture is not available")
		}
		return t.touch.Call(ctx, payload)
	default:
		return "", fmt.Errorf("unsupported delegated tool %q", toolName)
	}
}

func (t *QuickActionTool) listJSON(platform string) string {
	type listItem struct {
		ID       string `json:"id"`
		Label    string `json:"label"`
		Category string `json:"category"`
		Platform string `json:"platform,omitempty"`
		Status   string `json:"status"`
		Tool     string `json:"tool,omitempty"`
		Note     string `json:"note,omitempty"`
	}

	globalQuickActions.mu.RLock()
	defer globalQuickActions.mu.RUnlock()

	ids := make([]string, 0, len(globalQuickActions.document.Actions))
	for id := range globalQuickActions.document.Actions {
		ids = append(ids, id)
	}
	sort.Strings(ids)

	items := make([]listItem, 0, len(ids))
	for _, id := range ids {
		action := globalQuickActions.document.Actions[id]
		if platform == "" {
			for _, p := range supportedQuickActionPlatforms {
				binding, ok := action.Platforms[p]
				if !ok {
					continue
				}
				items = append(items, listItem{
					ID:       id,
					Label:    action.Label,
					Category: action.Category,
					Platform: p,
					Status:   bindingStatus(binding),
					Tool:     strings.TrimSpace(binding.Tool),
					Note:     firstNonEmpty(binding.Note, platformNote(id, p, binding)),
				})
			}
			continue
		}
		binding, ok := action.Platforms[platform]
		if !ok {
			continue
		}
		items = append(items, listItem{
			ID:       id,
			Label:    action.Label,
			Category: action.Category,
			Platform: platform,
			Status:   bindingStatus(binding),
			Tool:     strings.TrimSpace(binding.Tool),
			Note:     firstNonEmpty(binding.Note, platformNote(id, platform, binding)),
		})
	}

	return jsonString(map[string]interface{}{
		"ok":       true,
		"platform": platform,
		"actions":  items,
	})
}

func bindingStatus(binding quickActionBinding) string {
	status := strings.TrimSpace(binding.Status)
	if status == "" {
		return quickActionStatusReserved
	}
	return status
}

func platformNote(actionID, platform string, binding quickActionBinding) string {
	if strings.TrimSpace(binding.Tool) != "" {
		return ""
	}
	return fmt.Sprintf("%s on %s has no primary tool binding", actionID, platform)
}

func (t *QuickActionTool) errorJSON(message string) string {
	return jsonString(map[string]interface{}{
		"ok":    false,
		"error": message,
	})
}

func quickActionBehaviorSummary() string {
	return strings.Join([]string{
		"Common actions: back, home, hide_app, quit_app, app_switch, spotlight_search, copy, paste, cut, undo, redo, select_all, find, send, browser_new_tab, browser_close_tab, browser_refresh, browser_address_bar, screenshot_full, screenshot_region.",
		"- Infer platform from screenshot/context and pass platform=ios/android/mac.",
		"- Prefer quick_action before ad-hoc keyboard_tap or touch_gesture when an active catalog entry exists.",
		"- If status=reserved or ok=false: skip quick_action and use direct input tools instead.",
		"- If ok=true but the screenshot shows no expected change: treat as ineffective, try alternative=true once or switch tools.",
		"- Never loop on the same quick_action binding; change tool or strategy after one failed attempt.",
	}, "\n")
}
