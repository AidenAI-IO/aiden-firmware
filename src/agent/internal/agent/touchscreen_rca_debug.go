package agent

import (
	"aiden-agent/internal/agent/screen"
	"fmt"
	"log"
	"os"
	"strings"
	"sync/atomic"
)

const touchscreenRCADebugEnv = "AIDEN_TOUCHSCREEN_RCA_DEBUG"

var touchscreenRCADebugEnabledValue atomic.Bool

func init() {
	touchscreenRCADebugEnabledValue.Store(touchscreenRCADebugEnabledFromEnv())
}

func touchscreenRCALogf(format string, args ...any) {
	if !touchscreenRCADebugEnabledCached() {
		return
	}
	message := fmt.Sprintf("[touchscreen-rca] "+format, args...)
	message = strings.NewReplacer("\r", "\\r", "\n", "\\n").Replace(message)
	log.Print(message)
}

func touchscreenRCADebugEnabledCached() bool {
	return touchscreenRCADebugEnabledValue.Load()
}

func touchscreenRCADebugEnabledFromEnv() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(touchscreenRCADebugEnv))) {
	case "1", "true", "t", "yes", "y", "on":
		return true
	default:
		return false
	}
}

func formatTouchscreenRCAMetadata(meta *frameMetadata) string {
	if !touchscreenRCADebugEnabledCached() {
		return ""
	}
	if meta == nil {
		return "<nil>"
	}
	return fmt.Sprintf(
		"{seq:%d width:%d height:%d source_width:%d source_height:%d crop_x:%d crop_y:%d crop_width:%d crop_height:%d pixel_format:%q stride:%d bytes:%d stale:%v}",
		meta.Seq,
		meta.Width,
		meta.Height,
		meta.SourceWidth,
		meta.SourceHeight,
		meta.CropX,
		meta.CropY,
		meta.CropWidth,
		meta.CropHeight,
		meta.PixelFormat,
		meta.Stride,
		meta.Bytes,
		meta.Stale,
	)
}

func formatTouchscreenRCAMappingSummary(screen *screen.ScreenState) string {
	if !touchscreenRCADebugEnabledCached() {
		return ""
	}
	if screen == nil {
		return "screen=nil"
	}
	width, height, active, age, ok := screen.ActiveAreaWithAge()
	if !ok {
		return "active_area_ok=false"
	}
	return fmt.Sprintf(
		"active_area_ok=true width=%d height=%d active=%s age_ms=%d",
		width,
		height,
		active.Format(),
		age.Milliseconds(),
	)
}

func formatTouchscreenRCAPointerPoint(point *pointerPoint) string {
	if point == nil {
		return "<nil>"
	}
	return fmt.Sprintf("{x:%g y:%g}", point.X.Float64(), point.Y.Float64())
}

func touchscreenRCAPointerMode(pc *pointerController) string {
	if pc == nil {
		return "pointer=nil"
	}
	if pc.touchscreen {
		return "touchscreen"
	}
	return "absolute_mouse"
}

func formatTouchscreenRCAFloatPtr(value *float64) string {
	if value == nil {
		return "<nil>"
	}
	return fmt.Sprintf("%.3f", *value)
}

func formatTouchscreenRCAQuickActionMapping(tool *QuickActionTool) string {
	if !touchscreenRCADebugEnabledCached() {
		return ""
	}
	if tool == nil {
		return "quick_action=nil"
	}
	if tool.touch == nil {
		return "quick_action.touch=nil"
	}
	return formatTouchscreenRCAMappingSummary(tool.touch.screen)
}

func formatTouchscreenRCAQuickActionMappingSummary(tool *QuickActionTool) string {
	if !touchscreenRCADebugEnabledCached() {
		return ""
	}
	if tool == nil {
		return "quick_action=nil"
	}
	if tool.touch == nil {
		return "quick_action.touch=nil"
	}
	return formatTouchscreenRCAMappingSummary(tool.touch.screen)
}

func touchscreenRCALogResolvedPoint(label string, screen *screen.ScreenState, pc *pointerController, raw *pointerPoint, coordSpace string, resolved resolvedPointerPoint) {
	if !touchscreenRCADebugEnabledCached() {
		return
	}
	touchscreenRCALogf(
		"%s resolved raw_point=%s coord_space=%q pointer_mode=%s absolute=(%d,%d) mapping_at_resolve={%s}",
		label,
		formatTouchscreenRCAPointerPoint(raw),
		coordSpace,
		touchscreenRCAPointerMode(pc),
		resolved.x,
		resolved.y,
		formatTouchscreenRCAMappingSummary(screen),
	)
}
