package agent

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/tmc/langchaingo/llms"
	langtools "github.com/tmc/langchaingo/tools"
)

const (
	toolInputModeJSON = "json"
	toolInputModeText = "text"
)

type ToolSpec struct {
	Tool         langtools.Tool
	Name         string
	Description  string
	Category     string
	InputMode    string
	ExampleInput string
	HTTPExposed  bool
	AgentExposed bool
	AgentRoles   []RoleName
}

type ToolSpecs struct {
	byName map[string]ToolSpec
	names  []string
}

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

type toolSpecMetadata struct {
	Category     string
	InputMode    string
	ExampleInput string
	HTTPExposed  *bool
	AgentExposed *bool
	AgentRoles   []RoleName
}

var builtInToolSpecMetadata = map[string]toolSpecMetadata{
	"audio_volume": {
		Category:     "audio",
		InputMode:    toolInputModeJSON,
		ExampleInput: `{}`,
	},
	"current_time": {
		Category:     "system",
		InputMode:    toolInputModeText,
		ExampleInput: `{"timezone":"Asia/Shanghai"}`,
	},
	toolWaitForWakeup: {
		Category:     "system",
		InputMode:    toolInputModeJSON,
		ExampleInput: `{"reason":"user asked me to wait for wakeup"}`,
	},
	"inspect_episode": {
		Category:     "memory",
		InputMode:    toolInputModeJSON,
		ExampleInput: `{"id":"ep_..."}`,
	},
	"keyboard_tap": {
		Category:     "input",
		InputMode:    toolInputModeJSON,
		ExampleInput: `{"keys":["ctrl","c"]}`,
	},
	"keyboard_text": {
		Category:     "input",
		InputMode:    toolInputModeJSON,
		ExampleInput: `{"text":"Settings"}`,
	},
	"enter_text_in_field": {
		Category:     "input",
		InputMode:    toolInputModeJSON,
		ExampleInput: `{"text":"你好","platform":"android","focus":{"x":450,"y":105,"coord_space":"normalized"},"segments":["ni","hao"]}`,
	},
	"enter_text_via_bridge": {
		Category:     "input",
		InputMode:    toolInputModeJSON,
		ExampleInput: `{"text":"hello world","platform":"android","focus":{"x":450,"y":105,"coord_space":"normalized"}}`,
	},
	"mouse_click": {
		Category:     "input",
		InputMode:    toolInputModeJSON,
		ExampleInput: `{"x":500,"y":500,"button":"left","coord_space":"normalized"}`,
	},
	"mouse_move": {
		Category:     "input",
		InputMode:    toolInputModeJSON,
		ExampleInput: `{"x":500,"y":500,"coord_space":"normalized"}`,
	},
	"mouse_scroll": {
		Category:     "input",
		InputMode:    toolInputModeJSON,
		ExampleInput: `{"delta":-3}`,
	},
	"quick_action": {
		Category:     "input",
		InputMode:    toolInputModeJSON,
		ExampleInput: `{"list":true,"platform":"ios"}`,
	},
	"recall_device_memory": {
		Category:     "memory",
		InputMode:    toolInputModeJSON,
		ExampleInput: `{"terms":["微信"],"tags":["登录"],"entities":["微信App"],"types":["procedure","failure"],"device_id":"default","limit":5}`,
	},
	"recall_session_chunks": {
		Category:     "memory",
		InputMode:    toolInputModeJSON,
		ExampleInput: `{"tags":["login"],"limit":5}`,
	},
	"recall_memory": {
		Category:     "memory",
		InputMode:    toolInputModeJSON,
		ExampleInput: `{"tags":["login"],"limit":5}`,
	},
	"save_memory": {
		Category:     "memory",
		InputMode:    toolInputModeJSON,
		ExampleInput: `{"type":"procedure","title":"Login flow","content":"...","tags":["login"]}`,
		AgentRoles:   []RoleName{RolePlanner},
	},
	"forget_memory": {
		Category:     "memory",
		InputMode:    toolInputModeJSON,
		ExampleInput: `{"id":"mem_...","reason":"obsolete"}`,
	},
	"screenshot": {
		Category:     "observation",
		InputMode:    toolInputModeJSON,
		ExampleInput: `{}`,
	},
	"image_diff": {
		Category:     "observation",
		InputMode:    toolInputModeJSON,
		ExampleInput: `{"before":"<base64-jpeg>","after":"<base64-jpeg>"}`,
	},
	"shell": {
		Category:     "system",
		InputMode:    toolInputModeJSON,
		ExampleInput: `{"command":"pwd"}`,
	},
	"skill_list": {
		Category:     "skills",
		InputMode:    toolInputModeJSON,
		ExampleInput: `{"query":"planner","include_archived":false}`,
	},
	"skill_mark_used": {
		Category:     "skills",
		InputMode:    toolInputModeJSON,
		ExampleInput: `{"name":"planner"}`,
	},
	"skill_read": {
		Category:     "skills",
		InputMode:    toolInputModeJSON,
		ExampleInput: `{"name":"planner"}`,
	},
	"touch_gesture": {
		Category:     "input",
		InputMode:    toolInputModeJSON,
		ExampleInput: `{"type":"tap","point":{"x":500,"y":500}}`,
	},
	"weather": {
		Category:     "system",
		InputMode:    toolInputModeText,
		ExampleInput: `{"location":"Shanghai"}`,
	},
	"web_search": {
		Category:     "web",
		InputMode:    toolInputModeText,
		ExampleInput: `{"query":"Aiden hardware agent"}`,
	},
	"wikipedia": {
		Category:     "web",
		InputMode:    toolInputModeText,
		ExampleInput: `{"query":"Raspberry Pi"}`,
	},
	"calculator": {
		Category:     "system",
		InputMode:    toolInputModeText,
		ExampleInput: `{"expression":"2 + 2"}`,
	},
	"web_scraper": {
		Category:     "web",
		InputMode:    toolInputModeText,
		ExampleInput: `{"url":"https://example.com"}`,
	},
	"wait_for_stable_screen": {
		Category:     "observation",
		InputMode:    toolInputModeJSON,
		ExampleInput: `{"timeout_ms":3500,"stable_ms":500,"diff_threshold":2}`,
	},
	"request_human_handoff": {
		Category:     "handoff",
		InputMode:    toolInputModeJSON,
		ExampleInput: `{"reason":"Need user confirmation"}`,
	},
	"skill_manage": {
		Category:     "skills",
		InputMode:    toolInputModeJSON,
		ExampleInput: `{"action":"list"}`,
		HTTPExposed:  toolSpecBoolPtr(false),
		AgentExposed: toolSpecBoolPtr(true),
	},
	"open_app": {
		Category:     "phone",
		InputMode:    toolInputModeJSON,
		ExampleInput: `{"name":"App Store"}`,
		HTTPExposed:  toolSpecBoolPtr(false),
	},
	"clipboard": {
		Category:     "phone",
		InputMode:    toolInputModeJSON,
		ExampleInput: `{}`,
		HTTPExposed:  toolSpecBoolPtr(false),
	},
	"calendar": {
		Category:     "phone",
		InputMode:    toolInputModeJSON,
		ExampleInput: `{}`,
		HTTPExposed:  toolSpecBoolPtr(false),
	},
	"contacts": {
		Category:     "phone",
		InputMode:    toolInputModeJSON,
		ExampleInput: `{}`,
		HTTPExposed:  toolSpecBoolPtr(false),
	},
	"notification": {
		Category:     "phone",
		InputMode:    toolInputModeJSON,
		ExampleInput: `{}`,
		HTTPExposed:  toolSpecBoolPtr(false),
	},
}

func toolSpecBoolPtr(value bool) *bool {
	return &value
}

func NewToolSpecs(tools []langtools.Tool) *ToolSpecs {
	specs := &ToolSpecs{
		byName: make(map[string]ToolSpec, len(tools)),
		names:  make([]string, 0, len(tools)),
	}
	for _, tool := range tools {
		if tool == nil {
			continue
		}
		spec := NewToolSpec(tool)
		key := toolSpecKey(spec.Name)
		if _, exists := specs.byName[key]; !exists {
			specs.names = append(specs.names, spec.Name)
		}
		specs.byName[key] = spec
	}
	sort.Strings(specs.names)
	return specs
}

func NewToolSpec(tool langtools.Tool) ToolSpec {
	if tool == nil {
		return ToolSpec{}
	}
	name := tool.Name()
	meta, hasMeta := builtInToolSpecMetadata[name]
	httpExposed := hasMeta
	if meta.HTTPExposed != nil {
		httpExposed = *meta.HTTPExposed
	}
	// Agent tools can be injected by runtime deps without HTTP exposure metadata.
	agentExposed := true
	if meta.AgentExposed != nil {
		agentExposed = *meta.AgentExposed
	}
	return ToolSpec{
		Tool:         tool,
		Name:         name,
		Description:  strings.TrimSpace(tool.Description()),
		Category:     defaultString(meta.Category, "general"),
		InputMode:    defaultString(meta.InputMode, toolInputModeText),
		ExampleInput: meta.ExampleInput,
		HTTPExposed:  httpExposed,
		AgentExposed: agentExposed,
		AgentRoles:   append([]RoleName{}, meta.AgentRoles...),
	}
}

func (spec ToolSpec) AgentExposedToRole(role RoleName) bool {
	if !spec.AgentExposed {
		return false
	}
	if len(spec.AgentRoles) == 0 {
		return true
	}
	for _, allowed := range spec.AgentRoles {
		if strings.EqualFold(string(allowed), string(role)) {
			return true
		}
	}
	return false
}

func (s *ToolSpecs) Lookup(name string) (ToolSpec, bool) {
	if s == nil {
		return ToolSpec{}, false
	}
	spec, ok := s.byName[toolSpecKey(name)]
	return spec, ok
}

func (s *ToolSpecs) All() []ToolSpec {
	if s == nil {
		return nil
	}
	result := make([]ToolSpec, 0, len(s.names))
	for _, name := range s.names {
		if spec, ok := s.Lookup(name); ok {
			result = append(result, spec)
		}
	}
	return result
}

func (s *ToolSpecs) Descriptors() []ToolDescriptor {
	if s == nil {
		return nil
	}
	descriptors := make([]ToolDescriptor, 0, len(s.names))
	for _, spec := range s.All() {
		if !spec.HTTPExposed {
			continue
		}
		descriptors = append(descriptors, spec.Descriptor())
	}
	return descriptors
}

func (s *ToolSpecs) DescriptorByName(name string) (ToolDescriptor, bool) {
	spec, ok := s.Lookup(name)
	if !ok || !spec.HTTPExposed {
		return ToolDescriptor{}, false
	}
	return spec.Descriptor(), true
}

func (spec ToolSpec) Descriptor() ToolDescriptor {
	return ToolDescriptor{
		Name:         spec.Name,
		Category:     defaultString(spec.Category, "general"),
		Description:  strings.TrimSpace(spec.Description),
		InputMode:    defaultString(spec.InputMode, toolInputModeText),
		ExampleInput: spec.ExampleInput,
		HTTP: ToolHTTPBinding{
			Method: "POST",
			Path:   "/api/tools/" + spec.Name,
		},
	}
}

func (spec ToolSpec) LLMTool() llms.Tool {
	return llms.Tool{
		Type: "function",
		Function: &llms.FunctionDefinition{
			Name:        spec.Name,
			Description: strings.TrimSpace(spec.Description),
			Parameters:  spec.LLMSchema(),
		},
	}
}

func (spec ToolSpec) LLMSchema() map[string]any {
	if structured, ok := spec.Tool.(structuredInputTool); ok {
		if schema := structured.ArgsSchema(); len(schema) > 0 {
			return toolParametersSchema(schema)
		}
	}
	return genericToolParameters()
}

func (spec ToolSpec) NormalizeInput(input string) string {
	input = normalizeToolInput(input)
	input = unwrapCompatibleToolInput(input)
	return normalizeToolInput(input)
}

func (spec ToolSpec) ValidateInput(input string) error {
	if defaultString(spec.InputMode, toolInputModeJSON) != toolInputModeJSON {
		return nil
	}
	trimmed := strings.TrimSpace(input)
	if trimmed == "" {
		return nil
	}
	if !json.Valid([]byte(trimmed)) {
		return fmt.Errorf("%s input must be valid JSON after compatibility conversion", spec.Name)
	}
	return nil
}

func unwrapCompatibleToolInput(input string) string {
	trimmed := strings.TrimSpace(input)
	if trimmed == "" || !strings.HasPrefix(trimmed, "{") {
		return input
	}

	var fields map[string]json.RawMessage
	if err := json.Unmarshal([]byte(trimmed), &fields); err != nil {
		return input
	}
	rawArg, ok := fields["__arg1"]
	if !ok {
		return input
	}
	var arg string
	if err := json.Unmarshal(rawArg, &arg); err != nil {
		return input
	}
	return arg
}

func toolSpecKey(name string) string {
	return strings.ToUpper(strings.TrimSpace(name))
}
