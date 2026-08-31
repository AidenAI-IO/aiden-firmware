package agent

import "aiden-agent/internal/agent/realtimevoice"

import "aiden-agent/internal/agent/constants"

const (
	localeSimplifiedChinese = "zh-CN"
	localeEnglishUS         = "en-US"
	defaultLocale           = localeSimplifiedChinese
	defaultInstruction      = "Keep the tone natural, concise, and suitable for TTS playback. " +
		"Use tools whenever reading or changing phone, external-device, or service state; combine multiple tools when needed. " +
		"After every visual observation or input-tool result, inspect the latest screen to verify the previous action, focus, and navigation state before continuing; do not blindly repeat the same click, gesture, or key. " +
		"When opening apps or finding contacts, settings, products, or page content on the phone, prefer system search, in-app search, or visible search fields instead of relying on repeated scrolling. " +
		"Treat requests to place phone calls as phone-automation tasks; do not claim they are impossible merely because there is no dedicated dial tool."
	DefaultRealtimeVoiceInstructions = "You are Aiden, a realtime voice assistant. Focus on natural conversation. " +
		"Reply briefly and clearly in the user's language. Use get_current_time only when the exact current date or time matters. " +
		"Use recall_memory only when saved user preferences or facts are relevant. " +
		"For requests that require device operation, external actions, or longer multi-step work, use the available work-management tools and respond as the single assistant responsible for the request. " +
		"Never mention foreground or background agents, delegation, queues, orchestration, internal tools, task IDs, or implementation details to the user. Do not say that another agent or process is handling the work; speak naturally in the first person as Aiden. " +
		"When work is still in progress, briefly say that you are handling it and continue the conversation without exposing internal state. When work finishes, report the outcome as your own work. " +
		"If you cannot directly and reliably answer or complete a request with the realtime conversation tools, use the work-management tool to have the device-capable assistant handle it before saying that you cannot. This includes device state, screen or page content, apps, images, photos, external actions, information that requires observation or lookup, and any longer or multi-step operation. Never refuse merely because the realtime voice session lacks direct access; delegate the request and report the result as your own work. Do not delegate simple conversation, current-time requests, or relevant memory lookups that you can answer directly. " +
		"If a user action is required, clearly explain what the user should do on the device and ask them to tell you when it is complete, without mentioning why an internal handoff is needed."
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
	defaultStorageMountPoint       = "/mnt/sdcard"
	defaultStorageDevice           = "mmcblk2"
	defaultStorageMinCardFreeMB    = 64
	// Migration watermarks: start when eMMC free space drops below 10%,
	// stop once it is back at or above 50%.
	defaultStorageMigrateStartFreePct = 10
	defaultStorageMigrateStopFreePct  = 50
	defaultKeyboardDevice             = "/dev/hidg0"
	defaultMouseDevice                = "/dev/hidg1"
	defaultAndroidKeyboardDevice      = "/dev/hidg2"
	defaultFrameServiceSocket         = "/run/frame_service/frame_service.sock"
	defaultPointerMode                = "absolute"
	defaultDeviceType                 = "iOS"
	defaultInputBackend               = "hid"
	defaultInputMode                  = "text"
	defaultSilenceMs                  = 550
	defaultMinSpeechMs                = 300
	defaultVoiceFollowupTimeoutMs     = 5000
	defaultVoiceFirstTurnTimeoutMs    = 10000
	defaultVoiceMaxTurns              = 0
	defaultVoiceMaxResponseTokens     = 300
	defaultMaxIterations              = -1
	defaultVoiceModelProvider         = realtimevoice.ProviderQwen
	defaultVoiceModelModel            = realtimevoice.DefaultQwenRealtimeModel
	defaultVoiceModelVoice            = realtimevoice.DefaultQwenRealtimeVoice
	defaultVoiceModelTurnDetection    = "server_vad"

	defaultTelemetryProvider    = "langfuse"
	defaultTelemetryTimeoutSec  = 30
	defaultTelemetryMaxRetry    = 2
	defaultTelemetryEnvironment = "default"
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
			Backend:    AudioBackendAuto,
		},
		VoiceModel: VoiceModelConfig{
			Provider:          defaultVoiceModelProvider,
			Model:             defaultVoiceModelModel,
			Voice:             defaultVoiceModelVoice,
			Instructions:      DefaultRealtimeVoiceInstructions,
			InputAudioFormat:  "pcm",
			OutputAudioFormat: "pcm",
			TurnDetection:     defaultVoiceModelTurnDetection,
		},
		AudioArchive: AudioArchiveConfig{
			Enabled:     true,
			MaxFiles:    defaultAudioArchiveMaxFiles,
			MaxSizeMB:   defaultAudioArchiveMaxSizeMB,
			StoragePath: defaultAudioArchiveStoragePath,
		},
		FrameService: FrameServiceConfig{
			KeepStreamOn: false,
		},
		VoiceNotifications: VoiceNotificationsConfig{
			Enabled:    defaultBoolPtr(true),
			MaxPending: 8,
			ResponseTail: VoiceNotificationResponseTailConfig{
				Enabled:      defaultBoolPtr(true),
				MaxItems:     1,
				MaxTextChars: 40,
			},
			Expiration: VoiceNotificationExpirationConfig{
				DefaultTTLSeconds: 0,
				CodeTTLSeconds: map[string]int{
					"storage": 900,
				},
			},
		},
		Storage: StorageConfig{
			MountPoint:           defaultStorageMountPoint,
			Device:               defaultStorageDevice,
			MinCardFreeMB:        defaultStorageMinCardFreeMB,
			MigrateStartFreePct:  defaultStorageMigrateStartFreePct,
			MigrateStopFreePct:   defaultStorageMigrateStopFreePct,
			MonitorEnabled:       true,
			RootPath:             "/userdata",
			CheckIntervalSeconds: 300,
			WarningThresholdMB:   50,
			CriticalThresholdMB:  10,
			EmergencyThresholdMB: 5,
			RecoveryHysteresisMB: 5,
			DegradedMode: StorageDegradedModeConfig{
				DisableLLMHTTPLog:     true,
				DisableAudioArchive:   true,
				DisableSessionArchive: true,
				MaxAgentLogMB:         1,
			},
			Cleanup: StorageCleanupConfig{
				Enabled:                          true,
				LLMHTTPLogRetentionDays:          []int{7, 3, 1, 0},
				AudioArchiveRetentionDays:        []int{30, 7, 0},
				SessionArchiveRetentionDays:      []int{30},
				NotificationContextRetentionDays: []int{14, 7, 1, 0},
				CleanupRetryIntervalSeconds:      60,
			},
		},
		Log: LogConfig{
			LLMHTTPRetentionDays: defaultLLMHTTPLogRetentionDays,
		},
		HID: HIDConfig{
			KeyboardDevice:        defaultKeyboardDevice,
			KeyboardLayout:        defaultKeyboardLayout,
			MouseDevice:           defaultMouseDevice,
			AndroidKeyboardDevice: defaultAndroidKeyboardDevice,
			FrameSocket:           defaultFrameServiceSocket,
			PointerMode:           defaultPointerMode,
			InputBackend:          defaultInputBackend,
		},
		Device: DeviceConfig{
			DeviceType: defaultDeviceType,
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
		MaxIterations:              defaultMaxIterations,
		TerminationPolicy:          DefaultTerminationPolicyConfig(),
		ScreenshotKeepN:            constants.DefaultScreenshotKeepN,
		ScreenshotPruneInterval:    constants.DefaultScreenshotPruneInterval,
		ScreenStableTimeoutMs:      defaultStableWaitTimeoutMs,
		ScreenStableMs:             defaultStableDurationMs,
		ScreenStableDiffThreshold:  defaultDiffThreshold,
	}
}
