package agent

import (
	"context"
	"fmt"
	"strings"

	langtools "github.com/tmc/langchaingo/tools"
)

// ToolSet is a fixed collection of built-in tools, keyed by name.
type ToolSet struct {
	tools map[string]langtools.Tool
}

// NewBuiltinToolSet returns all built-in tools. Tools are not configurable;
// everything is registered here with its runtime dependencies already wired up.
func NewBuiltinToolSet(hidCfg HIDConfig, audioCfg AudioConfig) *ToolSet {
	kbDev := NewHIDDevice(hidCfg.KeyboardDeviceOrDefault())
	mouseDev := NewHIDDevice(hidCfg.MouseDeviceOrDefault())
	screen := &screenState{}

	return &ToolSet{
		tools: map[string]langtools.Tool{
			"keyboard_tap":  &KeyboardTapTool{dev: kbDev},
			"keyboard_text": &KeyboardTextTool{dev: kbDev},
			"mouse_click":   &MouseClickTool{dev: mouseDev, screen: screen},
			"mouse_move":    &MouseMoveTool{dev: mouseDev, screen: screen},
			"mouse_scroll":  &MouseScrollTool{dev: mouseDev},
			"touch_gesture": &TouchGestureTool{dev: mouseDev, screen: screen},
			"screenshot":    NewScreenshotTool(hidCfg.FrameSocketOrDefault(), screen),
			"audio_volume":  NewAudioVolumeTool(audioCfg.SocketOrDefault()),
		},
	}
}

func (s *ToolSet) Get(name string) (langtools.Tool, bool) {
	t, ok := s.tools[name]
	return t, ok
}

func (s *ToolSet) All() []langtools.Tool {
	result := make([]langtools.Tool, 0, len(s.tools))
	for _, t := range s.tools {
		result = append(result, t)
	}
	return result
}

func (s *ToolSet) Names() []string {
	names := make([]string, 0, len(s.tools))
	for name := range s.tools {
		names = append(names, name)
	}
	return names
}

// ActivateSkillTool allows the LLM to activate skills at runtime.
type ActivateSkillTool struct {
	skillManager *SkillManager
}

func NewActivateSkillTool(skillManager *SkillManager) *ActivateSkillTool {
	return &ActivateSkillTool{skillManager: skillManager}
}

func (t *ActivateSkillTool) Name() string { return "activate_skill" }

func (t *ActivateSkillTool) Description() string {
	index := t.skillManager.GetIndex()
	skills := index.All()

	if len(skills) == 0 {
		return "No skills available to activate."
	}

	var builder strings.Builder
	builder.WriteString("Activate a skill to gain specialized capabilities. Available skills:\n")
	for name, skill := range skills {
		builder.WriteString(fmt.Sprintf("- %s: %s\n", name, skill.Description))
	}
	builder.WriteString("\nInput: skill name to activate")
	return builder.String()
}

func (t *ActivateSkillTool) Call(ctx context.Context, input string) (string, error) {
	skillName := strings.TrimSpace(input)
	if skillName == "" {
		return "", fmt.Errorf("skill name is required")
	}

	if err := t.skillManager.Activate(ctx, skillName); err != nil {
		return "", err
	}

	skill, ok := t.skillManager.GetIndex().Get(skillName)
	if !ok {
		return "", fmt.Errorf("skill %q not found", skillName)
	}

	return fmt.Sprintf("Skill %q activated. Instructions:\n%s", skillName, skill.Instructions), nil
}
