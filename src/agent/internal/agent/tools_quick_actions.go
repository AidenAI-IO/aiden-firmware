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
	Status        string                   `json:"status"`
	Tool          string                   `json:"tool"`
	Input         json.RawMessage          `json:"input"`
	Note          string                   `json:"note,omitempty"`
	Alternatives  []quickActionBinding     `json:"alternatives,omitempty"`
}

type quickActionDefinition struct {
	Label     string                            `json:"label"`
	Aliases   []string                          `json:"aliases,omitempty"`
	Category  string                            `json:"category"`
	Platforms map[string]quickActionBinding     `json:"platforms"`
}

type quickActionsDocument struct {
	Version int                              `json:"version"`
	Actions map[string]quickActionDefinition `json:"actions"`
}

type quickActionsTable struct {
	mu       sync.RWMutex
	document quickActionsDocument
	aliasMap map[string]string
	source   string
}

var globalQuickActions = newQuickActionsTable()

func newQuickActionsTable() *quickActionsTable {
	t := &quickActionsTable{}
	if err := t.loadFromBytes(defaultQuickActionsJSON, "embedded"); err != nil {
		t.document = quickActionsDocument{Actions: map[string]quickActionDefinition{}}
		t.aliasMap = map[string]string{}
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
	for id, action := range doc.Actions {
		aliasMap[normalizeQuickActionKey(id)] = id
		aliasMap[normalizeQuickActionKey(action.Label)] = id
		for _, alias := range action.Aliases {
			aliasMap[normalizeQuickActionKey(alias)] = id
		}
	}
	t.mu.Lock()
	t.document = doc
	t.aliasMap = aliasMap
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
	return id, ok
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
		`Prefer this tool over ad-hoc keyboard_tap or touch_gesture when the requested operation matches a catalog entry. ` +
		`Input JSON examples: {"action":"back","platform":"ios"}, {"action":"copy","platform":"android"}, {"list":true,"platform":"ios"}. ` +
		`Supported platforms: ios, android, mac. ` +
		`Use list=true to inspect available actions and their status (active or reserved). ` +
		`If an action is reserved on a platform, do not improvise a different shortcut; report the blocker or ask the user. ` +
		`After a failed quick_action, you may retry with alternative=true to use the configured fallback binding. ` +
		`Common actions: back, home, app_switch, notification_center, control_center, spotlight_search, app_drawer, copy, paste, cut, undo, redo, select_all, find, browser_new_tab, browser_close_tab, browser_refresh, browser_address_bar.`)
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
	if err != nil && !args.List {
		return t.errorJSON(err.Error()), nil
	}

	if args.List {
		return t.listJSON(platform), nil
	}

	actionID, ok := globalQuickActions.resolveActionID(args.Action)
	if !ok {
		return t.errorJSON(fmt.Sprintf("unknown action %q; use {\"list\":true,\"platform\":\"%s\"}", args.Action, platform)), nil
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
		"- For common navigation, text editing, browser, and system-panel operations, prefer quick_action over ad-hoc keyboard_tap or touch_gesture.",
		"- Infer the target platform from screenshot/context and pass platform=ios/android/mac.",
		"- Use {\"list\":true,\"platform\":\"...\"} to check whether an action is active; if status=reserved, do not improvise a different shortcut.",
		"- If quick_action fails and alternatives are returned, retry with alternative=true or alternative_index.",
		"- Maintain bindings in quick_actions.json; after verification, change status from reserved to active.",
	}, "\n")
}
