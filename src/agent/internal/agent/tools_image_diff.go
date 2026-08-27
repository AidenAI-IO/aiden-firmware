package agent

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"image"
	"image/jpeg"
	"math"
	"path/filepath"
	"strings"
)

type imageDiffAttachmentResolver func(string) ([]byte, error)

type imageDiffAttachmentResolverContextKey struct{}

func withImageDiffAttachmentResolver(ctx context.Context, resolver imageDiffAttachmentResolver) context.Context {
	if resolver == nil {
		return ctx
	}
	return context.WithValue(ctx, imageDiffAttachmentResolverContextKey{}, resolver)
}

func imageDiffAttachmentResolverFromContext(ctx context.Context) imageDiffAttachmentResolver {
	if ctx == nil {
		return nil
	}
	resolver, _ := ctx.Value(imageDiffAttachmentResolverContextKey{}).(imageDiffAttachmentResolver)
	return resolver
}

// ImageDiffTool compares two JPEG screenshots and returns pixel-level difference
// metrics. Agent calls normally reference persisted screenshot attachments by
// filename; direct HTTP callers may continue to pass Base64 JPEG data.
type ImageDiffTool struct{}

func (t *ImageDiffTool) Name() string { return "image_diff" }

func (t *ImageDiffTool) Description() string {
	return `Compare two JPEG screenshots and return pixel-level difference metrics. ` +
		`Tool Lab and internal/script callers may pass screenshot_attachment_id values from the current context, or Base64 JPEG data returned by the screenshot tool. ` +
		`"region" is optional normalized coordinates (0-1000) to restrict comparison to a sub-region — use this to focus on the scrollable area and ignore static UI chrome. ` +
		`Returns: ` +
		`"changed" (bool, true when diff_ratio > 0.01), ` +
		`"diff_ratio" (0-1 fraction of pixels that changed significantly), ` +
		`"primary_axis" ("horizontal", "vertical", or "none" — dominant direction of change). ` +
		`Use this tool after a touch_gesture swipe to verify the gesture had an effect: ` +
		`diff_ratio < 0.03 means the content did not move (increase swipe distance or check target region); ` +
		`primary_axis helps confirm the swipe direction matched the intended scroll axis.`
}

func (t *ImageDiffTool) ArgsSchema() map[string]any {
	regionSchema := objectArgsSchema(map[string]any{
		"x": coordinateSchema("Normalized region left coordinate.", 100),
		"y": coordinateSchema("Normalized region top coordinate.", 200),
		"w": coordinateSchema("Normalized region width.", 600),
		"h": coordinateSchema("Normalized region height.", 400),
	}, "x", "y", "w", "h")
	regionSchema["examples"] = []map[string]any{{"x": 100, "y": 200, "w": 600, "h": 400}}

	return objectArgsSchema(map[string]any{
		"before": stringArgSchema("Earlier screenshot_attachment_id copied from the visual observation, or Base64 JPEG data for direct HTTP calls."),
		"after":  stringArgSchema("Later screenshot_attachment_id copied from the visual observation, or Base64 JPEG data for direct HTTP calls."),
		"region": regionSchema,
	}, "before", "after")
}

func (t *ImageDiffTool) Call(ctx context.Context, input string) (string, error) {
	var args struct {
		Before string           `json:"before"`
		After  string           `json:"after"`
		Region *imageDiffRegion `json:"region"`
	}
	if err := json.Unmarshal([]byte(input), &args); err != nil {
		return toolErrorResultf(ctx, CodeInvalidArguments, "invalid input: %v. Expected JSON format: {\"before\": \"<screenshot_attachment_id>\", \"after\": \"<screenshot_attachment_id>\", \"region\": {\"x\": 300, \"y\": 200, \"w\": 400, \"h\": 600}}", err), nil
	}
	if strings.TrimSpace(args.Before) == "" {
		return toolErrorResultString(ctx, CodeInvalidArguments, "before is required"), nil
	}
	if strings.TrimSpace(args.After) == "" {
		return toolErrorResultString(ctx, CodeInvalidArguments, "after is required"), nil
	}

	beforeData, err := resolveImageDiffInput(ctx, args.Before)
	if err != nil {
		return toolErrorResultf(ctx, CodeInvalidArguments, "resolve before: %v", err), nil
	}
	afterData, err := resolveImageDiffInput(ctx, args.After)
	if err != nil {
		return toolErrorResultf(ctx, CodeInvalidArguments, "resolve after: %v", err), nil
	}

	if len(beforeData) < 2 || beforeData[0] != 0xFF || beforeData[1] != 0xD8 {
		return toolErrorResultString(ctx, CodeInvalidArguments, "before is not JPEG format (image_diff only supports JPEG). Use a screenshot_attachment_id or the 'data' field from screenshot tool results"), nil
	}
	if len(afterData) < 2 || afterData[0] != 0xFF || afterData[1] != 0xD8 {
		return toolErrorResultString(ctx, CodeInvalidArguments, "after is not JPEG format (image_diff only supports JPEG). Use a screenshot_attachment_id or the 'data' field from screenshot tool results"), nil
	}

	beforeImg, err := jpeg.Decode(bytes.NewReader(beforeData))
	if err != nil {
		return toolErrorResultf(ctx, CodeInvalidArguments, "decode before JPEG: %v", err), nil
	}
	afterImg, err := jpeg.Decode(bytes.NewReader(afterData))
	if err != nil {
		return toolErrorResultf(ctx, CodeInvalidArguments, "decode after JPEG: %v", err), nil
	}

	fullBounds := beforeImg.Bounds()
	if afterImg.Bounds() != fullBounds {
		return toolErrorResultString(ctx, CodeInvalidArguments, "before and after images have different dimensions"), nil
	}

	bounds := fullBounds
	if args.Region != nil {
		var err error
		bounds, err = args.Region.toPixelRect(fullBounds)
		if err != nil {
			return toolErrorResultf(ctx, CodeInvalidArguments, "%v", err), nil
		}
	}

	result, err := computeImageDiff(beforeImg, afterImg, bounds)
	if err != nil {
		return toolErrorResultf(ctx, CodeToolExecutionFailed, "%v", err), nil
	}

	out, _ := json.Marshal(result)
	return string(out), nil
}

func resolveImageDiffInput(ctx context.Context, input string) ([]byte, error) {
	value := strings.TrimSpace(input)
	if isScreenshotAttachmentID(value) {
		resolver := imageDiffAttachmentResolverFromContext(ctx)
		if resolver == nil {
			return nil, fmt.Errorf("screenshot attachment %q is unavailable outside an active Agent context", value)
		}
		data, err := resolver(value)
		if err != nil {
			return nil, fmt.Errorf("screenshot attachment %q: %w", value, err)
		}
		return data, nil
	}
	data, err := base64.StdEncoding.DecodeString(value)
	if err != nil {
		return nil, fmt.Errorf("expected a screenshot_attachment_id or Base64 JPEG: %w", err)
	}
	return data, nil
}

func isScreenshotAttachmentID(value string) bool {
	if value == "" || filepath.Base(value) != value || strings.ContainsAny(value, `/\\`) {
		return false
	}
	switch strings.ToLower(filepath.Ext(value)) {
	case ".jpg", ".jpeg", ".png", ".webp", ".gif":
		return true
	default:
		return false
	}
}

type imageDiffRegion struct {
	X float64 `json:"x"`
	Y float64 `json:"y"`
	W float64 `json:"w"`
	H float64 `json:"h"`
}

func (r *imageDiffRegion) toPixelRect(full image.Rectangle) (image.Rectangle, error) {
	if !finiteNormalized(r.X) || !finiteNormalized(r.Y) || !finiteNormalized(r.W) || !finiteNormalized(r.H) {
		return image.Rectangle{}, fmt.Errorf("region x/y/w/h must be finite normalized values in [0,1000]")
	}
	if r.W <= 0 || r.H <= 0 {
		return image.Rectangle{}, fmt.Errorf("region w/h must be greater than 0")
	}

	w := full.Dx()
	h := full.Dy()
	x0 := clampInt(full.Min.X+int(math.Round(r.X/1000.0*float64(w))), full.Min.X, full.Max.X)
	y0 := clampInt(full.Min.Y+int(math.Round(r.Y/1000.0*float64(h))), full.Min.Y, full.Max.Y)
	x1 := clampInt(full.Min.X+int(math.Round((r.X+r.W)/1000.0*float64(w))), full.Min.X, full.Max.X)
	y1 := clampInt(full.Min.Y+int(math.Round((r.Y+r.H)/1000.0*float64(h))), full.Min.Y, full.Max.Y)
	return image.Rect(x0, y0, x1, y1), nil
}

func finiteNormalized(value float64) bool {
	return !math.IsNaN(value) && !math.IsInf(value, 0) && value >= 0 && value <= 1000
}

type imageDiffResult struct {
	Changed     bool    `json:"changed"`
	DiffRatio   float64 `json:"diff_ratio"`
	PrimaryAxis string  `json:"primary_axis"`
}

// diffThreshold is the per-channel absolute difference that counts as a changed
// pixel. JPEG compression introduces ~5-10 unit noise; 15 gives a comfortable
// margin while still catching real content movement.
const (
	diffThreshold = 15
	// imageDiffChangedRatioThreshold requires more than 1% of compared pixels
	// to change before the screen is considered meaningfully different.
	imageDiffChangedRatioThreshold = 0.01
)

func computeImageDiff(before, after image.Image, bounds image.Rectangle) (*imageDiffResult, error) {
	w := bounds.Dx()
	h := bounds.Dy()
	if w <= 0 || h <= 0 {
		return &imageDiffResult{PrimaryAxis: "none"}, nil
	}

	totalPixels := w * h
	changedPixels := 0

	// Row and column changed-pixel counts for axis detection.
	rowChanged := make([]int, h)
	colChanged := make([]int, w)

	for py := 0; py < h; py++ {
		for px := 0; px < w; px++ {
			imgX := bounds.Min.X + px
			imgY := bounds.Min.Y + py

			br, bg, bb, _ := before.At(imgX, imgY).RGBA()
			ar, ag, ab, _ := after.At(imgX, imgY).RGBA()

			// RGBA() returns values in [0, 65535]; shift to [0, 255].
			dr := absDiff(int(br>>8), int(ar>>8))
			dg := absDiff(int(bg>>8), int(ag>>8))
			db := absDiff(int(bb>>8), int(ab>>8))

			if dr > diffThreshold || dg > diffThreshold || db > diffThreshold {
				changedPixels++
				rowChanged[py]++
				colChanged[px]++
			}
		}
	}

	diffRatio := float64(changedPixels) / float64(totalPixels)
	primaryAxis := detectPrimaryAxis(rowChanged, colChanged, changedPixels)

	return &imageDiffResult{
		Changed:     diffRatio > imageDiffChangedRatioThreshold,
		DiffRatio:   math.Round(diffRatio*1000) / 1000,
		PrimaryAxis: primaryAxis,
	}, nil
}

// detectPrimaryAxis decides whether changed pixels are distributed more along
// rows (vertical scroll) or columns (horizontal scroll).
//
// For a vertical swipe the changed region spans many rows but is narrow in
// columns (the content moved up/down). For a horizontal swipe the opposite
// holds. We compare the standard deviation of the two distributions: higher
// std-dev means the changes are concentrated in fewer rows/columns, which
// indicates motion along that axis.
func detectPrimaryAxis(rowChanged, colChanged []int, totalChanged int) string {
	if totalChanged == 0 {
		return "none"
	}

	rowStd := stdDev(rowChanged)
	colStd := stdDev(colChanged)

	const minStd = 0.5 // ignore noise
	if rowStd < minStd && colStd < minStd {
		return "none"
	}

	// Higher std-dev in rows → changes concentrated in certain rows → vertical motion.
	// Higher std-dev in cols → changes concentrated in certain cols → horizontal motion.
	if rowStd > colStd*1.3 {
		return "vertical"
	}
	if colStd > rowStd*1.3 {
		return "horizontal"
	}
	return "none"
}

func stdDev(values []int) float64 {
	if len(values) == 0 {
		return 0
	}
	var sum float64
	for _, v := range values {
		sum += float64(v)
	}
	mean := sum / float64(len(values))
	var variance float64
	for _, v := range values {
		d := float64(v) - mean
		variance += d * d
	}
	variance /= float64(len(values))
	return math.Sqrt(variance)
}

func absDiff(a, b int) int {
	if a > b {
		return a - b
	}
	return b - a
}
