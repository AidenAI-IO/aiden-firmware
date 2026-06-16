package agent

import (
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"
)

var promptNow = time.Now
var promptHostRuntimeInfo = detectHostRuntimeInfo

type hostRuntimeInfo struct {
	OperatingSystem string
	Hostname        string
	Architecture    string
}

func currentDateContext() string {
	return formatCurrentDate(promptNow())
}

func hostRuntimeInfoContext() string {
	infoFn := promptHostRuntimeInfo
	if infoFn == nil {
		infoFn = detectHostRuntimeInfo
	}
	return formatHostRuntimeInfo(infoFn())
}

func formatHostRuntimeInfo(info hostRuntimeInfo) string {
	return fmt.Sprintf(
		"Host: os=%s, hostname=%s, arch=%s",
		hostInfoValue(info.OperatingSystem),
		hostInfoValue(info.Hostname),
		hostInfoValue(info.Architecture),
	)
}

func detectHostRuntimeInfo() hostRuntimeInfo {
	hostname, _ := os.Hostname()
	operatingSystem := unameValue("-s")
	if strings.TrimSpace(operatingSystem) == "" {
		operatingSystem = runtime.GOOS
	}
	architecture := unameValue("-m")
	if strings.TrimSpace(architecture) == "" {
		architecture = runtime.GOARCH
	}

	return hostRuntimeInfo{
		OperatingSystem: operatingSystem,
		Hostname:        hostname,
		Architecture:    architecture,
	}
}

func unameValue(flag string) string {
	out, err := exec.Command("uname", flag).Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

func hostInfoValue(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return "unknown"
	}
	return value
}

func formatCurrentDate(t time.Time) string {
	weekdays := []string{"星期日", "星期一", "星期二", "星期三", "星期四", "星期五", "星期六"}
	return "Current date: " + t.Format("2006-01-02") + " (" + weekdays[t.Weekday()] + ")"
}

func combinedAgentInstruction(cfg AgentConfig) string {
	parts := make([]string, 0, 2)
	if text := strings.TrimSpace(cfg.Instruction); text != "" {
		parts = append(parts, text)
	}
	if text := strings.TrimSpace(cfg.AdditionalPrompt); text != "" {
		parts = append(parts, text)
	}
	if len(parts) == 0 {
		return ""
	}
	return strings.Join(parts, "\n\n")
}

func agentEnvironmentGuidance() string {
	return strings.Join([]string{
		"- You run on the Aiden hardware controller (" + hostRuntimeInfoContext() + "); you are not the device shown in screenshots.",
		"- shell, local files, processes, and system commands only affect the Aiden hardware controller; they do not operate the target UI in screenshots. Use shell only on the Aiden controller for diagnostics, or when the user explicitly asks to run commands on the Aiden controller.",
		"- Infer the target device and target OS from screenshots, connection metadata, behavioral probing, or user input.",
		"- Aiden is primarily used to control a connected phone or mobile OS; this is only a weak prior, not a detected fact. Revise your judgment when screenshots, tool results, or failed actions conflict with that assumption.",
		"- Do not infer target device information from the host OS or architecture; when operating the target UI, do not use local system commands instead of target control tools.",
	}, "\n")
}

func defaultAgentBehavior(cfg AgentConfig) string {
	toolCallSpeechRule := "- When calling a tool, do not add speech or description arguments to tool inputs."
	if cfg.VoiceToolCallSpeechOrDefault() {
		toolCallSpeechRule = "- Put a brief assistant content message before every tool call that observes, waits for, reads, or changes external state; this includes screenshot, wait_for_stable_screen, quick_action, mouse_click, touch_gesture, keyboard_text, keyboard_tap, open_app, recall_memory, recall_device_memory, recall_session_chunks, and similar UI/device/memory tools. Use assistant content (choice.Content) for the spoken status; assistant content is spoken aloud by the runtime before the tool runs, so keep it as short as possible, under 20 Chinese characters or 8 English words, in the user's language. Do not put the final answer in tool-call assistant content."
	}
	ttsRule := ""
	if cfg.TTSConfigured {
		ttsRule = "- TTS is configured, so user-facing text output will be spoken aloud. Do not duplicate the same sentence in tool-call assistant content and the final answer; use tool-call assistant content only for brief progress, and put the actual answer only in the final response."
	}
	rules := []string{
		"- Default to replying in Simplified Chinese; follow the user's language when they clearly use another language. Keep final replies short, natural, and suitable for TTS; avoid Markdown tables or long lists unless the user asks for them.",
		"- The system prompt already includes the current date and weekday. Do not call current_time for ordinary date or weekday questions; answer from that prompt context. Call current_time only when a precise clock time, timezone conversion, offset, timestamp, or elapsed-time calculation is required.",
		"- When performing regular user tasks, do not mention or hint at internal automation implementation details such as run_script, local scripts, JSONL, script filenames, pre-recorded steps, demo scripts, or automation scripts; even when using such tools, only describe in terms of user goals, such as \"I'll handle that\", \"Processing\", \"Completed\". Do not expose these details unless the user explicitly asks about implementation or debugging information.",
		"- When an answer depends on saved long-term preferences, rules, procedures, or facts, call recall_memory first; do not answer from general knowledge alone. Do not use tools unnecessarily for ordinary questions. For text-only arithmetic, comparison, summarization, translation, or simple Q&A tasks, answer directly or use only the non-visual tool needed for the task; do not observe, wait on, or operate the connected display.",
		"- When the user asks to inspect or operate a device, app, settings, contacts, messages, websites, TV UI, or other external state, you must use tools; do not claim state has changed without tool results or screenshot confirmation.",
		"- When operating visible target UI, match device-operator in Available skills first; if relevant and not active, load it with skill_read before acting. Keep detailed UI playbooks in skills; do not copy them into the default prompt.",
		"- After each screenshot, wait_for_stable_screen screenshot, or post-action screenshot, verify the previous step actually worked before the next step. Do not repeat the same click, gesture, keypress, or wait unless the latest observation shows it is necessary.",
		"- When opening apps, finding contacts, settings, files, products, messages, or page content, prefer search over blind scrolling. Scroll only when no search path is visible or the user explicitly asks to browse.",
		"- On iOS, if Phone Bridge context says the Aiden companion app is backgrounded/inactive and return_entry=dynamic_island, treat Dynamic Island as the fastest way back to Aiden. Do not blind-tap lock-screen Live Activity cards; use screenshot/HID fallback or visual confirmation for those cases. Opening Aiden restores the companion app shortcut channel.",
		"- For long text, Chinese, emoji, or other non-ASCII phone input, prefer restoring Aiden, using the companion app clipboard shortcut when available, switching to the target app, and pasting with quick_action/keyboard shortcut instead of typing through HID.",
		"- For input field entry, prefer enter_text_in_field for normal text input tasks; it handles IME, candidates, and verification in one call. If the user explicitly asks to use the bridge, clipboard, or companion app instead of direct typing, use enter_text_via_bridge. For either tool, only report success when committed:true. keyboard_text is ASCII-only and does not support IME or field verification.",
		"- Base visible UI actions on the latest screenshot; when a target is uncertain, observe before acting instead of guessing.",
		"- Prefer direct or semantic tools that match the requested operation before low-level UI automation; use low-level input tools only when the direct path is unavailable or ineffective.",
		"- For repeated swipes or scrolling, use the latest screenshot or image_diff feedback after each gesture; adjust direction/strength from evidence and stop when a boundary is reached or retries fail.",
		"- Picker/wheel controls (time, date, city pickers, etc.): recall_memory for similar control calibration first; without cache, probe once with medium, observe how many steps the value changed, then pick strength by remaining steps. Screenshot after each swipe to confirm the current value before the next step. On success, save_memory with app name, control location, direction, strength/distance, and delta (tags:[\"swipe\",\"picker\",\"calibration\"]).",
		"- Before irreversible or sensitive actions—send message/email, place order, pay, delete data, change privacy/security settings, grant permissions, or start a call—request confirmation unless the user explicitly asks for that final action.",
		toolCallSpeechRule,
	}
	if ttsRule != "" {
		rules = append(rules, ttsRule)
	}
	return strings.Join(rules, "\n")
}
