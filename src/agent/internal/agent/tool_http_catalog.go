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
	return r.ToolSpecs().HTTPDescriptors()
}

func (r *Runtime) ToolDescriptorByName(name string) (ToolDescriptor, bool) {
	spec, ok := r.ToolSpecs().LookupHTTP(name)
	if !ok {
		return ToolDescriptor{}, false
	}
	return spec.Descriptor(), true
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
		Description: "Use the Aiden HTTP tool API to observe and operate the connected device with all HTTP-exposed tools.",
		ToolNames:   names,
		Markdown: buildHTTPToolSkillMarkdown(
			"aiden-http-tool-suite",
			"Use the Aiden HTTP tool API to observe and operate the connected device with all HTTP-exposed tools.",
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
	builder.WriteString("- After actions that may animate, navigate, or load content, call `wait_for_stable_screen` before judging the result. It returns a screenshot observation; `screen_changed=false` means no visible change was observed during the wait window, and `stable=false` means the screen kept changing (for example video playback), so treat the screenshot as a best-effort observation.\n")
	builder.WriteString("- After any screenshot, wait_for_stable_screen screenshot, or post-action screenshot, inspect the current screen before choosing the next action; do not repeat the same click, gesture, or key unless the image proves the previous action did not take effect.\n")
	builder.WriteString("- When opening apps or finding contacts, settings, products, or page content on a phone, prefer system search, in-app search, or visible search fields before scrolling through pages or lists. Use `search_launch_app` when app search plus tapping is the fastest path.\n")
	builder.WriteString("- `search_launch_app` success confirms only that the app opened. Before any text-entry tool, the latest screenshot must clearly show the actual editable field/composer and the focus point must be inside it; app home screens, folder/list views, blank areas, and create/new buttons are not input fields.\n")
	builder.WriteString("- Phone Bridge routing is state-dependent: foreground Aiden uses WebSocket; iOS background with PiP Bridge mode enabled can run only background-safe data tools (`bridge_clipboard`, `bridge_calendar`, `bridge_contacts`, `bridge_notification`) through the HTTP queue; `bridge_open_app` and UI actions still require foreground Aiden, Dynamic Island restore, or HID/screenshot fallback.\n")
	builder.WriteString("- For input field entry, use `enter_text`. It first uses the Phone Bridge clipboard route when available, otherwise it preserves text order by combining HID ASCII typing with IME entry for non-ASCII runs. It owns clipboard write, paste, and verification, so do not stage `bridge_clipboard` manually.\n")
	builder.WriteString("- For pointer and touch inputs, click the visible target center from the latest screenshot and prefer `coord_space: \"normalized\"` with 0-1000 coordinates where (0,0) is top-left, (1000,1000) is bottom-right, (500,500) is center.\n")
	builder.WriteString("- For `shell` background sessions, use `action:start`, then `poll`/`write`/`submit`/`send_keys`, and always finish with `stop`.\n\n")
	builder.WriteString("Available tools in this skill:\n")
	for _, descriptor := range descriptors {
		builder.WriteString(fmt.Sprintf("- `%s`: %s\n", descriptor.Name, descriptor.Description))
		if descriptor.Name == "screenshot" {
			builder.WriteString("  Successful output JSON includes `width`, `height`, `format`, `size`, and base64 JPEG `data`.\n")
		} else if descriptor.Name == "wait_for_stable_screen" {
			builder.WriteString("  Successful output JSON includes `ok`, `stable`, `elapsed_ms`, `screen_changed`, optional `last_diff`, plus `screen_stable`, `stable_wait_ms`, `width`, `height`, `format`, `size`, and base64 JPEG `data` from the captured screenshot.\n")
		} else if descriptor.Category == "input" {
			builder.WriteString("  On successful execution, output JSON includes `action_output`, `screen_stable`, `stable_wait_ms`, `screen_changed`, `width`, `height`, `format`, `size`, and base64 JPEG `data` from a post-action screenshot. `screen_changed=false` means no visible change was observed during the wait window; `screen_stable=false` is not a failure.\n")
		}
		if strings.TrimSpace(descriptor.ExampleInput) != "" {
			builder.WriteString(fmt.Sprintf("  Example input: `%s`\n", descriptor.ExampleInput))
		}
	}
	builder.WriteString("\nExecution rules:\n")
	builder.WriteString("- Treat `is_error=true` or outputs that start with `error:` as failures.\n")
	builder.WriteString("- Avoid pixel-based pointer actions unless calibrated; if you must use them, call `screenshot` first because stale or mismatched screen dimensions will be rejected.\n")
	builder.WriteString("- For successful keyboard, mouse, and touch calls, inspect the returned post-action screenshot before deciding the next step. `screen_changed=false` means no visible change was observed during the wait window. `screen_stable=false` means the wait timed out while the screen kept changing; continue if the screenshot is still useful. For separate observations after an action, call `wait_for_stable_screen` and inspect its returned screenshot.\n")
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
