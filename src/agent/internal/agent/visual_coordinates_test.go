package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"image"
	"image/png"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"aiden-agent/internal/agent/contextmanager"
	"aiden-agent/internal/agent/executor"
	"aiden-agent/internal/agent/messages"
	"aiden-agent/internal/agent/model"
	"github.com/tmc/langchaingo/chains"
	"github.com/tmc/langchaingo/llms"
	langtools "github.com/tmc/langchaingo/tools"
)

func visualTestMessage(t *testing.T, w, h int) messages.Message {
	t.Helper()
	var data bytes.Buffer
	if err := png.Encode(&data, image.NewRGBA(image.Rect(0, 0, w, h))); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "screen.png")
	if err := os.WriteFile(path, data.Bytes(), 0600); err != nil {
		t.Fatal(err)
	}
	return messages.Message{Role: messages.MessageRoleUser, Attachments: []messages.Attachment{{FilePath: path, MIMEType: "image/png", Source: messages.AttachmentSourceScreenshotObservation}}}
}

func TestVisualCoordinatesOutboundImageAndReplay(t *testing.T) {
	for _, size := range [][2]int{{1179, 2556}, {2556, 1179}, {2000, 2000}, {447, 972}, {1, 100}} {
		t.Run(fmt.Sprint(size), func(t *testing.T) {
			original := visualTestMessage(t, size[0], size[1])
			v := newVisualCoordinates()
			out := v.Transform([]messages.Message{original})
			if v.latest == nil || original.Attachments[0].PreparedData != nil {
				t.Fatal("missing frame or source mutated")
			}
			standard := messages.ConvertMessageList(out)
			var caption string
			var cfg image.Config
			var emittedData []byte
			for _, part := range standard[0].Parts {
				switch p := part.(type) {
				case llms.TextContent:
					caption = p.Text
				case llms.BinaryContent:
					emittedData = p.Data
					var err error
					cfg, _, err = image.DecodeConfig(bytes.NewReader(p.Data))
					if err != nil {
						t.Fatal(err)
					}
				}
			}
			// Images should use original dimensions without downsampling
			if cfg.Width != size[0] || cfg.Height != size[1] {
				t.Fatalf("image dimensions changed: expected %dx%d, got %dx%d", size[0], size[1], cfg.Width, cfg.Height)
			}
			// Verify byte-for-byte source preservation (no re-encode)
			sourceData, err := os.ReadFile(original.Attachments[0].FilePath)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(emittedData, sourceData) {
				t.Fatal("emitted bytes differ from source — image was re-encoded")
			}
			if !strings.Contains(caption, fmt.Sprintf("width=%d height=%d", cfg.Width, cfg.Height)) {
				t.Fatal("caption differs from actual image")
			}
			firstLatest := v.latest
			v.Transform([]messages.Message{original})
			if v.latest == nil {
				t.Fatal("replay lost frame")
			}
			// Replay with the same message should give a frame with the same dimensions.
			if v.latest.width != firstLatest.width || v.latest.height != firstLatest.height {
				t.Fatal("replay changed frame dimensions")
			}
			other := newVisualCoordinates()
			other.Transform([]messages.Message{original})
			if other.latest == firstLatest {
				t.Fatal("frame leaked across runs")
			}
			stored, err := json.Marshal(out[0].Attachments[0])
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(stored), "Prepared") {
				t.Fatal("transient frame data persisted")
			}
		})
	}
}

type visualRecordingTool struct {
	schema map[string]any
	name   string
	inputs []map[string]any
}

func (t *visualRecordingTool) Name() string        { return t.name }
func (t *visualRecordingTool) Description() string { return "test tool" }
func (t *visualRecordingTool) ArgsSchema() map[string]any {
	if t.schema == nil {
		t.schema = (&TouchGestureTool{}).ArgsSchema()
	}
	return t.schema
}
func (t *visualRecordingTool) Call(_ context.Context, input string) (string, error) {
	var args map[string]any
	if err := json.Unmarshal([]byte(input), &args); err != nil {
		return "", err
	}
	t.inputs = append(t.inputs, args)
	return "ok", nil
}

func TestVisualCoordinatesToolBoundary(t *testing.T) {
	v := newVisualCoordinates()
	msg := visualTestMessage(t, 101, 201)
	v.Transform([]messages.Message{msg})
	base := &visualRecordingTool{name: "touch_gesture"}
	tool := v.wrap([]langtools.Tool{base})[0]
	// Invalid inputs that should be rejected before reaching the device tool.
	for _, input := range []string{
		`{"point":{"x":101,"y":100}}`,  // x out of bounds (max is 100)
		`{"point":{"x":-1,"y":100}}`,   // negative x
		`{"point":{"x":"50","y":100}}`, // x is string not number
	} {
		_, _ = tool.Call(context.Background(), `{"type":"tap",`+input[1:])
		if len(base.inputs) != 0 {
			t.Fatalf("invalid input reached device: %s", input)
		}
	}
	// Valid input with coordinate conversion.
	result, err := tool.Call(context.Background(), `{"type":"swipe","start":{"x":50,"y":100},"end":{"x":100,"y":200},"speed":2500}`)
	if err != nil || result != "ok" || len(base.inputs) != 1 {
		t.Fatalf("call = %s, %v", result, err)
	}
	got := base.inputs[0]
	if got["speed"] != float64(2500) {
		t.Fatal(got)
	}
	if got["start"].(map[string]any)["x"] != float64(500) || got["end"].(map[string]any)["y"] != float64(1000) {
		t.Fatal(got)
	}
	// Verify the same frame can be used multiple times.
	v.Transform([]messages.Message{msg})
	_, _ = tool.Call(context.Background(), `{"type":"tap","point":{"x":50,"y":100}}`)
	if len(base.inputs) != 2 {
		t.Fatal("coordinate adapter imposed a single-use restriction")
	}
}

func TestVisualCoordinatesAllGeometry(t *testing.T) {
	args := map[string]any{}
	if err := json.Unmarshal([]byte(`{"focus":{"x":50,"y":100},"actions":[{"point":{"x":100,"y":0},"speed":2500}],"column_x":50,"center_y":100,"row_spacing":20}`), &args); err != nil {
		t.Fatal(err)
	}
	if err := convertVisualArguments(args, visualFrame{width: 101, height: 201}); err != nil {
		t.Fatal(err)
	}
	if math.Abs(args["row_spacing"].(float64)-100) > 1e-9 || args["column_x"] != float64(500) || args["focus"].(map[string]any)["y"] != float64(500) {
		t.Fatal(args)
	}
	point := args["actions"].([]any)[0].(map[string]any)["point"].(map[string]any)
	if point["x"] != float64(1000) || point["y"] != float64(0) {
		t.Fatal(point)
	}
}

func TestVisualCoordinatesBadLatestImageFailsClosed(t *testing.T) {
	v := newVisualCoordinates()
	good := visualTestMessage(t, 100, 200)
	bad := messages.Message{Role: messages.MessageRoleUser, Attachments: []messages.Attachment{{FilePath: "/nonexistent/visual-test.png", MIMEType: "image/png", Source: messages.AttachmentSourceScreenshotObservation}}}
	out := v.Transform([]messages.Message{good, bad})
	if v.latest != nil || len(out[1].Attachments) != 0 {
		t.Fatal("bad latest image left an actionable frame")
	}
}

// Source bytes are forwarded without a re-encode, so nothing downstream decodes
// the payload before the provider does. The header alone is not enough to trust.
func TestVisualCoordinatesRejectsTruncatedPayload(t *testing.T) {
	var buf bytes.Buffer
	if err := png.Encode(&buf, image.NewRGBA(image.Rect(0, 0, 400, 800))); err != nil {
		t.Fatal(err)
	}
	truncated := buf.Bytes()[:buf.Len()/2]
	if _, _, err := image.DecodeConfig(bytes.NewReader(truncated)); err != nil {
		t.Fatalf("test needs a parseable header to be meaningful: %v", err)
	}
	path := filepath.Join(t.TempDir(), "truncated.png")
	if err := os.WriteFile(path, truncated, 0600); err != nil {
		t.Fatal(err)
	}
	v := newVisualCoordinates()
	out := v.Transform([]messages.Message{{Role: messages.MessageRoleUser, Attachments: []messages.Attachment{{FilePath: path, MIMEType: "image/png", Source: messages.AttachmentSourceScreenshotObservation}}}})
	if v.latest != nil || len(out[0].Attachments) != 0 {
		t.Fatal("truncated payload reached the model as an actionable frame")
	}
}

func TestVisualCoordinatesSchemaDoesNotMutateOriginal(t *testing.T) {
	base := &visualRecordingTool{name: "touch_gesture"}
	target := &visualCoordinateTool{Tool: base}
	original := base.ArgsSchema()
	before, _ := json.Marshal(original)
	schema := target.ArgsSchema()
	props := schema["properties"].(map[string]any)
	point := props["point"].(map[string]any)["properties"].(map[string]any)["x"].(map[string]any)
	if point["maximum"] != nil {
		t.Fatal("pixel schema unexpectedly carried normalized constraint", schema)
	}
	after, _ := json.Marshal(original)
	if !bytes.Equal(before, after) {
		t.Fatal("underlying schema mutated")
	}
}

type visualCapabilityTool struct {
	visualRecordingTool
	deviceType func() string
}

func (t *visualCapabilityTool) ReturnsVisualObservation() bool     { return true }
func (t *visualCapabilityTool) SetDeviceTypeFunc(fn func() string) { t.deviceType = fn }

func TestVisualCoordinatesForwardsCapabilities(t *testing.T) {
	base := &visualCapabilityTool{visualRecordingTool: visualRecordingTool{name: "touch_gesture"}}
	wrapped := newVisualCoordinates().wrap([]langtools.Tool{base})[0]
	if !wrapped.(visualObservationTool).ReturnsVisualObservation() {
		t.Fatal("lost visual observation capability")
	}
	wrapped.(runtimeDeviceTypeConfigurable).SetDeviceTypeFunc(func() string { return "ios" })
	if base.deviceType == nil || base.deviceType() != "ios" {
		t.Fatal("lost device type callback")
	}
	plain := &visualCoordinateTool{Tool: &visualRecordingTool{name: "touch_gesture"}}
	if plain.ReturnsVisualObservation() {
		t.Fatal("plain tool became visual")
	}
	plain.SetDeviceTypeFunc(func() string { return "ios" })
}

type visualSchemaTool struct {
	langtools.Tool
	schema map[string]any
}

func (t *visualSchemaTool) ArgsSchema() map[string]any { return t.schema }

func TestVisualCoordinatesSchemaFallback(t *testing.T) {
	base := &visualRecordingTool{name: "touch_gesture"}
	// Embedding the narrow Tool interface deliberately hides ArgsSchema.
	noSchema := struct{ langtools.Tool }{base}
	if got := (&visualCoordinateTool{Tool: noSchema}).ArgsSchema(); got != nil {
		t.Fatal(got)
	}
	for _, schema := range []map[string]any{nil, {}, {"properties": "invalid"}, {"properties": map[string]any{}, "invalid": func() {}}} {
		tool := &visualCoordinateTool{Tool: &visualSchemaTool{Tool: base, schema: schema}}
		got := tool.ArgsSchema()
		if len(got) != len(schema) {
			t.Fatalf("fallback changed schema: %v", got)
		}
	}
}

type visualLoopModel struct {
	calls    int
	provider string
	disabled bool
}

func (m *visualLoopModel) Spec() model.ModelSpec {
	return model.ModelSpec{Provider: m.provider, Name: "visual-test", ContextWindow: 100000}
}
func (m *visualLoopModel) CallOptions() []chains.ChainCallOption { return nil }
func (m *visualLoopModel) Call(context.Context, string, ...llms.CallOption) (string, error) {
	return "", fmt.Errorf("unexpected Call")
}
func (m *visualLoopModel) GenerateContent(_ context.Context, input []llms.MessageContent, options ...llms.CallOption) (*llms.ContentResponse, error) {
	m.calls++
	if m.calls > 1 {
		return contentResponse("done"), nil
	}
	hasPreparedImage := false
	for _, msg := range input {
		for _, part := range msg.Parts {
			if p, ok := part.(llms.TextContent); ok {
				if strings.Contains(p.Text, "Image dimensions:") && strings.Contains(p.Text, "Pixel centers span") {
					hasPreparedImage = true
				}
			}
		}
	}
	args := `{"type":"tap","point":{"x":500,"y":500}}`
	if m.provider == "anthropic" && !m.disabled {
		if !hasPreparedImage {
			return nil, fmt.Errorf("no prepared image reached model")
		}
		args = `{"type":"tap","point":{"x":50,"y":100}}`
	} else if hasPreparedImage {
		return nil, fmt.Errorf("non-Claude or disabled model was transformed")
	}
	return &llms.ContentResponse{Choices: []*llms.ContentChoice{{ToolCalls: []llms.ToolCall{{ID: "visual_call", Type: "function", FunctionCall: &llms.FunctionCall{Name: "touch_gesture", Arguments: args}}}}}}, nil
}

func TestVisualCoordinatesAgentLoopWiring(t *testing.T) {
	for _, provider := range []string{"anthropic", "openai"} {
		t.Run(provider, func(t *testing.T) {
			manager, err := contextmanager.NewContextManagerFromMessageList(t.TempDir(), []messages.Message{visualTestMessage(t, 101, 201)})
			if err != nil {
				t.Fatal(err)
			}
			backend := &visualRecordingTool{name: "touch_gesture"}
			m := &visualLoopModel{provider: provider}
			loop := NewAgentLoop(m, RoleProfile{Tools: []langtools.Tool{backend}}, 3, nil, nil, executor.ScreenshotPruningConfig{}, manager)
			answer, err := loop.Run(context.Background(), "tap center")
			if err != nil || answer != "done" {
				t.Fatalf("Run = %q, %v", answer, err)
			}
			if len(backend.inputs) != 1 {
				t.Fatalf("device calls = %d", len(backend.inputs))
			}
			point := backend.inputs[0]["point"].(map[string]any)
			if point["x"] != float64(500) || point["y"] != float64(500) {
				t.Fatal(point)
			}
		})
	}
}

func TestVisualCoordinatesUploadsDoNotChangeScreenSpace(t *testing.T) {
	screenshot := visualTestMessage(t, 101, 201)
	upload := visualTestMessage(t, 800, 600)
	upload.Attachments[0].Source = ""
	v := newVisualCoordinates()
	v.Transform([]messages.Message{screenshot})
	firstLatest := v.latest
	out := v.Transform([]messages.Message{screenshot, upload})
	// The upload should not change the latest screenshot or participate in adaptation.
	if firstLatest == nil || v.latest == nil || v.latest.width != firstLatest.width || v.latest.height != firstLatest.height || out[1].Attachments[0].PreparedData != nil || out[1].Attachments[0].PreparedCaption != "" {
		t.Fatal("ordinary upload participated in screenshot adaptation")
	}
	args := map[string]any{"x": float64(50), "y": float64(100)}
	if _, err := v.convert(args); err != nil || args["x"] != float64(500) || args["y"] != float64(500) {
		t.Fatalf("screenshot mapping changed: %v, %v", args, err)
	}
	for i := 0; i < 2; i++ {
		if _, err := v.convert(map[string]any{"x": float64(50), "y": float64(100)}); err != nil {
			t.Fatal("unexpected frame consumption", err)
		}
	}
}

func TestVisualCoordinatesRebuildDoesNotRequireNewScreenshot(t *testing.T) {
	msg := visualTestMessage(t, 101, 201)
	first := newVisualCoordinates()
	first.Transform([]messages.Message{msg})
	resumed := newVisualCoordinates()
	resumed.Transform([]messages.Message{msg})
	// Both runs see the same message; both should have a usable frame.
	if _, err := resumed.convert(map[string]any{"x": float64(50), "y": float64(100)}); err != nil {
		t.Fatal("adapter imposed new capture requirement", err)
	}
}
