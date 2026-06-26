package agent

import "testing"

func TestResolveOpenAppTargetsAppAliasStaysSemantic(t *testing.T) {
	args := openAppArgs{App: " weixin "}

	if err := resolveOpenAppTargets(&args); err != nil {
		t.Fatalf("resolveOpenAppTargets returned error: %v", err)
	}

	if args.App != "weixin" {
		t.Fatalf("app = %q, want semantic alias", args.App)
	}
	if args.URL != "" {
		t.Fatalf("url = %q, want empty", args.URL)
	}
}

func TestResolveOpenAppTargetsNameAlias(t *testing.T) {
	args := openAppArgs{Name: "微信"}

	if err := resolveOpenAppTargets(&args); err != nil {
		t.Fatalf("resolveOpenAppTargets returned error: %v", err)
	}

	if args.App != "微信" {
		t.Fatalf("app = %q, want name copied into app", args.App)
	}
	if args.Name != "微信" {
		t.Fatalf("name = %q, want original alias preserved", args.Name)
	}
}

func TestResolveOpenAppTargetsURL(t *testing.T) {
	args := openAppArgs{URL: " https://example.com/path?q=1 "}

	if err := resolveOpenAppTargets(&args); err != nil {
		t.Fatalf("resolveOpenAppTargets returned error: %v", err)
	}

	if args.URL != "https://example.com/path?q=1" {
		t.Fatalf("url = %q, want trimmed URL", args.URL)
	}
}

func TestResolveOpenAppTargetsAppCanBeURL(t *testing.T) {
	args := openAppArgs{App: "https://example.org"}

	if err := resolveOpenAppTargets(&args); err != nil {
		t.Fatalf("resolveOpenAppTargets returned error: %v", err)
	}

	if args.App != "" {
		t.Fatalf("app = %q, want URL moved out of app", args.App)
	}
	if args.URL != "https://example.org" {
		t.Fatalf("url = %q, want requested URL", args.URL)
	}
}

func TestResolveOpenAppTargetsPhoneNumber(t *testing.T) {
	args := openAppArgs{App: "phone", PhoneNumber: " 10086 "}

	if err := resolveOpenAppTargets(&args); err != nil {
		t.Fatalf("resolveOpenAppTargets returned error: %v", err)
	}

	if args.PhoneNumber != "10086" {
		t.Fatalf("phone_number = %q, want trimmed phone number", args.PhoneNumber)
	}
}

func TestResolveOpenAppTargetsRejectsInvalidCombinations(t *testing.T) {
	tests := []openAppArgs{
		{},
		{App: "  "},
		{URL: "not-a-url"},
		{App: "微信", URL: "https://example.com"},
		{App: "微信", PhoneNumber: "10086"},
		{URL: "https://example.com", PhoneNumber: "10086"},
	}

	for _, args := range tests {
		if err := resolveOpenAppTargets(&args); err == nil {
			t.Fatalf("resolveOpenAppTargets(%#v) returned nil error, want error", args)
		}
	}
}

func TestOpenAppResultMetadataForApp(t *testing.T) {
	args := openAppArgs{App: "微信"}
	if err := resolveOpenAppTargets(&args); err != nil {
		t.Fatalf("resolveOpenAppTargets returned error: %v", err)
	}

	if got := openAppResultMethod(args); got != "open_app" {
		t.Fatalf("method = %q, want open_app", got)
	}
	if got := openAppResultTarget(args); got != "微信" {
		t.Fatalf("target = %q, want app alias", got)
	}
	if got := openAppResultMechanism(args, "ios_url_scheme"); got != "ios_url_scheme" {
		t.Fatalf("mechanism = %q, want app-side launch method", got)
	}
}

func TestOpenAppResultMetadataForURL(t *testing.T) {
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
	if got := openAppResultMechanism(args, "dial"); got != "dial" {
		t.Fatalf("mechanism = %q, want dial", got)
	}
}
