package agent

import (
	"strings"
	"testing"
)

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
