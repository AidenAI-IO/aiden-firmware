package agent

import (
	"fmt"
	"sort"
	"strings"

	langtools "github.com/tmc/langchaingo/tools"
)

const (
	toolInputModeJSON = "json"
	toolInputModeText = "text"
)

type ToolHTTPBinding struct {
	Method string `json:"method"`
	Path   string `json:"path"`
}

type ToolDescriptor struct {
	Name         string          `json:"name"`
	Category     string          `json:"category"`
	Description  string          `json:"description"`
	InputMode    string          `json:"input_mode"`
	ExampleInput string          `json:"example_input"`
	HTTP         ToolHTTPBinding `json:"http"`
}

type ToolSkillDefinition struct {
	Name        string   `json:"name"`
	Description string   `json:"description"`
	ToolNames   []string `json:"tool_names"`
	Markdown    string   `json:"markdown"`
}

type toolCatalogEntry struct {
	Category     string
	InputMode    string
	ExampleInput string
}

var builtInToolCatalog = map[string]toolCatalogEntry{
	"activate_skill": {
		Category:     "skills",
		InputMode:    toolInputModeText,
		ExampleInput: "planner",
	},
	"audio_volume": {
		Category:     "audio",
		InputMode:    toolInputModeJSON,
		ExampleInput: `{}`,
	},
	"keyboard_tap": {
		Category:     "input",
		InputMode:    toolInputModeJSON,
		ExampleInput: `{"keys":["ctrl","c"]}`,
	},
	"keyboard_text": {
		Category:     "input",
		InputMode:    toolInputModeJSON,
		ExampleInput: `{"text":"hello world"}`,
	},
	"mouse_click": {
		Category:     "input",
		InputMode:    toolInputModeJSON,
		ExampleInput: `{"x":0.5,"y":0.5,"button":"left","coord_space":"normalized"}`,
	},
	"mouse_move": {
		Category:     "input",
		InputMode:    toolInputModeJSON,
		ExampleInput: `{"x":0.5,"y":0.5,"coord_space":"normalized"}`,
	},
	"mouse_scroll": {
		Category:     "input",
		InputMode:    toolInputModeJSON,
		ExampleInput: `{"delta":-3}`,
	},
	"screenshot": {
		Category:     "observation",
		InputMode:    toolInputModeJSON,
		ExampleInput: `{}`,
	},
	"shell": {
		Category:     "system",
		InputMode:    toolInputModeJSON,
		ExampleInput: `{"command":"pwd"}`,
	},
	"touch_gesture": {
		Category:     "input",
		InputMode:    toolInputModeJSON,
		ExampleInput: `{"type":"tap","point":{"x":0.5,"y":0.5}}`,
	},
}

func (r *Runtime) OwnedTools() []langtools.Tool {
	owned := make([]langtools.Tool, 0, len(r.tools.tools)+1)
	if r.skillsLoaded {
		owned = append(owned, NewActivateSkillTool(r.skills))
	}
	owned = append(owned, r.tools.All()...)
	sort.Slice(owned, func(i, j int) bool {
		return owned[i].Name() < owned[j].Name()
	})
	return owned
}

func (r *Runtime) ToolDescriptors() []ToolDescriptor {
	tools := r.OwnedTools()
	descriptors := make([]ToolDescriptor, 0, len(tools))
	for _, tool := range tools {
		meta := builtInToolCatalog[tool.Name()]
		exampleInput := meta.ExampleInput
		if tool.Name() == "activate_skill" {
			exampleInput = r.defaultActivateSkillExample()
		}
		descriptors = append(descriptors, ToolDescriptor{
			Name:         tool.Name(),
			Category:     defaultString(meta.Category, "general"),
			Description:  strings.TrimSpace(tool.Description()),
			InputMode:    defaultString(meta.InputMode, toolInputModeJSON),
			ExampleInput: exampleInput,
			HTTP: ToolHTTPBinding{
				Method: "POST",
				Path:   "/api/tools/" + tool.Name(),
			},
		})
	}
	return descriptors
}

func (r *Runtime) ToolDescriptorByName(name string) (ToolDescriptor, bool) {
	for _, descriptor := range r.ToolDescriptors() {
		if descriptor.Name == name {
			return descriptor, true
		}
	}
	return ToolDescriptor{}, false
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
	builder.WriteString("- Use `screenshot` before and after state-changing pointer or touch actions.\n")
	builder.WriteString("- Prefer `coord_space: \"normalized\"` for pointer and touch inputs so the same call survives display-resolution changes.\n")
	builder.WriteString("- For `shell` background sessions, use `action:start`, then `poll`/`write`/`submit`/`send_keys`, and always finish with `stop`.\n\n")
	builder.WriteString("Available tools in this skill:\n")
	for _, descriptor := range descriptors {
		builder.WriteString(fmt.Sprintf("- `%s`: %s\n", descriptor.Name, descriptor.Description))
		if descriptor.Name == "screenshot" {
			builder.WriteString("  Successful output JSON includes `width`, `height`, `format`, `size`, and base64 JPEG `data`.\n")
		}
		if strings.TrimSpace(descriptor.ExampleInput) != "" {
			builder.WriteString(fmt.Sprintf("  Example input: `%s`\n", descriptor.ExampleInput))
		}
	}
	builder.WriteString("\nExecution rules:\n")
	builder.WriteString("- Treat `is_error=true` or outputs that start with `error:` as failures.\n")
	builder.WriteString("- Use `screenshot` before pixel-based pointer actions when screen dimensions may be stale.\n")
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

func (r *Runtime) defaultActivateSkillExample() string {
	if r == nil || r.skills == nil || r.skills.GetIndex() == nil {
		return ""
	}
	names := r.skills.GetIndex().Names()
	sort.Strings(names)
	if len(names) == 0 {
		return ""
	}
	return names[0]
}
