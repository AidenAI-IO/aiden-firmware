package agent

const (
	localeSimplifiedChinese = "zh-CN"
	localeEnglishUS         = "en-US"
	defaultLocale           = localeSimplifiedChinese
	defaultInstruction      = "Keep the tone natural, concise, and suitable for TTS playback. " +
		"Use tools whenever reading or changing phone, external-device, or service state; combine multiple tools when needed. " +
		"After every visual observation or input-tool result, inspect the latest screen to verify the previous action, focus, and navigation state before continuing; do not blindly repeat the same click, gesture, or key. " +
		"When opening apps or finding contacts, settings, products, or page content on the phone, prefer system search, in-app search, or visible search fields instead of relying on repeated scrolling. " +
		"Treat requests to place phone calls as phone-automation tasks; do not claim they are impossible merely because there is no dedicated dial tool."
	defaultModelProvider           = "openrouter"
	defaultModelName               = "bytedance-seed/seed-2.0-lite"
	defaultModelTemperature        = 0.2
	defaultModelMaxResponseTokens  = 1000
	defaultModelLogRawHTTP         = true
	defaultModelReasoningEffort    = ""
	defaultTTSProvider             = "minimax-cn"
	defaultTTSVoiceID              = "male-qn-qingse"
	defaultTTSEmotion              = "happy"
	defaultTTSSpeed                = 1.0
	defaultSTTProvider             = "openai-whisper"
	defaultSTTModel                = "whisper-1"
	defaultSTTLanguage             = "zh"
	tencentASRProvider             = "tencent-asr"
	legacyTencentProvider          = "tencent"
	legacyTencentASRProvider       = "tencent_asr"
	defaultTencentASRRegion        = "ap-shanghai"
	defaultTencentASREngineModel   = "16k_zh"
	defaultAudioSocket             = "/run/audio_service/audio_service.sock"
	defaultAudioSampleRate         = 16000
	defaultAudioChannels           = 1
	defaultAudioBitWidth           = 16
	defaultAudioArchiveStoragePath = "/userdata/audio"
	defaultAudioArchiveMaxFiles    = 500
	defaultAudioArchiveMaxSizeMB   = 100
	defaultLLMHTTPLogRetentionDays = 7
	defaultKeyboardDevice          = "/dev/hidg0"
	defaultMouseDevice             = "/dev/hidg1"
	defaultAndroidKeyboardDevice   = "/dev/hidg2"
	defaultFrameServiceSocket      = "/run/frame_service/frame_service.sock"
	defaultPointerMode             = "absolute"
	defaultInputBackend            = "hid"
	defaultInputMode               = "text"
	defaultTriggerMode             = "manual"
	defaultSilenceMs               = 550
	defaultMinSpeechMs             = 300
	defaultVoiceFollowupTimeoutMs  = 5000
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
			Provider: defaultModelProvider,
			Model:    defaultModelName,
			// Temperature is intentionally left unset here; the effective default
			// is resolved from model metadata at load time (see
			// applyModelTemperatureDefault), falling back to defaultModelTemperature.
			MaxResponseTokens: defaultModelMaxResponseTokens,
			LogRawHTTP:        defaultModelLogRawHTTP,
			ReasoningEffort:   defaultModelReasoningEffort,
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
			Language: defaultSTTLanguage,
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
		Log: LogConfig{
			LLMHTTPRetentionDays: defaultLLMHTTPLogRetentionDays,
		},
		HID: HIDConfig{
			KeyboardDevice:         defaultKeyboardDevice,
			MouseDevice:            defaultMouseDevice,
			AndroidKeyboardDevice:  defaultAndroidKeyboardDevice,
			FrameSocket:            defaultFrameServiceSocket,
			DynamicKeyboard:        false,
			DynamicKeyboardControl: defaultDynamicKeyboardControl,
			PointerMode:            defaultPointerMode,
			InputBackend:           defaultInputBackend,
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
		Locale:                     defaultLocale,
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
		TerminationPolicy:          DefaultTerminationPolicyConfig(),
		ScreenshotKeepN:            defaultScreenshotKeepN,
		ScreenshotPruneInterval:    defaultScreenshotPruneInterval,
		ScreenStableTimeoutMs:      defaultStableWaitTimeoutMs,
		ScreenStableMs:             defaultStableDurationMs,
		ScreenStableDiffThreshold:  defaultDiffThreshold,
	}
}
