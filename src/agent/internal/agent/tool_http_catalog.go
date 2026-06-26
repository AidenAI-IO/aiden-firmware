package agent

import (
	"fmt"
	"sort"
	"strings"

	langtools "github.com/tmc/langchaingo/tools"
)

func (r *Runtime) OwnedTools() []langtools.Tool {
	owned := make([]langtools.Tool, 0, len(r.tools.tools))
	owned = append(owned, r.tools.All()...)
	sort.Slice(owned, func(i, j int) bool {
		return owned[i].Name() < owned[j].Name()
	})
	return owned
}

func (r *Runtime) ToolSpecs() *ToolSpecs {
	return NewToolSpecs(r.OwnedTools())
}

func (r *Runtime) ToolDescriptors() []ToolDescriptor {
	return r.ToolSpecs().Descriptors()
}

func isAgentToolExposed(name string) bool {
	if strings.TrimSpace(name) == "" {
		return false
	}
	meta, ok := builtInToolSpecMetadata[name]
	if !ok {
		return true
	}
	if meta.AgentExposed != nil {
		return *meta.AgentExposed
	}
	return true
}

func (r *Runtime) ToolDescriptorByName(name string) (ToolDescriptor, bool) {
	return r.ToolSpecs().DescriptorByName(name)
}

const defaultHTTPToolSkillBaseURL = "http://127.0.0.1:8080"

func (r *Runtime) HTTPToolSkills(baseURL string) []ToolSkillDefinition {
	descriptors := r.ToolDescriptors()
	if len(descriptors) == 0 {
		return nil
	}

	baseURL = normalizeHTTPToolSkillBaseURL(baseURL)

	names := make([]string, 0, len(descriptors))
	for _, descriptor := range descriptors {
		names = append(names, descriptor.Name)
	}

	return []ToolSkillDefinition{{
		Name:        "aiden-http-tool-suite",
		Description: "Use the Aiden HTTP tool API to observe and operate the connected device with the full built-in tool set.",
		ToolNames:   names,
		Markdown: buildHTTPToolSkillMarkdown(
			"aiden-http-tool-suite",
			"Use the Aiden HTTP tool API to observe and operate the connected device with the full built-in tool set.",
			baseURL,
			descriptors,
		),
	}}
}

func buildHTTPToolSkillMarkdown(name, description string, baseURL string, descriptors []ToolDescriptor) string {
	var builder strings.Builder
	builder.WriteString("---\n")
	builder.WriteString(fmt.Sprintf("name: %s\n", name))
	builder.WriteString(fmt.Sprintf("description: %s\n", description))
	builder.WriteString("metadata:\n")
	builder.WriteString("  transport: http\n")
	builder.WriteString("  source: aiden-agent\n")
	builder.WriteString("  base_url_env: AIDEN_AGENT_BASE_URL\n")
	builder.WriteString(fmt.Sprintf("  base_url_default: %s\n", baseURL))
	builder.WriteString("  tool_names:\n")
	for _, descriptor := range descriptors {
		builder.WriteString(fmt.Sprintf("    - %s\n", descriptor.Name))
	}
	builder.WriteString("---\n\n")
	builder.WriteString("Use the Aiden HTTP tool API instead of assuming direct local access to the connected device.\n\n")
	builder.WriteString("Connection:\n")
	builder.WriteString("- Prefer the base URL from `AIDEN_AGENT_BASE_URL`.\n")
	builder.WriteString(fmt.Sprintf("- If `AIDEN_AGENT_BASE_URL` is unset, use `%s`.\n", baseURL))
	if baseURL != defaultHTTPToolSkillBaseURL {
		builder.WriteString(fmt.Sprintf("- Local fallback when the published host is unavailable: `%s`.\n", defaultHTTPToolSkillBaseURL))
	}
	builder.WriteString("- When the base URL points at a private IP or LAN hostname, bypass outbound HTTP proxies (`NO_PROXY`, `no_proxy`, or client-specific proxy bypass settings).\n\n")
	builder.WriteString("Discovery:\n")
	builder.WriteString("- `GET /api/tools` lists every exposed tool, its description, example input, and HTTP path.\n")
	builder.WriteString("- `GET /api/tool-skills` returns generated skill bundles like this one.\n\n")
	builder.WriteString("Invocation:\n")
	builder.WriteString("- `POST /api/tools/{tool_name}`\n")
	builder.WriteString("- Request body: `{\"input\": <JSON object or string>}` or `{\"raw_input\": \"<plain tool input>\"}`.\n")
	builder.WriteString("- Success and tool failures both return JSON; inspect `is_error` and `output`.\n\n")
	builder.WriteString("Transport failures:\n")
	builder.WriteString("- Treat connection errors, timeouts, empty replies, and non-2xx HTTP status codes as transport failures before inspecting any tool payload.\n")
	builder.WriteString("- For private device URLs, suspect proxy interference first if the TCP port is reachable but HTTP returns gateway/proxy errors.\n\n")
	builder.WriteString("Recommended workflow:\n")
	builder.WriteString("- Start with `GET /api/tools` if you need to confirm the tool list or example payloads.\n")
	builder.WriteString("- Use `screenshot` before input actions when you need current screen context; keyboard, mouse, and touch tools automatically wait for screen stability (or until the configured timeout) and return a post-action screenshot.\n")
	builder.WriteString("- After actions that may animate, navigate, or load content, call `wait_for_stable_screen` before judging the result. It returns a screenshot observation; `stable=false` means the screen kept changing (for example video playback), so treat the screenshot as a best-effort observation.\n")
	builder.WriteString("- After any screenshot, wait_for_stable_screen screenshot, or post-action screenshot, inspect the current screen before choosing the next action; do not repeat the same click, gesture, or key unless the image proves the previous action did not take effect.\n")
	builder.WriteString("- When opening apps or finding contacts, settings, products, or page content on a phone, prefer system search, in-app search, or visible search fields before scrolling through pages or lists.\n")
	builder.WriteString("- For iOS/Android external app text entry, use `enter_text_in_field`. On iOS, if the text is known while Aiden is foreground, first call `clipboard` with action=write, then open/navigate the target app; once the field is visible, `enter_text_in_field` focuses, pastes the prepared clipboard, and verifies without reopening Aiden. Do not rely on background WebSocket/HTTP clipboard delivery as the primary path. On Android, it can use connected bridge clipboard/paste without leaving the target app. It falls back to HID/IME typing in the same call if clipboard input fails. `keyboard_text` is ASCII-only without IME support.\n")
	builder.WriteString("- For pointer and touch inputs, click the visible target center from the latest screenshot and prefer `coord_space: \"normalized\"` with 0-1000 coordinates where (0,0) is top-left, (1000,1000) is bottom-right, (500,500) is center. Use `coord_space: \"pixel\"` only when the screenshot pixel coordinates are known to match the HID pointer surface.\n")
	builder.WriteString("- For `shell` background sessions, use `action:start`, then `poll`/`write`/`submit`/`send_keys`, and always finish with `stop`.\n\n")
	builder.WriteString("Available tools in this skill:\n")
	for _, descriptor := range descriptors {
		builder.WriteString(fmt.Sprintf("- `%s`: %s\n", descriptor.Name, descriptor.Description))
		if descriptor.Name == "screenshot" {
			builder.WriteString("  Successful output JSON includes `width`, `height`, `format`, `size`, and base64 JPEG `data`.\n")
		} else if descriptor.Name == "wait_for_stable_screen" {
			builder.WriteString("  Successful output JSON includes `ok`, `stable`, `elapsed_ms`, optional `last_diff`, plus `screen_stable`, `stable_wait_ms`, `width`, `height`, `format`, `size`, and base64 JPEG `data` from the captured screenshot.\n")
		} else if descriptor.Category == "input" {
			builder.WriteString("  On successful execution, output JSON includes `action_output`, `screen_stable`, `stable_wait_ms`, `width`, `height`, `format`, `size`, and base64 JPEG `data` from a post-action screenshot. `screen_stable=false` is not a failure.\n")
		}
		if strings.TrimSpace(descriptor.ExampleInput) != "" {
			builder.WriteString(fmt.Sprintf("  Example input: `%s`\n", descriptor.ExampleInput))
		}
	}
	builder.WriteString("\nExecution rules:\n")
	builder.WriteString("- Treat `is_error=true` or outputs that start with `error:` as failures.\n")
	builder.WriteString("- Avoid pixel-based pointer actions unless calibrated; if you must use them, call `screenshot` first because stale or mismatched screen dimensions will be rejected.\n")
	builder.WriteString("- For successful keyboard, mouse, and touch calls, inspect the returned post-action screenshot before deciding the next step. `screen_stable=false` means the wait timed out while the screen kept changing; continue if the screenshot is still useful. For separate observations after an action, call `wait_for_stable_screen` and inspect its returned screenshot.\n")
	builder.WriteString("- Do not use repeated scrolling as the first search strategy on phone UIs; try available search controls first.\n")
	builder.WriteString("- Keep tool input minimal and deterministic; prefer the example payloads as a starting point.\n")
	return builder.String()
}

func normalizeHTTPToolSkillBaseURL(baseURL string) string {
	baseURL = strings.TrimSpace(baseURL)
	baseURL = strings.TrimRight(baseURL, "/")
	if baseURL == "" {
		return defaultHTTPToolSkillBaseURL
	}
	return baseURL
}

func defaultString(value, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return value
}
