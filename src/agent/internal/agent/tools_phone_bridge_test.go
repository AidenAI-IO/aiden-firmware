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
	args := openAppArgs{App: " phone ", Name: " phone ", PhoneNumber: " 10086 "}

	if err := resolveOpenAppTargets(&args); err != nil {
		t.Fatalf("resolveOpenAppTargets returned error: %v", err)
	}

	if args.App != "phone" {
		t.Fatalf("app = %q, want trimmed phone alias", args.App)
	}
	if args.Name != "phone" {
		t.Fatalf("name = %q, want trimmed phone alias", args.Name)
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

func TestResolveOpenAppTargetsUnknownAppStaysSemantic(t *testing.T) {
	args := openAppArgs{App: "NoSuchApp12345"}

	if err := resolveOpenAppTargets(&args); err != nil {
		t.Fatalf("resolveOpenAppTargets returned error: %v", err)
	}

	if args.App != "NoSuchApp12345" {
		t.Fatalf("app = %q, want semantic alias", args.App)
	}
	if args.URL != "" {
		t.Fatalf("url = %q, want empty", args.URL)
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
