package agent

import (
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

var openAppCmdSeq atomic.Uint64

//go:embed app_mapping.json
var defaultAppMappingJSON []byte

// AppMappingFileName is the filename used to look up runtime overrides under
// configDir, and the bundled file shipped with the firmware.
const AppMappingFileName = "app_mapping.json"

// BundledAppMappingPath is the on-device OEM install path, populated by
// _build_image.sh from src/agent/internal/agent/app_mapping.json.
const BundledAppMappingPath = "/oem/usr/share/aiden/" + AppMappingFileName

type appMappingEntry struct {
	IOSURLs         []string `json:"ios_urls"`
	AndroidPackages []string `json:"android_packages"`
}

type appMappingTable struct {
	mu      sync.RWMutex
	entries map[string]appMappingEntry
	source  string // diagnostic: where the loaded table came from
}

var globalAppMapping = newAppMappingTable()

func newAppMappingTable() *appMappingTable {
	t := &appMappingTable{}
	if err := t.loadFromBytes(defaultAppMappingJSON, "embedded"); err != nil {
		// The embedded JSON is part of the binary; if it fails to parse the
		// build is broken, but we degrade to an empty table rather than panic.
		t.entries = map[string]appMappingEntry{}
		t.source = "embedded(invalid)"
	}
	return t
}

func (t *appMappingTable) loadFromBytes(data []byte, source string) error {
	parsed := map[string]appMappingEntry{}
	if err := json.Unmarshal(data, &parsed); err != nil {
		return err
	}
	normalized := make(map[string]appMappingEntry, len(parsed))
	for k, v := range parsed {
		normalized[strings.ToLower(strings.TrimSpace(k))] = v
	}
	t.mu.Lock()
	t.entries = normalized
	t.source = source
	t.mu.Unlock()
	return nil
}

func (t *appMappingTable) loadFromFile(path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return t.loadFromBytes(data, path)
}

func (t *appMappingTable) lookup(key string) (appMappingEntry, bool) {
	t.mu.RLock()
	defer t.mu.RUnlock()
	entry, ok := t.entries[strings.ToLower(strings.TrimSpace(key))]
	return entry, ok
}

// loadAppMappingForConfig picks the highest-priority mapping file available
// and loads it into the global table. Order: configDir override → bundled
// firmware file → embedded defaults (already loaded at init).
func loadAppMappingForConfig(configDir string, logger *Logger) {
	candidates := make([]string, 0, 2)
	if configDir != "" {
		candidates = append(candidates, filepath.Join(configDir, AppMappingFileName))
	}
	candidates = append(candidates, BundledAppMappingPath)

	for _, path := range candidates {
		if _, err := os.Stat(path); err != nil {
			continue
		}
		if err := globalAppMapping.loadFromFile(path); err != nil {
			if logger != nil {
				logger.Warn("app_mapping: failed to load %s: %v (falling back)", path, err)
			}
			continue
		}
		if logger != nil {
			logger.Info("app_mapping: loaded %s", path)
		}
		return
	}
	if logger != nil {
		logger.Info("app_mapping: using embedded defaults")
	}
}

type OpenAppTool struct {
	bridge   *PhoneBridge
	restorer *PhoneBridgeRestorer
}

func NewOpenAppTool(bridge *PhoneBridge, restorer *PhoneBridgeRestorer) *OpenAppTool {
	return &OpenAppTool{bridge: bridge, restorer: restorer}
}

func (t *OpenAppTool) Name() string { return "open_app" }

func (t *OpenAppTool) Description() string {
	return `Open an app or dial a phone number on the connected phone via the phone bridge. ` +
		`Use this instead of manually finding and tapping app icons when the phone bridge is connected. ` +
		`If the Aiden companion app is backgrounded on iOS and the Dynamic Island entry is visible, reopen Aiden from that entry first, then use this tool before searching the home screen. ` +
		`Input JSON: {"app":"WeChat"}, {"app":"browser"}, {"url":"https://example.com"}, {"app":"https://example.com"}, or {"ios_urls":["weixin://"],"android_packages":["com.tencent.mm"]}. ` +
		`If this tool returns {"ok":true}, the app launch request is complete; answer the user immediately unless they asked for additional actions inside that app. ` +
		`To dial a phone number, use: {"app":"phone","phone_number":"10086"} or just {"phone_number":"10086"}. ` +
		`Use {"app":"browser"} to open the browser itself, and {"url":"https://example.com"} to open a specific webpage. ` +
		`Common apps: WeChat(微信), Alipay(支付宝), Safari, Chrome, Settings(设置), Phone(电话), Messages(短信), ` +
		`Camera(相机), Photos(相册), Maps(地图), Notes(备忘录), Calendar(日历), Reminders(提醒事项), ` +
		`Contacts(通讯录), Mail(邮件), AppStore(应用商店), Music(音乐), Files(文件), Clock(时钟), Health(健康), ` +
		`Taobao(淘宝), Douyin(抖音), Meituan(美团), Didi(滴滴), Xiaohongshu(小红书), Bilibili(哔哩哔哩), JD(京东), Eleme(饿了么). ` +
		`On iOS, if Aiden is in background and a Dynamic Island return entry is available, this tool restores Aiden to foreground and waits for WebSocket reconnect before opening the target.`
}

func (t *OpenAppTool) ArgsSchema() map[string]any {
	return objectArgsSchema(map[string]any{
		"app":              stringArgSchema("App name, alias, package, bundle id, or browser shortcut target."),
		"name":             stringArgSchema("Alias for app."),
		"url":              stringArgSchema("HTTP or HTTPS URL to open."),
		"ios_urls":         stringArrayArgSchema("Explicit iOS URL schemes to try."),
		"android_packages": stringArrayArgSchema("Explicit Android package names or intent actions to try."),
		"phone_number":     stringArgSchema("Phone number to dial."),
	})
}

type openAppArgs struct {
	App             string   `json:"app"`
	Name            string   `json:"name"`
	URL             string   `json:"url"`
	IOSURLs         []string `json:"ios_urls"`
	AndroidPackages []string `json:"android_packages"`
	PhoneNumber     string   `json:"phone_number"`
}

func isHTTPURL(value string) bool {
	value = strings.ToLower(strings.TrimSpace(value))
	return strings.HasPrefix(value, "http://") || strings.HasPrefix(value, "https://")
}

func applyOpenAppURL(args *openAppArgs, rawURL string) error {
	targetURL := strings.TrimSpace(rawURL)
	if !isHTTPURL(targetURL) {
		return fmt.Errorf("url must start with http:// or https://")
	}
	args.IOSURLs = []string{targetURL}
	args.AndroidPackages = []string{"android.intent.action.VIEW:" + targetURL}
	return nil
}

func hasOpenAppExplicitTargets(args openAppArgs) bool {
	return len(args.IOSURLs) > 0 || len(args.AndroidPackages) > 0
}

func isPhoneAppAlias(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "", "phone", "telephone", "dial", "dialer", "电话":
		return true
	default:
		return false
	}
}

func resolveOpenAppTargets(args *openAppArgs) error {
	if args == nil {
		return fmt.Errorf("missing open_app args")
	}
	if strings.TrimSpace(args.App) == "" && strings.TrimSpace(args.Name) != "" {
		args.App = args.Name
	}
	hasApp := strings.TrimSpace(args.App) != ""
	hasURL := strings.TrimSpace(args.URL) != ""
	hasExplicitTargets := hasOpenAppExplicitTargets(*args)

	if strings.TrimSpace(args.PhoneNumber) != "" {
		if hasURL || hasExplicitTargets {
			return fmt.Errorf("phone_number cannot be combined with url, ios_urls, or android_packages")
		}
		if hasApp && !isPhoneAppAlias(args.App) {
			return fmt.Errorf("phone_number can only be combined with app/name when the app is phone")
		}
		telURL := "tel:" + strings.TrimSpace(args.PhoneNumber)
		args.IOSURLs = []string{telURL}
		args.AndroidPackages = []string{"android.intent.action.DIAL:" + strings.TrimSpace(args.PhoneNumber)}
		return nil
	}

	if hasURL {
		if hasApp || hasExplicitTargets {
			return fmt.Errorf("url cannot be combined with app, name, ios_urls, or android_packages")
		}
		return applyOpenAppURL(args, args.URL)
	}

	if hasApp && hasExplicitTargets {
		return fmt.Errorf("app/name cannot be combined with ios_urls or android_packages")
	}

	if hasApp {
		key := strings.ToLower(strings.TrimSpace(args.App))
		if mapped, ok := globalAppMapping.lookup(key); ok {
			args.IOSURLs = mapped.IOSURLs
			args.AndroidPackages = mapped.AndroidPackages
		} else if isHTTPURL(key) {
			return applyOpenAppURL(args, args.App)
		} else {
			return fmt.Errorf("unknown app %q, please provide url, ios_urls, or android_packages explicitly", args.App)
		}
	}

	if len(args.IOSURLs) == 0 && len(args.AndroidPackages) == 0 {
		return fmt.Errorf("must provide app name, url, ios_urls, or android_packages")
	}
	return nil
}

func firstOpenAppIOSURL(args openAppArgs) string {
	for _, target := range args.IOSURLs {
		if value := strings.TrimSpace(target); value != "" {
			return value
		}
	}
	return ""
}

func firstOpenAppAndroidPackage(args openAppArgs) string {
	for _, target := range args.AndroidPackages {
		if value := strings.TrimSpace(target); value != "" {
			return value
		}
	}
	return ""
}

func androidActionPayload(target, prefix string) (string, bool) {
	target = strings.TrimSpace(target)
	if !strings.HasPrefix(strings.ToLower(target), prefix) {
		return "", false
	}
	return strings.TrimSpace(target[len(prefix):]), true
}

func openAppResultMethod(args openAppArgs) string {
	if strings.TrimSpace(args.PhoneNumber) != "" {
		return "dial"
	}
	if strings.TrimSpace(args.URL) != "" || isHTTPURL(args.App) {
		return "open_url"
	}
	if strings.TrimSpace(args.App) != "" {
		return "open_app"
	}
	if target := firstOpenAppIOSURL(args); target != "" {
		lower := strings.ToLower(target)
		switch {
		case isHTTPURL(target):
			return "open_url"
		case strings.HasPrefix(lower, "tel:"):
			return "dial"
		}
	}
	if target := firstOpenAppAndroidPackage(args); target != "" {
		lower := strings.ToLower(target)
		switch {
		case strings.HasPrefix(lower, "android.intent.action.dial:"):
			return "dial"
		case strings.HasPrefix(lower, "android.intent.action.view:"):
			if payload, ok := androidActionPayload(target, "android.intent.action.view:"); ok && isHTTPURL(payload) {
				return "open_url"
			}
		}
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
	if target := firstOpenAppIOSURL(args); target != "" {
		lower := strings.ToLower(target)
		if strings.HasPrefix(lower, "tel:") {
			return strings.TrimPrefix(target, target[:4])
		}
		return target
	}
	if target := firstOpenAppAndroidPackage(args); target != "" {
		lower := strings.ToLower(target)
		if strings.HasPrefix(lower, "android.intent.action.dial:") {
			if payload, ok := androidActionPayload(target, "android.intent.action.dial:"); ok {
				return payload
			}
		}
		if strings.HasPrefix(lower, "android.intent.action.view:") {
			if payload, ok := androidActionPayload(target, "android.intent.action.view:"); ok {
				return payload
			}
		}
		return target
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

	if len(args.IOSURLs) > 0 {
		target := strings.TrimSpace(args.IOSURLs[0])
		lower := strings.ToLower(target)
		switch {
		case isHTTPURL(target):
			return "open_url"
		case strings.HasPrefix(lower, "tel:"):
			return "dial"
		case strings.HasPrefix(lower, "shortcuts://"):
			return "ios_shortcut"
		case target != "":
			return "ios_url_scheme"
		}
	}

	if len(args.AndroidPackages) > 0 {
		target := strings.TrimSpace(args.AndroidPackages[0])
		lower := strings.ToLower(target)
		switch {
		case strings.HasPrefix(lower, "android.intent.action.dial:"):
			return "dial"
		case strings.HasPrefix(lower, "android.intent.action.view:"):
			return "open_url"
		case strings.HasPrefix(lower, "intent:"):
			return "android_intent"
		case strings.Contains(lower, "://"):
			return "android_deeplink"
		case target != "":
			return "android_package"
		}
	}

	return method
}

func (t *OpenAppTool) Call(ctx context.Context, input string) (string, error) {
	var args openAppArgs
	trimmed := strings.TrimSpace(input)
	if strings.HasPrefix(trimmed, "{") {
		if err := json.Unmarshal([]byte(trimmed), &args); err != nil {
			return fmt.Sprintf("error: invalid input: %v. Expected JSON format: {\"app\": \"WeChat\"} or {\"ios_urls\": [\"url\"], \"android_packages\": [\"pkg\"]}. Common mistakes: missing quotes around field names and string values", err), nil
		}
	} else {
		args.App = trimmed
	}

	if err := resolveOpenAppTargets(&args); err != nil {
		return jsonString(map[string]interface{}{
			"ok":    false,
			"error": err.Error(),
		}), nil
	}

	restored, err := ensurePhoneBridgeReadyForCommand(ctx, t.bridge, t.restorer)
	if err != nil {
		return jsonString(map[string]interface{}{
			"ok":       false,
			"error":    err.Error(),
			"fallback": "If a Dynamic Island entry is visible, tap it to reopen Aiden, wait for Phone Bridge to reconnect, then retry; otherwise use HID actions.",
		}), nil
	}

	cmdID := fmt.Sprintf("open_%d_%d", time.Now().UnixMilli(), openAppCmdSeq.Add(1))
	cmd := BridgeCommand{
		ID:              cmdID,
		Type:            "open_app",
		IOSURLs:         args.IOSURLs,
		AndroidPackages: args.AndroidPackages,
		TimeoutMs:       10000,
	}

	resp, err := t.bridge.SendCommand(ctx, cmd)
	if err != nil {
		status := PhoneBridgeStatus{}
		if t.bridge != nil {
			status = t.bridge.Status()
		}
		return jsonString(map[string]interface{}{
			"ok":       false,
			"error":    err.Error(),
			"fallback": phoneBridgeRecoveryGuidance(status),
		}), nil
	}

	result := map[string]interface{}{
		"ok":     resp.Error == nil,
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
	if resp.Error != nil {
		result["error"] = resp.Error.Message
		status := PhoneBridgeStatus{}
		if t.bridge != nil {
			status = t.bridge.Status()
		}
		result["fallback"] = phoneBridgeRecoveryGuidance(status)
	}
	return jsonString(result), nil
}

func (s *ToolSet) RegisterPhoneBridge(bridge *PhoneBridge) {
	if bridge == nil {
		return
	}
	s.phoneBridge = bridge
	if status := bridge.Status(); status.Environment != nil {
		env := clonePhoneEnvironment(*status.Environment)
		s.UpdateDeviceEnvironment(&env)
	}
	if s.phoneBridgeRestorer != nil {
		s.phoneBridgeRestorer.SetBridge(bridge)
	}
	s.tools["open_app"] = NewOpenAppTool(bridge, s.phoneBridgeRestorer)
	s.tools["clipboard"] = NewClipboardTool(bridge, s.phoneBridgeRestorer)
	s.tools["calendar"] = NewCalendarTool(bridge, s.phoneBridgeRestorer)
	s.tools["contacts"] = NewContactsTool(bridge, s.phoneBridgeRestorer)
	s.tools["notification"] = NewNotificationTool(bridge, s.phoneBridgeRestorer)
}

func isPhoneBridgeToolName(name string) bool {
	switch name {
	case "open_app", "clipboard", "calendar", "contacts", "notification":
		return true
	default:
		return false
	}
}
