package agent

import (
	"aiden-agent/internal/agent/model"
	"aiden-agent/internal/agent/screen"
	"path/filepath"
	"sort"
	"time"

	langtools "github.com/tmc/langchaingo/tools"
)

// ToolSet is a fixed collection of built-in tools, keyed by name.
type ToolSet struct {
	tools                map[string]langtools.Tool
	screen               *screen.ScreenState
	phoneBridge          *PhoneBridge
	phoneBridgeRestorer  *PhoneBridgeRestorer
	textInputHW          *textInputHardwareDeps
	iosKeyboardIsolation *iosKeyboardIsolationController
}

type runtimePlatformConfigurable interface {
	SetPlatformFn(func() string)
}

// NewBuiltinToolSet returns all built-in tools. Tools are not configurable;
// everything is registered here with its runtime dependencies already wired up.
type BuiltinToolSetOption func(*builtinToolSetOptions)

type builtinToolSetOptions struct {
	waitForWakeupController *WaitForWakeupController
	screenStable            ScreenStableDefaults
	scriptsDir              string
	screenState             *screen.ScreenState
}

func WithWaitForWakeupController(controller *WaitForWakeupController) BuiltinToolSetOption {
	return func(options *builtinToolSetOptions) {
		options.waitForWakeupController = controller
	}
}

func WithScreenStableDefaults(defaults ScreenStableDefaults) BuiltinToolSetOption {
	return func(options *builtinToolSetOptions) {
		options.screenStable = defaults
	}
}

func WithRunScriptScriptsDir(dir string) BuiltinToolSetOption {
	return func(options *builtinToolSetOptions) {
		options.scriptsDir = dir
	}
}

// WithScreenState makes the tools publish visual observations to a shared
// ScreenState. When omitted, a private state is created for backwards
// compatibility with callers that only need the tool set itself.
func WithScreenState(state *screen.ScreenState) BuiltinToolSetOption {
	return func(options *builtinToolSetOptions) {
		options.screenState = state
	}
}

func NewBuiltinToolSet(hidCfg HIDConfig, audioCfg AudioConfig, searchCfg SearchConfig, proxyCfg ProxyConfig, options ...BuiltinToolSetOption) *ToolSet {
	return newHardwareToolSet(hidCfg, audioCfg, searchCfg, proxyCfg, options...)
}

func NewBuiltinToolSetFromConfig(cfg Config, proxyCfg ProxyConfig, options ...BuiltinToolSetOption) *ToolSet {
	defaultOptions := make([]BuiltinToolSetOption, 0, len(options)+1)
	if cfg.ConfigDir != "" {
		defaultOptions = append(defaultOptions, WithRunScriptScriptsDir(filepath.Join(cfg.ConfigDir, "scripts")))
	}
	options = append(defaultOptions, options...)
	return newHardwareToolSet(cfg.HID, cfg.Audio, cfg.Search, proxyCfg, options...)
}

var scriptCallableToolNames = map[string]struct{}{
	"audio_volume":           {},
	"enter_text":             {},
	"image_diff":             {},
	"keyboard_tap":           {},
	"mouse_click":            {},
	"mouse_move":             {},
	"mouse_scroll":           {},
	toolBridgeOpenApp:        {},
	"quick_action":           {},
	"screenshot":             {},
	"search_launch_app":      {},
	"touch_gesture":          {},
	"wait_for_stable_screen": {},
}

func isScriptCallableTool(name string) bool {
	_, ok := scriptCallableToolNames[name]
	return ok
}

func newHardwareToolSet(hidCfg HIDConfig, audioCfg AudioConfig, searchCfg SearchConfig, proxyCfg ProxyConfig, options ...BuiltinToolSetOption) *ToolSet {
	toolOptions := builtinToolSetOptions{}
	for _, option := range options {
		if option != nil {
			option(&toolOptions)
		}
	}

	kbDev := NewHIDDevice(hidCfg.KeyboardDeviceOrDefault())
	androidKbDev := NewHIDDevice(hidCfg.AndroidKeyboardDeviceOrDefault())
	screen := toolOptions.screenState
	if screen == nil {
		screen = newToolScreenState()
	}
	pointer := newPointerController(hidCfg)
	iosKeyboardIsolation := newIOSKeyboardIsolationController(hidCfg, kbDev, pointer.dev, androidKbDev)
	pointer.iosKeyboardIsolation = iosKeyboardIsolation
	var adbInput *ADBInputController
	if hidCfg.InputBackendADB() {
		adbInput = NewADBInputController(screen)
	}
	touchscreenRCALogf(
		"newHardwareToolSet pointer_mode=%q pointer_device=%q keyboard_device=%q keyboard_layout=%q android_keyboard_device=%q frame_socket=%q",
		hidCfg.PointerModeOrDefault(),
		hidCfg.MouseDeviceOrDefault(),
		hidCfg.KeyboardDeviceOrDefault(),
		hidCfg.KeyboardLayoutOrDefault(),
		hidCfg.AndroidKeyboardDeviceOrDefault(),
		hidCfg.FrameSocketOrDefault(),
	)
	screenshot := NewScreenshotTool(hidCfg.FrameSocketOrDefault(), screen)
	screenStable := toolOptions.screenStable.Resolved()
	waitStable := NewWaitStableScreenTool(hidCfg.FrameSocketOrDefault(), screenStable, screen)
	keyboardTap := &KeyboardTapTool{dev: kbDev, androidDev: androidKbDev, pointerMode: hidCfg.PointerModeOrDefault(), adb: adbInput, keyboardLayout: hidCfg.KeyboardLayoutOrDefault(), iosKeyboardIsolation: iosKeyboardIsolation}
	keyboardText := &KeyboardTextTool{dev: kbDev, adb: adbInput, keyboardLayout: hidCfg.KeyboardLayoutOrDefault(), iosKeyboardIsolation: iosKeyboardIsolation}
	touchGesture := &TouchGestureTool{pc: pointer, screen: screen, adb: adbInput}
	wheelNudge := &WheelNudgeTool{pc: pointer, screen: screen, requireFreshScreenshot: true}
	quickAction := &QuickActionTool{keyboard: keyboardTap, touch: touchGesture, iosKeyboardIsolation: iosKeyboardIsolation}
	mouseClick := &MouseClickTool{pc: pointer, screen: screen, adb: adbInput}
	textInputHW := &textInputHardwareDeps{
		pointerMode:  hidCfg.PointerModeOrDefault(),
		mouseClick:   mouseClick,
		touchGesture: touchGesture,
		keyboardTap:  keyboardTap,
		keyboardText: keyboardText,
		quickAction:  quickAction,
		screenshot:   screenshot,
	}

	tools := map[string]langtools.Tool{
		"keyboard_tap":           newPostActionStableScreenshotTool(keyboardTap, waitStable, screenshot, postActionScreenshotDelay, screenStable),
		"mouse_click":            newPostActionStableScreenshotTool(mouseClick, waitStable, screenshot, postActionScreenshotDelay, screenStable),
		"mouse_move":             newPostActionStableScreenshotTool(&MouseMoveTool{pc: pointer, screen: screen, adb: adbInput}, waitStable, screenshot, postActionScreenshotDelay, screenStable),
		"mouse_scroll":           newPostActionStableScreenshotTool(&MouseScrollTool{pc: pointer, adb: adbInput}, waitStable, screenshot, postActionScreenshotDelay, screenStable),
		"touch_gesture":          newPostActionStableScreenshotTool(touchGesture, waitStable, screenshot, postActionScreenshotDelay, screenStable),
		"wheel_nudge":            newPostActionStableScreenshotTool(wheelNudge, waitStable, screenshot, postActionScreenshotDelay, screenStable),
		"quick_action":           newPostActionStableScreenshotTool(quickAction, waitStable, screenshot, postActionScreenshotDelay, screenStable),
		"screenshot":             screenshot,
		"wait_for_stable_screen": waitStable,
		"image_diff":             &ImageDiffTool{},
		"audio_volume":           NewAudioVolumeTool(audioCfg.SocketOrDefault()),
		"shell":                  &ShellTool{proxy: proxyCfg},
		"weather":                NewWeatherTool(proxyCfg),
		"web_search":             NewWebSearchTool(searchCfg, proxyCfg),
		"wikipedia":              NewWikipediaTool(proxyCfg),
		"web_scraper":            NewWebScraperTool(proxyCfg),
	}
	if toolOptions.waitForWakeupController != nil {
		tools[toolWaitForWakeup] = NewWaitForWakeupTool(toolOptions.waitForWakeupController)
	}
	runScript := NewRunScriptTool(toolOptions.scriptsDir, func(name string) (langtools.Tool, bool) {
		if !isScriptCallableTool(name) {
			return nil, false
		}
		tool, ok := tools[name]
		return tool, ok
	})
	runScript.iosKeyboardIsolation = iosKeyboardIsolation
	tools["run_script"] = runScript
	tools["list_scripts"] = NewListScriptsTool(toolOptions.scriptsDir)
	tools["read_script"] = NewReadScriptTool(toolOptions.scriptsDir)
	tools["write_script"] = NewWriteScriptTool(toolOptions.scriptsDir)
	// Always register human handoff tool - no callback needed for non-blocking version
	tools["request_human_handoff"] = NewHumanHandoffTool()

	toolSet := &ToolSet{
		tools:                tools,
		screen:               screen,
		phoneBridgeRestorer:  NewPhoneBridgeRestorer(nil, pointer),
		textInputHW:          textInputHW,
		iosKeyboardIsolation: iosKeyboardIsolation,
	}
	touchGesture.primeScreenMapping = toolSet.PrimeScreenMapping
	return toolSet
}

func newToolScreenState() *screen.ScreenState {
	return &screen.ScreenState{}
}

func (s *ToolSet) RegisterEnterTextTool(models model.Model, platformFn func() string) {
	if s == nil || s.textInputHW == nil || models == nil {
		return
	}
	engine := newTextInputEngine(*s.textInputHW, newLLMTextInputVision(models))
	bridgeTool := &textInputBridge{
		hw:       s.textInputHW,
		vision:   newLLMTextInputVision(models),
		bridgeFn: func() *PhoneBridge { return s.phoneBridge },
		restorer: s.phoneBridgeRestorer,
	}
	entryTool := &EnterTextTool{engine: engine, bridgeTool: bridgeTool, iosKeyboardIsolation: s.iosKeyboardIsolation}
	searchOpenTool := &appSearchOpenTool{
		hw:                   s.textInputHW,
		vision:               newLLMTextInputVision(models),
		platformFn:           platformFn,
		entryTool:            entryTool,
		launchDelay:          appSearchOpenLaunchDelay,
		iosKeyboardIsolation: s.iosKeyboardIsolation,
	}
	s.tools["search_launch_app"] = searchOpenTool
	s.tools["enter_text"] = newPostActionScreenshotTool(entryTool, s.textInputHW.screenshot, 300*time.Millisecond)
}

func (s *ToolSet) SetRunScriptSpeaker(speaker runScriptSpeaker) {
	if s == nil {
		return
	}
	tool, ok := s.tools["run_script"]
	if !ok {
		return
	}
	if runScript, ok := tool.(*RunScriptTool); ok {
		runScript.SetSpeaker(speaker)
	}
}

func (s *ToolSet) SetRuntimePlatformFn(fn func() string) {
	if s == nil {
		return
	}
	for _, name := range []string{"enter_text", "search_launch_app"} {
		tool, ok := s.tools[name]
		if !ok {
			continue
		}
		if configurable, ok := tool.(runtimePlatformConfigurable); ok {
			configurable.SetPlatformFn(fn)
		}
	}
}

func (s *ToolSet) Get(name string) (langtools.Tool, bool) {
	if s == nil || !s.toolAvailable(name) {
		return nil, false
	}
	t, ok := s.tools[name]
	return t, ok
}

func (s *ToolSet) All() []langtools.Tool {
	if s == nil {
		return nil
	}
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
	if s == nil {
		return nil
	}
	names := make([]string, 0, len(s.tools))
	for name := range s.tools {
		if !s.toolAvailable(name) {
			continue
		}
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func (s *ToolSet) toolAvailable(name string) bool {
	if !isPhoneBridgeToolName(name) {
		return true
	}
	if s.phoneBridge == nil {
		return false
	}
	return phoneBridgeToolAvailable(s.phoneBridge.getStatus(), name)
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

func (s *ToolSet) UpdateDeviceEnvironment(env *PhoneEnvironment) {
	if s == nil || s.screen == nil {
		return
	}
	if env == nil {
		s.screen.ClearPhoneScreenInfo()
		return
	}
	s.screen.UpdatePhoneScreenInfo(env.Screen)
}

func (s *ToolSet) RegisterMemoryTools(memoryDir string, summaryMaxChunks int, longTermStore *LongTermMemoryStore) {
	if memoryDir == "" {
		return
	}
	sessionStore := NewSessionMemoryStore(filepath.Join(memoryDir, "session"), summaryMaxChunks)
	archivedStore := NewArchivedSessionStore(filepath.Join(memoryDir, "session_archive"))
	if longTermStore == nil {
		longTermStore = NewLongTermMemoryStore(filepath.Join(memoryDir, "long_term"), WithLifecycleDir(filepath.Join(memoryDir, "lifecycle")))
	}
	deviceStore := NewDeviceMemoryStore(filepath.Join(memoryDir, "device"))
	episodeStore := NewTaskEpisodeStore(filepath.Join(memoryDir, "episodes"))
	s.tools["recall_session_chunks"] = NewRecallSessionChunksTool(sessionStore, archivedStore)
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
