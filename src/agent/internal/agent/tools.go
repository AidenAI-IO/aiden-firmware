package agent

import (
	"path/filepath"
	"sort"

	langtools "github.com/tmc/langchaingo/tools"
)

// ToolSet is a fixed collection of built-in tools, keyed by name.
type ToolSet struct {
	tools map[string]langtools.Tool
}

// NewBuiltinToolSet returns all built-in tools. Tools are not configurable;
// everything is registered here with its runtime dependencies already wired up.
type BuiltinToolSetOption func(*builtinToolSetOptions)

type builtinToolSetOptions struct {
	sleepController *SleepController
}

func WithSleepController(controller *SleepController) BuiltinToolSetOption {
	return func(options *builtinToolSetOptions) {
		options.sleepController = controller
	}
}

func NewBuiltinToolSet(hidCfg HIDConfig, audioCfg AudioConfig, searchCfg SearchConfig, proxyCfg ProxyConfig, options ...BuiltinToolSetOption) *ToolSet {
	toolOptions := builtinToolSetOptions{}
	for _, option := range options {
		if option != nil {
			option(&toolOptions)
		}
	}

	kbDev := NewHIDDevice(hidCfg.KeyboardDeviceOrDefault())
	screen := &screenState{}
	pointer := newPointerController(hidCfg)
	screenshot := NewScreenshotTool(hidCfg.FrameSocketOrDefault(), screen)

	tools := map[string]langtools.Tool{
		"keyboard_tap":  newPostActionScreenshotTool(&KeyboardTapTool{dev: kbDev}, screenshot, postActionScreenshotDelay),
		"keyboard_text": newPostActionScreenshotTool(&KeyboardTextTool{dev: kbDev}, screenshot, postActionScreenshotDelay),
		"mouse_click":   newPostActionScreenshotTool(&MouseClickTool{pc: pointer, screen: screen}, screenshot, postActionScreenshotDelay),
		"mouse_move":    newPostActionScreenshotTool(&MouseMoveTool{pc: pointer, screen: screen}, screenshot, postActionScreenshotDelay),
		"mouse_scroll":  newPostActionScreenshotTool(&MouseScrollTool{pc: pointer}, screenshot, postActionScreenshotDelay),
		"touch_gesture": newPostActionScreenshotTool(&TouchGestureTool{pc: pointer, screen: screen}, screenshot, postActionScreenshotDelay),
		"screenshot":    screenshot,
		"image_diff":    &ImageDiffTool{},
		"audio_volume":  NewAudioVolumeTool(audioCfg.SocketOrDefault()),
		"shell":         &ShellTool{proxy: proxyCfg},
		"current_time":  NewCurrentTimeTool(),
		"weather":       NewWeatherTool(proxyCfg),
		"web_search":    NewWebSearchTool(searchCfg, proxyCfg),
		"wikipedia":     NewWikipediaTool(proxyCfg),
		"calculator":    NewCalculatorTool(),
		"web_scraper":   NewWebScraperTool(proxyCfg),
	}
	if toolOptions.sleepController != nil {
		tools["enter_sleep"] = NewEnterSleepTool(toolOptions.sleepController)
	}

	return &ToolSet{tools: tools}
}

func (s *ToolSet) Get(name string) (langtools.Tool, bool) {
	t, ok := s.tools[name]
	return t, ok
}

func (s *ToolSet) All() []langtools.Tool {
	names := s.Names()
	result := make([]langtools.Tool, 0, len(names))
	for _, name := range names {
		if t, ok := s.tools[name]; ok {
			result = append(result, t)
		}
	}
	return result
}

func (s *ToolSet) Names() []string {
	names := make([]string, 0, len(s.tools))
	for name := range s.tools {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func (s *ToolSet) RegisterMemoryTools(memoryDir string, profileFn ProfileFn, summaryMaxChunks int, debouncer *ProfileDebouncer) {
	if memoryDir == "" {
		return
	}
	sessionStore := NewSessionMemoryStore(filepath.Join(memoryDir, "session"), summaryMaxChunks)
	longTermStore := NewLongTermMemoryStore(filepath.Join(memoryDir, "long_term"), WithLifecycleDir(filepath.Join(memoryDir, "lifecycle")), WithStoreProfileFn(profileFn), WithProfileDebouncer(debouncer))
	s.tools["recall_session_chunks"] = NewRecallSessionChunksTool(sessionStore)
	s.tools["recall_memory"] = NewRecallMemoryTool(longTermStore)
	s.tools["save_memory"] = NewSaveMemoryTool(longTermStore)
	s.tools["forget_memory"] = NewForgetMemoryTool(longTermStore)
}

func (s *ToolSet) RegisterSkillTools(skillsDir, manifestPath string, onModify ...func()) {
	usagePath := usagePathForManifest(manifestPath)
	s.tools["skill_list"] = NewSkillListTool(skillsDir, usagePath)
	s.tools["skill_read"] = NewSkillReadTool(skillsDir, usagePath)
	s.tools["skill_manage"] = NewSkillManageTool(skillsDir, manifestPath, onModify...)
	s.tools["skill_mark_used"] = NewSkillMarkUsedTool(skillsDir, usagePath)
}

func usagePathForManifest(manifestPath string) string {
	if manifestPath == "" {
		return ""
	}
	return filepath.Join(filepath.Dir(manifestPath), "usage.json")
}
