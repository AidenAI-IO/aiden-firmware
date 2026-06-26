package agent

import (
	"context"
	"strings"
	"testing"
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

func TestSearchOpenAppToolAvailable(t *testing.T) {
	runtime := newRuntimeWithTextEntryTools()
	if _, ok := runtime.tools.Get("search_launch_app"); !ok {
		t.Fatal("expected search_launch_app tool to be registered")
	}
}

func TestAppSearchOpenFlowCanBeReused(t *testing.T) {
	vision := &stubTextInputVision{analyses: []textInputScreenAnalysis{{ObservedMode: textInputModeASCII}}}
	quick := &recordingTextInputTool{name: "quick_action", out: `{"ok":true}`}
	touch := &recordingTextInputTool{name: "touch_gesture", out: "ok"}
	keyboardText := &recordingTextInputTool{name: "keyboard_text", out: "ok"}
	hw := &textInputHardwareDeps{mouseClick: textInputStubTool{name: "mouse_click", out: "ok"}, quickAction: quick, touchGesture: touch, keyboardText: keyboardText, keyboardTap: textInputStubTool{name: "keyboard_tap", out: "ok"}}
	hw.screenshot = textInputStubTool{name: "screenshot", out: `{"format":"jpeg","width":100,"height":100,"data":"abc"}`}
	entryTool := &EnterTextInFieldTool{engine: newTextInputEngine(*hw, vision)}
	called := 0
	result, err := runAppSearchOpenFlow(context.Background(), appSearchOpenFlowConfig{
		hw:        hw,
		vision:    vision,
		platform:  "android",
		searchTerm: "Aiden",
		entryTool: entryTool,
		findAppTapFn: func(context.Context, screenshotResult, string) (bridgeSearchResult, error) {
			called++
			return bridgeSearchResult{Found: true, TapPoint: focusPointArgs{X: 500, Y: 200, CoordSpace: "normalized"}, Label: "Aiden"}, nil
		},
		confirmAppOpenFn: func(context.Context, screenshotResult, string) (bridgeAppOpenResult, error) {
			return bridgeAppOpenResult{Opened: true, Reason: "Aiden app visible"}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Opened {
		t.Fatalf("expected opened result, got %#v", result)
	}
	if called != 1 {
		t.Fatalf("findAppTapFn calls=%d", called)
	}
	if len(quick.calls) != 1 || len(keyboardText.calls) != 1 || len(touch.calls) != 1 {
		t.Fatalf("unexpected tool calls: quick=%v keyboard=%v touch=%v", quick.calls, keyboardText.calls, touch.calls)
	}
}

func TestAppSearchOpenFlowFallsBackToShorterTerm(t *testing.T) {
	vision := &stubTextInputVision{analyses: []textInputScreenAnalysis{{ObservedMode: textInputModeASCII}, {ObservedMode: textInputModeASCII}}}
	quick := &recordingTextInputTool{name: "quick_action", out: `{"ok":true}`}
	touch := &recordingTextInputTool{name: "touch_gesture", out: "ok"}
	keyboardText := &recordingTextInputTool{name: "keyboard_text", out: "ok"}
	kbTap := &recordingTextInputTool{name: "keyboard_tap", out: "ok"}
	hw := &textInputHardwareDeps{mouseClick: textInputStubTool{name: "mouse_click", out: "ok"}, quickAction: quick, touchGesture: touch, keyboardText: keyboardText, keyboardTap: kbTap}
	hw.screenshot = textInputStubTool{name: "screenshot", out: `{"format":"jpeg","width":100,"height":100,"data":"abc"}`}
	entryTool := &EnterTextInFieldTool{engine: newTextInputEngine(*hw, vision)}
	terms := []string{}
	result, err := runAppSearchOpenFlow(context.Background(), appSearchOpenFlowConfig{
		hw:        hw,
		vision:    vision,
		platform:  "android",
		searchTerm: "Aiden Bridge",
		entryTool: entryTool,
		findAppTapFn: func(_ context.Context, _ screenshotResult, term string) (bridgeSearchResult, error) {
			terms = append(terms, term)
			if term == "Aiden" {
				return bridgeSearchResult{Found: true, TapPoint: focusPointArgs{X: 500, Y: 220, CoordSpace: "normalized"}, Label: "Aiden"}, nil
			}
			return bridgeSearchResult{Found: false, TapPoint: focusPointArgs{CoordSpace: "normalized"}}, nil
		},
		confirmAppOpenFn: func(context.Context, screenshotResult, string) (bridgeAppOpenResult, error) {
			return bridgeAppOpenResult{Opened: true, Reason: "Aiden app visible"}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Opened {
		t.Fatalf("expected opened result, got %#v", result)
	}
	if len(terms) < 2 || terms[0] != "Aiden Bridge" || terms[1] != "Aiden" {
		t.Fatalf("unexpected search terms: %#v", terms)
	}
	if len(keyboardText.calls) < 2 {
		t.Fatalf("expected two search entry attempts, got keyboard_text calls=%v", keyboardText.calls)
	}
}
