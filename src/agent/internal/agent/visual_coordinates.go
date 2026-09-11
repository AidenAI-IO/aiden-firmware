package agent

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"image"
	_ "image/jpeg"
	_ "image/png"
	"math"
	"os"
	"strings"
	"sync"
	"time"

	"aiden-agent/internal/agent/messages"
	langtools "github.com/tmc/langchaingo/tools"
)

type visualFrame struct {
	width, height int
	data          []byte
}

// visualPoint uses pixel centers in the captured screenshot. Device code receives
// only normalizedPoint; no provider-specific scaling belongs in that layer.
type visualPoint struct{ X, Y float64 }
type normalizedPoint struct{ X, Y float64 }

func (f visualFrame) normalize(point visualPoint) (normalizedPoint, error) {
	x, err := normalizeVisualAxis(point.X, f.width)
	if err != nil {
		return normalizedPoint{}, fmt.Errorf("x: %w", err)
	}
	y, err := normalizeVisualAxis(point.Y, f.height)
	if err != nil {
		return normalizedPoint{}, fmt.Errorf("y: %w", err)
	}
	return normalizedPoint{X: x, Y: y}, nil
}

func normalizeVisualAxis(value float64, size int) (float64, error) {
	if size < 1 || math.IsNaN(value) || math.IsInf(value, 0) || value < 0 || value > float64(size-1) {
		return 0, fmt.Errorf("pixel value must be between 0 and %d", size-1)
	}
	return value / float64(max(1, size-1)) * 1000, nil
}

// A registry is private to one AgentLoop.Run. Images provide coordinate spaces only. Source screenshots already represent the device active area,
// so the existing device mapping runs just once.
type visualCoordinates struct {
	mu     sync.Mutex
	frames map[string]visualFrame
	latest *visualFrame
}

func newVisualCoordinates() *visualCoordinates {
	return &visualCoordinates{frames: make(map[string]visualFrame)}
}

const visualCoordinateInstruction = "Visual coordinate protocol: for touch_gesture, mouse_move, enter_text.focus and wheel_nudge geometry, use pixel coordinates in the attached image. Do not rescale coordinates or call a normalization tool. Speed parameters retain normalized units per second."

func (v *visualCoordinates) Transform(input []messages.Message) []messages.Message {
	v.mu.Lock()
	defer v.mu.Unlock()
	out := make([]messages.Message, len(input), len(input)+1)
	frames := make(map[string]visualFrame)
	v.latest = nil
	for i, msg := range input {
		out[i] = msg.Clone()
		attachments := make([]messages.Attachment, 0, len(msg.Attachments))
		for _, attachment := range msg.Attachments {
			if attachment.Source != messages.AttachmentSourceScreenshotObservation || !strings.HasPrefix(attachment.MIMEType, "image/") {
				attachments = append(attachments, attachment)
				continue
			}
			// Include the occurrence path so identical consecutive screenshots can
			// still represent distinct observations.
			data, err := os.ReadFile(attachment.FilePath)
			key := fmt.Sprintf("%s:%x", attachment.FilePath, sha256.Sum256(data))
			frame, ok := v.frames[key]
			if err == nil && !ok {
				frame, err = v.prepare(key, data)
			}
			if err != nil {
				out[i].Content += "\n[Image unavailable: could not prepare visual frame. Request a new screenshot before acting.]"
				v.latest = nil
				continue
			}
			frames[key] = frame
			v.latest = &frame
			attachment.PreparedData = &frame.data
			attachment.PreparedCaption = fmt.Sprintf("Image dimensions: width=%d height=%d. Pixel centers span x=0..%d and y=0..%d.", frame.width, frame.height, frame.width-1, frame.height-1)
			attachments = append(attachments, attachment)
		}
		out[i].Attachments = attachments
	}
	v.frames = frames
	// This is transient, like the prepared bytes, and never enters saved history.
	out = append(out, messages.Message{Role: messages.MessageRoleSystem, Content: visualCoordinateInstruction})
	return out
}

func (v *visualCoordinates) prepare(key string, data []byte) (visualFrame, error) {
	cfg, _, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return visualFrame{}, err
	}
	if cfg.Width < 1 || cfg.Height < 1 || float64(cfg.Width)*float64(cfg.Height) > 40000000 {
		return visualFrame{}, fmt.Errorf("invalid or oversized source image")
	}
	// Validate the complete payload by decoding it; discard the result and forward the original bytes.
	if _, _, err := image.Decode(bytes.NewReader(data)); err != nil {
		return visualFrame{}, fmt.Errorf("invalid image payload: %w", err)
	}
	// Pass the original image through unchanged — no re-encode, no dimension change.
	return visualFrame{
		width:  cfg.Width,
		height: cfg.Height,
		data:   data,
	}, nil
}

// convert resolves the coordinate space using the most recent screenshot.
// Existing screenshot and device safety checks remain owned by the device tools.
func (v *visualCoordinates) convert(args map[string]any) (visualFrame, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.latest == nil {
		return visualFrame{}, fmt.Errorf("no screenshot available; request a fresh screenshot")
	}
	if err := convertVisualArguments(args, *v.latest); err != nil {
		return visualFrame{}, err
	}
	return *v.latest, nil
}

func (v *visualCoordinates) wrap(tools []langtools.Tool) []langtools.Tool {
	out := append([]langtools.Tool(nil), tools...)
	for i, tool := range out {
		switch tool.Name() {
		case "touch_gesture", "mouse_move", "enter_text", "wheel_nudge":
			out[i] = &visualCoordinateTool{Tool: tool, frames: v}
		}
	}
	return out
}

type visualCoordinateTool struct {
	langtools.Tool
	frames *visualCoordinates
}

func (t *visualCoordinateTool) ReturnsVisualObservation() bool {
	visual, ok := t.Tool.(visualObservationTool)
	return ok && visual.ReturnsVisualObservation()
}

func (t *visualCoordinateTool) SetDeviceTypeFunc(fn func() string) {
	if tool, ok := t.Tool.(runtimeDeviceTypeConfigurable); ok {
		tool.SetDeviceTypeFunc(fn)
	}
}

func (t *visualCoordinateTool) Description() string {
	description := t.Tool.Description()
	// Keep gesture semantics, but remove the old instruction to do arithmetic.
	if start := strings.Index(description, "Base coordinates on the latest screenshot"); start >= 0 {
		if end := strings.Index(description[start:], "Swipe direction names"); end >= 0 {
			description = description[:start] + description[start+end:]
		}
	}
	if t.Name() == "mouse_move" {
		description = "Move the mouse without clicking."
	}
	if t.Name() == "wheel_nudge" {
		description = "Nudge a picker wheel toward the requested value. Geometry is measured in the prepared image's pixels."
	}
	return description + " " + visualCoordinateInstruction
}

func (t *visualCoordinateTool) ArgsSchema() map[string]any {
	provider, ok := t.Tool.(interface{ ArgsSchema() map[string]any })
	if !ok {
		return nil
	}
	// Deep-copy before changing nested schemas owned by the underlying tool.
	original := provider.ArgsSchema()
	data, err := json.Marshal(original)
	if err != nil {
		return original
	}
	var schema map[string]any
	if err := json.Unmarshal(data, &schema); err != nil || schema == nil {
		return original
	}
	var rewrite func(map[string]any)
	rewrite = func(node map[string]any) {
		delete(node, "examples")
		if props, ok := node["properties"].(map[string]any); ok {
			for key, value := range props {
				child, ok := value.(map[string]any)
				if !ok {
					continue
				}
				switch key {
				case "x", "y", "column_x", "center_y", "visible_target_y", "row_spacing":
					props[key] = numberArgSchema("Pixel coordinate or spacing in the image; must be inside the image bounds.")
				default:
					rewrite(child)
				}
			}
		}
		if items, ok := node["items"].(map[string]any); ok {
			rewrite(items)
		}
	}
	rewrite(schema)
	return schema
}

func (t *visualCoordinateTool) DynamicExampleInput() string {
	// Reuse non-coordinate example fields; pixel coordinates are self-explanatory.
	return builtInToolSpecMetadata[t.Name()].ExampleInput
}

func (t *visualCoordinateTool) Call(ctx context.Context, input string) (string, error) {
	var args map[string]any
	if err := json.Unmarshal([]byte(input), &args); err != nil || args == nil {
		return toolErrorResultString(ctx, CodeInvalidArguments, "Expected a JSON object with image pixel coordinates"), nil
	}
	frame, err := t.frames.convert(args)
	if err != nil {
		return toolErrorResultf(ctx, CodeInvalidArguments, "%v", err), nil
	}
	data, err := json.Marshal(args)
	if err != nil {
		return "", err
	}
	if recorder := EpisodeRecorderFromContext(ctx); recorder != nil {
		recorder.RecordEvent(TaskEpisodeEvent{Type: "visual_coordinate_mapping", Ts: time.Now().Format(time.RFC3339Nano), Metadata: map[string]interface{}{
			"tool": t.Name(), "image_width": frame.width, "image_height": frame.height,
			"pixel_input": json.RawMessage(input), "normalized_input": json.RawMessage(data),
		}})
	}
	return t.Tool.Call(ctx, string(data))
}

func convertVisualArguments(args map[string]any, frame visualFrame) error {
	_, hasX := args["x"]
	_, hasY := args["y"]
	if hasX != hasY {
		return fmt.Errorf("pixel points require both x and y")
	}
	if hasX {
		x, xok := args["x"].(float64)
		y, yok := args["y"].(float64)
		if !xok || !yok {
			return fmt.Errorf("pixel x and y must be numbers")
		}
		point, err := frame.normalize(visualPoint{X: x, Y: y})
		if err != nil {
			return err
		}
		args["x"], args["y"] = point.X, point.Y
	}
	for key, value := range args {
		if key == "x" || key == "y" {
			continue
		}
		switch key {
		case "point", "start", "end", "focus":
			point, ok := value.(map[string]any)
			if !ok || point["x"] == nil || point["y"] == nil {
				return fmt.Errorf("%s requires a pixel point object containing x and y", key)
			}
		}
		limit := 0
		switch key {
		case "coordinate", "start_x", "start_y", "end_x", "end_y":
			return fmt.Errorf("%s is unsupported by the visual coordinate protocol; use named point/start/end objects", key)
		case "column_x":
			limit = frame.width
		case "center_y", "visible_target_y", "row_spacing":
			limit = frame.height
		}
		if limit > 0 {
			n, ok := value.(float64)
			if !ok {
				return fmt.Errorf("%s must be a pixel value between 0 and %d", key, limit-1)
			}
			normalized, err := normalizeVisualAxis(n, limit)
			if err != nil {
				return fmt.Errorf("%s: %w", key, err)
			}
			args[key] = normalized
			continue
		}
		switch child := value.(type) {
		case map[string]any:
			if err := convertVisualArguments(child, frame); err != nil {
				return err
			}
		case []any:
			for _, item := range child {
				if obj, ok := item.(map[string]any); ok {
					if err := convertVisualArguments(obj, frame); err != nil {
						return err
					}
				}
			}
		}
	}
	return nil
}
