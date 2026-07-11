package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

const phoneBridgeBackgroundSafeDataToolNote = `On iOS, when Aiden is backgrounded with PiP Bridge mode active, this data tool can run through the HTTP command queue without restoring Aiden. If PiP is not active but the Dynamic Island return entry is available, the tool restores Aiden to foreground before sending the command. PiP Bridge mode is not a foreground substitute for bridge_open_app or UI actions. `

// nextBridgeCmdID builds a unique command id for a bridge command type. It
// reuses openAppCmdSeq so every outbound bridge command shares one counter.
func nextBridgeCmdID(prefix string) string {
	return fmt.Sprintf("%s_%d_%d", prefix, time.Now().UnixMilli(), openAppCmdSeq.Add(1))
}

func bridgeNotConnected(status ...PhoneBridgeStatus) string {
	result := map[string]interface{}{
		"ok":    false,
		"error": "phone bridge not connected",
	}
	if len(status) > 0 {
		result["fallback"] = phoneBridgeRecoveryGuidance(status[0])
	}
	return jsonString(result)
}

func toolErrorBridgeResp(err error, status ...PhoneBridgeStatus) string {
	result := map[string]interface{}{"ok": false, "error": err.Error()}
	if len(status) > 0 {
		result["fallback"] = phoneBridgeRecoveryGuidance(status[0])
	}
	return jsonString(result)
}

func bridgeStatusForError(bridge *PhoneBridge) []PhoneBridgeStatus {
	if bridge == nil {
		return nil
	}
	return []PhoneBridgeStatus{bridge.Status()}
}

// ClipboardTool reads and writes the connected phone's system clipboard.
type ClipboardTool struct {
	bridge   *PhoneBridge
	restorer *PhoneBridgeRestorer
}

func NewClipboardTool(bridge *PhoneBridge, restorer *PhoneBridgeRestorer) *ClipboardTool {
	return &ClipboardTool{bridge: bridge, restorer: restorer}
}

func (t *ClipboardTool) Name() string { return toolBridgeClipboard }

func (t *ClipboardTool) Description() string {
	return `Read or write the connected phone's system clipboard via the phone bridge. ` +
		`Use this as a fast cross-app content channel for long or non-ASCII text: write the clipboard in Aiden, switch to the target app, then paste. ` +
		phoneBridgeBackgroundSafeDataToolNote
}

func (t *ClipboardTool) ArgsSchema() map[string]any {
	return objectArgsSchema(map[string]any{
		"action": stringEnumArgSchema("Clipboard action.", "read", "write"),
		"text":   stringArgSchema("Text to write when action is write."),
	}, "action")
}

type clipboardArgs struct {
	Action string `json:"action"`
	Text   string `json:"text"`
}

func (t *ClipboardTool) Call(ctx context.Context, input string) (string, error) {
	var args clipboardArgs
	if err := json.Unmarshal([]byte(strings.TrimSpace(input)), &args); err != nil {
		te := NewToolError(CodeInvalidArguments, fmt.Sprintf("invalid input: %v. Expected JSON like {\"action\":\"read\"} or {\"action\":\"write\",\"text\":\"...\"}", err))
		SetToolError(ctx, te)
		return toolErrorString(te), nil
	}

	action := strings.ToLower(strings.TrimSpace(args.Action))
	switch action {
	case "read":
		return t.read(ctx)
	case "write":
		return t.write(ctx, args.Text)
	default:
		te := NewToolError(CodeInvalidArguments, fmt.Sprintf("unknown action %q, expected \"read\" or \"write\"", args.Action))
		SetToolError(ctx, te)
		return toolErrorString(te), nil
	}
}

func (t *ClipboardTool) read(ctx context.Context) (string, error) {
	resp, restored, err := sendRoutedBridgeCommand(ctx, t.bridge, t.restorer, BridgeCommand{
		ID:        nextBridgeCmdID("clip_read"),
		Type:      "clipboard_read",
		TimeoutMs: 5000,
	})
	if err != nil {
		te := NewToolError(CodeBridgeNotConnected, err.Error())
		SetToolError(ctx, te)
		return toolErrorString(te), nil
	}
	if resp.Error != nil {
		SetToolError(ctx, resp.Error)
		return toolErrorString(resp.Error), nil
	}
	var data struct {
		Text string `json:"text"`
	}
	if len(resp.Data) > 0 {
		if err := json.Unmarshal(resp.Data, &data); err != nil {
			te := NewToolError(CodeToolExecutionFailed, fmt.Sprintf("decode clipboard data: %v", err))
			SetToolError(ctx, te)
			return toolErrorString(te), nil
		}
	}
	result := map[string]interface{}{"ok": true, "text": data.Text}
	if restored {
		result["restored_from_return_entry"] = true
	}
	return jsonString(result), nil
}

func (t *ClipboardTool) write(ctx context.Context, text string) (string, error) {
	payload, _ := json.Marshal(map[string]string{"text": text})
	resp, restored, err := sendRoutedBridgeCommand(ctx, t.bridge, t.restorer, BridgeCommand{
		ID:        nextBridgeCmdID("clip_write"),
		Type:      "clipboard_write",
		Payload:   payload,
		TimeoutMs: 5000,
	})
	if err != nil {
		te := NewToolError(CodeBridgeNotConnected, err.Error())
		SetToolError(ctx, te)
		return toolErrorString(te), nil
	}
	if resp.Error != nil {
		SetToolError(ctx, resp.Error)
		return toolErrorString(resp.Error), nil
	}
	result := map[string]interface{}{"ok": true}
	if restored {
		result["restored_from_return_entry"] = true
	}
	if t.bridge != nil {
		t.bridge.NoteClipboardWrite(text)
	}
	return jsonString(result), nil
}

// CalendarTool creates, queries, and deletes system calendar events on the
// connected phone via the phone bridge.
type CalendarTool struct {
	bridge   *PhoneBridge
	restorer *PhoneBridgeRestorer
}

func NewCalendarTool(bridge *PhoneBridge, restorer *PhoneBridgeRestorer) *CalendarTool {
	return &CalendarTool{bridge: bridge, restorer: restorer}
}

func (t *CalendarTool) Name() string { return toolBridgeCalendar }

func (t *CalendarTool) Description() string {
	return `Create, query, or delete system calendar events on the connected phone via the phone bridge. ` +
		`Times are RFC3339 strings with timezone offset, e.g. "2026-06-02T15:00:00+08:00". Use the connected phone environment timezone when available; otherwise use shell to obtain a controller-time baseline and do not assume it matches the phone timezone. ` +
		`Confirm details with the user before creating or deleting events. ` +
		phoneBridgeBackgroundSafeDataToolNote
}

func (t *CalendarTool) ArgsSchema() map[string]any {
	return objectArgsSchema(map[string]any{
		"action":               stringEnumArgSchema("Calendar action.", "create", "query", "delete"),
		"event_id":             stringArgSchema("Calendar event id for delete."),
		"title":                stringArgSchema("Event title for create."),
		"start_at":             stringArgSchema("Event start time as RFC3339 with timezone."),
		"end_at":               stringArgSchema("Optional event end time as RFC3339 with timezone."),
		"from":                 stringArgSchema("Query start time as RFC3339 with timezone."),
		"to":                   stringArgSchema("Query end time as RFC3339 with timezone."),
		"all_day":              boolArgSchema("Whether the created event is all-day."),
		"location":             stringArgSchema("Optional event location."),
		"notes":                stringArgSchema("Optional event notes."),
		"alarm_minutes_before": minIntegerArgSchema("Reminder offset in minutes before the event.", 0),
	}, "action")
}

type calendarArgs struct {
	Action             string `json:"action"`
	EventID            string `json:"event_id"`
	Title              string `json:"title"`
	StartAt            string `json:"start_at"`
	EndAt              string `json:"end_at"`
	From               string `json:"from"`
	To                 string `json:"to"`
	AllDay             bool   `json:"all_day"`
	Location           string `json:"location"`
	Notes              string `json:"notes"`
	AlarmMinutesBefore int    `json:"alarm_minutes_before"`
}

func (t *CalendarTool) Call(ctx context.Context, input string) (string, error) {
	var args calendarArgs
	if err := json.Unmarshal([]byte(strings.TrimSpace(input)), &args); err != nil {
		te := NewToolError(CodeInvalidArguments, fmt.Sprintf("invalid input: %v. Expected JSON like {\"action\":\"create\",\"title\":\"...\",\"start_at\":\"2026-06-02T15:00:00+08:00\"} or {\"action\":\"query\",\"from\":\"...\",\"to\":\"...\"}", err))
		SetToolError(ctx, te)
		return toolErrorString(te), nil
	}

	switch strings.ToLower(strings.TrimSpace(args.Action)) {
	case "create":
		return t.create(ctx, args)
	case "query":
		return t.query(ctx, args)
	case "delete":
		return t.delete(ctx, args)
	default:
		te := NewToolError(CodeInvalidArguments, fmt.Sprintf("unknown action %q, expected \"create\", \"query\", or \"delete\"", args.Action))
		SetToolError(ctx, te)
		return toolErrorString(te), nil
	}
}

func (t *CalendarTool) create(ctx context.Context, args calendarArgs) (string, error) {
	if strings.TrimSpace(args.Title) == "" {
		te := NewToolError(CodeInvalidArguments, "create requires a title")
		SetToolError(ctx, te)
		return toolErrorString(te), nil
	}
	if strings.TrimSpace(args.StartAt) == "" {
		te := NewToolError(CodeInvalidArguments, "create requires a start_at time (RFC3339)")
		SetToolError(ctx, te)
		return toolErrorString(te), nil
	}
	payload, _ := json.Marshal(map[string]interface{}{
		"title":                args.Title,
		"start_at":             args.StartAt,
		"end_at":               args.EndAt,
		"all_day":              args.AllDay,
		"location":             args.Location,
		"notes":                args.Notes,
		"alarm_minutes_before": args.AlarmMinutesBefore,
	})
	resp, restored, err := sendRoutedBridgeCommand(ctx, t.bridge, t.restorer, BridgeCommand{
		ID:        nextBridgeCmdID("cal_create"),
		Type:      "calendar_create",
		Payload:   payload,
		TimeoutMs: 8000,
	})
	if err != nil {
		te := NewToolError(CodeBridgeNotConnected, err.Error())
		SetToolError(ctx, te)
		return toolErrorString(te), nil
	}
	if resp.Error != nil {
		SetToolError(ctx, resp.Error)
		return toolErrorString(resp.Error), nil
	}
	var data struct {
		EventID string `json:"event_id"`
	}
	if len(resp.Data) > 0 {
		if err := json.Unmarshal(resp.Data, &data); err != nil {
			te := NewToolError(CodeToolExecutionFailed, fmt.Sprintf("decode calendar data: %v", err))
			SetToolError(ctx, te)
			return toolErrorString(te), nil
		}
	}
	result := map[string]interface{}{"ok": true, "event_id": data.EventID}
	if restored {
		result["restored_from_return_entry"] = true
	}
	return jsonString(result), nil
}

func (t *CalendarTool) query(ctx context.Context, args calendarArgs) (string, error) {
	if strings.TrimSpace(args.From) == "" || strings.TrimSpace(args.To) == "" {
		te := NewToolError(CodeInvalidArguments, "query requires both from and to times (RFC3339)")
		SetToolError(ctx, te)
		return toolErrorString(te), nil
	}
	payload, _ := json.Marshal(map[string]string{"from": args.From, "to": args.To})
	resp, restored, err := sendRoutedBridgeCommand(ctx, t.bridge, t.restorer, BridgeCommand{
		ID:        nextBridgeCmdID("cal_query"),
		Type:      "calendar_query",
		Payload:   payload,
		TimeoutMs: 8000,
	})
	if err != nil {
		te := NewToolError(CodeBridgeNotConnected, err.Error())
		SetToolError(ctx, te)
		return toolErrorString(te), nil
	}
	if resp.Error != nil {
		SetToolError(ctx, resp.Error)
		return toolErrorString(resp.Error), nil
	}
	var data struct {
		Events []map[string]interface{} `json:"events"`
	}
	if len(resp.Data) > 0 {
		if err := json.Unmarshal(resp.Data, &data); err != nil {
			te := NewToolError(CodeToolExecutionFailed, fmt.Sprintf("decode calendar data: %v", err))
			SetToolError(ctx, te)
			return toolErrorString(te), nil
		}
	}
	if data.Events == nil {
		data.Events = []map[string]interface{}{}
	}
	result := map[string]interface{}{"ok": true, "events": data.Events}
	if restored {
		result["restored_from_return_entry"] = true
	}
	return jsonString(result), nil
}

func (t *CalendarTool) delete(ctx context.Context, args calendarArgs) (string, error) {
	if strings.TrimSpace(args.EventID) == "" {
		te := NewToolError(CodeInvalidArguments, "delete requires an event_id")
		SetToolError(ctx, te)
		return toolErrorString(te), nil
	}
	payload, _ := json.Marshal(map[string]string{"event_id": args.EventID})
	resp, restored, err := sendRoutedBridgeCommand(ctx, t.bridge, t.restorer, BridgeCommand{
		ID:        nextBridgeCmdID("cal_delete"),
		Type:      "calendar_delete",
		Payload:   payload,
		TimeoutMs: 8000,
	})
	if err != nil {
		te := NewToolError(CodeBridgeNotConnected, err.Error())
		SetToolError(ctx, te)
		return toolErrorString(te), nil
	}
	if resp.Error != nil {
		SetToolError(ctx, resp.Error)
		return toolErrorString(resp.Error), nil
	}
	result := map[string]interface{}{"ok": true}
	if restored {
		result["restored_from_return_entry"] = true
	}
	return jsonString(result), nil
}

// ContactsTool queries, creates, and updates contacts on the connected phone.
type ContactsTool struct {
	bridge   *PhoneBridge
	restorer *PhoneBridgeRestorer
}

func NewContactsTool(bridge *PhoneBridge, restorer *PhoneBridgeRestorer) *ContactsTool {
	return &ContactsTool{bridge: bridge, restorer: restorer}
}

func (t *ContactsTool) Name() string { return toolBridgeContacts }

func (t *ContactsTool) Description() string {
	return `Query, create, or update contacts on the connected phone via the phone bridge. ` +
		`Confirm details with the user before creating or updating contacts. ` +
		phoneBridgeBackgroundSafeDataToolNote
}

func (t *ContactsTool) ArgsSchema() map[string]any {
	return objectArgsSchema(map[string]any{
		"action":        stringEnumArgSchema("Contacts action.", "query", "create", "update"),
		"contact_id":    stringArgSchema("Contact id for update."),
		"query":         stringArgSchema("Search query for contact lookup."),
		"limit":         minIntegerArgSchema("Maximum query results.", 1),
		"name":          stringArgSchema("Contact display name."),
		"phone_numbers": stringArrayArgSchema("Contact phone numbers."),
		"emails":        stringArrayArgSchema("Contact email addresses."),
		"organization":  stringArgSchema("Contact organization."),
		"notes":         stringArgSchema("Contact notes."),
	}, "action")
}

type contactsArgs struct {
	Action       string   `json:"action"`
	ContactID    string   `json:"contact_id"`
	Query        string   `json:"query"`
	Limit        int      `json:"limit"`
	Name         string   `json:"name"`
	PhoneNumbers []string `json:"phone_numbers"`
	Emails       []string `json:"emails"`
	Organization string   `json:"organization"`
	Notes        string   `json:"notes"`
}

func (t *ContactsTool) Call(ctx context.Context, input string) (string, error) {
	var args contactsArgs
	if err := json.Unmarshal([]byte(strings.TrimSpace(input)), &args); err != nil {
		te := NewToolError(CodeInvalidArguments, fmt.Sprintf("invalid input: %v. Expected JSON format: {\"action\":\"query\",\"query\":\"name\",\"limit\":20} or {\"action\":\"create\",\"name\":\"...\",\"phone_numbers\":[\"...\"],\"emails\":[\"...\"]} or {\"action\":\"update\",\"contact_id\":\"...\",\"name\":\"...\"}. Arrays must use square brackets", err))
		SetToolError(ctx, te)
		return toolErrorString(te), nil
	}

	switch strings.ToLower(strings.TrimSpace(args.Action)) {
	case "query":
		return t.query(ctx, args)
	case "create":
		return t.create(ctx, args)
	case "update":
		return t.update(ctx, args)
	default:
		te := NewToolError(CodeInvalidArguments, fmt.Sprintf("unknown action %q, expected \"query\", \"create\", or \"update\"", args.Action))
		SetToolError(ctx, te)
		return toolErrorString(te), nil
	}
}

func (t *ContactsTool) query(ctx context.Context, args contactsArgs) (string, error) {
	limit := args.Limit
	if limit <= 0 {
		limit = 20
	}
	payload, _ := json.Marshal(map[string]interface{}{
		"query": args.Query,
		"limit": limit,
	})
	resp, restored, err := sendRoutedBridgeCommand(ctx, t.bridge, t.restorer, BridgeCommand{
		ID:        nextBridgeCmdID("contacts_query"),
		Type:      "contacts_query",
		Payload:   payload,
		TimeoutMs: 8000,
	})
	if err != nil {
		te := NewToolError(CodeBridgeNotConnected, err.Error())
		SetToolError(ctx, te)
		return toolErrorString(te), nil
	}
	result := map[string]interface{}{"ok": true}
	if restored {
		result["restored_from_return_entry"] = true
	}
	if resp.Error != nil {
		SetToolError(ctx, resp.Error)
		return toolErrorString(resp.Error), nil
	}
	var data struct {
		Contacts []map[string]interface{} `json:"contacts"`
	}
	if len(resp.Data) > 0 {
		if err := json.Unmarshal(resp.Data, &data); err != nil {
			te := NewToolError(CodeToolExecutionFailed, fmt.Sprintf("decode contacts data: %v", err))
			SetToolError(ctx, te)
			return toolErrorString(te), nil
		}
	}
	if data.Contacts == nil {
		data.Contacts = []map[string]interface{}{}
	}
	result["contacts"] = data.Contacts
	return jsonString(result), nil
}

func (t *ContactsTool) create(ctx context.Context, args contactsArgs) (string, error) {
	if strings.TrimSpace(args.Name) == "" {
		te := NewToolError(CodeInvalidArguments, "create requires a name")
		SetToolError(ctx, te)
		return toolErrorString(te), nil
	}
	payload, _ := json.Marshal(map[string]interface{}{
		"name":          args.Name,
		"phone_numbers": args.PhoneNumbers,
		"emails":        args.Emails,
		"organization":  args.Organization,
		"notes":         args.Notes,
	})
	resp, restored, err := sendRoutedBridgeCommand(ctx, t.bridge, t.restorer, BridgeCommand{
		ID:        nextBridgeCmdID("contacts_create"),
		Type:      "contacts_create",
		Payload:   payload,
		TimeoutMs: 8000,
	})
	if err != nil {
		te := NewToolError(CodeBridgeNotConnected, err.Error())
		SetToolError(ctx, te)
		return toolErrorString(te), nil
	}
	result := map[string]interface{}{"ok": true}
	if restored {
		result["restored_from_return_entry"] = true
	}
	if resp.Error != nil {
		SetToolError(ctx, resp.Error)
		return toolErrorString(resp.Error), nil
	}
	var data struct {
		ContactID string `json:"contact_id"`
	}
	if len(resp.Data) > 0 {
		if err := json.Unmarshal(resp.Data, &data); err != nil {
			te := NewToolError(CodeToolExecutionFailed, fmt.Sprintf("decode contacts data: %v", err))
			SetToolError(ctx, te)
			return toolErrorString(te), nil
		}
	}
	result["contact_id"] = data.ContactID
	return jsonString(result), nil
}

func (t *ContactsTool) update(ctx context.Context, args contactsArgs) (string, error) {
	if strings.TrimSpace(args.ContactID) == "" {
		te := NewToolError(CodeInvalidArguments, "update requires a contact_id")
		SetToolError(ctx, te)
		return toolErrorString(te), nil
	}
	payload, _ := json.Marshal(map[string]interface{}{
		"contact_id":    args.ContactID,
		"name":          args.Name,
		"phone_numbers": args.PhoneNumbers,
		"emails":        args.Emails,
		"organization":  args.Organization,
		"notes":         args.Notes,
	})
	resp, restored, err := sendRoutedBridgeCommand(ctx, t.bridge, t.restorer, BridgeCommand{
		ID:        nextBridgeCmdID("contacts_update"),
		Type:      "contacts_update",
		Payload:   payload,
		TimeoutMs: 8000,
	})
	if err != nil {
		te := NewToolError(CodeBridgeNotConnected, err.Error())
		SetToolError(ctx, te)
		return toolErrorString(te), nil
	}
	result := map[string]interface{}{"ok": true}
	if restored {
		result["restored_from_return_entry"] = true
	}
	if resp.Error != nil {
		SetToolError(ctx, resp.Error)
		return toolErrorString(resp.Error), nil
	}
	return jsonString(result), nil
}

// NotificationTool sends local notifications on the connected phone.
type NotificationTool struct {
	bridge   *PhoneBridge
	restorer *PhoneBridgeRestorer
}

func NewNotificationTool(bridge *PhoneBridge, restorer *PhoneBridgeRestorer) *NotificationTool {
	return &NotificationTool{bridge: bridge, restorer: restorer}
}

func (t *NotificationTool) Name() string { return toolBridgeNotification }

func (t *NotificationTool) Description() string {
	return `Send local notifications on the connected phone via the phone bridge. ` +
		`Use this to remind the user or bring the companion app back to foreground. ` +
		phoneBridgeBackgroundSafeDataToolNote
}

func (t *NotificationTool) ArgsSchema() map[string]any {
	return objectArgsSchema(map[string]any{
		"title":       stringArgSchema("Notification title."),
		"body":        stringArgSchema("Notification body."),
		"schedule_at": stringArgSchema("Optional scheduled send time as RFC3339 with timezone; if omitted, sent immediately."),
		"sound":       boolArgSchema("Whether to play a sound."),
		"badge":       minIntegerArgSchema("Optional app badge count.", 0),
	}, "title")
}

type notificationArgs struct {
	Title      string `json:"title"`
	Body       string `json:"body"`
	ScheduleAt string `json:"schedule_at"`
	Sound      bool   `json:"sound"`
	Badge      int    `json:"badge"`
}

func (t *NotificationTool) Call(ctx context.Context, input string) (string, error) {
	var args notificationArgs
	if err := json.Unmarshal([]byte(strings.TrimSpace(input)), &args); err != nil {
		te := NewToolError(CodeInvalidArguments, fmt.Sprintf("invalid input: %v. Expected JSON format: {\"title\":\"Reminder\",\"body\":\"Take medicine\",\"schedule_at\":\"2026-06-04T18:00:00+08:00\",\"sound\":true,\"badge\":1}. schedule_at is optional, sound and badge are boolean/number", err))
		SetToolError(ctx, te)
		return toolErrorString(te), nil
	}

	if strings.TrimSpace(args.Title) == "" {
		te := NewToolError(CodeInvalidArguments, "notification requires a title")
		SetToolError(ctx, te)
		return toolErrorString(te), nil
	}

	payload, _ := json.Marshal(map[string]interface{}{
		"title":       args.Title,
		"body":        args.Body,
		"schedule_at": args.ScheduleAt,
		"sound":       args.Sound,
		"badge":       args.Badge,
	})
	resp, restored, err := sendRoutedBridgeCommand(ctx, t.bridge, t.restorer, BridgeCommand{
		ID:        nextBridgeCmdID("notification"),
		Type:      "notification_send",
		Payload:   payload,
		TimeoutMs: 5000,
	})
	if err != nil {
		te := NewToolError(CodeBridgeNotConnected, err.Error())
		SetToolError(ctx, te)
		return toolErrorString(te), nil
	}
	result := map[string]interface{}{"ok": true}
	if restored {
		result["restored_from_return_entry"] = true
	}
	if resp.Error != nil {
		SetToolError(ctx, resp.Error)
		return toolErrorString(resp.Error), nil
	}
	var data struct {
		NotificationID string `json:"notification_id"`
	}
	if len(resp.Data) > 0 {
		if err := json.Unmarshal(resp.Data, &data); err != nil {
			te := NewToolError(CodeToolExecutionFailed, fmt.Sprintf("decode notification data: %v", err))
			SetToolError(ctx, te)
			return toolErrorString(te), nil
		}
	}
	result["notification_id"] = data.NotificationID
	return jsonString(result), nil
}
