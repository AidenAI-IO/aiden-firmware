package agent

const (
	defaultInstruction = "默认用简体中文回答，语气要像真人说话，简短自然，适合 TTS 播放。" +
		"需要读取或改变手机、外部设备或服务状态时必须使用工具；可以连续组合多个工具完成任务。" +
		"每次截图、wait_for_stable_screen 返回截图或输入工具返回 post-action screenshot 后，都要先根据最新画面判断上一步是否已经生效、焦点是否改变、页面是否跳转；不要连续重复同一个点击、手势或按键。" +
		"在手机上打开 App、查找联系人、设置项、商品或页面内容时，优先使用系统搜索、App 内搜索或页面上的搜索框；不要先靠连续滑动、翻页来碰运气。" +
		"keyboard_text 是模拟美式键盘按键，必须传 JSON，例如 {\"text\":\"App Store\"}；不要传裸字符串；只能输入 ASCII 可键入字符，不能直接输入中文、emoji 或其他非键盘字符，需要中文时改用拼音/英文关键词并从候选或搜索结果中选择。" +
		"点击要以最新截图为准，选择可见目标的中心点，并优先使用 coord_space:\"normalized\" 的 0-1000 坐标（(0,0) 左上角，(1000,1000) 右下角，(500,500) 中心）；手机投屏/截图可能被缩放，pixel 坐标容易和实际触控坐标偏移。除非用户明确要求或坐标系已经校准，不要使用 coord_space:\"pixel\"。坐标不确定时先截图确认，不要用大概位置连续试点。" +
		"用户要求拨打电话时，把它当作手机 UI 自动化任务：先用截图确认状态，再用 touch_gesture、mouse_click、keyboard_text、keyboard_tap 等工具打开拨号或联系人、输入号码并点击拨号；不要因为没有单独的拨打电话工具就说做不到。" +
		"手机边缘手势要从物理边缘附近开始，返回优先用 touch_gesture 的 type back，回主屏优先用 type home；手写 swipe 时左边缘返回用 start.x=1 左右，底边回主页用 start.y=999 左右。"
	defaultModelProvider           = "openrouter"
	defaultModelName               = "bytedance-seed/seed-2.0-lite"
	defaultModelTemperature        = 0.2
	defaultModelMaxResponseTokens  = 1000
	defaultTTSProvider             = "minimax-ws"
	defaultTTSVoiceID              = "male-qn-qingse"
	defaultTTSEmotion              = "happy"
	defaultTTSSpeed                = 1.0
	defaultSTTProvider             = "openai-whisper"
	defaultSTTModel                = "whisper-1"
	tencentASRProvider             = "tencent-asr"
	legacyTencentProvider          = "tencent"
	legacyTencentASRProvider       = "tencent_asr"
	defaultTencentASRRegion        = "ap-guangzhou"
	defaultTencentASREngineModel   = "16k_zh"
	defaultAudioSocket             = "/run/audio_service/audio_service.sock"
	defaultAudioSampleRate         = 16000
	defaultAudioChannels           = 1
	defaultAudioBitWidth           = 16
	defaultAudioArchiveStoragePath = "/userdata/audio"
	defaultAudioArchiveMaxFiles    = 500
	defaultAudioArchiveMaxSizeMB   = 100
	defaultKeyboardDevice          = "/dev/hidg0"
	defaultMouseDevice             = "/dev/hidg1"
	defaultFrameServiceSocket      = "/run/frame_service/frame_service.sock"
	defaultPointerMode             = "absolute"
	defaultInputMode               = "text"
	defaultTriggerMode             = "manual"
	defaultSilenceMs               = 650
	defaultMinSpeechMs             = 300
	defaultVoiceFollowupTimeoutMs  = 6000
	defaultVoiceFirstTurnTimeoutMs = 10000
	defaultVoiceMaxTurns           = 0
	defaultVoiceMaxResponseTokens  = 300
	defaultTodoReminderToolCalls   = 3
	defaultMaxIterations           = -1
	defaultScreenshotKeepN         = 3
	defaultScreenshotPruneInterval = 25
	defaultTelemetryProvider       = "langfuse"
	defaultTelemetryTimeoutSec     = 30
	defaultTelemetryMaxRetry       = 2
	defaultTelemetryEnvironment    = "default"
)

func defaultBoolPtr(value bool) *bool {
	v := value
	return &v
}

func DefaultConfig() Config {
	return Config{
		Model: ModelConfig{
			Provider:          defaultModelProvider,
			Model:             defaultModelName,
			Temperature:       defaultModelTemperature,
			MaxResponseTokens: defaultModelMaxResponseTokens,
		},
		TTS: TTSConfig{
			Provider: defaultTTSProvider,
			VoiceID:  defaultTTSVoiceID,
			Emotion:  defaultTTSEmotion,
			Speed:    defaultTTSSpeed,
		},
		STT: STTConfig{
			Provider: defaultSTTProvider,
			Model:    defaultSTTModel,
		},
		Audio: AudioConfig{
			Socket:     defaultAudioSocket,
			SampleRate: defaultAudioSampleRate,
			Channels:   defaultAudioChannels,
			BitWidth:   defaultAudioBitWidth,
		},
		AudioArchive: AudioArchiveConfig{
			Enabled:     true,
			MaxFiles:    defaultAudioArchiveMaxFiles,
			MaxSizeMB:   defaultAudioArchiveMaxSizeMB,
			StoragePath: defaultAudioArchiveStoragePath,
		},
		HID: HIDConfig{
			KeyboardDevice: defaultKeyboardDevice,
			MouseDevice:    defaultMouseDevice,
			FrameSocket:    defaultFrameServiceSocket,
			PointerMode:    defaultPointerMode,
		},
		Search: SearchConfig{
			Provider: searchProviderDuckDuckGo,
		},
		Telemetry: TelemetryConfig{
			Enabled:           defaultBoolPtr(false),
			Provider:          defaultTelemetryProvider,
			UploadScreenshots: defaultBoolPtr(true),
			UploadTimeoutSec:  defaultTelemetryTimeoutSec,
			MaxRetry:          defaultTelemetryMaxRetry,
			Tags:              []string{},
			Environment:       defaultTelemetryEnvironment,
		},
		Instruction:                defaultInstruction,
		InputMode:                  defaultInputMode,
		TriggerMode:                defaultTriggerMode,
		VADBackend:                 defaultVADBackend,
		VADModelPath:               defaultVADModelPath,
		VADHelperPath:              defaultVADHelperPath,
		VADSpeechThreshold:         defaultVADSpeechThreshold,
		SilenceMs:                  defaultSilenceMs,
		MinSpeechMs:                defaultMinSpeechMs,
		VoiceFollowupEnabled:       defaultBoolPtr(false),
		VoiceFollowupTimeoutMs:     defaultVoiceFollowupTimeoutMs,
		VoiceFirstTurnTimeoutMs:    defaultVoiceFirstTurnTimeoutMs,
		VoiceMaxTurns:              defaultVoiceMaxTurns,
		VoiceInterruptOnWakeup:     defaultBoolPtr(true),
		VoiceStreamingTTSEnabled:   defaultBoolPtr(true),
		VoiceToolCallSpeech:        defaultBoolPtr(false),
		VoiceProgressSpeechEnabled: defaultBoolPtr(true),
		VoiceSpeechSummaryEnabled:  defaultBoolPtr(true),
		VoiceMaxResponseTokens:     defaultVoiceMaxResponseTokens,
		TodoReminderToolCalls:      defaultTodoReminderToolCalls,
		MaxIterations:              defaultMaxIterations,
		ScreenshotKeepN:            defaultScreenshotKeepN,
		ScreenshotPruneInterval:    defaultScreenshotPruneInterval,
		ScreenStableTimeoutMs:      defaultStableWaitTimeoutMs,
		ScreenStableMs:             defaultStableDurationMs,
		ScreenStableDiffThreshold:  defaultDiffThreshold,
	}
}
