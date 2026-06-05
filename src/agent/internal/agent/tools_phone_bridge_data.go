package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// nextBridgeCmdID builds a unique command id for a bridge command type. It
// reuses openAppCmdSeq so every outbound bridge command shares one counter.
func nextBridgeCmdID(prefix string) string {
	return fmt.Sprintf("%s_%d_%d", prefix, time.Now().UnixMilli(), openAppCmdSeq.Add(1))
}

func bridgeNotConnected() string {
	return jsonString(map[string]interface{}{
		"ok":    false,
		"error": "phone bridge not connected",
	})
}

func bridgeRespError(err error) string {
	return jsonString(map[string]interface{}{"ok": false, "error": err.Error()})
}

// bridgeMsgError returns a JSON envelope for a tool-level error string. Keeps
// every branch of these tools on the same {"ok":false,"error":"..."} contract
// so callers do not have to special-case plain strings vs JSON.
func bridgeMsgError(format string, a ...interface{}) string {
	msg := format
	if len(a) > 0 {
		msg = fmt.Sprintf(format, a...)
	}
	return jsonString(map[string]interface{}{"ok": false, "error": msg})
}

// ClipboardTool reads and writes the connected phone's system clipboard.
type ClipboardTool struct {
	bridge *PhoneBridge
}

func NewClipboardTool(bridge *PhoneBridge) *ClipboardTool {
	return &ClipboardTool{bridge: bridge}
}

func (t *ClipboardTool) Name() string { return "clipboard" }

func (t *ClipboardTool) Description() string {
	return `Read or write the connected phone's system clipboard via the phone bridge. ` +
		`Input JSON: {"action":"read"} returns {"ok":true,"text":"..."}; ` +
		`{"action":"write","text":"content"} sets the clipboard and returns {"ok":true}. ` +
		`Use this as a fast cross-app content channel instead of HID copy/paste when the phone bridge is connected. ` +
		`If the phone bridge is not connected, this tool fails and there is no HID fallback for clipboard access.`
}

type clipboardArgs struct {
	Action string `json:"action"`
	Text   string `json:"text"`
}

func (t *ClipboardTool) Call(ctx context.Context, input string) (string, error) {
	if !t.bridge.Connected() {
		return bridgeNotConnected(), nil
	}

	var args clipboardArgs
	if err := json.Unmarshal([]byte(strings.TrimSpace(input)), &args); err != nil {
		return bridgeMsgError("invalid input: %v", err), nil
	}

	action := strings.ToLower(strings.TrimSpace(args.Action))
	switch action {
	case "read":
		return t.read(ctx)
	case "write":
		return t.write(ctx, args.Text)
	default:
		return bridgeMsgError("unknown action %q, expected \"read\" or \"write\"", args.Action), nil
	}
}

func (t *ClipboardTool) read(ctx context.Context) (string, error) {
	resp, err := t.bridge.SendCommand(ctx, BridgeCommand{
		ID:        nextBridgeCmdID("clip_read"),
		Type:      "clipboard_read",
		TimeoutMs: 5000,
	})
	if err != nil {
		return bridgeRespError(err), nil
	}
	result := map[string]interface{}{"ok": resp.OK}
	if !resp.OK {
		result["error"] = resp.Error
		return jsonString(result), nil
	}
	var data struct {
		Text string `json:"text"`
	}
	if len(resp.Data) > 0 {
		if err := json.Unmarshal(resp.Data, &data); err != nil {
			result["ok"] = false
			result["error"] = fmt.Sprintf("decode clipboard data: %v", err)
			return jsonString(result), nil
		}
	}
	result["text"] = data.Text
	return jsonString(result), nil
}

func (t *ClipboardTool) write(ctx context.Context, text string) (string, error) {
	payload, _ := json.Marshal(map[string]string{"text": text})
	resp, err := t.bridge.SendCommand(ctx, BridgeCommand{
		ID:        nextBridgeCmdID("clip_write"),
		Type:      "clipboard_write",
		Payload:   payload,
		TimeoutMs: 5000,
	})
	if err != nil {
		return bridgeRespError(err), nil
	}
	result := map[string]interface{}{"ok": resp.OK}
	if !resp.OK {
		result["error"] = resp.Error
	}
	return jsonString(result), nil
}

// CalendarTool creates, queries, and deletes system calendar events on the
// connected phone via the phone bridge.
type CalendarTool struct {
	bridge *PhoneBridge
}

func NewCalendarTool(bridge *PhoneBridge) *CalendarTool {
	return &CalendarTool{bridge: bridge}
}

func (t *CalendarTool) Name() string { return "calendar" }

func (t *CalendarTool) Description() string {
	return `Create, query, or delete system calendar events on the connected phone via the phone bridge. ` +
		`Times are RFC3339 strings with timezone offset, e.g. "2026-06-02T15:00:00+08:00". Use current_time first if you need the timezone or "now". ` +
		`Create: {"action":"create","title":"Dentist","start_at":"2026-06-02T15:00:00+08:00","end_at":"2026-06-02T16:00:00+08:00","all_day":false,"location":"Clinic","notes":"...","alarm_minutes_before":30} -> {"ok":true,"event_id":"..."}. ` +
		`Query: {"action":"query","from":"2026-06-02T00:00:00+08:00","to":"2026-06-03T00:00:00+08:00"} -> {"ok":true,"events":[{"event_id","title","start_at","end_at","location"}]}. ` +
		`Delete: {"action":"delete","event_id":"..."} -> {"ok":true}. ` +
		`Confirm details with the user before creating or deleting events. If the phone bridge is not connected, this tool fails and there is no HID fallback.`
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
	if !t.bridge.Connected() {
		return bridgeNotConnected(), nil
	}

	var args calendarArgs
	if err := json.Unmarshal([]byte(strings.TrimSpace(input)), &args); err != nil {
		return bridgeMsgError("invalid input: %v", err), nil
	}

	switch strings.ToLower(strings.TrimSpace(args.Action)) {
	case "create":
		return t.create(ctx, args)
	case "query":
		return t.query(ctx, args)
	case "delete":
		return t.delete(ctx, args)
	default:
		return bridgeMsgError("unknown action %q, expected \"create\", \"query\", or \"delete\"", args.Action), nil
	}
}

func (t *CalendarTool) create(ctx context.Context, args calendarArgs) (string, error) {
	if strings.TrimSpace(args.Title) == "" {
		return bridgeMsgError("create requires a title"), nil
	}
	if strings.TrimSpace(args.StartAt) == "" {
		return bridgeMsgError("create requires a start_at time (RFC3339)"), nil
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
	resp, err := t.bridge.SendCommand(ctx, BridgeCommand{
		ID:        nextBridgeCmdID("cal_create"),
		Type:      "calendar_create",
		Payload:   payload,
		TimeoutMs: 8000,
	})
	if err != nil {
		return bridgeRespError(err), nil
	}
	result := map[string]interface{}{"ok": resp.OK}
	if !resp.OK {
		result["error"] = resp.Error
		return jsonString(result), nil
	}
	var data struct {
		EventID string `json:"event_id"`
	}
	if len(resp.Data) > 0 {
		if err := json.Unmarshal(resp.Data, &data); err != nil {
			result["ok"] = false
			result["error"] = fmt.Sprintf("decode calendar data: %v", err)
			return jsonString(result), nil
		}
	}
	result["event_id"] = data.EventID
	return jsonString(result), nil
}

func (t *CalendarTool) query(ctx context.Context, args calendarArgs) (string, error) {
	if strings.TrimSpace(args.From) == "" || strings.TrimSpace(args.To) == "" {
		return bridgeMsgError("query requires both from and to times (RFC3339)"), nil
	}
	payload, _ := json.Marshal(map[string]string{"from": args.From, "to": args.To})
	resp, err := t.bridge.SendCommand(ctx, BridgeCommand{
		ID:        nextBridgeCmdID("cal_query"),
		Type:      "calendar_query",
		Payload:   payload,
		TimeoutMs: 8000,
	})
	if err != nil {
		return bridgeRespError(err), nil
	}
	result := map[string]interface{}{"ok": resp.OK}
	if !resp.OK {
		result["error"] = resp.Error
		return jsonString(result), nil
	}
	var data struct {
		Events []map[string]interface{} `json:"events"`
	}
	if len(resp.Data) > 0 {
		if err := json.Unmarshal(resp.Data, &data); err != nil {
			result["ok"] = false
			result["error"] = fmt.Sprintf("decode calendar data: %v", err)
			return jsonString(result), nil
		}
	}
	if data.Events == nil {
		data.Events = []map[string]interface{}{}
	}
	result["events"] = data.Events
	return jsonString(result), nil
}

func (t *CalendarTool) delete(ctx context.Context, args calendarArgs) (string, error) {
	if strings.TrimSpace(args.EventID) == "" {
		return bridgeMsgError("delete requires an event_id"), nil
	}
	payload, _ := json.Marshal(map[string]string{"event_id": args.EventID})
	resp, err := t.bridge.SendCommand(ctx, BridgeCommand{
		ID:        nextBridgeCmdID("cal_delete"),
		Type:      "calendar_delete",
		Payload:   payload,
		TimeoutMs: 8000,
	})
	if err != nil {
		return bridgeRespError(err), nil
	}
	result := map[string]interface{}{"ok": resp.OK}
	if !resp.OK {
		result["error"] = resp.Error
	}
	return jsonString(result), nil
}

// ContactsTool queries, creates, and updates contacts on the connected phone.
type ContactsTool struct {
	bridge *PhoneBridge
}

func NewContactsTool(bridge *PhoneBridge) *ContactsTool {
	return &ContactsTool{bridge: bridge}
}

func (t *ContactsTool) Name() string { return "contacts" }

func (t *ContactsTool) Description() string {
	return `Query, create, or update contacts on the connected phone via the phone bridge. ` +
		`Query: {"action":"query","query":"张三","limit":20} -> {"ok":true,"contacts":[{"contact_id","name","phone_numbers","emails"}]}. ` +
		`Create: {"action":"create","name":"李四","phone_numbers":["+86 139 8765 4321"],"emails":["lisi@example.com"],"organization":"公司","notes":"备注"} -> {"ok":true,"contact_id":"..."}. ` +
		`Update: {"action":"update","contact_id":"...","name":"新名字","phone_numbers":[...],"emails":[...]} -> {"ok":true}. ` +
		`Confirm details with the user before creating or updating contacts. If the phone bridge is not connected, this tool fails.`
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
	if !t.bridge.Connected() {
		return bridgeNotConnected(), nil
	}

	var args contactsArgs
	if err := json.Unmarshal([]byte(strings.TrimSpace(input)), &args); err != nil {
		return bridgeMsgError("invalid input: %v", err), nil
	}

	switch strings.ToLower(strings.TrimSpace(args.Action)) {
	case "query":
		return t.query(ctx, args)
	case "create":
		return t.create(ctx, args)
	case "update":
		return t.update(ctx, args)
	default:
		return bridgeMsgError("unknown action %q, expected \"query\", \"create\", or \"update\"", args.Action), nil
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
	resp, err := t.bridge.SendCommand(ctx, BridgeCommand{
		ID:        nextBridgeCmdID("contacts_query"),
		Type:      "contacts_query",
		Payload:   payload,
		TimeoutMs: 8000,
	})
	if err != nil {
		return bridgeRespError(err), nil
	}
	result := map[string]interface{}{"ok": resp.OK}
	if !resp.OK {
		result["error"] = resp.Error
		return jsonString(result), nil
	}
	var data struct {
		Contacts []map[string]interface{} `json:"contacts"`
	}
	if len(resp.Data) > 0 {
		if err := json.Unmarshal(resp.Data, &data); err != nil {
			result["ok"] = false
			result["error"] = fmt.Sprintf("decode contacts data: %v", err)
			return jsonString(result), nil
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
		return bridgeMsgError("create requires a name"), nil
	}
	payload, _ := json.Marshal(map[string]interface{}{
		"name":          args.Name,
		"phone_numbers": args.PhoneNumbers,
		"emails":        args.Emails,
		"organization":  args.Organization,
		"notes":         args.Notes,
	})
	resp, err := t.bridge.SendCommand(ctx, BridgeCommand{
		ID:        nextBridgeCmdID("contacts_create"),
		Type:      "contacts_create",
		Payload:   payload,
		TimeoutMs: 8000,
	})
	if err != nil {
		return bridgeRespError(err), nil
	}
	result := map[string]interface{}{"ok": resp.OK}
	if !resp.OK {
		result["error"] = resp.Error
		return jsonString(result), nil
	}
	var data struct {
		ContactID string `json:"contact_id"`
	}
	if len(resp.Data) > 0 {
		if err := json.Unmarshal(resp.Data, &data); err != nil {
			result["ok"] = false
			result["error"] = fmt.Sprintf("decode contacts data: %v", err)
			return jsonString(result), nil
		}
	}
	result["contact_id"] = data.ContactID
	return jsonString(result), nil
}

func (t *ContactsTool) update(ctx context.Context, args contactsArgs) (string, error) {
	if strings.TrimSpace(args.ContactID) == "" {
		return bridgeMsgError("update requires a contact_id"), nil
	}
	payload, _ := json.Marshal(map[string]interface{}{
		"contact_id":    args.ContactID,
		"name":          args.Name,
		"phone_numbers": args.PhoneNumbers,
		"emails":        args.Emails,
		"organization":  args.Organization,
		"notes":         args.Notes,
	})
	resp, err := t.bridge.SendCommand(ctx, BridgeCommand{
		ID:        nextBridgeCmdID("contacts_update"),
		Type:      "contacts_update",
		Payload:   payload,
		TimeoutMs: 8000,
	})
	if err != nil {
		return bridgeRespError(err), nil
	}
	result := map[string]interface{}{"ok": resp.OK}
	if !resp.OK {
		result["error"] = resp.Error
	}
	return jsonString(result), nil
}

// NotificationTool sends local notifications on the connected phone.
type NotificationTool struct {
	bridge *PhoneBridge
}

func NewNotificationTool(bridge *PhoneBridge) *NotificationTool {
	return &NotificationTool{bridge: bridge}
}

func (t *NotificationTool) Name() string { return "notification" }

func (t *NotificationTool) Description() string {
	return `Send local notifications on the connected phone via the phone bridge. ` +
		`Input JSON: {"title":"提醒","body":"该吃药了","schedule_at":"2026-06-04T18:00:00+08:00","sound":true,"badge":1}. ` +
		`The schedule_at field is optional (RFC3339 with timezone); if omitted, the notification is sent immediately. ` +
		`Returns {"ok":true,"notification_id":"..."} on success. ` +
		`Use this to remind the user or bring the companion app back to foreground. ` +
		`If the phone bridge is not connected, this tool fails.`
}

type notificationArgs struct {
	Title      string `json:"title"`
	Body       string `json:"body"`
	ScheduleAt string `json:"schedule_at"`
	Sound      bool   `json:"sound"`
	Badge      int    `json:"badge"`
}

func (t *NotificationTool) Call(ctx context.Context, input string) (string, error) {
	if !t.bridge.Connected() {
		return bridgeNotConnected(), nil
	}

	var args notificationArgs
	if err := json.Unmarshal([]byte(strings.TrimSpace(input)), &args); err != nil {
		return bridgeMsgError("invalid input: %v", err), nil
	}

	if strings.TrimSpace(args.Title) == "" {
		return bridgeMsgError("notification requires a title"), nil
	}

	payload, _ := json.Marshal(map[string]interface{}{
		"title":       args.Title,
		"body":        args.Body,
		"schedule_at": args.ScheduleAt,
		"sound":       args.Sound,
		"badge":       args.Badge,
	})
	resp, err := t.bridge.SendCommand(ctx, BridgeCommand{
		ID:        nextBridgeCmdID("notification"),
		Type:      "notification_send",
		Payload:   payload,
		TimeoutMs: 5000,
	})
	if err != nil {
		return bridgeRespError(err), nil
	}
	result := map[string]interface{}{"ok": resp.OK}
	if !resp.OK {
		result["error"] = resp.Error
		return jsonString(result), nil
	}
	var data struct {
		NotificationID string `json:"notification_id"`
	}
	if len(resp.Data) > 0 {
		if err := json.Unmarshal(resp.Data, &data); err != nil {
			result["ok"] = false
			result["error"] = fmt.Sprintf("decode notification data: %v", err)
			return jsonString(result), nil
		}
	}
	result["notification_id"] = data.NotificationID
	return jsonString(result), nil
}
