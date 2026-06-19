package agent

const (
	defaultInstruction = "默认用简体中文回答，语气要像真人说话，简短自然，适合 TTS 播放。" +
		"需要读取或改变手机、外部设备或服务状态时必须使用工具；可以连续组合多个工具完成任务。" +
		"每次视觉观察或输入工具返回后，都要先根据最新画面判断上一步是否已经生效、焦点是否改变、页面是否跳转；不要盲目重复同一个点击、手势或按键。" +
		"在手机上打开 App、查找联系人、设置项、商品或页面内容时，优先使用系统搜索、App 内搜索或页面上的搜索框；不要先靠连续滑动、翻页来碰运气。" +
		"用户要求拨打电话时，把它当作手机自动化任务；不要因为没有单独的拨打电话工具就说做不到。"
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
	defaultBenchmarkJudgeModel     = defaultModelName
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
	defaultScreenshotPruneInterval = 2
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
		Benchmark: BenchmarkConfig{
			JudgeModel: defaultBenchmarkJudgeModel,
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
		VoiceToolCallSpeech:        defaultBoolPtr(true),
		VoiceProgressSpeechEnabled: defaultBoolPtr(true),
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
