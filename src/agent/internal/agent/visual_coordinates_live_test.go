package agent

// Opt-in real-provider perception evaluation. This uses the production model
// client, outbound transform, tool schema, and coordinate adapter. The receiving
// tool records the normalized point instead of sending input to a real device.
import (
	"context"
	"encoding/json"
	"fmt"
	"image"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"aiden-agent/internal/agent/contextmanager"
	"aiden-agent/internal/agent/executor"
	"aiden-agent/internal/agent/messages"
	"github.com/tmc/langchaingo/llms"
	langtools "github.com/tmc/langchaingo/tools"
)

type livePerceptionTask struct {
	ID     string `json:"id"`
	Prompt string `json:"prompt"`
	Image  string `json:"input_screenshot"`
	Rubric []struct {
		Check string `json:"check"`
	} `json:"rubric"`
}

type livePerceptionResult struct {
	Task            string         `json:"task"`
	Variant         string         `json:"variant"`
	Repeat          int            `json:"repeat"`
	Model           string         `json:"model"`
	LatencyMS       int64          `json:"latency_ms"`
	Bounds          [4]float64     `json:"bounds"`
	ModelInput      string         `json:"model_input,omitempty"`
	NormalizedInput map[string]any `json:"normalized_input,omitempty"`
	Hit             bool           `json:"hit"`
	Error           string         `json:"error,omitempty"`
	Response        string         `json:"response,omitempty"`
	Usage           map[string]any `json:"usage,omitempty"`
}

func TestVisualCoordinatesLive(t *testing.T) {
	if os.Getenv("AIDEN_VISUAL_LIVE") != "1" {
		t.Skip("set AIDEN_VISUAL_LIVE=1 to make paid real-model requests")
	}
	base, token := os.Getenv("ANTHROPIC_BASE_URL"), os.Getenv("ANTHROPIC_AUTH_TOKEN")
	modelName := os.Getenv("AIDEN_VISUAL_MODEL")
	output := os.Getenv("AIDEN_VISUAL_RESULTS")
	if base == "" || token == "" || modelName == "" || output == "" {
		t.Fatal("requires ANTHROPIC_BASE_URL, ANTHROPIC_AUTH_TOKEN, AIDEN_VISUAL_MODEL, AIDEN_VISUAL_RESULTS")
	}
	if err := os.MkdirAll(output, 0700); err != nil {
		t.Fatal(err)
	}
	repeats := 1
	if raw := os.Getenv("AIDEN_VISUAL_REPEATS"); raw != "" {
		var err error
		repeats, err = strconv.Atoi(raw)
		if err != nil || repeats < 1 || repeats > 10 {
			t.Fatal("invalid repeat count")
		}
	}
	suitePath, err := filepath.Abs("../../../../benchmark/suites/perception/perception_v1.json")
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(suitePath)
	if err != nil {
		t.Fatal(err)
	}
	var suite struct {
		PromptPrefix string               `json:"prompt_prefix"`
		Tasks        []livePerceptionTask `json:"tasks"`
	}
	if err = json.Unmarshal(data, &suite); err != nil {
		t.Fatal(err)
	}
	slots := make(chan struct{}, 3)
	variants := []string{"baseline", "prepared"}
	if os.Getenv("AIDEN_VISUAL_ABLATION") == "1" {
		variants = []string{"original_normalized", "small_normalized", "original_pixel", "small_pixel"}
	}
	for repeat := 1; repeat <= repeats; repeat++ {
		for _, task := range suite.Tasks {
			if filter := os.Getenv("AIDEN_VISUAL_TASK"); filter != "" && task.ID != filter {
				continue
			}
			for _, variant := range variants {
				t.Run(fmt.Sprintf("%s/%s/%d", task.ID, variant, repeat), func(t *testing.T) {
					t.Parallel()
					slots <- struct{}{}
					defer func() { <-slots }()
					result := livePerceptionResult{Task: task.ID, Variant: variant, Repeat: repeat, Model: modelName}
					defer func() {
						data, err := json.MarshalIndent(result, "", "  ")
						if err != nil {
							t.Error(err)
							return
						}
						if err = os.WriteFile(filepath.Join(output, fmt.Sprintf("%s-%s-%02d.json", task.ID, variant, repeat)), data, 0600); err != nil {
							t.Error(err)
						}
						t.Logf("hit=%t latency_ms=%d error=%s", result.Hit, result.LatencyMS, result.Error)
					}()
					for _, rubric := range task.Rubric {
						matches := regexp.MustCompile(`\[(\d+),\s*(\d+)\]`).FindAllStringSubmatch(rubric.Check, -1)
						if len(matches) == 2 {
							for i, m := range matches {
								result.Bounds[i*2], _ = strconv.ParseFloat(m[1], 64)
								result.Bounds[i*2+1], _ = strconv.ParseFloat(m[2], 64)
							}
						}
					}
					if result.Bounds == [4]float64{} {
						t.Fatal("missing target bounds")
					}
					imagePath := filepath.Join(filepath.Dir(suitePath), task.Image)
					f, err := os.Open(imagePath)
					if err != nil {
						t.Fatal(err)
					}
					size, _, err := image.DecodeConfig(f)
					f.Close()
					if err != nil {
						t.Fatal(err)
					}
					input := []messages.Message{
						{Role: messages.MessageRoleSystem, Content: defaultAgentBehavior() + "\nThis is a single-frame perception evaluation. Locate the requested target in the attached screenshot and make exactly one touch_gesture tap. No screenshot, skill lookup, probing, or retry is available in this evaluation."},
						{Role: messages.MessageRoleUser, Content: suite.PromptPrefix + "\n" + task.Prompt + fmt.Sprintf("\nAttached screenshot source_width=%d source_height=%d.", size.Width, size.Height), Attachments: []messages.Attachment{{FilePath: imagePath, MIMEType: "image/jpeg", Source: messages.AttachmentSourceScreenshotObservation}}},
					}
					manager, err := contextmanager.NewContextManagerFromMessageList(t.TempDir(), input)
					if err != nil {
						t.Fatal(err)
					}
					backend := &visualRecordingTool{name: "touch_gesture"}
					var tool langtools.Tool = &livePerceptionTool{visualRecordingTool: backend}
					var transforms []executor.OutboundMessageTransform
					if os.Getenv("AIDEN_VISUAL_ABLATION") == "1" {
						config := VisualCoordinateConfig{}
						if strings.HasPrefix(variant, "original_") {
							config.MaxEdge = 40000
							config.MaxPixels = 40000000
						}
						frames := newVisualCoordinates(config)
						pixel := strings.HasSuffix(variant, "_pixel")
						transforms = append(transforms, liveAblationTransform{frames: frames})
						var receiver langtools.Tool = backend
						if pixel {
							receiver = frames.wrap([]langtools.Tool{backend})[0]
						}
						tool = &liveAblationTool{Tool: receiver, pixel: pixel}
					}
					if variant == "prepared" {
						frames := newVisualCoordinates(VisualCoordinateConfig{})
						transforms = append(transforms, frames)
						tool = frames.wrap([]langtools.Tool{tool})[0]
					}
					client := newAnthropicModel(base, modelName, token, &http.Client{Timeout: 120 * time.Second}).(*anthropicModel)
					client.streamMaxRetries = 0
					client.protocolRetries = 0
					exec := executor.NewLLMExecutor(client, manager, transforms...)
					ctx, cancel := context.WithTimeout(context.Background(), 125*time.Second)
					defer cancel()
					started := time.Now()
					response, err := exec.GenerateContent(ctx, llms.WithTools([]llms.Tool{NewToolSpec(tool).LLMTool()}), llms.WithMaxTokens(4096), llms.WithTemperature(0))
					result.LatencyMS = time.Since(started).Milliseconds()
					if err != nil {
						result.Error = err.Error()
						return
					}
					if len(response.Choices) == 0 {
						result.Error = "empty choices"
						return
					}
					choice := response.Choices[0]
					result.Response = choice.Content
					result.Usage = choice.GenerationInfo
					if len(choice.ToolCalls) != 1 || choice.ToolCalls[0].FunctionCall == nil {
						result.Error = "expected exactly one tool call"
						return
					}
					call := choice.ToolCalls[0].FunctionCall
					result.ModelInput = call.Arguments
					if call.Name != "touch_gesture" {
						result.Error = "unexpected tool: " + call.Name
						return
					}
					out, err := tool.Call(ctx, call.Arguments)
					if err != nil {
						result.Error = err.Error()
						return
					}
					if len(backend.inputs) != 1 {
						result.Error = strings.TrimSpace(out)
						return
					}
					result.NormalizedInput = backend.inputs[0]
					if result.NormalizedInput["type"] != "tap" {
						result.Error = "not a tap"
						return
					}
					point, ok := result.NormalizedInput["point"].(map[string]any)
					if !ok {
						result.Error = "missing point"
						return
					}
					x, xok := point["x"].(float64)
					y, yok := point["y"].(float64)
					result.Hit = xok && yok && x >= result.Bounds[0] && x <= result.Bounds[1] && y >= result.Bounds[2] && y <= result.Bounds[3]
				})
			}
		}
	}
}

type livePerceptionTool struct{ *visualRecordingTool }

func (t *livePerceptionTool) Description() string { return (&TouchGestureTool{}).Description() }

// Factorial controls: identical prompt, caption, JPEG encoder and minimal tap
// schema in all four cells. Only resizing and coordinate units differ.
type liveAblationTransform struct{ frames *visualCoordinates }

func (a liveAblationTransform) Transform(input []messages.Message) []messages.Message {
	out := a.frames.Transform(input)
	out = out[:len(out)-1] // remove production pixel-specific system guidance
	for i := range out {
		for j := range out[i].Attachments {
			attachment := &out[i].Attachments[j]
			for _, frame := range a.frames.frames {
				if strings.Contains(attachment.PreparedCaption, frame.id) {
					attachment.PreparedCaption = fmt.Sprintf("Attached image frame_id=%s image_width=%d image_height=%d. These are the actual attached image dimensions.", frame.id, frame.width, frame.height)
				}
			}
		}
	}
	return out
}

type liveAblationTool struct {
	langtools.Tool
	pixel bool
}

func (t *liveAblationTool) Description() string {
	unit := "normalized 0-1000 coordinates, where (0,0) is top-left and (1000,1000) is bottom-right"
	if t.pixel {
		unit = "pixel coordinates in the attached image, where (0,0) is top-left and (image_width-1,image_height-1) is bottom-right"
	}
	return "Tap the requested target's visible center using " + unit + ". Include the frame_id from the image caption."
}
func (t *liveAblationTool) ArgsSchema() map[string]any {
	return objectArgsSchema(map[string]any{
		"type":     stringEnumArgSchema("Gesture", "tap"),
		"frame_id": stringArgSchema("Frame ID from the image caption"),
		"point":    objectArgsSchema(map[string]any{"x": numberArgSchema("X in the tool's coordinate system"), "y": numberArgSchema("Y in the tool's coordinate system")}, "x", "y"),
	}, "type", "frame_id", "point")
}
