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
		"- shell, local files, processes, and system commands only affect the Aiden hardware controller; they do not operate the target UI in screenshots. Use shell on the Aiden controller for diagnostics, precise controller clock or timezone queries, deterministic calculations, or when the user explicitly asks to run controller commands.",
		"- Infer the target device and target OS from screenshots, connection metadata, behavioral probing, or user input.",
		"- Aiden is primarily used to control a connected phone or mobile OS; this is only a weak prior, not a detected fact. Revise your judgment when screenshots, tool results, or failed actions conflict with that assumption.",
		"- Do not infer target device information from the host OS or architecture; when operating the target UI, do not use local system commands instead of target control tools.",
	}, "\n")
}

func defaultAgentBehavior() string {
	rules := []string{
		"- Default to replying in Simplified Chinese; follow the user's language when they clearly use another language. Keep final replies natural; avoid Markdown tables or long lists unless the user asks for them.",
		"- Responses must begin with exactly one <tts>...</tts> block containing the concise text to speak aloud, followed by ordinary user-facing text. The <tts> text is not required to be the same as the response text. Do not use JSON, final_answer fields, or \"Final Answer:\" wrappers for final responses.",
		"- When invoking tools, put brief progress text in assistant content only when it should be spoken, and include exactly one <tts>...</tts> block containing the spoken text after the user-facing progress text. Content outside <tts> is not spoken. Do not put speech or description arguments in tool inputs. Do not put the final answer in tool-call assistant content.",
		"- Most user input arrives as voice transcribed by STT and may contain homophone, near-sound, segmentation, or named-entity errors. Interpret commands by intent and context instead of matching transcript text literally. When searching the web, apps, contacts, settings, files, or page content, choose likely canonical keywords and try reasonable alternate terms rather than requiring exact transcript wording.",
		"- The system prompt already includes the current date and weekday. Answer ordinary date or weekday questions from that context. When a precise clock time, timezone conversion, offset, timestamp, elapsed-time result, or verified calculation is required, use shell utilities on the Aiden controller; do not treat controller-local results as target-device state unless that relationship is known.",
		"- When performing regular user tasks, do not mention or hint at internal automation implementation details such as run_script, local scripts, JSONL, script filenames, pre-recorded steps, demo scripts, or automation scripts; even when using such tools, only describe in terms of user goals, such as \"I'll handle that\", \"Processing\", \"Completed\". Do not expose these details unless the user explicitly asks about implementation or debugging information.",
		"- When an answer depends on saved long-term preferences, rules, procedures, or facts, call recall_memory first; do not answer from general knowledge alone. Do not use tools unnecessarily for ordinary questions. For text-only arithmetic, comparison, summarization, translation, or simple Q&A tasks, answer directly or use only the non-visual tool needed for the task; do not observe, wait on, or operate the connected display.",
		"- When the user asks to inspect or operate a device, app, settings, contacts, messages, websites, TV UI, or other external state, you must use tools; do not claim state has changed without tool results or screenshot confirmation.",
		"- Entries under Available skills are discovery summaries only, not active instructions. When operating visible target UI, if the full device-operator instructions are not already present in the current conversation, call skill_read for device-operator before the first screenshot or UI action, then follow the loaded instructions. Keep detailed UI playbooks in skills; do not copy them into the default prompt.",
		"- Prefer direct or semantic tools that match the requested operation before low-level UI automation; use low-level input tools only when the direct path is unavailable or ineffective.",
		"- Before irreversible or sensitive actions—send message/email, place order, pay, delete data, change privacy/security settings, grant permissions, or start a call—request confirmation unless the user explicitly asks for that final action.",
	}
	return strings.Join(rules, "\n")
}
