package agent

import (
	"context"
	"encoding/json"
	"math"
)

const modelVisibleImageMaxEdge = 1568

// NormalizePointTool converts a point from one visual coordinate frame into
// the pointer tools' fixed normalized coordinate plane.
type NormalizePointTool struct {
	maxImageEdge int
}

func NewNormalizePointTool(provider, model string) *NormalizePointTool {
	maxImageEdge := 0
	if IsAnthropicModel(provider, model) {
		maxImageEdge = modelVisibleImageMaxEdge
	}
	return &NormalizePointTool{maxImageEdge: maxImageEdge}
}

func (t *NormalizePointTool) Name() string { return "normalize_point" }

func (t *NormalizePointTool) Description() string {
	return `Convert a target point measured directly in the model-visible screenshot into normalized pointer coordinates. Pass that visual estimate as pixel_x and pixel_y without rescaling it, plus source_width and source_height from Attached content. The tool accounts for the model's image display scaling. For a tap, copy the returned coordinate and point fields unchanged into touch_gesture; never pass the pixel values directly.`
}

func (t *NormalizePointTool) ArgsSchema() map[string]any {
	return objectArgsSchema(map[string]any{
		"pixel_x":       numberArgSchema("Target center X measured directly in the model-visible screenshot; do not rescale it to source pixels.", 130),
		"pixel_y":       numberArgSchema("Target center Y measured directly in the model-visible screenshot; do not rescale it to source pixels.", 202),
		"source_width":  minIntegerArgSchema("Actual attached image width shown as source_width in Attached content.", 1, 447),
		"source_height": minIntegerArgSchema("Actual attached image height shown as source_height in Attached content.", 1, 972),
	}, "pixel_x", "pixel_y", "source_width", "source_height")
}

func (t *NormalizePointTool) Call(ctx context.Context, input string) (string, error) {
	var args struct {
		PixelX       float64 `json:"pixel_x"`
		PixelY       float64 `json:"pixel_y"`
		SourceWidth  int     `json:"source_width"`
		SourceHeight int     `json:"source_height"`
	}
	if err := decodeStrictJSONObject(input, &args); err != nil {
		return toolErrorResultf(ctx, CodeInvalidArguments, "invalid input: %v. Expected JSON format: {\"pixel_x\": 130, \"pixel_y\": 202, \"source_width\": 447, \"source_height\": 972}", err), nil
	}
	if math.IsNaN(args.PixelX) || math.IsInf(args.PixelX, 0) || math.IsNaN(args.PixelY) || math.IsInf(args.PixelY, 0) {
		return toolErrorResultString(ctx, CodeInvalidArguments, "pixel_x and pixel_y must be finite numbers"), nil
	}
	if args.SourceWidth <= 0 || args.SourceHeight <= 0 {
		return toolErrorResultString(ctx, CodeInvalidArguments, "source_width and source_height must be positive integers"), nil
	}
	coordinateWidth, coordinateHeight := modelVisibleImageDimensions(args.SourceWidth, args.SourceHeight, t.maxImageEdge)
	if args.PixelX < 0 || args.PixelX > float64(coordinateWidth-1) || args.PixelY < 0 || args.PixelY > float64(coordinateHeight-1) {
		return toolErrorResultString(ctx, CodeInvalidArguments, "pixel_x and pixel_y must be inside the model-visible screenshot bounds; use the visual estimate directly without converting it to source-image pixels"), nil
	}
	out, err := json.Marshal(map[string]any{
		"coordinate": "normalized",
		"point": map[string]any{
			"x": math.Round(args.PixelX / math.Max(float64(coordinateWidth-1), 1) * 1000),
			"y": math.Round(args.PixelY / math.Max(float64(coordinateHeight-1), 1) * 1000),
		},
	})
	if err != nil {
		return toolErrorResultf(ctx, CodeToolExecutionFailed, "encode normalized point: %v", err), nil
	}
	return string(out), nil
}

func modelVisibleImageDimensions(width, height, maxEdge int) (int, int) {
	longEdge := max(width, height)
	if maxEdge <= 0 || longEdge <= maxEdge {
		return width, height
	}
	return (width*maxEdge + longEdge/2) / longEdge,
		(height*maxEdge + longEdge/2) / longEdge
}
