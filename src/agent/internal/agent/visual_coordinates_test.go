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
	"regexp"
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
			v := newVisualCoordinates(VisualCoordinateConfig{})
			out := v.Transform([]messages.Message{original})
			id := v.latest
			if id == "" || original.Attachments[0].PreparedData != nil {
				t.Fatal("missing frame or source mutated")
			}
			standard := messages.ConvertMessageList(out)
			var caption string
			var cfg image.Config
			for _, part := range standard[0].Parts {
				switch p := part.(type) {
				case llms.TextContent:
					caption = p.Text
				case llms.BinaryContent:
					var err error
					cfg, _, err = image.DecodeConfig(bytes.NewReader(p.Data))
					if err != nil {
						t.Fatal(err)
					}
				}
			}
			if cfg.Width > 1280 || cfg.Height > 1280 || cfg.Width*cfg.Height > 1000000 {
				t.Fatalf("oversize image: %+v", cfg)
			}
			if !strings.Contains(caption, fmt.Sprintf("image_width=%d image_height=%d", cfg.Width, cfg.Height)) {
				t.Fatal("caption differs from actual image")
			}
			v.Transform([]messages.Message{original})
			if v.latest != id {
				t.Fatal("replay changed frame identity")
			}
			other := newVisualCoordinates(VisualCoordinateConfig{})
			other.Transform([]messages.Message{original})
			if other.latest == id {
				t.Fatal("frame leaked across runs")
			}
			stored, err := json.Marshal(out[0].Attachments[0])
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(stored), "Prepared") || strings.Contains(string(stored), id) {
				t.Fatal("transient frame data persisted")
			}
		})
	}
}

type visualRecordingTool struct {
	name   string
	inputs []map[string]any
}

func (t *visualRecordingTool) Name() string               { return t.name }
func (t *visualRecordingTool) Description() string        { return "test tool" }
func (t *visualRecordingTool) ArgsSchema() map[string]any { return (&TouchGestureTool{}).ArgsSchema() }
func (t *visualRecordingTool) Call(_ context.Context, input string) (string, error) {
	var args map[string]any
	if err := json.Unmarshal([]byte(input), &args); err != nil {
		return "", err
	}
	t.inputs = append(t.inputs, args)
	return "ok", nil
}

func TestVisualCoordinatesToolBoundary(t *testing.T) {
	v := newVisualCoordinates(VisualCoordinateConfig{})
	first := visualTestMessage(t, 101, 201)
	v.Transform([]messages.Message{first})
	oldID := "unknown-frame"
	second := visualTestMessage(t, 101, 201)
	v.Transform([]messages.Message{first, second})
	id := v.latest
	base := &visualRecordingTool{name: "touch_gesture"}
	tool := v.wrap([]langtools.Tool{base})[0]
	for _, input := range []string{
		`{"type":"tap","point":{"x":50,"y":100}}`,
		fmt.Sprintf(`{"frame_id":%q,"point":{"x":50,"y":100}}`, oldID),
		fmt.Sprintf(`{"frame_id":%q,"point":{"x":101,"y":100}}`, id),
		fmt.Sprintf(`{"frame_id":%q,"point":{"x":-1,"y":100}}`, id),
		fmt.Sprintf(`{"frame_id":%q,"point":{"x":"50","y":100}}`, id),
	} {
		_, _ = tool.Call(context.Background(), input)
		if len(base.inputs) != 0 {
			t.Fatalf("invalid input reached device: %s", input)
		}
	}
	result, err := tool.Call(context.Background(), fmt.Sprintf(`{"frame_id":%q,"type":"swipe","start":{"x":50,"y":100},"end":{"x":100,"y":200},"speed":2500}`, id))
	if err != nil || result != "ok" || len(base.inputs) != 1 {
		t.Fatalf("call = %s, %v", result, err)
	}
	got := base.inputs[0]
	if got["frame_id"] != nil || got["speed"] != float64(2500) {
		t.Fatal(got)
	}
	if got["start"].(map[string]any)["x"] != float64(500) || got["end"].(map[string]any)["y"] != float64(1000) {
		t.Fatal(got)
	}
	v.Transform([]messages.Message{first, second})
	_, _ = tool.Call(context.Background(), fmt.Sprintf(`{"frame_id":%q,"point":{"x":50,"y":100}}`, id))
	if len(base.inputs) != 2 {
		t.Fatal("coordinate adapter imposed a new single-use restriction")
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
	v := newVisualCoordinates(VisualCoordinateConfig{})
	good := visualTestMessage(t, 100, 200)
	bad := messages.Message{Role: messages.MessageRoleUser, Attachments: []messages.Attachment{{FilePath: "/nonexistent/visual-test.png", MIMEType: "image/png", Source: messages.AttachmentSourceScreenshotObservation}}}
	out := v.Transform([]messages.Message{good, bad})
	if v.latest != "" || len(out[1].Attachments) != 0 {
		t.Fatal("bad latest image left an actionable frame")
	}
}

func TestVisualCoordinatesSchemaDoesNotMutateOriginal(t *testing.T) {
	base := &visualRecordingTool{name: "touch_gesture"}
	target := &visualCoordinateTool{Tool: base}
	schema := target.ArgsSchema()
	props := schema["properties"].(map[string]any)
	point := props["point"].(map[string]any)["properties"].(map[string]any)["x"].(map[string]any)
	if point["maximum"] != nil || props["frame_id"] == nil {
		t.Fatal(schema)
	}
	if base.ArgsSchema()["properties"].(map[string]any)["frame_id"] != nil {
		t.Fatal("underlying schema mutated")
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
	var id string
	for _, msg := range input {
		for _, part := range msg.Parts {
			if p, ok := part.(llms.TextContent); ok {
				match := regexp.MustCompile(`frame_id=(frame_[a-f0-9]+)`).FindStringSubmatch(p.Text)
				if len(match) > 1 {
					id = match[1]
				}
			}
		}
	}
	args := `{"type":"tap","point":{"x":500,"y":500}}`
	if m.provider == "anthropic" && !m.disabled {
		if id == "" {
			return nil, fmt.Errorf("no prepared image reached model")
		}
		args = fmt.Sprintf(`{"frame_id":%q,"type":"tap","point":{"x":50,"y":100}}`, id)
	} else if id != "" {
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
	v := newVisualCoordinates(VisualCoordinateConfig{})
	v.Transform([]messages.Message{screenshot})
	id := v.latest
	out := v.Transform([]messages.Message{screenshot, upload})
	if v.latest != id || out[1].Attachments[0].PreparedData != nil || out[1].Attachments[0].PreparedCaption != "" {
		t.Fatal("ordinary upload participated in screenshot adaptation")
	}
	args := map[string]any{"x": float64(50), "y": float64(100)}
	if _, err := v.convert(id, args); err != nil || args["x"] != float64(500) || args["y"] != float64(500) {
		t.Fatalf("screenshot mapping changed: %v, %v", args, err)
	}
	for i := 0; i < 2; i++ {
		if _, err := v.convert(id, map[string]any{"x": float64(50), "y": float64(100)}); err != nil {
			t.Fatal("unexpected frame consumption", err)
		}
	}
}

func TestVisualCoordinatesRebuildDoesNotRequireNewScreenshot(t *testing.T) {
	msg := visualTestMessage(t, 101, 201)
	first := newVisualCoordinates(VisualCoordinateConfig{})
	first.Transform([]messages.Message{msg})
	resumed := newVisualCoordinates(VisualCoordinateConfig{})
	resumed.Transform([]messages.Message{msg})
	if _, err := resumed.convert(first.latest, map[string]any{"x": float64(50), "y": float64(100)}); err == nil {
		t.Fatal("old run ID unexpectedly resolved")
	}
	if _, err := resumed.convert(resumed.latest, map[string]any{"x": float64(50), "y": float64(100)}); err != nil {
		t.Fatal("adapter imposed new capture requirement", err)
	}
}
