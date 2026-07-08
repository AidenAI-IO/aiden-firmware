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
	Exposure     []ToolExposure
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
	Exposure     []ToolExposure  `json:"exposure,omitempty"`
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
	Exposure     []ToolExposure
}

type ToolExposure string

const (
	// Active exposure types (checked by runtime and HTTP catalog)
	ToolExposureAgentDefault ToolExposure = "agent_default" // Available to agent by default
	ToolExposureSkillScoped  ToolExposure = "skill_scoped"  // Available only when skill allows
	ToolExposureAlwaysCore   ToolExposure = "always_core"   // Always available, bypasses skill restrictions
	ToolExposureHTTP         ToolExposure = "http"          // Available via HTTP API

	// Reserved exposure types (assigned to tools but not yet queried by any code)
	ToolExposureScript ToolExposure = "script" // Reserved: for script-accessible tools
	ToolExposureDebug  ToolExposure = "debug"  // Reserved: for debug/diagnostic tools
	ToolExposureAdmin  ToolExposure = "admin"  // Reserved: for administrative operations
)

var builtInToolSpecMetadata = map[string]toolSpecMetadata{
	"audio_volume": {
		Category:     "audio",
		InputMode:    toolInputModeJSON,
		ExampleInput: `{}`,
		Exposure:     []ToolExposure{ToolExposureAgentDefault, ToolExposureSkillScoped, ToolExposureHTTP, ToolExposureScript},
	},
	"current_time": {
		Category:     "system",
		InputMode:    toolInputModeText,
		ExampleInput: `{"timezone":"Asia/Shanghai"}`,
		Exposure:     []ToolExposure{ToolExposureAlwaysCore, ToolExposureHTTP, ToolExposureScript},
	},
	toolWaitForWakeup: {
		Category:     "system",
		InputMode:    toolInputModeJSON,
		ExampleInput: `{"reason":"user asked me to wait for wakeup"}`,
		Exposure:     []ToolExposure{ToolExposureAgentDefault, ToolExposureHTTP},
	},
	"inspect_episode": {
		Category:     "memory",
		InputMode:    toolInputModeJSON,
		ExampleInput: `{"id":"ep_..."}`,
		Exposure:     []ToolExposure{ToolExposureSkillScoped, ToolExposureHTTP, ToolExposureDebug},
	},
	"keyboard_tap": {
		Category:     "input",
		InputMode:    toolInputModeJSON,
		ExampleInput: `{"keys":["ctrl","c"]}`,
		Exposure:     []ToolExposure{ToolExposureAgentDefault, ToolExposureHTTP, ToolExposureScript},
	},
	"keyboard_text": {
		Category:     "input",
		InputMode:    toolInputModeJSON,
		ExampleInput: `{"text":"Settings"}`,
		Exposure:     []ToolExposure{ToolExposureSkillScoped, ToolExposureHTTP, ToolExposureScript, ToolExposureDebug},
	},
	"enter_text_in_field": {
		Category:     "input",
		InputMode:    toolInputModeJSON,
		ExampleInput: `{"text":"你好","platform":"android","focus":{"x":450,"y":105,"coord_space":"normalized"},"segments":["ni","hao"]}`,
		Exposure:     []ToolExposure{ToolExposureAgentDefault, ToolExposureHTTP, ToolExposureScript},
	},
	"enter_text_via_bridge": {
		Category:     "input",
		InputMode:    toolInputModeJSON,
		ExampleInput: `{"text":"hello world","platform":"android","focus":{"x":450,"y":105,"coord_space":"normalized"}}`,
		Exposure:     []ToolExposure{ToolExposureSkillScoped, ToolExposureHTTP, ToolExposureScript, ToolExposureDebug},
	},
	"mouse_click": {
		Category:     "input",
		InputMode:    toolInputModeJSON,
		ExampleInput: `{"x":500,"y":500,"button":"left","coord_space":"normalized"}`,
		Exposure:     []ToolExposure{ToolExposureSkillScoped, ToolExposureHTTP, ToolExposureScript, ToolExposureDebug},
	},
	"mouse_move": {
		Category:     "input",
		InputMode:    toolInputModeJSON,
		ExampleInput: `{"x":500,"y":500,"coord_space":"normalized"}`,
		Exposure:     []ToolExposure{ToolExposureSkillScoped, ToolExposureHTTP, ToolExposureScript, ToolExposureDebug},
	},
	"mouse_scroll": {
		Category:     "input",
		InputMode:    toolInputModeJSON,
		ExampleInput: `{"delta":-3}`,
		Exposure:     []ToolExposure{ToolExposureSkillScoped, ToolExposureHTTP, ToolExposureScript, ToolExposureDebug},
	},
	"quick_action": {
		Category:     "input",
		InputMode:    toolInputModeJSON,
		ExampleInput: `{"list":true,"platform":"ios"}`,
		Exposure:     []ToolExposure{ToolExposureAgentDefault, ToolExposureHTTP, ToolExposureScript},
	},
	"recall_device_memory": {
		Category:     "memory",
		InputMode:    toolInputModeJSON,
		ExampleInput: `{"terms":["微信"],"tags":["登录"],"entities":["微信App"],"types":["procedure","failure"],"device_id":"default","limit":5}`,
		Exposure:     []ToolExposure{ToolExposureSkillScoped, ToolExposureHTTP, ToolExposureDebug},
	},
	"recall_session_chunks": {
		Category:     "memory",
		InputMode:    toolInputModeJSON,
		ExampleInput: `{"tags":["login"],"limit":5}`,
		Exposure:     []ToolExposure{ToolExposureAlwaysCore, ToolExposureHTTP},
	},
	"recall_memory": {
		Category:     "memory",
		InputMode:    toolInputModeJSON,
		ExampleInput: `{"tags":["login"],"limit":5}`,
		Exposure:     []ToolExposure{ToolExposureAlwaysCore, ToolExposureHTTP},
	},
	"save_memory": {
		Category:     "memory",
		InputMode:    toolInputModeJSON,
		ExampleInput: `{"type":"procedure","title":"Login flow","content":"...","tags":["login"]}`,
		Exposure:     []ToolExposure{ToolExposureAlwaysCore, ToolExposureHTTP},
	},
	"forget_memory": {
		Category:     "memory",
		InputMode:    toolInputModeJSON,
		ExampleInput: `{"id":"mem_...","reason":"obsolete"}`,
		Exposure:     []ToolExposure{ToolExposureAlwaysCore, ToolExposureHTTP},
	},
	"screenshot": {
		Category:     "observation",
		InputMode:    toolInputModeJSON,
		ExampleInput: `{}`,
		Exposure:     []ToolExposure{ToolExposureAgentDefault, ToolExposureHTTP, ToolExposureScript},
	},
	"image_diff": {
		Category:     "observation",
		InputMode:    toolInputModeJSON,
		ExampleInput: `{"before":"<base64-jpeg>","after":"<base64-jpeg>"}`,
		Exposure:     []ToolExposure{ToolExposureSkillScoped, ToolExposureHTTP, ToolExposureScript, ToolExposureDebug},
	},
	"shell": {
		Category:     "system",
		InputMode:    toolInputModeJSON,
		ExampleInput: `{"command":"pwd"}`,
		Exposure:     []ToolExposure{ToolExposureSkillScoped, ToolExposureHTTP, ToolExposureDebug},
	},
	"skill_list": {
		Category:     "skills",
		InputMode:    toolInputModeJSON,
		ExampleInput: `{"query":"device","include_archived":false}`,
		Exposure:     []ToolExposure{ToolExposureSkillScoped, ToolExposureHTTP},
	},
	"skill_mark_used": {
		Category:     "skills",
		InputMode:    toolInputModeJSON,
		ExampleInput: `{"name":"device-operator"}`,
		Exposure:     []ToolExposure{ToolExposureAdmin},
	},
	"skill_read": {
		Category:     "skills",
		InputMode:    toolInputModeJSON,
		ExampleInput: `{"name":"device-operator"}`,
		Exposure:     []ToolExposure{ToolExposureAlwaysCore, ToolExposureHTTP},
	},
	"touch_gesture": {
		Category:     "input",
		InputMode:    toolInputModeJSON,
		ExampleInput: `{"type":"tap","point":{"x":500,"y":500}}`,
		Exposure:     []ToolExposure{ToolExposureAgentDefault, ToolExposureHTTP, ToolExposureScript},
	},
	"weather": {
		Category:     "system",
		InputMode:    toolInputModeText,
		ExampleInput: `{"location":"Shanghai"}`,
		Exposure:     []ToolExposure{ToolExposureSkillScoped, ToolExposureHTTP},
	},
	"web_search": {
		Category:     "web",
		InputMode:    toolInputModeText,
		ExampleInput: `{"query":"Aiden hardware agent"}`,
		Exposure:     []ToolExposure{ToolExposureSkillScoped, ToolExposureHTTP},
	},
	"wikipedia": {
		Category:     "web",
		InputMode:    toolInputModeText,
		ExampleInput: `{"query":"Raspberry Pi"}`,
		Exposure:     []ToolExposure{ToolExposureSkillScoped, ToolExposureHTTP},
	},
	"calculator": {
		Category:     "system",
		InputMode:    toolInputModeText,
		ExampleInput: `{"expression":"2 + 2"}`,
		Exposure:     []ToolExposure{ToolExposureSkillScoped, ToolExposureHTTP, ToolExposureScript},
	},
	"web_scraper": {
		Category:     "web",
		InputMode:    toolInputModeText,
		ExampleInput: `{"url":"https://example.com"}`,
		Exposure:     []ToolExposure{ToolExposureSkillScoped, ToolExposureHTTP},
	},
	"wait_for_stable_screen": {
		Category:     "observation",
		InputMode:    toolInputModeJSON,
		ExampleInput: `{"timeout_ms":3500,"stable_ms":500,"diff_threshold":2}`,
		Exposure:     []ToolExposure{ToolExposureAgentDefault, ToolExposureHTTP, ToolExposureScript},
	},
	"request_human_handoff": {
		Category:     "handoff",
		InputMode:    toolInputModeJSON,
		ExampleInput: `{"reason":"authentication","details":"Login screen requires password","suggested_action":"Please enter your credentials on the device"}`,
		Exposure:     []ToolExposure{ToolExposureAlwaysCore, ToolExposureHTTP},
	},
	"run_script": {
		Category:     "demo",
		InputMode:    toolInputModeJSON,
		ExampleInput: `{"file":"demo.jsonl"}`,
		Exposure:     []ToolExposure{ToolExposureAgentDefault, ToolExposureSkillScoped, ToolExposureHTTP},
	},
	"list_scripts": {
		Category:     "demo",
		InputMode:    toolInputModeJSON,
		ExampleInput: `{}`,
		Exposure:     []ToolExposure{ToolExposureSkillScoped, ToolExposureHTTP},
	},
	"read_script": {
		Category:     "demo",
		InputMode:    toolInputModeJSON,
		ExampleInput: `{"file":"demo.jsonl"}`,
		Exposure:     []ToolExposure{ToolExposureSkillScoped, ToolExposureHTTP},
	},
	"write_script": {
		Category:     "demo",
		InputMode:    toolInputModeJSON,
		ExampleInput: `{"file":"demo.jsonl","content":"# 打开设置演示\n{\"type\":\"wait\",\"ms\":500}\n{\"type\":\"tts\",\"text\":\"正在打开设置\"}"}`,
		Exposure:     []ToolExposure{ToolExposureSkillScoped, ToolExposureHTTP},
	},
	"skill_manage": {
		Category:     "skills",
		InputMode:    toolInputModeJSON,
		ExampleInput: `{"action":"list"}`,
		Exposure:     []ToolExposure{ToolExposureAdmin},
	},
	"open_app": {
		Category:     "phone",
		InputMode:    toolInputModeJSON,
		ExampleInput: `{"app":"微信"}`,
		Exposure:     []ToolExposure{ToolExposureAgentDefault, ToolExposureHTTP, ToolExposureScript},
	},
	"search_launch_app": {
		Category:     "phone",
		InputMode:    toolInputModeJSON,
		ExampleInput: `{"app":"WeChat"}`,
		Exposure:     []ToolExposure{ToolExposureAgentDefault, ToolExposureHTTP, ToolExposureScript},
	},
	"clipboard": {
		Category:     "phone",
		InputMode:    toolInputModeJSON,
		ExampleInput: `{}`,
		Exposure:     []ToolExposure{ToolExposureSkillScoped, ToolExposureHTTP},
	},
	"calendar": {
		Category:     "phone",
		InputMode:    toolInputModeJSON,
		ExampleInput: `{}`,
		Exposure:     []ToolExposure{ToolExposureSkillScoped, ToolExposureHTTP},
	},
	"contacts": {
		Category:     "phone",
		InputMode:    toolInputModeJSON,
		ExampleInput: `{}`,
		Exposure:     []ToolExposure{ToolExposureSkillScoped, ToolExposureHTTP},
	},
	"notification": {
		Category:     "phone",
		InputMode:    toolInputModeJSON,
		ExampleInput: `{}`,
		Exposure:     []ToolExposure{ToolExposureSkillScoped, ToolExposureHTTP},
	},
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
	meta := builtInToolSpecMetadata[name]
	return ToolSpec{
		Tool:         tool,
		Name:         name,
		Description:  strings.TrimSpace(tool.Description()),
		Category:     defaultString(meta.Category, "general"),
		InputMode:    defaultString(meta.InputMode, toolInputModeText),
		ExampleInput: meta.ExampleInput,
		Exposure:     normalizeToolExposure(meta.Exposure),
	}
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
		descriptors = append(descriptors, spec.Descriptor())
	}
	return descriptors
}

func (s *ToolSpecs) DescriptorByName(name string) (ToolDescriptor, bool) {
	spec, ok := s.Lookup(name)
	if !ok {
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
		Exposure:     append([]ToolExposure{}, spec.Exposure...),
		HTTP: ToolHTTPBinding{
			Method: "POST",
			Path:   "/api/tools/" + spec.Name,
		},
	}
}

// defaultToolExposures returns the exposure set applied when a tool has no explicit exposure metadata.
func defaultToolExposures() []ToolExposure {
	return []ToolExposure{ToolExposureAgentDefault, ToolExposureHTTP}
}

func normalizeToolExposure(exposure []ToolExposure) []ToolExposure {
	if len(exposure) == 0 {
		return defaultToolExposures()
	}
	seen := make(map[ToolExposure]struct{}, len(exposure))
	result := make([]ToolExposure, 0, len(exposure))
	for _, value := range exposure {
		if strings.TrimSpace(string(value)) == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	return result
}

func (spec ToolSpec) HasExposure(exposure ToolExposure) bool {
	for _, value := range spec.Exposure {
		if value == exposure {
			return true
		}
	}
	return false
}

func toolHasExposure(name string, exposure ToolExposure) bool {
	meta, ok := builtInToolSpecMetadata[name]
	if !ok {
		// Unknown tools get the default exposure set
		for _, defaultExp := range defaultToolExposures() {
			if defaultExp == exposure {
				return true
			}
		}
		return false
	}
	for _, value := range normalizeToolExposure(meta.Exposure) {
		if value == exposure {
			return true
		}
	}
	return false
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
