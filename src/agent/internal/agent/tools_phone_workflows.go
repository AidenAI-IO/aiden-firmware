package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// PreparePhoneMessageTool performs the Aiden-foreground half of a cross-app
// messaging workflow before the target app is opened.
type PreparePhoneMessageTool struct {
	bridge   *PhoneBridge
	restorer *PhoneBridgeRestorer
}

// PreparePhoneAppWorkflowTool batches reorderable PhoneBridge app-side work
// before opening a target app. The clipboard is only one optional carrier in
// this workflow; the important boundary is target-app navigation.
type PreparePhoneAppWorkflowTool struct {
	bridge   *PhoneBridge
	restorer *PhoneBridgeRestorer
}

func NewPreparePhoneMessageTool(bridge *PhoneBridge, restorer *PhoneBridgeRestorer) *PreparePhoneMessageTool {
	return &PreparePhoneMessageTool{bridge: bridge, restorer: restorer}
}

func NewPreparePhoneAppWorkflowTool(bridge *PhoneBridge, restorer *PhoneBridgeRestorer) *PreparePhoneAppWorkflowTool {
	return &PreparePhoneAppWorkflowTool{bridge: bridge, restorer: restorer}
}

func (t *PreparePhoneMessageTool) Name() string { return "prepare_phone_message" }

func (t *PreparePhoneAppWorkflowTool) Name() string { return "prepare_phone_app_workflow" }

func (t *PreparePhoneMessageTool) Description() string {
	return `Prepare a cross-app phone message while Aiden is foreground, before opening the target app. ` +
		`This is the first-class workflow for tasks like "query a Contacts phone number, then ask a WeChat friend whether that number is still used". ` +
		`It batches expensive Aiden app-side work in one foreground phase: optional contacts query, message rendering, clipboard write, and optional target-app open as the final boundary. ` +
		`Use this instead of separate contacts + clipboard + open_app calls when message text depends on phone-side data. ` +
		`After it returns ok=true, navigate/search inside the target app if needed, then call enter_text_in_field with the returned target_text and send_after_commit=true.`
}

func (t *PreparePhoneAppWorkflowTool) Description() string {
	return `Prepare a target-app phone workflow while Aiden is foreground, before opening the target app. ` +
		`This is the first-class workflow for stabilizing open_app with other PhoneBridge app-side tools: run reorderable direct app operations first, optionally render/write final clipboard text, then optionally open the target app as the final boundary. ` +
		`Use it for cross-app tasks where the target app message depends on Contacts, Calendar, clipboard read, notification scheduling, or future direct PhoneBridge tools. ` +
		`Do not use it for UI-only app reading; only structured direct PhoneBridge operations can run before the target app. ` +
		`After it returns ok=true, navigate/search inside the target app if needed, then call enter_text_in_field with the returned target_text and send_after_commit=true when sending.`
}

func (t *PreparePhoneMessageTool) ArgsSchema() map[string]any {
	return objectArgsSchema(map[string]any{
		"contact_query":    stringArgSchema("Optional Contacts query whose phone numbers or contact name are needed in the message."),
		"message_template": stringArgSchema("Message template. Supports {{phone_numbers}}, {{phone_number}}, {{contact_name}}, {{contact_names}}, {{contact_query}}, and {{target_label}} placeholders."),
		"message_text":     stringArgSchema("Exact final message text when no template rendering is needed. If contact_query returns phone numbers, this text must include every returned phone number."),
		"artifact_id":      stringArgSchema("Optional committed-plan target_text artifact id to mark prepared after clipboard write."),
		"target_app":       stringArgSchema("Target app to open after clipboard preparation, for example WeChat or 微信."),
		"target_label":     stringArgSchema("Target chat/contact label inside the target app, for example the WeChat friend to message."),
		"open_target_app":  boolArgSchema("Open target_app after preparing clipboard. This should be the final Aiden-side action before target-app UI automation."),
		"limit":            minIntegerArgSchema("Maximum contacts returned by contact_query. Default 10.", 1),
	}, "target_app")
}

func (t *PreparePhoneAppWorkflowTool) ArgsSchema() map[string]any {
	return objectArgsSchema(map[string]any{
		"app_side_actions": map[string]any{
			"type":        "array",
			"description": "Ordered direct PhoneBridge operations to run while Aiden is foreground, before target-app navigation. Do not include open_app/search_launch_app here.",
			"items": objectArgsSchema(map[string]any{
				"id":      stringArgSchema("Stable id for this action's result, used by target_text_template placeholders such as {{calendar_lookup.event_notes}}."),
				"tool":    stringEnumArgSchema("Direct PhoneBridge app-side tool.", "contacts", "calendar", "clipboard", "notification"),
				"action":  stringArgSchema("Tool action, for example query, read, write, create, update, delete, or send."),
				"payload": map[string]any{"type": "object", "additionalProperties": true, "description": "Tool-specific JSON payload, matching the underlying direct tool fields without the action key."},
			}, "tool", "action"),
		},
		"target_text_template": stringArgSchema("Optional final text template for the target app. Supports aggregate placeholders like {{phone_numbers}}, {{event_notes}}, {{clipboard_text}}, {{target_label}}, and per-action placeholders like {{contact_lookup.phone_numbers}} or {{calendar_lookup.event_notes}}."),
		"target_text":          stringArgSchema("Exact final text when no template rendering is needed."),
		"artifact_id":          stringArgSchema("Optional committed-plan target_text artifact id to mark prepared after clipboard write."),
		"prepare_clipboard":    boolArgSchema("Write target_text/target_text_template result to the phone clipboard before opening target_app. Defaults to true when target text is provided."),
		"target_app":           stringArgSchema("Target app to open after all app-side preparation, for example WeChat or 微信."),
		"target_label":         stringArgSchema("Target chat/contact label inside the target app."),
		"open_target_app":      boolArgSchema("Open target_app after all app-side actions and optional clipboard preparation. This is the final phase boundary."),
	}, "target_app")
}

type preparePhoneMessageArgs struct {
	ContactQuery    string `json:"contact_query"`
	MessageTemplate string `json:"message_template"`
	MessageText     string `json:"message_text"`
	ArtifactID      string `json:"artifact_id"`
	TargetApp       string `json:"target_app"`
	TargetLabel     string `json:"target_label"`
	OpenTargetApp   bool   `json:"open_target_app"`
	Limit           int    `json:"limit"`
}

type phoneMessageContact struct {
	ContactID    string   `json:"contact_id,omitempty"`
	Name         string   `json:"name,omitempty"`
	PhoneNumbers []string `json:"phone_numbers,omitempty"`
	Emails       []string `json:"emails,omitempty"`
}

type phoneMessageSourceValues struct {
	ContactNames []string `json:"contact_names,omitempty"`
	PhoneNumbers []string `json:"phone_numbers,omitempty"`
}

type preparePhoneAppWorkflowArgs struct {
	AppSideActions     []phoneWorkflowAction `json:"app_side_actions"`
	TargetText         string                `json:"target_text"`
	TargetTextTemplate string                `json:"target_text_template"`
	ArtifactID         string                `json:"artifact_id"`
	PrepareClipboard   *bool                 `json:"prepare_clipboard"`
	TargetApp          string                `json:"target_app"`
	TargetLabel        string                `json:"target_label"`
	OpenTargetApp      bool                  `json:"open_target_app"`
}

type phoneWorkflowAction struct {
	ID      string          `json:"id"`
	Tool    string          `json:"tool"`
	Action  string          `json:"action"`
	Payload json.RawMessage `json:"payload"`
}

type phoneWorkflowActionResult struct {
	ID          string `json:"id"`
	Tool        string `json:"tool"`
	Action      string `json:"action"`
	CommandType string `json:"command_type"`
	Method      string `json:"method,omitempty"`
	Data        any    `json:"data,omitempty"`
}

type phoneWorkflowSourceValues struct {
	ContactNames   []string `json:"contact_names,omitempty"`
	PhoneNumbers   []string `json:"phone_numbers,omitempty"`
	EventTitles    []string `json:"event_titles,omitempty"`
	EventNotes     []string `json:"event_notes,omitempty"`
	EventLocations []string `json:"event_locations,omitempty"`
	ClipboardTexts []string `json:"clipboard_texts,omitempty"`
}

func (t *PreparePhoneMessageTool) Call(ctx context.Context, input string) (string, error) {
	var args preparePhoneMessageArgs
	if err := json.Unmarshal([]byte(strings.TrimSpace(input)), &args); err != nil {
		te := NewToolError(CodeInvalidArguments, fmt.Sprintf("invalid input: %v. Expected JSON like {\"contact_query\":\"张三\",\"message_template\":\"张三的手机号是{{phone_numbers}}，还在用吗？\",\"target_app\":\"微信\",\"target_label\":\"李四\",\"open_target_app\":true}", err))
		SetToolError(ctx, te)
		return toolErrorString(te), nil
	}
	args.ContactQuery = strings.TrimSpace(args.ContactQuery)
	args.MessageTemplate = strings.TrimSpace(args.MessageTemplate)
	args.MessageText = strings.TrimSpace(args.MessageText)
	args.TargetApp = strings.TrimSpace(args.TargetApp)
	args.TargetLabel = strings.TrimSpace(args.TargetLabel)
	if args.Limit <= 0 {
		args.Limit = 10
	}
	if args.TargetApp == "" {
		te := NewToolError(CodeInvalidArguments, "target_app is required")
		SetToolError(ctx, te)
		return toolErrorString(te), nil
	}
	if args.MessageText == "" && args.MessageTemplate == "" {
		te := NewToolError(CodeInvalidArguments, "message_template or message_text is required")
		SetToolError(ctx, te)
		return toolErrorString(te), nil
	}

	var contacts []phoneMessageContact
	var source phoneMessageSourceValues
	restored := false
	steps := []string{"workflow: start Aiden-foreground message preparation"}
	if args.ContactQuery != "" {
		var queryRestored bool
		var err error
		contacts, queryRestored, err = t.queryContacts(ctx, args.ContactQuery, args.Limit)
		if err != nil {
			return t.toolErrorOutput(ctx, err), nil
		}
		restored = restored || queryRestored
		source = phoneMessageSourceValuesFromContacts(contacts)
		steps = append(steps, "workflow: queried contacts before target-app navigation")
		if len(contacts) == 0 {
			te := NewToolErrorWithDetails(CodeToolExecutionFailed, "contacts query returned no contacts; do not open the target app until source data is available", map[string]any{
				"contact_query": args.ContactQuery,
			})
			SetToolError(ctx, te)
			return toolErrorString(te), nil
		}
	}

	targetText, err := renderPhoneMessageText(args, source)
	if err != nil {
		te := NewToolError(CodeInvalidArguments, err.Error())
		SetToolError(ctx, te)
		return toolErrorString(te), nil
	}
	if missing := missingPhoneMessageSourcePhoneNumbers(source, targetText); len(missing) > 0 {
		te := NewToolErrorWithDetails(CodeInvalidArguments, "message text omits phone number(s) returned by Contacts; include the source value before opening the target app", map[string]any{
			"missing_phone_numbers": missing,
			"contact_query":         args.ContactQuery,
		})
		SetToolError(ctx, te)
		return toolErrorString(te), nil
	}

	clipboardRestored, err := t.writeClipboard(ctx, targetText)
	if err != nil {
		return t.toolErrorOutput(ctx, err), nil
	}
	restored = restored || clipboardRestored
	steps = append(steps, "workflow: wrote final target text to clipboard before target-app navigation")

	openedTarget := false
	openMechanism := ""
	if args.OpenTargetApp {
		if args.TargetApp == "" {
			te := NewToolError(CodeInvalidArguments, "open_target_app requires target_app")
			SetToolError(ctx, te)
			return toolErrorString(te), nil
		}
		var openRestored bool
		var err error
		openMechanism, openRestored, err = t.openTargetApp(ctx, args.TargetApp)
		if err != nil {
			return t.toolErrorOutput(ctx, err), nil
		}
		restored = restored || openRestored
		openedTarget = true
		steps = append(steps, "workflow: opened target app after clipboard preparation")
	}

	result := map[string]any{
		"ok":                 true,
		"workflow":           "prepare_phone_message",
		"target_app":         args.TargetApp,
		"target_label":       args.TargetLabel,
		"target_text":        targetText,
		"clipboard_prepared": true,
		"opened_target_app":  openedTarget,
		"contacts":           contacts,
		"source_values":      source,
		"steps":              steps,
		"next_tool_hint": map[string]any{
			"tool":              "enter_text_in_field",
			"text":              targetText,
			"send_after_commit": true,
			"note":              "After the correct target chat field is visible, focus it and use this text. The prepared clipboard can be pasted without reopening Aiden.",
		},
	}
	if restored {
		result["restored_from_return_entry"] = true
	}
	if openMechanism != "" {
		result["open_mechanism"] = openMechanism
	}
	return jsonString(result), nil
}

func (t *PreparePhoneAppWorkflowTool) Call(ctx context.Context, input string) (string, error) {
	var args preparePhoneAppWorkflowArgs
	if err := json.Unmarshal([]byte(strings.TrimSpace(input)), &args); err != nil {
		te := NewToolError(CodeInvalidArguments, fmt.Sprintf("invalid input: %v. Expected JSON like {\"app_side_actions\":[{\"id\":\"calendar_lookup\",\"tool\":\"calendar\",\"action\":\"query\",\"payload\":{\"from\":\"2026-06-30T00:00:00+08:00\",\"to\":\"2026-07-01T00:00:00+08:00\"}}],\"target_text_template\":\"今天日历备注里写的是：{{calendar_lookup.event_notes}}，这件事进展如何？\",\"target_app\":\"微信\",\"target_label\":\"张三\",\"open_target_app\":true}", err))
		SetToolError(ctx, te)
		return toolErrorString(te), nil
	}
	args.TargetApp = strings.TrimSpace(args.TargetApp)
	args.TargetLabel = strings.TrimSpace(args.TargetLabel)
	args.TargetText = strings.TrimSpace(args.TargetText)
	args.TargetTextTemplate = strings.TrimSpace(args.TargetTextTemplate)
	if args.TargetApp == "" {
		te := NewToolError(CodeInvalidArguments, "target_app is required")
		SetToolError(ctx, te)
		return toolErrorString(te), nil
	}
	if len(args.AppSideActions) == 0 && args.TargetText == "" && args.TargetTextTemplate == "" {
		te := NewToolError(CodeInvalidArguments, "app_side_actions or target_text/target_text_template is required; use open_app directly for launch-only requests")
		SetToolError(ctx, te)
		return toolErrorString(te), nil
	}

	steps := []string{"workflow: start Aiden-foreground app-side preparation"}
	templateValues := map[string]string{
		"target_app":   args.TargetApp,
		"target_label": args.TargetLabel,
	}
	seenIDs := map[string]struct{}{}
	sourceValues := phoneWorkflowSourceValues{}
	actionResults := make([]phoneWorkflowActionResult, 0, len(args.AppSideActions))
	restored := false
	for i, action := range args.AppSideActions {
		action.Tool = strings.ToLower(strings.TrimSpace(action.Tool))
		action.Action = strings.ToLower(strings.TrimSpace(action.Action))
		action.ID = strings.TrimSpace(action.ID)
		if action.ID == "" {
			action.ID = fmt.Sprintf("%s_%d", action.Tool, i+1)
		}
		if _, exists := seenIDs[action.ID]; exists {
			te := NewToolErrorWithDetails(CodeInvalidArguments, "app_side_actions ids must be unique", map[string]any{"duplicate_id": action.ID})
			SetToolError(ctx, te)
			return toolErrorString(te), nil
		}
		seenIDs[action.ID] = struct{}{}
		commandType, err := phoneWorkflowBridgeCommandType(action)
		if err != nil {
			te := NewToolErrorWithDetails(CodeInvalidArguments, err.Error(), map[string]any{
				"action_id": action.ID,
				"tool":      action.Tool,
				"action":    action.Action,
			})
			SetToolError(ctx, te)
			return toolErrorString(te), nil
		}
		payload := action.Payload
		if len(payload) == 0 {
			payload = json.RawMessage(`{}`)
		}
		resp, actionRestored, err := sendForegroundBridgeCommand(ctx, t.bridge, t.restorer, BridgeCommand{
			ID:        nextBridgeCmdID("workflow_" + commandType),
			Type:      commandType,
			Payload:   payload,
			TimeoutMs: phoneWorkflowCommandTimeoutMs(commandType),
		})
		if err != nil {
			return t.toolErrorOutput(ctx, phoneWorkflowBridgePreconditionError(t.bridge, err)), nil
		}
		restored = restored || actionRestored
		if resp.Error != nil {
			return t.toolErrorOutput(ctx, resp.Error), nil
		}
		data, err := phoneWorkflowDecodeData(resp.Data)
		if err != nil {
			te := NewToolError(CodeToolExecutionFailed, fmt.Sprintf("decode %s data: %v", commandType, err))
			SetToolError(ctx, te)
			return toolErrorString(te), nil
		}
		actionResults = append(actionResults, phoneWorkflowActionResult{
			ID:          action.ID,
			Tool:        action.Tool,
			Action:      action.Action,
			CommandType: commandType,
			Method:      resp.Method,
			Data:        data,
		})
		extracted := phoneWorkflowExtractSourceValues(commandType, resp.Data)
		sourceValues.merge(extracted.sourceValues)
		templateValues[action.ID] = extracted.summary
		for key, value := range extracted.placeholders {
			templateValues[action.ID+"."+key] = value
		}
		steps = append(steps, fmt.Sprintf("workflow: ran %s/%s before target-app navigation", action.Tool, action.Action))
	}
	phoneWorkflowAddAggregateTemplateValues(templateValues, sourceValues)

	targetText := args.TargetText
	if targetText == "" && args.TargetTextTemplate != "" {
		var err error
		targetText, err = renderPhoneWorkflowTemplate(args.TargetTextTemplate, templateValues)
		if err != nil {
			te := NewToolError(CodeInvalidArguments, err.Error())
			SetToolError(ctx, te)
			return toolErrorString(te), nil
		}
	}

	prepareClipboard := targetText != ""
	if args.PrepareClipboard != nil {
		prepareClipboard = *args.PrepareClipboard
	}
	if prepareClipboard && targetText == "" {
		te := NewToolError(CodeInvalidArguments, "prepare_clipboard requires target_text or target_text_template")
		SetToolError(ctx, te)
		return toolErrorString(te), nil
	}
	if prepareClipboard {
		clipboardRestored, err := t.writeClipboard(ctx, targetText)
		if err != nil {
			return t.toolErrorOutput(ctx, err), nil
		}
		restored = restored || clipboardRestored
		steps = append(steps, "workflow: wrote final target text to clipboard before target-app navigation")
	}

	openedTarget := false
	openMechanism := ""
	if args.OpenTargetApp {
		var openRestored bool
		var err error
		openMechanism, openRestored, err = t.openTargetApp(ctx, args.TargetApp)
		if err != nil {
			return t.toolErrorOutput(ctx, err), nil
		}
		restored = restored || openRestored
		openedTarget = true
		steps = append(steps, "workflow: opened target app after all app-side preparation")
	}

	result := map[string]any{
		"ok":                 true,
		"workflow":           "prepare_phone_app_workflow",
		"target_app":         args.TargetApp,
		"target_label":       args.TargetLabel,
		"target_text":        targetText,
		"clipboard_prepared": prepareClipboard,
		"opened_target_app":  openedTarget,
		"actions":            actionResults,
		"source_values":      sourceValues,
		"steps":              steps,
	}
	if targetText != "" {
		result["next_tool_hint"] = map[string]any{
			"tool":              "enter_text_in_field",
			"text":              targetText,
			"send_after_commit": true,
			"note":              "After the correct target chat field is visible, focus it and use this text. The prepared clipboard can be pasted without reopening Aiden.",
		}
	}
	if restored {
		result["restored_from_return_entry"] = true
	}
	if openMechanism != "" {
		result["open_mechanism"] = openMechanism
	}
	return jsonString(result), nil
}

func (t *PreparePhoneMessageTool) queryContacts(ctx context.Context, query string, limit int) ([]phoneMessageContact, bool, error) {
	payload, _ := json.Marshal(map[string]any{
		"query": query,
		"limit": limit,
	})
	resp, restored, err := sendForegroundBridgeCommand(ctx, t.bridge, t.restorer, BridgeCommand{
		ID:        nextBridgeCmdID("workflow_contacts_query"),
		Type:      "contacts_query",
		Payload:   payload,
		TimeoutMs: 8000,
	})
	if err != nil {
		return nil, restored, phoneWorkflowBridgePreconditionError(t.bridge, err)
	}
	if resp.Error != nil {
		return nil, restored, resp.Error
	}
	var data struct {
		Contacts []phoneMessageContact `json:"contacts"`
	}
	if len(resp.Data) > 0 {
		if err := json.Unmarshal(resp.Data, &data); err != nil {
			return nil, restored, NewToolError(CodeToolExecutionFailed, fmt.Sprintf("decode contacts data: %v", err))
		}
	}
	if data.Contacts == nil {
		data.Contacts = []phoneMessageContact{}
	}
	return data.Contacts, restored, nil
}

func (t *PreparePhoneMessageTool) writeClipboard(ctx context.Context, text string) (bool, error) {
	payload, _ := json.Marshal(map[string]string{"text": text})
	resp, restored, err := sendForegroundBridgeCommand(ctx, t.bridge, t.restorer, BridgeCommand{
		ID:        nextBridgeCmdID("workflow_clip_write"),
		Type:      "clipboard_write",
		Payload:   payload,
		TimeoutMs: 5000,
	})
	if err != nil {
		return restored, phoneWorkflowBridgePreconditionError(t.bridge, err)
	}
	if resp.Error != nil {
		return restored, resp.Error
	}
	return restored, nil
}

func (t *PreparePhoneMessageTool) openTargetApp(ctx context.Context, app string) (string, bool, error) {
	resp, restored, err := sendForegroundBridgeCommand(ctx, t.bridge, t.restorer, BridgeCommand{
		ID:        nextBridgeCmdID("workflow_open_app"),
		Type:      "open_app",
		App:       strings.TrimSpace(app),
		TimeoutMs: 10000,
	})
	if err != nil {
		return "", restored, phoneWorkflowBridgePreconditionError(t.bridge, err)
	}
	if resp.Error != nil {
		return "", restored, resp.Error
	}
	return openAppResultMechanism(openAppArgs{App: app}, resp.Method), restored, nil
}

func (t *PreparePhoneAppWorkflowTool) writeClipboard(ctx context.Context, text string) (bool, error) {
	payload, _ := json.Marshal(map[string]string{"text": text})
	resp, restored, err := sendForegroundBridgeCommand(ctx, t.bridge, t.restorer, BridgeCommand{
		ID:        nextBridgeCmdID("workflow_clip_write"),
		Type:      "clipboard_write",
		Payload:   payload,
		TimeoutMs: 5000,
	})
	if err != nil {
		return restored, phoneWorkflowBridgePreconditionError(t.bridge, err)
	}
	if resp.Error != nil {
		return restored, resp.Error
	}
	return restored, nil
}

func (t *PreparePhoneAppWorkflowTool) openTargetApp(ctx context.Context, app string) (string, bool, error) {
	resp, restored, err := sendForegroundBridgeCommand(ctx, t.bridge, t.restorer, BridgeCommand{
		ID:        nextBridgeCmdID("workflow_open_app"),
		Type:      "open_app",
		App:       strings.TrimSpace(app),
		TimeoutMs: 10000,
	})
	if err != nil {
		return "", restored, phoneWorkflowBridgePreconditionError(t.bridge, err)
	}
	if resp.Error != nil {
		return "", restored, resp.Error
	}
	return openAppResultMechanism(openAppArgs{App: app}, resp.Method), restored, nil
}

func (t *PreparePhoneMessageTool) toolErrorOutput(ctx context.Context, err error) string {
	if err == nil {
		return ""
	}
	if te, ok := err.(*ToolError); ok {
		SetToolError(ctx, te)
		return toolErrorString(te)
	}
	te := NewToolError(CodeToolExecutionFailed, err.Error())
	SetToolError(ctx, te)
	return toolErrorString(te)
}

func (t *PreparePhoneAppWorkflowTool) toolErrorOutput(ctx context.Context, err error) string {
	if err == nil {
		return ""
	}
	if te, ok := err.(*ToolError); ok {
		SetToolError(ctx, te)
		return toolErrorString(te)
	}
	te := NewToolError(CodeToolExecutionFailed, err.Error())
	SetToolError(ctx, te)
	return toolErrorString(te)
}

func phoneWorkflowBridgePreconditionError(bridge *PhoneBridge, err error) *ToolError {
	if err == nil {
		return nil
	}
	status := PhoneBridgeStatus{}
	if bridge != nil {
		status = bridge.Status()
	}
	return NewToolErrorWithDetails(CodeBridgeNotConnected,
		fmt.Sprintf("%v. Keep Aiden in foreground while running prepare_phone_app_workflow/prepare_phone_message; after target_app is opened, do not use PhoneBridge app-side preparation tools again.", err),
		map[string]any{"fallback": phoneBridgeRecoveryGuidance(status)})
}

func phoneMessageSourceValuesFromContacts(contacts []phoneMessageContact) phoneMessageSourceValues {
	var source phoneMessageSourceValues
	for _, contact := range contacts {
		source.ContactNames = uniqueNonEmpty(append(source.ContactNames, contact.Name))
		source.PhoneNumbers = uniqueNonEmpty(append(source.PhoneNumbers, contact.PhoneNumbers...))
	}
	return source
}

func renderPhoneMessageText(args preparePhoneMessageArgs, source phoneMessageSourceValues) (string, error) {
	if text := strings.TrimSpace(args.MessageText); text != "" {
		return text, nil
	}
	template := strings.TrimSpace(args.MessageTemplate)
	if template == "" {
		return "", fmt.Errorf("message_template or message_text is required")
	}
	phoneNumber := ""
	if len(source.PhoneNumbers) > 0 {
		phoneNumber = source.PhoneNumbers[0]
	}
	contactName := ""
	if len(source.ContactNames) > 0 {
		contactName = source.ContactNames[0]
	}
	replacer := strings.NewReplacer(
		"{{phone_numbers}}", strings.Join(source.PhoneNumbers, "、"),
		"{{phone_number}}", phoneNumber,
		"{{contact_names}}", strings.Join(source.ContactNames, "、"),
		"{{contact_name}}", contactName,
		"{{contact_query}}", strings.TrimSpace(args.ContactQuery),
		"{{target_label}}", strings.TrimSpace(args.TargetLabel),
	)
	text := strings.TrimSpace(replacer.Replace(template))
	if text == "" {
		return "", fmt.Errorf("rendered message text is empty")
	}
	if templateHasPlaceholder(text) {
		return "", fmt.Errorf("message_template contains unsupported placeholder(s): %s", strings.Join(templatePlaceholderRE.FindAllString(text, -1), ", "))
	}
	return text, nil
}

func missingPhoneMessageSourcePhoneNumbers(source phoneMessageSourceValues, text string) []string {
	if len(source.PhoneNumbers) == 0 {
		return nil
	}
	var missing []string
	for _, phone := range uniqueNonEmpty(source.PhoneNumbers) {
		if !artifactTextContainsValue(text, phone) {
			missing = append(missing, phone)
		}
	}
	return missing
}

func phoneWorkflowBridgeCommandType(action phoneWorkflowAction) (string, error) {
	if strings.EqualFold(action.Tool, "open_app") || strings.EqualFold(action.Tool, "search_launch_app") {
		return "", fmt.Errorf("open_app/search_launch_app cannot be an app_side_action; target-app launch is the final workflow boundary")
	}
	allowed := map[string]map[string]string{
		"contacts": {
			"query":  "contacts_query",
			"create": "contacts_create",
			"update": "contacts_update",
		},
		"calendar": {
			"query":  "calendar_query",
			"create": "calendar_create",
			"delete": "calendar_delete",
		},
		"clipboard": {
			"read":  "clipboard_read",
			"write": "clipboard_write",
		},
		"notification": {
			"send": "notification_send",
		},
	}
	actions, ok := allowed[action.Tool]
	if !ok {
		return "", fmt.Errorf("unsupported app_side_action tool %q; use only structured direct PhoneBridge tools before target-app navigation", action.Tool)
	}
	if commandType, ok := actions[action.Action]; ok {
		return commandType, nil
	}
	return "", fmt.Errorf("unsupported app_side_action %s/%s; use only structured direct PhoneBridge tool/action pairs before target-app navigation", action.Tool, action.Action)
}

func phoneWorkflowCommandTimeoutMs(commandType string) int {
	switch commandType {
	case "open_app":
		return 10000
	case "clipboard_read", "clipboard_write":
		return 5000
	default:
		return 8000
	}
}

func phoneWorkflowClipboardWriteText(payload json.RawMessage) string {
	var data struct {
		Text string `json:"text"`
	}
	_ = json.Unmarshal(payload, &data)
	return strings.TrimSpace(data.Text)
}

func phoneWorkflowDecodeData(raw json.RawMessage) (any, error) {
	if len(raw) == 0 {
		return map[string]any{}, nil
	}
	var data any
	if err := json.Unmarshal(raw, &data); err != nil {
		return nil, err
	}
	if data == nil {
		return map[string]any{}, nil
	}
	return data, nil
}

type phoneWorkflowExtractedSourceValues struct {
	summary      string
	placeholders map[string]string
	sourceValues phoneWorkflowSourceValues
}

type phoneWorkflowCalendarEvent struct {
	EventID  string `json:"event_id,omitempty"`
	Title    string `json:"title,omitempty"`
	StartAt  string `json:"start_at,omitempty"`
	EndAt    string `json:"end_at,omitempty"`
	Location string `json:"location,omitempty"`
	Notes    string `json:"notes,omitempty"`
	AllDay   bool   `json:"all_day,omitempty"`
}

func phoneWorkflowExtractSourceValues(commandType string, raw json.RawMessage) phoneWorkflowExtractedSourceValues {
	result := phoneWorkflowExtractedSourceValues{placeholders: map[string]string{}}
	switch commandType {
	case "contacts_query":
		var data struct {
			Contacts []phoneMessageContact `json:"contacts"`
		}
		_ = json.Unmarshal(raw, &data)
		source := phoneMessageSourceValuesFromContacts(data.Contacts)
		result.sourceValues.ContactNames = source.ContactNames
		result.sourceValues.PhoneNumbers = source.PhoneNumbers
		result.placeholders["contact_names"] = strings.Join(source.ContactNames, "、")
		result.placeholders["contact_name"] = phoneWorkflowFirstNonEmpty(source.ContactNames)
		result.placeholders["phone_numbers"] = strings.Join(source.PhoneNumbers, "、")
		result.placeholders["phone_number"] = phoneWorkflowFirstNonEmpty(source.PhoneNumbers)
		result.placeholders["contacts"] = phoneWorkflowContactsSummary(data.Contacts)
		result.summary = result.placeholders["contacts"]
	case "calendar_query":
		var data struct {
			Events []phoneWorkflowCalendarEvent `json:"events"`
		}
		_ = json.Unmarshal(raw, &data)
		result.sourceValues.EventTitles = phoneWorkflowEventTitles(data.Events)
		result.sourceValues.EventNotes = phoneWorkflowEventNotes(data.Events)
		result.sourceValues.EventLocations = phoneWorkflowEventLocations(data.Events)
		result.placeholders["event_titles"] = strings.Join(result.sourceValues.EventTitles, "、")
		result.placeholders["event_title"] = phoneWorkflowFirstNonEmpty(result.sourceValues.EventTitles)
		result.placeholders["event_notes"] = strings.Join(result.sourceValues.EventNotes, "、")
		result.placeholders["event_note"] = phoneWorkflowFirstNonEmpty(result.sourceValues.EventNotes)
		result.placeholders["event_locations"] = strings.Join(result.sourceValues.EventLocations, "、")
		result.placeholders["event_location"] = phoneWorkflowFirstNonEmpty(result.sourceValues.EventLocations)
		result.placeholders["events"] = phoneWorkflowEventsSummary(data.Events)
		result.summary = result.placeholders["events"]
	case "clipboard_read":
		var data struct {
			Text string `json:"text"`
		}
		_ = json.Unmarshal(raw, &data)
		text := strings.TrimSpace(data.Text)
		if text != "" {
			result.sourceValues.ClipboardTexts = []string{text}
		}
		result.placeholders["text"] = text
		result.placeholders["clipboard_text"] = text
		result.summary = text
	default:
		if data, err := phoneWorkflowDecodeData(raw); err == nil {
			result.summary = phoneWorkflowCompactDataText(data)
		}
	}
	return result
}

func (v *phoneWorkflowSourceValues) merge(other phoneWorkflowSourceValues) {
	v.ContactNames = uniqueNonEmpty(append(v.ContactNames, other.ContactNames...))
	v.PhoneNumbers = uniqueNonEmpty(append(v.PhoneNumbers, other.PhoneNumbers...))
	v.EventTitles = uniqueNonEmpty(append(v.EventTitles, other.EventTitles...))
	v.EventNotes = uniqueNonEmpty(append(v.EventNotes, other.EventNotes...))
	v.EventLocations = uniqueNonEmpty(append(v.EventLocations, other.EventLocations...))
	v.ClipboardTexts = uniqueNonEmpty(append(v.ClipboardTexts, other.ClipboardTexts...))
}

func phoneWorkflowAddAggregateTemplateValues(values map[string]string, source phoneWorkflowSourceValues) {
	values["contact_names"] = strings.Join(source.ContactNames, "、")
	values["contact_name"] = phoneWorkflowFirstNonEmpty(source.ContactNames)
	values["phone_numbers"] = strings.Join(source.PhoneNumbers, "、")
	values["phone_number"] = phoneWorkflowFirstNonEmpty(source.PhoneNumbers)
	values["event_titles"] = strings.Join(source.EventTitles, "、")
	values["event_title"] = phoneWorkflowFirstNonEmpty(source.EventTitles)
	values["event_notes"] = strings.Join(source.EventNotes, "、")
	values["event_note"] = phoneWorkflowFirstNonEmpty(source.EventNotes)
	values["event_locations"] = strings.Join(source.EventLocations, "、")
	values["event_location"] = phoneWorkflowFirstNonEmpty(source.EventLocations)
	values["clipboard_text"] = strings.Join(source.ClipboardTexts, "、")
}

func renderPhoneWorkflowTemplate(template string, values map[string]string) (string, error) {
	template = strings.TrimSpace(template)
	if template == "" {
		return "", fmt.Errorf("target_text_template is empty")
	}
	missing := []string{}
	rendered := templatePlaceholderRE.ReplaceAllStringFunc(template, func(match string) string {
		key := strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(match, "{{"), "}}"))
		value, ok := values[key]
		if !ok {
			missing = append(missing, match)
			return match
		}
		return value
	})
	if len(missing) > 0 {
		return "", fmt.Errorf("target_text_template contains unsupported or unavailable placeholder(s): %s", strings.Join(uniqueNonEmpty(missing), ", "))
	}
	rendered = strings.TrimSpace(rendered)
	if rendered == "" {
		return "", fmt.Errorf("rendered target_text is empty")
	}
	return rendered, nil
}

func phoneWorkflowContactsSummary(contacts []phoneMessageContact) string {
	parts := make([]string, 0, len(contacts))
	for _, contact := range contacts {
		chunk := strings.TrimSpace(contact.Name)
		if nums := strings.Join(uniqueNonEmpty(contact.PhoneNumbers), "、"); nums != "" {
			if chunk != "" {
				chunk += ": "
			}
			chunk += nums
		}
		if chunk != "" {
			parts = append(parts, chunk)
		}
	}
	return strings.Join(parts, "；")
}

func phoneWorkflowEventTitles(events []phoneWorkflowCalendarEvent) []string {
	values := make([]string, 0, len(events))
	for _, event := range events {
		values = append(values, event.Title)
	}
	return uniqueNonEmpty(values)
}

func phoneWorkflowEventNotes(events []phoneWorkflowCalendarEvent) []string {
	values := make([]string, 0, len(events))
	for _, event := range events {
		values = append(values, event.Notes)
	}
	return uniqueNonEmpty(values)
}

func phoneWorkflowEventLocations(events []phoneWorkflowCalendarEvent) []string {
	values := make([]string, 0, len(events))
	for _, event := range events {
		values = append(values, event.Location)
	}
	return uniqueNonEmpty(values)
}

func phoneWorkflowEventsSummary(events []phoneWorkflowCalendarEvent) string {
	parts := make([]string, 0, len(events))
	for _, event := range events {
		chunkParts := uniqueNonEmpty([]string{event.Title, event.StartAt, event.Location, event.Notes})
		if len(chunkParts) > 0 {
			parts = append(parts, strings.Join(chunkParts, " "))
		}
	}
	return strings.Join(parts, "；")
}

func phoneWorkflowFirstNonEmpty(values []string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

func phoneWorkflowCompactDataText(data any) string {
	if data == nil {
		return ""
	}
	b, err := json.Marshal(data)
	if err != nil {
		return fmt.Sprint(data)
	}
	return string(b)
}
