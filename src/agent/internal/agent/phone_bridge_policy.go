package agent

import (
	"fmt"
	"strings"
	"time"
)

const phoneBridgeBackgroundStateMaxAge = 15 * time.Second

const (
	toolBridgeOpenApp      = "bridge_open_app"
	toolBridgeClipboard    = "bridge_clipboard"
	toolBridgeCalendar     = "bridge_calendar"
	toolBridgeContacts     = "bridge_contacts"
	toolBridgeNotification = "bridge_notification"
)

var phoneBridgeBackgroundSafeCommandTypes = map[string]struct{}{
	"clipboard_read":    {},
	"clipboard_write":   {},
	"calendar_create":   {},
	"calendar_query":    {},
	"calendar_delete":   {},
	"contacts_query":    {},
	"contacts_create":   {},
	"contacts_update":   {},
	"notification_send": {},
}

var phoneBridgeToolBackgroundCommandTypes = map[string]string{
	toolBridgeClipboard:    "clipboard_read",
	toolBridgeCalendar:     "calendar_query",
	toolBridgeContacts:     "contacts_query",
	toolBridgeNotification: "notification_send",
}

func isPhoneBridgeToolName(name string) bool {
	switch name {
	case toolBridgeOpenApp, toolBridgeClipboard, toolBridgeCalendar, toolBridgeContacts, toolBridgeNotification:
		return true
	default:
		return false
	}
}

func phoneBridgeToolAvailable(status PhoneBridgeStatus, name string) bool {
	switch name {
	case toolBridgeOpenApp:
		return !phoneBridgePiPBackgroundEnabled(status) && !phoneBridgeFGSBackgroundEnabled(status)
	case toolBridgeClipboard, toolBridgeCalendar, toolBridgeContacts, toolBridgeNotification:
		return phoneBridgeReadyForCommand(status) ||
			phoneBridgeCanUsePiPBackground(status, phoneBridgeBackgroundCommandTypeForTool(name)) ||
			phoneBridgeCanUseFGSBackground(status, phoneBridgeBackgroundCommandTypeForTool(name))
	default:
		return true
	}
}

func phoneBridgeReadyForCommand(status PhoneBridgeStatus) bool {
	if !status.Connected {
		return false
	}
	if !strings.EqualFold(strings.TrimSpace(status.Platform), "ios") {
		return true
	}
	state := strings.ToLower(strings.TrimSpace(status.AppState))
	return state == "" || state == "active"
}

func normalizeAppState(value string) (string, bool) {
	state := strings.ToLower(strings.TrimSpace(value))
	switch state {
	case "active", "background", "inactive":
		return state, true
	default:
		return "", false
	}
}

func phoneBridgeCanUsePiPBackground(status PhoneBridgeStatus, commandType string) bool {
	if !phoneBridgeBackgroundSafeCommandType(commandType) {
		return false
	}
	if !phoneBridgePiPBackgroundEnabled(status) {
		return false
	}
	if status.AppStateUpdatedAt == nil || time.Since(*status.AppStateUpdatedAt) > phoneBridgeBackgroundStateMaxAge {
		return false
	}
	return true
}

func phoneBridgeCanUseFGSBackground(status PhoneBridgeStatus, commandType string) bool {
	if !phoneBridgeBackgroundSafeCommandType(commandType) {
		return false
	}
	if !phoneBridgeFGSBackgroundEnabled(status) {
		return false
	}
	if status.FgsBridgeUpdatedAt == nil || time.Since(*status.FgsBridgeUpdatedAt) > phoneBridgeBackgroundStateMaxAge {
		return false
	}
	return true
}

func phoneBridgeBackgroundSafeCommandType(commandType string) bool {
	_, ok := phoneBridgeBackgroundSafeCommandTypes[strings.TrimSpace(commandType)]
	return ok
}

func phoneBridgePiPBackgroundEnabled(status PhoneBridgeStatus) bool {
	if status.PipBridgeEnabled == nil || !*status.PipBridgeEnabled {
		return false
	}
	state := strings.ToLower(strings.TrimSpace(status.AppState))
	return state == "" || state == "background" || state == "inactive"
}

func phoneBridgeFGSBackgroundEnabled(status PhoneBridgeStatus) bool {
	if status.FgsBridgeEnabled == nil || !*status.FgsBridgeEnabled {
		return false
	}
	if !strings.EqualFold(strings.TrimSpace(status.Platform), "android") {
		return false
	}
	state := strings.ToLower(strings.TrimSpace(status.AppState))
	return state == "background" || state == "inactive"
}

func phoneBridgeBackgroundCommandTypeForTool(name string) string {
	return phoneBridgeToolBackgroundCommandTypes[strings.TrimSpace(name)]
}

func phoneBridgeCanRestoreFromReturnEntry(status PhoneBridgeStatus) bool {
	if phoneBridgePiPBackgroundEnabled(status) {
		return false
	}
	if status.ReturnEntryAvailable == nil || !*status.ReturnEntryAvailable {
		return false
	}
	entry := strings.ToLower(strings.TrimSpace(status.ReturnEntry))
	return entry == "dynamic_island"
}

func phoneBridgeAppNeedsForeground(status PhoneBridgeStatus) bool {
	appState := strings.ToLower(strings.TrimSpace(status.AppState))
	return appState == "background" || appState == "inactive"
}

func phoneBridgeHasReturnEntry(status PhoneBridgeStatus) bool {
	if phoneBridgePiPBackgroundEnabled(status) {
		return false
	}
	if status.ReturnEntryAvailable != nil {
		return *status.ReturnEntryAvailable
	}
	entry := strings.ToLower(strings.TrimSpace(status.ReturnEntry))
	return entry != "" && entry != "none"
}

func phoneBridgeRecoveryGuidance(status PhoneBridgeStatus) string {
	if phoneBridgeCanRestoreFromReturnEntry(status) {
		return "Retry the companion app tool; it can reopen Aiden through the Dynamic Island entry, wait for Phone Bridge to reconnect, then send the command. Use home-screen search or HID fallback only if restore fails."
	}
	return "Use screenshot plus HID/touch fallback; only visible Dynamic Island return entries are auto-tapped, and lock-screen Live Activity or PiP-hidden entries need visual confirmation."
}

func phoneBridgeRestoreUnavailableError(status PhoneBridgeStatus) error {
	if phoneBridgeFGSBackgroundEnabled(status) {
		state := strings.TrimSpace(status.AppState)
		if state == "" {
			state = "background"
		}
		return fmt.Errorf("phone bridge app is %s with Android FGS Bridge enabled, but this command requires the companion app in foreground", state)
	}
	if phoneBridgePiPBackgroundEnabled(status) {
		state := strings.TrimSpace(status.AppState)
		if state == "" {
			state = "background"
		}
		return fmt.Errorf("phone bridge app is %s with PiP Bridge mode enabled, but this command requires the companion app in foreground", state)
	}
	if status.Connected {
		state := strings.TrimSpace(status.AppState)
		if state == "" {
			return fmt.Errorf("phone bridge is connected but not ready for foreground command")
		}
		return fmt.Errorf("phone bridge app is %s and no supported Dynamic Island return entry is available", state)
	}
	return fmt.Errorf("phone bridge not connected and no supported Dynamic Island return entry is available")
}
