package agent

import (
	"path/filepath"
	"sort"
	"time"

	langtools "github.com/tmc/langchaingo/tools"
)

// ToolSet is a fixed collection of built-in tools, keyed by name.
type ToolSet struct {
	tools  map[string]langtools.Tool
	screen *screenState
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
	waitStable := NewWaitStableScreenTool(hidCfg.FrameSocketOrDefault())

	tools := map[string]langtools.Tool{
		"keyboard_tap":           newPostActionStableScreenshotTool(&KeyboardTapTool{dev: kbDev}, waitStable, screenshot, postActionScreenshotDelay),
		"keyboard_text":          newPostActionStableScreenshotTool(&KeyboardTextTool{dev: kbDev}, waitStable, screenshot, postActionScreenshotDelay),
		"mouse_click":            newPostActionStableScreenshotTool(&MouseClickTool{pc: pointer, screen: screen}, waitStable, screenshot, postActionScreenshotDelay),
		"mouse_move":             newPostActionStableScreenshotTool(&MouseMoveTool{pc: pointer, screen: screen}, waitStable, screenshot, postActionScreenshotDelay),
		"mouse_scroll":           newPostActionStableScreenshotTool(&MouseScrollTool{pc: pointer}, waitStable, screenshot, postActionScreenshotDelay),
		"touch_gesture":          newPostActionStableScreenshotTool(&TouchGestureTool{pc: pointer, screen: screen}, waitStable, screenshot, postActionScreenshotDelay),
		"screenshot":             screenshot,
		"wait_for_stable_screen": waitStable,
		"image_diff":             &ImageDiffTool{},
		"audio_volume":           NewAudioVolumeTool(audioCfg.SocketOrDefault()),
		"shell":                  &ShellTool{proxy: proxyCfg},
		"current_time":           NewCurrentTimeTool(),
		"weather":                NewWeatherTool(proxyCfg),
		"web_search":             NewWebSearchTool(searchCfg, proxyCfg),
		"wikipedia":              NewWikipediaTool(proxyCfg),
		"calculator":             NewCalculatorTool(),
		"web_scraper":            NewWebScraperTool(proxyCfg),
	}
	if toolOptions.sleepController != nil {
		tools["enter_sleep"] = NewEnterSleepTool(toolOptions.sleepController)
	}

	// Always register human handoff tool - no callback needed for non-blocking version
	tools["request_human_handoff"] = NewHumanHandoffTool()

	return &ToolSet{tools: tools, screen: screen}
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

func (s *ToolSet) CurrentEnvironmentHints(maxAge time.Duration) CurrentEnvironmentHints {
	if s == nil || s.screen == nil {
		return CurrentEnvironmentHints{}
	}
	width, height, age, ok := s.screen.DimensionsWithAge()
	if !ok {
		return CurrentEnvironmentHints{}
	}
	if maxAge > 0 && age > maxAge {
		return CurrentEnvironmentHints{}
	}
	return CurrentEnvironmentHints{
		ScreenshotWidth:  width,
		ScreenshotHeight: height,
	}
}

func (s *ToolSet) RegisterMemoryTools(memoryDir string, profileFn ProfileFn, summaryMaxChunks int, debouncer *ProfileDebouncer) {
	if memoryDir == "" {
		return
	}
	sessionStore := NewSessionMemoryStore(filepath.Join(memoryDir, "session"), summaryMaxChunks)
	longTermStore := NewLongTermMemoryStore(filepath.Join(memoryDir, "long_term"), WithLifecycleDir(filepath.Join(memoryDir, "lifecycle")), WithStoreProfileFn(profileFn), WithProfileDebouncer(debouncer))
	deviceStore := NewDeviceMemoryStore(filepath.Join(memoryDir, "device"))
	episodeStore := NewTaskEpisodeStore(filepath.Join(memoryDir, "episodes"))
	s.tools["recall_session_chunks"] = NewRecallSessionChunksTool(sessionStore)
	s.tools["recall_memory"] = NewRecallMemoryTool(longTermStore)
	s.tools["save_memory"] = NewSaveMemoryTool(longTermStore)
	s.tools["forget_memory"] = NewForgetMemoryTool(longTermStore)
	s.tools["recall_device_memory"] = NewRecallDeviceMemoryTool(deviceStore)
	s.tools["inspect_episode"] = NewInspectEpisodeTool(episodeStore)
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
