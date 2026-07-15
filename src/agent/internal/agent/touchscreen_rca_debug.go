package agent

import (
	"fmt"
	"log"
	"os"
	"strings"
	"time"
)

const touchscreenRCADebugEnv = "AIDEN_TOUCHSCREEN_RCA_DEBUG"

func touchscreenRCALogf(format string, args ...any) {
	if !touchscreenRCADebugEnabled() {
		return
	}
	message := fmt.Sprintf("[touchscreen-rca] "+format, args...)
	message = strings.NewReplacer("\r", "\\r", "\n", "\\n").Replace(message)
	log.Print(message)
}

func touchscreenRCADebugEnabled() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(touchscreenRCADebugEnv))) {
	case "1", "true", "t", "yes", "y", "on":
		return true
	default:
		return false
	}
}

func formatTouchscreenRCAActiveArea(active screenActiveArea) string {
	return fmt.Sprintf("{x:%d y:%d w:%d h:%d valid:%v}", active.X, active.Y, active.Width, active.Height, active.Valid)
}

func formatTouchscreenRCAMetadata(meta *frameMetadata) string {
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

func formatTouchscreenRCAScreenMapping(screen *screenState) string {
	if screen == nil {
		return "screen=nil"
	}

	width, height, active, age, ok := screen.ActiveAreaWithAge()
	state := screen.MappingState()
	phoneScreen := screen.PhoneScreenInfo()
	phoneScreenText := formatPhoneScreen(phoneScreen)
	if phoneScreenText == "" {
		phoneScreenText = "<empty>"
	}

	rawAge := "unset"
	if !state.updatedAt.IsZero() {
		rawAge = fmt.Sprintf("%d", time.Since(state.updatedAt).Milliseconds())
	}
	effectiveAge := "unset"
	if ok {
		effectiveAge = fmt.Sprintf("%d", age.Milliseconds())
	}

	return fmt.Sprintf(
		"active_area_with_age_ok=%v effective_width=%d effective_height=%d effective_active=%s effective_age_ms=%s raw_width=%d raw_height=%d raw_active=%s raw_age_ms=%s phone_screen=%q",
		ok,
		width,
		height,
		formatTouchscreenRCAActiveArea(active),
		effectiveAge,
		state.width,
		state.height,
		formatTouchscreenRCAActiveArea(state.active),
		rawAge,
		phoneScreenText,
	)
}

func formatTouchscreenRCAMappingSummary(screen *screenState) string {
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
		formatTouchscreenRCAActiveArea(active),
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
	if tool == nil {
		return "quick_action=nil"
	}
	if tool.touch == nil {
		return "quick_action.touch=nil"
	}
	return formatTouchscreenRCAScreenMapping(tool.touch.screen)
}

func formatTouchscreenRCAQuickActionMappingSummary(tool *QuickActionTool) string {
	if tool == nil {
		return "quick_action=nil"
	}
	if tool.touch == nil {
		return "quick_action.touch=nil"
	}
	return formatTouchscreenRCAMappingSummary(tool.touch.screen)
}

func touchscreenRCALogResolvedPoint(label string, screen *screenState, pc *pointerController, raw *pointerPoint, coordSpace string, resolved resolvedPointerPoint) {
	touchscreenRCALogf(
		"%s resolved raw_point=%s coord_space=%q pointer_mode=%s absolute=(%d,%d) mapping_at_resolve={%s}",
		label,
		formatTouchscreenRCAPointerPoint(raw),
		coordSpace,
		touchscreenRCAPointerMode(pc),
		resolved.x,
		resolved.y,
		formatTouchscreenRCAScreenMapping(screen),
	)
}
