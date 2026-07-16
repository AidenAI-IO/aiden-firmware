package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync/atomic"
	"time"
)

var openAppCmdSeq atomic.Uint64

type OpenAppTool struct {
	bridge   *PhoneBridge
	restorer *PhoneBridgeRestorer
}

func NewOpenAppTool(bridge *PhoneBridge, restorer *PhoneBridgeRestorer) *OpenAppTool {
	return &OpenAppTool{bridge: bridge, restorer: restorer}
}

func (t *OpenAppTool) Name() string { return toolBridgeOpenApp }

func (t *OpenAppTool) Description() string {
	return `Open an app, webpage, or dial a phone number on the connected phone via the phone bridge. ` +
		`Use this instead of manually finding and tapping app icons when the phone bridge is connected. ` +
		`If the Aiden companion app is backgrounded on iOS and the Dynamic Island entry is visible, reopen Aiden from that entry first, then use this tool before searching the home screen. ` +
		`Pass the desired app name (WeChat/微信, browser, Taobao/淘宝, Douyin/抖音, Settings/设置, Safari, Chrome), webpage URL, or phone number; the companion app owns platform-specific launch details. ` +
		`When this tool returns ok:true, the app launch is complete; answer the user immediately unless they asked for additional actions inside that app. ` +
		`PiP Bridge mode does not make bridge_open_app background-safe: opening apps, URLs, or phone dialer still requires Aiden in foreground or a restore path such as the Dynamic Island entry.`
}

func (t *OpenAppTool) ArgsSchema() map[string]any {
	return objectArgsSchema(map[string]any{
		"app":          stringArgSchema("Preferred app name or alias, such as WeChat, 微信, weixin, or browser."),
		"url":          stringArgSchema("HTTP or HTTPS URL to open."),
		"phone_number": stringArgSchema("Phone number to dial."),
	})
}

type openAppArgs struct {
	App         string `json:"app"`
	URL         string `json:"url"`
	PhoneNumber string `json:"phone_number"`
}

func isHTTPURL(value string) bool {
	value = strings.ToLower(strings.TrimSpace(value))
	return strings.HasPrefix(value, "http://") || strings.HasPrefix(value, "https://")
}

func applyOpenAppURL(args *openAppArgs, rawURL string) *ToolError {
	targetURL := strings.TrimSpace(rawURL)
	if !isHTTPURL(targetURL) {
		return NewToolError(CodeInvalidArguments, "url must start with http:// or https://")
	}
	args.URL = targetURL
	args.App = ""
	return nil
}

func resolveOpenAppTargets(args *openAppArgs) *ToolError {
	if args == nil {
		return NewToolError(CodeInvalidArguments, "missing bridge_open_app args")
	}
	hasApp := strings.TrimSpace(args.App) != ""
	hasURL := strings.TrimSpace(args.URL) != ""

	if strings.TrimSpace(args.PhoneNumber) != "" {
		if hasApp || hasURL {
			return NewToolError(CodeInvalidArguments, "phone_number cannot be combined with app or url")
		}
		args.PhoneNumber = strings.TrimSpace(args.PhoneNumber)
		return nil
	}

	if hasURL {
		if hasApp {
			return NewToolError(CodeInvalidArguments, "url cannot be combined with app")
		}
		return applyOpenAppURL(args, args.URL)
	}

	if hasApp {
		key := strings.ToLower(strings.TrimSpace(args.App))
		if isHTTPURL(key) {
			return NewToolError(CodeInvalidArguments, "app must be an app name or alias; use url for HTTP/HTTPS URLs")
		}
		args.App = strings.TrimSpace(args.App)
		return nil
	}

	return NewToolError(CodeInvalidArguments, "must provide app name, url, or phone_number")
}

func openAppResultMethod(args openAppArgs) string {
	if strings.TrimSpace(args.PhoneNumber) != "" {
		return "dial"
	}
	if strings.TrimSpace(args.URL) != "" {
		return "open_url"
	}
	if strings.TrimSpace(args.App) != "" {
		return "open_app"
	}
	return "open_app"
}

func openAppResultTarget(args openAppArgs) string {
	if value := strings.TrimSpace(args.PhoneNumber); value != "" {
		return value
	}
	if value := strings.TrimSpace(args.URL); value != "" {
		return value
	}
	if value := strings.TrimSpace(args.App); value != "" {
		return value
	}
	return ""
}

func openAppResultMechanism(args openAppArgs, responseMethod string) string {
	method := strings.TrimSpace(responseMethod)
	switch method {
	case "ios_shortcut", "ios_url_scheme", "android_intent", "android_deeplink", "android_package", "dial":
		return method
	case "open_url":
		if openAppResultMethod(args) == "open_url" {
			return method
		}
	case "launch_package", "package_name":
		return "android_package"
	}

	return method
}

func (t *OpenAppTool) Call(ctx context.Context, input string) (string, error) {
	var args openAppArgs
	trimmed := strings.TrimSpace(input)
	if strings.HasPrefix(trimmed, "{") {
		if err := json.Unmarshal([]byte(trimmed), &args); err != nil {
			te := NewToolErrorWithDetails(CodeInvalidArguments,
				fmt.Sprintf("invalid input: %v. Expected JSON like {\"app\": \"WeChat\"}, {\"app\": \"微信\"}, or {\"url\": \"https://example.com\"}", err),
				map[string]any{"raw_input": trimmed})
			SetToolError(ctx, te)
			return toolErrorString(te), nil
		}
	} else {
		args.App = trimmed
	}

	if te := resolveOpenAppTargets(&args); te != nil {
		SetToolError(ctx, te)
		return toolErrorString(te), nil
	}

	restored, err := ensurePhoneBridgeReadyForCommand(ctx, t.bridge, t.restorer, "open_app")
	if err != nil {
		status := PhoneBridgeStatus{}
		if t.bridge != nil {
			status = t.bridge.getStatus()
		}
		guidance := phoneBridgeOpenAppRecoveryGuidance(status)
		te := NewToolErrorWithDetails(CodeBridgeNotConnected,
			fmt.Sprintf("%v. %s", err, guidance),
			map[string]any{"fallback": guidance})
		SetToolError(ctx, te)
		return toolErrorString(te), nil
	}

	cmdID := fmt.Sprintf("open_%d_%d", time.Now().UnixMilli(), openAppCmdSeq.Add(1))
	cmd := BridgeCommand{
		ID:          cmdID,
		Type:        "open_app",
		App:         strings.TrimSpace(args.App),
		URL:         strings.TrimSpace(args.URL),
		PhoneNumber: strings.TrimSpace(args.PhoneNumber),
		TimeoutMs:   10000,
	}

	resp, err := t.bridge.SendCommand(ctx, cmd)
	if err != nil {
		status := PhoneBridgeStatus{}
		if t.bridge != nil {
			status = t.bridge.getStatus()
		}
		te := NewToolErrorWithDetails(CodeToolExecutionFailed,
			fmt.Sprintf("send command: %v", err),
			map[string]any{"fallback": phoneBridgeRecoveryGuidance(status)})
		SetToolError(ctx, te)
		return toolErrorString(te), nil
	}

	if resp.Error != nil {
		status := PhoneBridgeStatus{}
		if t.bridge != nil {
			status = t.bridge.getStatus()
		}
		// Preserve upstream Code/Category; attach app-side fallback hint.
		te := resp.Error
		if te.Details == nil {
			te.Details = map[string]any{}
		}
		te.Details["fallback"] = phoneBridgeRecoveryGuidance(status)
		SetToolError(ctx, te)
		return toolErrorString(te), nil
	}

	result := map[string]interface{}{
		"ok":     true,
		"method": openAppResultMethod(args),
	}
	if restored {
		result["restored_from_return_entry"] = true
	}
	if target := openAppResultTarget(args); target != "" {
		result["target"] = target
	}
	if mechanism := openAppResultMechanism(args, resp.Method); mechanism != "" {
		result["mechanism"] = mechanism
	}
	return jsonString(result), nil
}

func (s *ToolSet) RegisterPhoneBridge(bridge *PhoneBridge) {
	if bridge == nil {
		return
	}
	s.phoneBridge = bridge
	if status := bridge.getStatus(); status.Environment != nil {
		env := clonePhoneEnvironment(*status.Environment)
		s.UpdateDeviceEnvironment(&env)
	}
	if s.phoneBridgeRestorer != nil {
		s.phoneBridgeRestorer.SetBridge(bridge)
	}
	s.tools[toolBridgeOpenApp] = NewOpenAppTool(bridge, s.phoneBridgeRestorer)
	s.tools[toolBridgeClipboard] = NewClipboardTool(bridge, s.phoneBridgeRestorer)
	s.tools[toolBridgeCalendar] = NewCalendarTool(bridge, s.phoneBridgeRestorer)
	s.tools[toolBridgeContacts] = NewContactsTool(bridge, s.phoneBridgeRestorer)
	s.tools[toolBridgeNotification] = NewNotificationTool(bridge, s.phoneBridgeRestorer)
}
