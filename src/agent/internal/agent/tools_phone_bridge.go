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
	return `Open an app or dial a phone number on the connected phone via the phone bridge. ` +
		`Use this instead of manually finding and tapping app icons when the phone bridge is connected. ` +
		`If the Aiden companion app is backgrounded on iOS and the Dynamic Island entry is visible, reopen Aiden from that entry first, then use this tool before searching the home screen. ` +
		`Input JSON: {"app":"WeChat"}, {"app":"微信"}, {"app":"weixin"}, {"app":"browser"}, {"url":"https://example.com"}, or {"phone_number":"10086"}. ` +
		`Pass only the desired app, webpage, or phone number; the companion app owns platform-specific launch details. ` +
		`If this tool returns {"ok":true}, the app launch request is complete; answer the user immediately unless they asked for additional actions inside that app. ` +
		`PiP Bridge mode does not make bridge_open_app background-safe: opening apps, URLs, or phone dialer still requires Aiden in foreground or a restore path such as the Dynamic Island return entry. ` +
		`To dial a phone number, use {"phone_number":"10086"}. ` +
		`Use {"app":"browser"} to open the browser itself, and {"url":"https://example.com"} to open a specific webpage. ` +
		`Common apps: WeChat(微信), Alipay(支付宝), Safari, Chrome, Settings(设置), Phone(电话), Messages(短信), ` +
		`Camera(相机), Photos(相册), Maps(地图), Notes(备忘录), Calendar(日历), Reminders(提醒事项), ` +
		`Contacts(通讯录), Mail(邮件), AppStore(应用商店), Music(音乐), Files(文件), Clock(时钟), Health(健康), ` +
		`Taobao(淘宝), Douyin(抖音), Meituan(美团), Didi(滴滴), Xiaohongshu(小红书), Bilibili(哔哩哔哩), JD(京东), Eleme(饿了么). ` +
		`On iOS, if Aiden is in background and a Dynamic Island return entry is available, this tool restores Aiden to foreground and waits for WebSocket reconnect before opening the target. If PiP Bridge mode is enabled but no foreground restore path is available, use HID/screenshot fallback for UI work.`
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

	restored, err := ensurePhoneBridgeReadyForCommand(ctx, t.bridge, t.restorer)
	if err != nil {
		te := NewToolErrorWithDetails(CodeBridgeNotConnected,
			fmt.Sprintf("%v. If a Dynamic Island entry is visible, tap it to reopen Aiden, wait for Phone Bridge to reconnect, then retry; otherwise use HID actions. %s", err, phoneBridgeDisconnectedRecoveryGuidance),
			map[string]any{"fallback": "tap Dynamic Island or use HID"})
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
