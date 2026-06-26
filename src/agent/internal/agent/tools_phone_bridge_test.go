package agent

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"nhooyr.io/websocket"
)

func TestBundledAppMappingPathUsesOEMPartition(t *testing.T) {
	if BundledAppMappingPath != "/oem/usr/share/aiden/app_mapping.json" {
		t.Fatalf("BundledAppMappingPath = %q, want OEM path", BundledAppMappingPath)
	}
}

func TestResolveOpenAppTargetsBrowserDoesNotUseFixedWebsite(t *testing.T) {
	args := openAppArgs{App: "browser"}

	if err := resolveOpenAppTargets(&args); err != nil {
		t.Fatalf("resolveOpenAppTargets returned error: %v", err)
	}

	if len(args.IOSURLs) != 1 || args.IOSURLs[0] != "x-web-search://" {
		t.Fatalf("browser ios_urls = %#v, want x-web-search://", args.IOSURLs)
	}
	if len(args.AndroidPackages) == 0 || !strings.HasPrefix(args.AndroidPackages[0], "intent:#Intent;") {
		t.Fatalf("browser android_packages = %#v, want browser intent first", args.AndroidPackages)
	}
	for _, target := range append(args.IOSURLs, args.AndroidPackages...) {
		if strings.Contains(target, "apple.com") || strings.Contains(target, "google.com") {
			t.Fatalf("browser target %q should not be a fixed website", target)
		}
	}
}

func TestResolveOpenAppTargetsSpecificURL(t *testing.T) {
	args := openAppArgs{URL: "https://example.com/path?q=1"}

	if err := resolveOpenAppTargets(&args); err != nil {
		t.Fatalf("resolveOpenAppTargets returned error: %v", err)
	}

	if got := args.IOSURLs; len(got) != 1 || got[0] != "https://example.com/path?q=1" {
		t.Fatalf("ios_urls = %#v, want requested URL", got)
	}
	if got := args.AndroidPackages; len(got) != 1 || got[0] != "android.intent.action.VIEW:https://example.com/path?q=1" {
		t.Fatalf("android_packages = %#v, want ACTION_VIEW requested URL", got)
	}
}

func TestResolveOpenAppTargetsCameraUsesShortcutsFallback(t *testing.T) {
	args := openAppArgs{App: "camera"}
	want := "shortcuts://x-callback-url/run-shortcut?x-error=camera://"

	if err := resolveOpenAppTargets(&args); err != nil {
		t.Fatalf("resolveOpenAppTargets returned error: %v", err)
	}

	if got := args.IOSURLs; len(got) != 1 || got[0] != want {
		t.Fatalf("ios_urls = %#v, want %q", got, want)
	}
	if got := args.AndroidPackages; len(got) != 1 || got[0] != "com.android.camera" {
		t.Fatalf("android_packages = %#v, want com.android.camera", got)
	}
}

func TestResolveOpenAppTargetsContactsUsesShortcutsFallback(t *testing.T) {
	args := openAppArgs{App: "contacts"}
	want := "shortcuts://x-callback-url/run-shortcut?x-error=contact://"

	if err := resolveOpenAppTargets(&args); err != nil {
		t.Fatalf("resolveOpenAppTargets returned error: %v", err)
	}

	if got := args.IOSURLs; len(got) != 1 || got[0] != want {
		t.Fatalf("ios_urls = %#v, want %q", got, want)
	}
	if got := args.AndroidPackages; len(got) != 1 || got[0] != "com.android.contacts" {
		t.Fatalf("android_packages = %#v, want com.android.contacts", got)
	}
}

func TestResolveOpenAppTargetsVoiceMemosUsesShortcutsFallback(t *testing.T) {
	args := openAppArgs{App: "voice memos"}
	want := "shortcuts://x-callback-url/run-shortcut?x-error=voicememos://"

	if err := resolveOpenAppTargets(&args); err != nil {
		t.Fatalf("resolveOpenAppTargets returned error: %v", err)
	}

	if got := args.IOSURLs; len(got) != 1 || got[0] != want {
		t.Fatalf("ios_urls = %#v, want %q", got, want)
	}
	if got := args.AndroidPackages; len(got) != 0 {
		t.Fatalf("android_packages = %#v, want none", got)
	}
}

func TestResolveOpenAppTargetsNameAlias(t *testing.T) {
	args := openAppArgs{Name: "Camera"}
	want := "shortcuts://x-callback-url/run-shortcut?x-error=camera://"

	if err := resolveOpenAppTargets(&args); err != nil {
		t.Fatalf("resolveOpenAppTargets returned error: %v", err)
	}

	if got := args.App; got != "Camera" {
		t.Fatalf("app = %q, want Camera", got)
	}
	if got := args.IOSURLs; len(got) != 1 || got[0] != want {
		t.Fatalf("ios_urls = %#v, want %q", got, want)
	}
}

func TestResolveOpenAppTargetsAppCanBeSpecificURL(t *testing.T) {
	args := openAppArgs{App: "https://example.org"}

	if err := resolveOpenAppTargets(&args); err != nil {
		t.Fatalf("resolveOpenAppTargets returned error: %v", err)
	}

	if got := args.IOSURLs; len(got) != 1 || got[0] != "https://example.org" {
		t.Fatalf("ios_urls = %#v, want requested URL", got)
	}
	if got := args.AndroidPackages; len(got) != 1 || got[0] != "android.intent.action.VIEW:https://example.org" {
		t.Fatalf("android_packages = %#v, want ACTION_VIEW requested URL", got)
	}
}

func TestResolveOpenAppTargetsRejectsUnknownApp(t *testing.T) {
	args := openAppArgs{App: "nonexistent app"}

	if err := resolveOpenAppTargets(&args); err == nil {
		t.Fatal("resolveOpenAppTargets returned nil error, want unknown app error")
	}
}

func TestResolveOpenAppTargetsRejectsInvalidURL(t *testing.T) {
	args := openAppArgs{URL: "not-a-url"}

	if err := resolveOpenAppTargets(&args); err == nil {
		t.Fatal("resolveOpenAppTargets returned nil error, want invalid URL error")
	}
}

func TestResolveOpenAppTargetsRejectsWhitespaceApp(t *testing.T) {
	args := openAppArgs{App: "  "}

	if err := resolveOpenAppTargets(&args); err == nil {
		t.Fatal("resolveOpenAppTargets returned nil error, want missing target error")
	}
}

func TestResolveOpenAppTargetsRejectsConflictingPhoneNumber(t *testing.T) {
	args := openAppArgs{
		App:         "browser",
		URL:         "https://example.com",
		PhoneNumber: "10086",
	}

	if err := resolveOpenAppTargets(&args); err == nil {
		t.Fatal("resolveOpenAppTargets returned nil error, want conflicting phone_number error")
	}
}

func TestResolveOpenAppTargetsRejectsConflictingExplicitTargets(t *testing.T) {
	tests := []openAppArgs{
		{URL: "https://example.com", IOSURLs: []string{"weixin://"}},
		{App: "WeChat", AndroidPackages: []string{"com.tencent.mm"}},
	}

	for _, args := range tests {
		if err := resolveOpenAppTargets(&args); err == nil {
			t.Fatalf("resolveOpenAppTargets(%#v) returned nil error, want conflict error", args)
		}
	}
}

func TestOpenAppResultMetadataForContactsShortcut(t *testing.T) {
	args := openAppArgs{App: "Contacts"}
	if err := resolveOpenAppTargets(&args); err != nil {
		t.Fatalf("resolveOpenAppTargets returned error: %v", err)
	}

	if got := openAppResultMethod(args); got != "open_app" {
		t.Fatalf("method = %q, want open_app", got)
	}
	if got := openAppResultTarget(args); got != "Contacts" {
		t.Fatalf("target = %q, want Contacts", got)
	}
	if got := openAppResultMechanism(args, "open_url"); got != "ios_shortcut" {
		t.Fatalf("mechanism = %q, want ios_shortcut", got)
	}
}

func TestOpenAppResultMetadataForSpecificURL(t *testing.T) {
	args := openAppArgs{URL: "https://example.com"}
	if err := resolveOpenAppTargets(&args); err != nil {
		t.Fatalf("resolveOpenAppTargets returned error: %v", err)
	}

	if got := openAppResultMethod(args); got != "open_url" {
		t.Fatalf("method = %q, want open_url", got)
	}
	if got := openAppResultTarget(args); got != "https://example.com" {
		t.Fatalf("target = %q, want requested URL", got)
	}
	if got := openAppResultMechanism(args, "open_url"); got != "open_url" {
		t.Fatalf("mechanism = %q, want open_url", got)
	}
}

func TestOpenAppResultMetadataForDirectIOSWebURL(t *testing.T) {
	args := openAppArgs{IOSURLs: []string{"https://example.com/direct"}}

	if got := openAppResultMethod(args); got != "open_url" {
		t.Fatalf("method = %q, want open_url", got)
	}
	if got := openAppResultTarget(args); got != "https://example.com/direct" {
		t.Fatalf("target = %q, want direct URL", got)
	}
	if got := openAppResultMechanism(args, ""); got != "open_url" {
		t.Fatalf("mechanism = %q, want open_url", got)
	}
}

func TestOpenAppResultMetadataForDirectAndroidActionView(t *testing.T) {
	args := openAppArgs{AndroidPackages: []string{"android.intent.action.VIEW:https://example.com/direct"}}

	if got := openAppResultMethod(args); got != "open_url" {
		t.Fatalf("method = %q, want open_url", got)
	}
	if got := openAppResultTarget(args); got != "https://example.com/direct" {
		t.Fatalf("target = %q, want direct URL", got)
	}
	if got := openAppResultMechanism(args, ""); got != "open_url" {
		t.Fatalf("mechanism = %q, want open_url", got)
	}
}

func TestOpenAppResultMetadataForDial(t *testing.T) {
	args := openAppArgs{PhoneNumber: "10086"}
	if err := resolveOpenAppTargets(&args); err != nil {
		t.Fatalf("resolveOpenAppTargets returned error: %v", err)
	}

	if got := openAppResultMethod(args); got != "dial" {
		t.Fatalf("method = %q, want dial", got)
	}
	if got := openAppResultTarget(args); got != "10086" {
		t.Fatalf("target = %q, want phone number", got)
	}
	if got := openAppResultMechanism(args, "open_url"); got != "dial" {
		t.Fatalf("mechanism = %q, want dial", got)
	}
	if got := openAppResultMechanism(args, ""); got != "dial" {
		t.Fatalf("fallback mechanism = %q, want dial", got)
	}
}

func TestOpenAppResultMetadataForAndroidIntent(t *testing.T) {
	args := openAppArgs{
		AndroidPackages: []string{"intent:#Intent;action=android.intent.action.MAIN;category=android.intent.category.APP_BROWSER;end"},
	}

	if got := openAppResultMethod(args); got != "open_app" {
		t.Fatalf("method = %q, want open_app", got)
	}
	if got := openAppResultMechanism(args, "open_url"); got != "android_intent" {
		t.Fatalf("mechanism = %q, want android_intent", got)
	}
	if got := openAppResultMechanism(args, ""); got != "android_intent" {
		t.Fatalf("fallback mechanism = %q, want android_intent", got)
	}
}

func TestOpenAppMissingArgsReturnsInvalidArguments(t *testing.T) {
	tool := &OpenAppTool{}
	ctx, _ := WithToolError(context.Background())
	out, err := tool.Call(ctx, `{}`)
	if err != nil {
		t.Fatalf("Call returned err: %v", err)
	}
	te := ToolErrorFromContext(ctx)
	if te == nil {
		t.Fatalf("expected structured ToolError on context; got nil")
	}
	if te.Code != CodeInvalidArguments {
		t.Errorf("Code = %q want %q", te.Code, CodeInvalidArguments)
	}
	if out != te.Message {
		t.Errorf("Call output (%q) must equal Error.Message (%q)", out, te.Message)
	}
}

func TestOpenAppUnknownAppReturnsUnknownApp(t *testing.T) {
	tool := &OpenAppTool{}
	ctx, _ := WithToolError(context.Background())
	out, err := tool.Call(ctx, `{"app":"NoSuchApp12345"}`)
	if err != nil {
		t.Fatalf("Call returned err: %v", err)
	}
	te := ToolErrorFromContext(ctx)
	if te == nil || te.Code != CodeUnknownApp {
		t.Fatalf("expected unknown_app; got %+v", te)
	}
	if out != te.Message {
		t.Errorf("Call output (%q) must equal Error.Message (%q)", out, te.Message)
	}
}

func TestContactsInvalidInputReturnsStructuredError(t *testing.T) {
	tool := NewContactsTool(nil, nil)
	ctx, _ := WithToolError(context.Background())
	out, err := tool.Call(ctx, `{bad json`)
	if err != nil {
		t.Fatalf("Call returned err: %v", err)
	}
	te := ToolErrorFromContext(ctx)
	if te == nil || te.Code != CodeInvalidArguments {
		t.Fatalf("expected invalid_arguments; got %+v", te)
	}
	if out != te.Message {
		t.Fatalf("Call output (%q) must equal Error.Message (%q)", out, te.Message)
	}
}

func TestNotificationInvalidInputReturnsStructuredError(t *testing.T) {
	tool := NewNotificationTool(nil, nil)
	ctx, _ := WithToolError(context.Background())
	out, err := tool.Call(ctx, `{bad json`)
	if err != nil {
		t.Fatalf("Call returned err: %v", err)
	}
	te := ToolErrorFromContext(ctx)
	if te == nil || te.Code != CodeInvalidArguments {
		t.Fatalf("expected invalid_arguments; got %+v", te)
	}
	if out != te.Message {
		t.Fatalf("Call output (%q) must equal Error.Message (%q)", out, te.Message)
	}
}

func TestContactsUpdatePropagatesAppToolError(t *testing.T) {
	bridge := newTestPhoneBridgeWithApp(t, func(cmd BridgeCommand) BridgeCommandResponse {
		return BridgeCommandResponse{
			ID:    cmd.ID,
			Error: NewToolError(CodePermissionDenied, "contacts permission denied"),
		}
	})
	tool := NewContactsTool(bridge, nil)
	ctx, _ := WithToolError(context.Background())

	out, err := tool.Call(ctx, `{"action":"update","contact_id":"abc","name":"Alice"}`)
	if err != nil {
		t.Fatalf("Call returned err: %v", err)
	}
	te := ToolErrorFromContext(ctx)
	if te == nil || te.Code != CodePermissionDenied {
		t.Fatalf("expected permission_denied; got %+v output=%q", te, out)
	}
	if out != te.Message {
		t.Fatalf("Call output (%q) must equal Error.Message (%q)", out, te.Message)
	}
}

func TestClipboardReadSuccessPreservesOKField(t *testing.T) {
	bridge := newTestPhoneBridgeWithApp(t, func(cmd BridgeCommand) BridgeCommandResponse {
		return BridgeCommandResponse{
			ID:   cmd.ID,
			Data: json.RawMessage(`{"text":"hello"}`),
		}
	})
	tool := NewClipboardTool(bridge, nil)

	out, err := tool.Call(context.Background(), `{"action":"read"}`)
	if err != nil {
		t.Fatalf("Call returned err: %v", err)
	}
	var payload struct {
		OK   bool   `json:"ok"`
		Text string `json:"text"`
	}
	if err := json.Unmarshal([]byte(out), &payload); err != nil {
		t.Fatalf("output is not JSON: %v", err)
	}
	if !payload.OK || payload.Text != "hello" {
		t.Fatalf("clipboard success payload = %+v, raw=%s; want ok=true text=hello", payload, out)
	}
}

func TestClipboardWriteRecordsPreparedText(t *testing.T) {
	message := "你好，请问这个手机号你还用吗？13204503813"
	bridge := newTestPhoneBridgeWithApp(t, func(cmd BridgeCommand) BridgeCommandResponse {
		if cmd.Type != "clipboard_write" {
			t.Fatalf("command type = %s", cmd.Type)
		}
		return BridgeCommandResponse{ID: cmd.ID}
	})
	tool := NewClipboardTool(bridge, nil)

	out, err := tool.Call(context.Background(), `{"action":"write","text":"`+message+`"}`)
	if err != nil {
		t.Fatalf("Call returned err: %v", err)
	}
	if !strings.Contains(out, `"ok": true`) {
		t.Fatalf("unexpected output: %s", out)
	}
	if !bridge.ClipboardRecentlyContains(message, time.Minute) {
		t.Fatal("expected bridge to remember prepared clipboard text")
	}
	if bridge.ClipboardRecentlyContains("different text", time.Minute) {
		t.Fatal("clipboard prepared text should require an exact match")
	}
}

func TestClipboardWriteDoesNotRestoreIOSBackgroundAiden(t *testing.T) {
	message := "你好，请问这个手机号你还用吗？13204503813"
	bridge := NewPhoneBridge(nil)
	t.Cleanup(func() { bridge.queue.Stop() })
	bridge.mu.Lock()
	bridge.connected = true
	bridge.platform = "ios"
	bridge.appState = "background"
	bridge.returnEntry = "dynamic_island"
	bridge.returnEntrySeen = true
	bridge.returnEntryOK = true
	bridge.mu.Unlock()

	restorer := NewPhoneBridgeRestorer(bridge, nil)
	restorer.tapReturnEntry = func(context.Context, PhoneBridgeStatus) error {
		t.Fatal("clipboard write must not restore Aiden from an already open target app")
		return nil
	}
	tool := NewClipboardTool(bridge, restorer)
	ctx, _ := WithToolError(context.Background())

	out, err := tool.Call(ctx, `{"action":"write","text":"`+message+`"}`)
	if err != nil {
		t.Fatalf("Call returned err: %v", err)
	}
	te := ToolErrorFromContext(ctx)
	if te == nil || te.Code != CodeBridgeNotConnected {
		t.Fatalf("expected bridge_not_connected error, got %+v output=%q", te, out)
	}
	for _, want := range []string{
		"clipboard_write requires Aiden foreground",
		"prepare clipboard before opening the target app",
		"Do not restore Aiden",
		"do not try app switching",
		"prepared in the earlier data-gathering step",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("clipboard write error missing %q: %s", want, out)
		}
	}
	if bridge.ClipboardRecentlyContains(message, time.Minute) {
		t.Fatal("failed clipboard write must not be recorded as prepared")
	}
}

func newTestPhoneBridgeWithApp(t *testing.T, handle func(BridgeCommand) BridgeCommandResponse) *PhoneBridge {
	t.Helper()
	bridge := NewPhoneBridge(nil)
	t.Cleanup(func() { bridge.queue.Stop() })

	server := httptest.NewServer(http.HandlerFunc(bridge.HandleWebSocket))
	t.Cleanup(server.Close)

	dialURL := "ws" + strings.TrimPrefix(server.URL, "http") + "?platform=android"
	dialCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	conn, _, err := websocket.Dial(dialCtx, dialURL, nil)
	if err != nil {
		t.Fatalf("websocket dial failed: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close(websocket.StatusNormalClosure, "") })

	go func() {
		for {
			_, data, err := conn.Read(context.Background())
			if err != nil {
				return
			}
			var cmd BridgeCommand
			if err := json.Unmarshal(data, &cmd); err != nil {
				return
			}
			resp := handle(cmd)
			if resp.ID == "" {
				resp.ID = cmd.ID
			}
			payload, err := json.Marshal(resp)
			if err != nil {
				return
			}
			if err := conn.Write(context.Background(), websocket.MessageText, payload); err != nil {
				return
			}
		}
	}()

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if bridge.Status().Connected {
			return bridge
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("phone bridge did not connect")
	return nil
}
