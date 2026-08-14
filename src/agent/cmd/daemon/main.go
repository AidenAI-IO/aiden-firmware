package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"aiden-agent/internal/agent"
	"aiden-agent/internal/logging"
	"aiden-agent/internal/ota"
)

const (
	wakeupListenTimeout                     = 10 * time.Second
	wakeupDebounceInterval                  = 500 * time.Millisecond
	defaultVoiceSteerListenTimeout          = 45 * time.Second
	voiceWakeupInterruptedCorrectionContext = "Voice interruption: the user pressed the physical wakeup button " +
		"while the previous voice turn was still running. The previous task was interrupted and did NOT complete. " +
		"Based on the user's new input, judge the relationship to the interrupted task:\n" +
		"- If the new input replaces the old task (e.g. \"don't do that\", \"do B instead\") → only execute the new request.\n" +
		"- If the new input adds to or reorders (e.g. \"then do B\", \"do B first\") → execute the new request first, then briefly ask whether to resume the interrupted task.\n" +
		"- If ambiguous → briefly confirm the user's intent before acting."
)

var voiceSteerListenTimeout = defaultVoiceSteerListenTimeout
var voiceTurnCancelWaitTimeout = 2 * time.Second
var wakeupDebounceNow = time.Now

var wakeupGPIOPins = []int{33, 32}

func main() {
	// Check if a subcommand is provided
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "config-check":
			os.Exit(runConfigCheck(os.Args[2:]))
		case "config-meta":
			os.Exit(runConfigMeta(os.Args[2:]))
		case "config":
			os.Exit(runConfig(os.Args[2:]))
		case "config-test":
			os.Exit(runConfigTest(os.Args[2:]))
		}
	}
	logging.InstallStandard("agent", os.Stderr)

	// Default: run as daemon
	var (
		dataDir                   = flag.String("dir", "", "path to the agent data directory holding agent.toml, skills, memory, cache and logs (required)")
		addr                      = flag.String("addr", "0.0.0.0:8080", "HTTP server address")
		deviceType                = registerDeviceTypeFlag(flag.CommandLine)
		environmentBridgeMode     = flag.Bool("environment-bridge-mode", false, "Enable environment bridge mode (forward selected tool calls to an environment bridge; see --environment-bridge-tools)")
		environmentBridgeEndpoint = flag.String("environment-bridge-endpoint", "", "Environment bridge endpoint (e.g., http://192.168.50.123:8080)")
		environmentBridgeTools    = flag.String("environment-bridge-tools", "", "Comma-separated tool names or glob patterns to forward when environment-bridge-mode is on, e.g. \"keyboard_*,mouse_*,screenshot\" or \"*\". Required with --environment-bridge-mode.")
		benchmarkTaskID           = flag.String("benchmark-task-id", "", "Benchmark task id to include on environment bridge requests for task routing")
		benchmarkTokenFile        = flag.String("benchmark-token-file", "", "Path to benchmark API bearer token file. Enables benchmark-only mutation endpoints.")
	)
	flag.Parse()

	dir := strings.TrimSpace(*dataDir)
	if dir == "" {
		_ = logging.LogEvent(logging.Error, "agent", "startup", "data_directory_missing")
		os.Exit(1)
	}

	cfg, err := agent.LoadRuntimeConfigFromDir(dir)
	if err != nil {
		_ = logging.LogEvent(logging.Error, "agent", "startup", "config_load_failed",
			logging.Field{Key: "dir", Value: dir},
			logging.Field{Key: "error", Value: err},
		)
		os.Exit(1)
	}
	if err := applyDeviceTypeOverride(&cfg, *deviceType); err != nil {
		_ = logging.LogEvent(logging.Error, "agent", "startup", "device_type_override_invalid",
			logging.Field{Key: "device_type", Value: strings.TrimSpace(*deviceType)},
			logging.Field{Key: "error", Value: err},
		)
		os.Exit(1)
	}

	// Apply CLI flags to override config file
	if *environmentBridgeMode {
		cfg.EnvironmentBridge.Enabled = true
		if *environmentBridgeEndpoint != "" {
			cfg.EnvironmentBridge.Endpoint = *environmentBridgeEndpoint
		}
		if cfg.EnvironmentBridge.Endpoint == "" {
			_ = logging.LogEvent(logging.Error, "agent", "startup", "environment_bridge_endpoint_missing")
			os.Exit(1)
		}
		cfg.EnvironmentBridge.Tools = parseCommaSeparated(*environmentBridgeTools)
		if len(cfg.EnvironmentBridge.Tools) == 0 {
			_ = logging.LogEvent(logging.Error, "agent", "startup", "environment_bridge_tools_missing")
			os.Exit(1)
		}
		cfg.EnvironmentBridge.BenchmarkTaskID = strings.TrimSpace(*benchmarkTaskID)
	}
	if strings.TrimSpace(*benchmarkTokenFile) != "" {
		data, err := os.ReadFile(strings.TrimSpace(*benchmarkTokenFile))
		if err != nil {
			_ = logging.LogEvent(logging.Error, "agent", "startup", "benchmark_token_read_failed",
				logging.Field{Key: "path", Value: strings.TrimSpace(*benchmarkTokenFile)},
				logging.Field{Key: "error", Value: err},
			)
			os.Exit(1)
		}
		cfg.Benchmark.Token = strings.TrimSpace(string(data))
		if cfg.Benchmark.Token == "" {
			_ = logging.LogEvent(logging.Error, "agent", "startup", "benchmark_token_empty")
			os.Exit(1)
		}
	}
	proxyConfig := agent.ProxyConfigFromEnvironment()
	if err := proxyConfig.Validate(); err != nil {
		_ = logging.LogEvent(logging.Error, "agent", "startup", "proxy_environment_invalid",
			logging.Field{Key: "error", Value: err},
		)
		os.Exit(1)
	}
	cfg.SkillMergeModel = agent.NewLLMSkillMergeModel(agent.NewModelManager(cfg.Model, proxyConfig))

	runtime, err := agent.NewRuntime(cfg)
	if err != nil {
		_ = logging.LogEvent(logging.Error, "agent", "startup", "runtime_create_failed",
			logging.Field{Key: "error", Value: err},
		)
		os.Exit(1)
	}
	defer runtime.Close()
	if err := runtime.StartStorageMonitor(); err != nil {
		log.Printf("[storage_monitor] startup check failed: %v", err)
	}
	if wrote, err := ota.WriteHealthMarkerIfPending("/userdata/ota/pending_boot.json", "/userdata/ota/health.ok"); err != nil {
		log.Printf("[ota] health marker not written: %v", err)
	} else if wrote {
		log.Printf("[ota] health marker written")
	}
	if err := runtime.PrimeScreenMappingOnStartup(context.Background()); err != nil {
		log.Printf("[init] screen mapping prime failed: %v", err)
	}

	inputMode := cfg.InputModeOrDefault()

	// HTTP server runs in all input modes so the web UI is available even
	// during voice (audio/stt) interactions.
	server := agent.NewServer(runtime, *addr)
	defer server.Close()

	_ = logging.LogEvent(logging.Info, "agent", "startup", "daemon_starting",
		logging.Field{Key: "addr", Value: *addr},
		logging.Field{Key: "dir", Value: dir},
	)
	if cfg.EnvironmentBridge.Enabled {
		fields := []logging.Field{
			{Key: "endpoint", Value: cfg.EnvironmentBridge.Endpoint},
			{Key: "tools", Value: cfg.EnvironmentBridge.Tools},
		}
		if cfg.EnvironmentBridge.BenchmarkTaskID != "" {
			fields = append(fields, logging.Field{Key: "benchmark_task_id", Value: cfg.EnvironmentBridge.BenchmarkTaskID})
		}
		_ = logging.LogEvent(logging.Info, "agent", "environment_bridge", "bridge_enabled", fields...)
	}
	if _, port, err := net.SplitHostPort(*addr); err == nil && port != "" {
		_ = logging.LogEvent(logging.Info, "agent", "http", "web_ui_available",
			logging.Field{Key: "url", Value: "http://localhost:" + port},
		)
		if cfg.HID.InputBackendADB() {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			if err := agent.EnsureADBReverse(ctx, port, port); err != nil {
				log.Printf("[adb] reverse tcp:%s -> tcp:%s setup failed: %v", port, port, err)
			} else {
				log.Printf("[adb] reverse tcp:%s -> tcp:%s configured for companion app desktop bridge", port, port)
			}
			cancel()
		}
	}
	_ = logging.LogEvent(logging.Info, "agent", "startup", "log_directory_configured",
		logging.Field{Key: "path", Value: filepath.Join(dir, "log")},
	)

	serverErr := make(chan error, 1)
	go func() {
		serverErr <- server.Start()
	}()

	if inputMode == "stt" {
		if shouldRunConsoleAudioLoop(cfg, stdinIsInteractive()) {
			go func() {
				if err := <-serverErr; err != nil {
					log.Printf("[server] HTTP server stopped: %v", err)
				}
			}()
			runAudioMode(cfg, runtime, server)
			return
		}
		log.Printf("[manual] stdin is not interactive; console audio loop disabled, HTTP audio controls remain available")
		if err := <-serverErr; err != nil {
			_ = logging.LogEvent(logging.Error, "agent", "http", "server_failed",
				logging.Field{Key: "error", Value: err},
			)
			os.Exit(1)
		}
		return
	}

	if err := <-serverErr; err != nil {
		_ = logging.LogEvent(logging.Error, "agent", "http", "server_failed",
			logging.Field{Key: "error", Value: err},
		)
		os.Exit(1)
	}
}

func registerDeviceTypeFlag(fs *flag.FlagSet) *string {
	return fs.String("device-type", "", "Override device.device_type for this daemon process (iOS, Android, macOS, windows, or linux)")
}

func applyDeviceTypeOverride(cfg *agent.Config, value string) error {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil
	}
	return cfg.OverrideDeviceType(value)
}

func stdinIsInteractive() bool {
	info, err := os.Stdin.Stat()
	if err != nil {
		return false
	}
	return (info.Mode() & os.ModeCharDevice) != 0
}

func shouldRunConsoleAudioLoop(cfg agent.Config, stdinInteractive bool) bool {
	inputMode := cfg.InputModeOrDefault()
	if inputMode != "stt" {
		return false
	}
	return cfg.TriggerModeOrDefault() != "manual" || stdinInteractive
}

func runAudioMode(cfg agent.Config, runtime *agent.Runtime, server *agent.Server) {
	dialog, err := agent.NewAudioDialog(runtime)
	if err != nil {
		_ = logging.LogEvent(logging.Error, "agent", "audio", "dialog_create_failed",
			logging.Field{Key: "error", Value: err},
		)
		os.Exit(1)
	}
	defer func() {
		if err := dialog.Close(); err != nil {
			log.Printf("[audio] close dialog: %v\n", err)
		}
	}()
	dialog.SetHistoryAppender(server.AppendHistory)
	dialog.SetStorageManager(runtime.Storage())
	dialog.SetStorageMonitor(runtime.StorageMonitor())

	// Register preempt hook: when a new run starts, release GPIO audio resources.
	runtime.RegisterPreemptHook(func() {
		dialog.InterruptOutput()
		if dialog.RecordingActive() {
			_ = dialog.StopRecording()
		}
	})

	inputMode := cfg.InputModeOrDefault()
	triggerMode := cfg.TriggerModeOrDefault()

	log.Printf("[init] Config loaded: model=%s, input_mode=%s, trigger_mode=%s\n",
		cfg.Model.Model, inputMode, triggerMode)
	vadBackend := cfg.VADBackendOrDefault()
	log.Printf("[init] VAD: backend=%s, rknn_model=%s, helper=%s, speech_threshold=%.2f, silence=%dms, min_speech=%dms\n",
		vadBackend,
		valueOrDefault(cfg.VADModelPath, agent.DefaultVADModelPath()),
		agent.ResolveVADHelperPath(vadBackend, cfg.VADHelperPath),
		floatOrDefault(cfg.VADSpeechThreshold, agent.DefaultVADSpeechThreshold()),
		cfg.SilenceMs,
		cfg.MinSpeechMs)

	// Setup signal handler
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)

	if triggerMode == "manual" {
		runManualMode(cfg, dialog, runtime, sigChan)
	} else if triggerMode == "wakeup" {
		runWakeupMode(cfg, dialog, runtime, sigChan, newGPIOWatcher)
	} else {
		log.Printf("[error] Unsupported trigger mode: %s\n", triggerMode)
		os.Exit(1)
	}
}

type audioDialogRunner interface {
	StartRecording() error
	StopRecording() error
	RecordingActive() bool
	ReadRecordChunk(timeoutMs uint32) (*agent.AudioChunkResult, error)
	ProcessVADFrame(samples []int16) ([]int16, error)
	ProcessUtterance(ctx context.Context, utterance []int16, runtime *agent.Runtime) error
	PrepareTurnInput(utterance []int16) (agent.TurnInput, error)
	RunVoiceTurn(ctx context.Context, input agent.TurnInput, utterance []int16, runtime *agent.Runtime) (agent.RunResult, error)
	RunVoiceTurnWithContext(ctx context.Context, input agent.TurnInput, utterance []int16, runtime *agent.Runtime, turnContext agent.VoiceTurnContext) (agent.RunResult, error)
	QueueSteer(input agent.TurnInput) bool
	BeginSteerInterrupt() bool
	ResumeSteerInterrupt() bool
	WaitForVoiceRunIdle(ctx context.Context) bool
	ForceResetVoiceRun()
	InterruptOutput()
	CanSpeakFinalText() bool
	Speak(ctx context.Context, text string, interrupt <-chan struct{}) error
	SpeakFinal(ctx context.Context, text string, interrupt <-chan struct{}) error
	FlushVAD() []int16
	FinishManualUtterance(pending []int16) []int16
	ResetVAD()
	VADFrameSamples() int
}

type manualStopResult struct {
	utterance  []int16
	vadHandled bool
}

type wakeupWatcher interface {
	Start() error
	Stop()
}

func valueOrDefault(value, fallback string) string {
	if value != "" {
		return value
	}
	return fallback
}

func floatOrDefault(value, fallback float64) float64 {
	if value > 0 {
		return value
	}
	return fallback
}

func parseCommaSeparated(input string) []string {
	if strings.TrimSpace(input) == "" {
		return nil
	}
	parts := strings.Split(input, ",")
	result := make([]string, 0, len(parts))
	for _, part := range parts {
		if trimmed := strings.TrimSpace(part); trimmed != "" {
			result = append(result, trimmed)
		}
	}
	return result
}

type wakeupWatcherFactory func(pin int, callback func()) (wakeupWatcher, error)

func newGPIOWatcher(pin int, callback func()) (wakeupWatcher, error) {
	return agent.NewGPIOWatcher(pin, callback)
}

func wakeupGPIOPinsLabel() string {
	return "GPIO 33/GPIO 32"
}

func startWakeupWatchers(newWatcher wakeupWatcherFactory, callback func()) ([]wakeupWatcher, error) {
	return startWakeupWatchersWithDebounce(newWatcher, callback, wakeupDebounceInterval, wakeupDebounceNow)
}

type wakeupDebouncer struct {
	mu       sync.Mutex
	interval time.Duration
	now      func() time.Time
	last     time.Time
}

func newWakeupDebouncer(interval time.Duration, now func() time.Time) *wakeupDebouncer {
	if now == nil {
		now = time.Now
	}
	return &wakeupDebouncer{
		interval: interval,
		now:      now,
	}
}

func (d *wakeupDebouncer) allow() bool {
	if d == nil || d.interval <= 0 {
		return true
	}

	now := d.now()
	d.mu.Lock()
	defer d.mu.Unlock()

	if !d.last.IsZero() {
		elapsed := now.Sub(d.last)
		if elapsed >= 0 && elapsed < d.interval {
			return false
		}
	}
	d.last = now
	return true
}

func startWakeupWatchersWithDebounce(newWatcher wakeupWatcherFactory, callback func(), debounceInterval time.Duration, now func() time.Time) ([]wakeupWatcher, error) {
	watchers := make([]wakeupWatcher, 0, len(wakeupGPIOPins))
	debouncer := newWakeupDebouncer(debounceInterval, now)
	for _, pin := range wakeupGPIOPins {
		pin := pin
		watcher, err := newWatcher(pin, func() {
			if !debouncer.allow() {
				log.Printf("[wakeup] GPIO %d wakeup event ignored by %s debounce", pin, debounceInterval)
				return
			}
			log.Printf("[wakeup] GPIO %d wakeup event", pin)
			callback()
		})
		if err != nil {
			stopWakeupWatchers(watchers)
			return nil, fmt.Errorf("create GPIO %d watcher: %w", pin, err)
		}
		if err := watcher.Start(); err != nil {
			stopWakeupWatchers(append(watchers, watcher))
			return nil, fmt.Errorf("start GPIO %d watcher: %w", pin, err)
		}
		watchers = append(watchers, watcher)
	}
	return watchers, nil
}

func stopWakeupWatchers(watchers []wakeupWatcher) {
	for i := len(watchers) - 1; i >= 0; i-- {
		watchers[i].Stop()
	}
}

type vadDebugReporter interface {
	VADDebugState() agent.VADDebugState
}

func formatVADDebugLog(state agent.VADDebugState) string {
	msg := fmt.Sprintf("[vad] probability=%.3f threshold=%.3f speaking=%v speech_frames=%d silence_frames=%d",
		state.Probability,
		state.Threshold,
		state.Speaking,
		state.SpeechFrames,
		state.SilenceFrames,
	)
	if state.LastError != "" {
		msg += fmt.Sprintf(" last_error=%q", state.LastError)
	}
	return msg
}

type voiceEvent int

const (
	voiceEventWakeup voiceEvent = iota + 1
)

type pendingVoiceTurn struct {
	input       agent.TurnInput
	utterance   []int16
	turnContext agent.VoiceTurnContext
}

type voiceTurnResult struct {
	interrupted            bool
	waitForWakeupRequested bool
	exit                   bool
	nextTurn               *pendingVoiceTurn
	followUpContext        agent.VoiceTurnContext
}

func runManualMode(cfg agent.Config, dialog audioDialogRunner, runtime *agent.Runtime, sigChan chan os.Signal) {
	log.Println("\n[ready] Press Enter to start recording, press Enter again to stop")

	scanner := bufio.NewScanner(os.Stdin)
	recording := false
	var manualStop chan manualStopResult
	var cancelRecording context.CancelFunc
	ctx := context.Background()

	for {
		select {
		case <-sigChan:
			if recording {
				if cancelRecording != nil {
					cancelRecording()
				}
				dialog.StopRecording()
			}
			log.Println("\n[exit] Stopped.")
			return
		default:
		}

		if !recording {
			log.Println("\n[manual] Press Enter to start...")
			if !scanner.Scan() {
				break
			}

			log.Println("[manual] Recording started, press Enter to stop...")
			if err := dialog.StartRecording(); err != nil {
				log.Printf("[error] Failed to start recording: %v\n", err)
				continue
			}
			recording = true

			manualStop = make(chan manualStopResult, 1)
			cancelRecording = startManualAudioLoop(ctx, dialog, runtime, manualStop)
		} else {
			if !scanner.Scan() {
				break
			}

			log.Println("[manual] Stop triggered by user")
			if cancelRecording != nil {
				cancelRecording()
				cancelRecording = nil
			}

			// Wait for the capture loop to flush tail PCM, then close capture
			// before TTS so playback is never fed back into the mic session.
			result := <-manualStop
			recording = false
			if result.vadHandled {
				log.Println("\n[ready] Waiting for next trigger...")
				continue
			}
			if err := dialog.StopRecording(); err != nil {
				log.Printf("[listen] stop recording before manual utterance processing: %v\n", err)
			}
			if len(result.utterance) > 0 {
				log.Println("[manual] Sending buffered audio without waiting for VAD")
				if err := dialog.ProcessUtterance(ctx, result.utterance, runtime); err != nil {
					log.Printf("[error] %v\n", err)
				}
			} else {
				log.Println("[manual] No buffered audio to send")
			}

			log.Println("\n[ready] Waiting for next trigger...")
		}
	}

	if err := scanner.Err(); err != nil {
		log.Printf("[error] %v\n", err)
	}
	if recording {
		if cancelRecording != nil {
			cancelRecording()
			cancelRecording = nil
		}
		dialog.StopRecording()
	}

	log.Println("\n[exit] Stopped.")
}

func startManualAudioLoop(ctx context.Context, dialog audioDialogRunner, runtime *agent.Runtime, manualStop chan<- manualStopResult) context.CancelFunc {
	loopCtx, cancel := context.WithCancel(ctx)
	go processAudioLoop(dialog, runtime, loopCtx, manualStop)
	return cancel
}

func runWakeupMode(cfg agent.Config, dialog audioDialogRunner, runtime *agent.Runtime, sigChan chan os.Signal, newWatcher wakeupWatcherFactory) {
	if cfg.InputModeOrDefault() == "stt" && !cfg.VoiceFollowupEnabledOrDefault() {
		runSingleTurnSTTWakeupMode(cfg, dialog, runtime, sigChan, newWatcher)
		return
	}
	if cfg.InputModeOrDefault() != "stt" || !cfg.VoiceFollowupEnabledOrDefault() {
		runLegacyWakeupMode(cfg, dialog, runtime, sigChan, newWatcher)
		return
	}

	log.Printf("\n[ready] Starting GPIO wakeup listeners on %s...", wakeupGPIOPinsLabel())

	events := make(chan voiceEvent, 1)

	watchers, err := startWakeupWatchers(newWatcher, func() {
		dialog.InterruptOutput()
		signalVoiceWakeupEvent(events)
	})
	if err != nil {
		log.Printf("[error] Failed to start GPIO wakeup listeners: %v\n", err)
		os.Exit(1)
	}
	defer stopWakeupWatchers(watchers)

	log.Printf("[ready] Waiting for wakeup event (%s)... Ctrl+C to quit", wakeupGPIOPinsLabel())

	for {
		select {
		case <-sigChan:
			log.Println("\n[exit] Stopped.")
			return
		case <-events:
			log.Println("\n[wakeup] GPIO wakeup triggered, opening voice session...")
			if exit := runVoiceSession(cfg, dialog, runtime, sigChan, events); exit {
				log.Println("\n[exit] Stopped.")
				return
			}
			log.Println("[ready] Waiting for wakeup event...")
		}
	}
}

func runLegacyWakeupMode(cfg agent.Config, dialog audioDialogRunner, runtime *agent.Runtime, sigChan chan os.Signal, newWatcher wakeupWatcherFactory) {
	log.Printf("\n[ready] Starting GPIO wakeup listeners on %s...", wakeupGPIOPinsLabel())

	ctx := context.Background()
	wakeupEvents := make(chan struct{}, 1)

	watchers, err := startWakeupWatchers(newWatcher, func() {
		dialog.InterruptOutput()
		signalWakeupEvent(wakeupEvents)
	})
	if err != nil {
		log.Printf("[error] Failed to start GPIO wakeup listeners: %v\n", err)
		os.Exit(1)
	}
	defer stopWakeupWatchers(watchers)

	log.Printf("[ready] Waiting for wakeup event (%s)... Ctrl+C to quit", wakeupGPIOPinsLabel())

	pendingWakeup := false
	for {
		if !pendingWakeup {
			select {
			case <-sigChan:
				if dialog != nil {
					dialog.StopRecording()
				}
				log.Println("\n[exit] Stopped.")
				return
			case <-wakeupEvents:
				log.Println("\n[wakeup] GPIO wakeup triggered, starting to listen...")
			}
		} else {
			pendingWakeup = false
		}

		// Start recording
		log.Println("[listen] Recording audio...")
		if err := dialog.StartRecording(); err != nil {
			log.Printf("[error] Failed to start recording: %v\n", err)
			continue
		}

		dialog.ResetVAD()
		exit, interrupted := processAudioUntilUtteranceWithWakeupInterrupt(
			dialog,
			runtime,
			ctx,
			sigChan,
			wakeupEvents,
			cfg.VoiceInterruptOnWakeupOrDefault(),
			wakeupListenTimeout,
		)
		if exit {
			dialog.StopRecording()
			log.Println("\n[exit] Stopped.")
			return
		}

		// Stop recording
		dialog.StopRecording()

		if interrupted {
			pendingWakeup = true
			continue
		}

		log.Println("[ready] Waiting for next wakeup event...")
	}
}

func runSingleTurnSTTWakeupMode(cfg agent.Config, dialog audioDialogRunner, runtime *agent.Runtime, sigChan chan os.Signal, newWatcher wakeupWatcherFactory) {
	log.Printf("\n[ready] Starting GPIO wakeup listeners on %s...", wakeupGPIOPinsLabel())

	events := make(chan voiceEvent, 1)

	watchers, err := startWakeupWatchers(newWatcher, func() {
		dialog.InterruptOutput()
		signalVoiceWakeupEvent(events)
	})
	if err != nil {
		log.Printf("[error] Failed to start GPIO wakeup listeners: %v\n", err)
		os.Exit(1)
	}
	defer stopWakeupWatchers(watchers)

	log.Printf("[ready] Waiting for wakeup event (%s)... Ctrl+C to quit", wakeupGPIOPinsLabel())

	var nextTurn *pendingVoiceTurn
	var pendingTurnContext agent.VoiceTurnContext
	pendingWakeup := false
	for {
		if nextTurn == nil {
			if !pendingWakeup {
				select {
				case <-sigChan:
					if dialog != nil {
						dialog.StopRecording()
					}
					log.Println("\n[exit] Stopped.")
					return
				case <-events:
					log.Println("\n[wakeup] GPIO wakeup triggered, starting to listen...")
				}
			} else {
				pendingWakeup = false
			}

			utterance, exit := listenOneUtterance(dialog, sigChan, events, wakeupListenTimeout)
			if exit {
				log.Println("\n[exit] Stopped.")
				return
			}
			if len(utterance) == 0 {
				log.Println("[listen] no utterance captured after wakeup")
				log.Println("[ready] Waiting for next wakeup event...")
				pendingTurnContext = agent.VoiceTurnContext{}
				continue
			}
			input, err := dialog.PrepareTurnInput(utterance)
			if err != nil {
				log.Printf("[error] prepare turn input failed: %v\n", err)
				log.Println("[ready] Waiting for next wakeup event...")
				pendingTurnContext = agent.VoiceTurnContext{}
				continue
			}
			nextTurn = &pendingVoiceTurn{
				input:       input,
				utterance:   append([]int16(nil), utterance...),
				turnContext: pendingTurnContext,
			}
			pendingTurnContext = agent.VoiceTurnContext{}
		}

		turn := nextTurn
		nextTurn = nil
		result := runVoiceTurnWithInputContext(cfg, dialog, runtime, turn.input, turn.utterance, sigChan, events, turn.turnContext, true)
		if result.exit {
			log.Println("\n[exit] Stopped.")
			return
		}
		if result.nextTurn != nil {
			nextTurn = result.nextTurn
			log.Println("[session] turn interrupted, running captured steering input")
			continue
		}
		if result.interrupted {
			pendingWakeup = true
			pendingTurnContext = interruptedFollowUpContext(result)
			log.Println("[session] playback interrupted, starting a new wakeup turn")
			continue
		}
		log.Println("[ready] Waiting for next wakeup event...")
	}
}

func signalVoiceWakeupEvent(events chan<- voiceEvent) {
	select {
	case events <- voiceEventWakeup:
	default:
		log.Println("[wakeup] pending wakeup already queued, coalescing duplicate GPIO wakeup event")
	}
}

func signalWakeupEvent(wakeupEvents chan<- struct{}) {
	select {
	case wakeupEvents <- struct{}{}:
	default:
		log.Println("[wakeup] pending wakeup already queued, coalescing duplicate GPIO wakeup event")
	}
}

func runVoiceSession(cfg agent.Config, dialog audioDialogRunner, runtime *agent.Runtime, sigChan chan os.Signal, events <-chan voiceEvent) bool {
	maxTurns := cfg.VoiceMaxTurns
	turns := 0
	firstTurn := true
	var nextTurn *pendingVoiceTurn
	var pendingTurnContext agent.VoiceTurnContext

	for {
		var input agent.TurnInput
		var utterance []int16
		var turnContext agent.VoiceTurnContext

		if nextTurn != nil {
			input = nextTurn.input
			utterance = nextTurn.utterance
			turnContext = nextTurn.turnContext
			nextTurn = nil
		} else {
			if maxTurns > 0 && turns >= maxTurns {
				log.Printf("[session] max turns reached (%d), closing voice session\n", maxTurns)
				return false
			}

			timeout := cfg.VoiceFollowupTimeoutOrDefault()
			if firstTurn {
				timeout = cfg.VoiceFirstTurnTimeoutOrDefault()
			}

			var exit bool
			utterance, exit = listenOneUtterance(dialog, sigChan, events, timeout)
			if exit {
				return true
			}
			if len(utterance) == 0 {
				log.Println("[session] listen timeout, closing voice session")
				return false
			}

			var err error
			input, err = dialog.PrepareTurnInput(utterance)
			if err != nil {
				log.Printf("[error] prepare turn input failed: %v\n", err)
				firstTurn = false
				continue
			}
			turnContext = pendingTurnContext
			pendingTurnContext = agent.VoiceTurnContext{}
		}

		result := runVoiceTurnWithInputContext(cfg, dialog, runtime, input, utterance, sigChan, events, turnContext, cfg.VoiceInterruptOnWakeupOrDefault())
		if result.exit {
			return true
		}
		firstTurn = false
		if result.waitForWakeupRequested {
			log.Println("[session] agent requested wakeup wait, closing voice session")
			return false
		}
		if result.nextTurn != nil {
			nextTurn = result.nextTurn
			log.Println("[session] turn interrupted, running captured steering input")
			continue
		}
		if result.interrupted {
			pendingTurnContext = interruptedFollowUpContext(result)
			log.Println("[session] turn interrupted, listening for follow-up")
			continue
		}
		turns++
	}
}

func runVoiceTurn(cfg agent.Config, dialog audioDialogRunner, runtime *agent.Runtime, utterance []int16, sigChan chan os.Signal, events <-chan voiceEvent) voiceTurnResult {
	input, err := dialog.PrepareTurnInput(utterance)
	if err != nil {
		log.Printf("[error] prepare turn input failed: %v\n", err)
		return voiceTurnResult{}
	}

	return runVoiceTurnWithInput(cfg, dialog, runtime, input, utterance, sigChan, events)
}

func runVoiceTurnWithInput(cfg agent.Config, dialog audioDialogRunner, runtime *agent.Runtime, input agent.TurnInput, utterance []int16, sigChan chan os.Signal, events <-chan voiceEvent) voiceTurnResult {
	return runVoiceTurnWithInputContext(cfg, dialog, runtime, input, utterance, sigChan, events, agent.VoiceTurnContext{}, cfg.VoiceInterruptOnWakeupOrDefault())
}

func runVoiceTurnWithInputContext(cfg agent.Config, dialog audioDialogRunner, runtime *agent.Runtime, input agent.TurnInput, utterance []int16, sigChan chan os.Signal, events <-chan voiceEvent, turnContext agent.VoiceTurnContext, interruptOnWakeup bool) voiceTurnResult {
	turnCtx, cancel := context.WithCancel(context.Background())
	resultCh := make(chan struct {
		result agent.RunResult
		err    error
	}, 1)
	go func() {
		result, err := dialog.RunVoiceTurnWithContext(turnCtx, input, utterance, runtime, turnContext)
		resultCh <- struct {
			result agent.RunResult
			err    error
		}{result: result, err: err}
	}()

	var result agent.RunResult
	var turnErr error
thinking:
	for {
		select {
		case <-sigChan:
			cancel()
			_ = waitForTurnCancellation(dialog, resultCh)
			return voiceTurnResult{exit: true}
		case <-events:
			if interruptOnWakeup {
				log.Println("[interrupt] GPIO wakeup during active turn, immediately canceling")
				dialog.InterruptOutput()
				cancel()
				canceledInfo := waitForTurnCancellation(dialog, resultCh)
				return voiceTurnResult{
					interrupted:     true,
					followUpContext: buildInterruptedContext(input, canceledInfo),
				}
			}
			log.Println("[interrupt] wakeup received during thinking, ignoring because wakeup interrupt is disabled")
		case turnResult := <-resultCh:
			cancel()
			if turnResult.err != nil {
				log.Printf("[error] %v\n", turnResult.err)
				turnErr = turnResult.err
				result = turnResult.result
				break thinking
			}
			result = turnResult.result
			break thinking
		}
	}

	if turnErr == nil && result.WaitForWakeupRequested {
		return voiceTurnResult{waitForWakeupRequested: true}
	}

	speechText := result.SpokenTextForConfig(cfg)
	if result.SpeechStreamed {
		return voiceTurnResult{}
	}
	prepared := runtime.PrepareSpokenText(context.Background(), agent.SpokenTextInput{
		ResponseText:   speechText,
		TurnFailure:    result.TurnFailure,
		TailAppendable: turnErr == nil && !result.SpeechStreamed && dialog.CanSpeakFinalText(),
	})
	if prepared.Text == "" {
		return voiceTurnResult{}
	}

	if dialog.RecordingActive() {
		if err := dialog.StopRecording(); err != nil {
			log.Printf("[listen] stop recording before TTS playback: %v\n", err)
		}
	}

	speakCtx, cancelSpeak := context.WithCancel(context.Background())
	speakCh := make(chan error, 1)
	go func() {
		speakCh <- dialog.SpeakFinal(speakCtx, prepared.Text, nil)
	}()

speaking:
	for {
		select {
		case <-sigChan:
			cancelSpeak()
			runtime.ReportSpokenTextDelivery(prepared.DeliveryToken, waitForSpeakCancel(speakCh))
			return voiceTurnResult{exit: true}
		case <-events:
			if interruptOnWakeup {
				log.Println("[interrupt] wakeup received during speaking, stopping playback")
				dialog.InterruptOutput()
				cancelSpeak()
				runtime.ReportSpokenTextDelivery(prepared.DeliveryToken, waitForSpeakCancel(speakCh))
				// During speaking phase, the turn has already completed, so we have the full result
				return voiceTurnResult{
					interrupted:     true,
					followUpContext: buildInterruptedContextWithResult(input, result),
				}
			}
		case err := <-speakCh:
			cancelSpeak()
			runtime.ReportSpokenTextDelivery(prepared.DeliveryToken, err)
			if err != nil && !errors.Is(err, context.Canceled) {
				log.Printf("[error] speak failed: %v\n", err)
			}
			break speaking
		}
	}

	return voiceTurnResult{}
}

func interruptedFollowUpContext(result voiceTurnResult) agent.VoiceTurnContext {
	if result.followUpContext != (agent.VoiceTurnContext{}) {
		return result.followUpContext
	}
	return interruptedVoiceCorrectionContext()
}

func captureVoiceSteer(cfg agent.Config, dialog audioDialogRunner, sigChan chan os.Signal, events <-chan voiceEvent) (*pendingVoiceTurn, bool) {
	log.Println("[steer] wakeup received during thinking, listening for steering input")
	dialog.InterruptOutput()
	utterance, exit := listenOneUtterance(dialog, sigChan, events, voiceSteerListenTimeoutForConfig(cfg))
	if exit {
		return nil, true
	}
	if len(utterance) == 0 {
		log.Println("[steer] no steering utterance captured")
		return nil, false
	}
	input, err := dialog.PrepareTurnInput(utterance)
	if err != nil {
		log.Printf("[steer] prepare steer input failed: %v\n", err)
		return nil, false
	}
	return &pendingVoiceTurn{
		input:       input,
		utterance:   append([]int16(nil), utterance...),
		turnContext: interruptedVoiceCorrectionContext(),
	}, false
}

func voiceSteerListenTimeoutForConfig(_ agent.Config) time.Duration {
	if voiceSteerListenTimeout > 0 {
		return voiceSteerListenTimeout
	}
	return defaultVoiceSteerListenTimeout
}

func interruptedVoiceCorrectionContext() agent.VoiceTurnContext {
	return agent.VoiceTurnContext{
		RuntimeContext: voiceWakeupInterruptedCorrectionContext,
	}
}

// buildInterruptedContext constructs context for an interrupted turn during thinking/execution phase.
// It preserves the user's input and any partial execution history from the canceled turn.
func buildInterruptedContext(originalInput agent.TurnInput, canceledInfo canceledTurnInfo) agent.VoiceTurnContext {
	var contextParts []string

	// Base interruption message
	contextParts = append(contextParts, voiceWakeupInterruptedCorrectionContext)

	// Add the user's original input
	userInput := strings.TrimSpace(originalInput.InputText)
	if userInput == "" && originalInput.Transcript != "" {
		userInput = strings.TrimSpace(originalInput.Transcript)
	}
	if userInput != "" {
		contextParts = append(contextParts, fmt.Sprintf("\nThe interrupted turn's user input was: \"%s\"", userInput))
	}

	// Add episode information if available for history lookup
	if canceledInfo.episodeID != "" {
		contextParts = append(contextParts, fmt.Sprintf("\nInterrupted episode ID: %s (you can inspect this episode for partial execution history)", canceledInfo.episodeID))
	}

	// Add error information if the turn failed
	if canceledInfo.err != nil && !errors.Is(canceledInfo.err, context.Canceled) {
		contextParts = append(contextParts, fmt.Sprintf("\nThe interrupted turn encountered an error: %v", canceledInfo.err))
	}

	return agent.VoiceTurnContext{
		RuntimeContext: strings.Join(contextParts, ""),
	}
}

// buildInterruptedContextWithResult constructs context for an interrupted turn during speaking phase.
// At this point the turn has fully completed, so we have the complete result.
func buildInterruptedContextWithResult(originalInput agent.TurnInput, result agent.RunResult) agent.VoiceTurnContext {
	var contextParts []string

	// Base interruption message
	contextParts = append(contextParts, voiceWakeupInterruptedCorrectionContext)

	// Add the user's original input
	userInput := strings.TrimSpace(originalInput.InputText)
	if userInput == "" && originalInput.Transcript != "" {
		userInput = strings.TrimSpace(originalInput.Transcript)
	}
	if userInput != "" {
		contextParts = append(contextParts, fmt.Sprintf("\nThe interrupted turn's user input was: \"%s\"", userInput))
	}

	// Add the agent's output that was interrupted during playback
	agentOutput := strings.TrimSpace(result.Output)
	if agentOutput != "" {
		contextParts = append(contextParts, fmt.Sprintf("\nThe agent's response (interrupted during playback) was: \"%s\"", agentOutput))
	}

	// Add episode information for full history
	if result.EpisodeID != "" {
		contextParts = append(contextParts, fmt.Sprintf("\nCompleted episode ID: %s (contains full execution history)", result.EpisodeID))
	}

	return agent.VoiceTurnContext{
		RuntimeContext: strings.Join(contextParts, ""),
	}
}

type canceledTurnInfo struct {
	result    agent.RunResult
	err       error
	episodeID string
}

func waitForTurnCancellation(dialog audioDialogRunner, resultCh <-chan struct {
	result agent.RunResult
	err    error
}) canceledTurnInfo {
	select {
	case turnResult := <-resultCh:
		return canceledTurnInfo{
			result:    turnResult.result,
			err:       turnResult.err,
			episodeID: turnResult.result.EpisodeID,
		}
	case <-time.After(voiceTurnCancelWaitTimeout):
		log.Println("[interrupt] current turn did not finish after signal cancellation; waiting for voice run to go idle")
		idleCtx, cancel := context.WithTimeout(context.Background(), voiceTurnCancelWaitTimeout)
		defer cancel()
		if dialog.WaitForVoiceRunIdle(idleCtx) {
			return canceledTurnInfo{}
		}
		log.Println("[interrupt] voice run still active after cancellation wait; forcing reset")
		dialog.ForceResetVoiceRun()
		return canceledTurnInfo{}
	}
}

func waitForSpeakCancel(speakCh <-chan error) error {
	select {
	case err := <-speakCh:
		if err != nil && !errors.Is(err, context.Canceled) {
			log.Printf("[error] speak failed after signal cancellation: %v\n", err)
		}
		return err
	case <-time.After(voiceTurnCancelWaitTimeout):
		log.Println("[interrupt] speech did not finish after signal cancellation; exiting")
		return context.Canceled
	}
}

func listenOneUtterance(dialog audioDialogRunner, sigChan chan os.Signal, events <-chan voiceEvent, listenTimeout time.Duration) ([]int16, bool) {
	log.Println("[listen] Recording audio...")
	if err := dialog.StartRecording(); err != nil {
		log.Printf("[error] Failed to start recording: %v\n", err)
		return nil, false
	}
	defer func() {
		if err := dialog.StopRecording(); err != nil {
			log.Printf("[listen] stop recording after listen: %v\n", err)
		}
	}()
	dialog.ResetVAD()
	utterance, exit := captureUtteranceWithTimeout(dialog, sigChan, events, listenTimeout)
	if !exit {
		drainWakeupsWhileListening(events)
	}
	return utterance, exit
}

func drainWakeupsWhileListening(events <-chan voiceEvent) {
	for {
		select {
		case <-events:
			ignoreWakeupWhileListening()
		default:
			return
		}
	}
}

func ignoreWakeupWhileListening() {
	log.Println("[listen] duplicate wakeup received while listening, ignoring")
}

func captureUtteranceWithTimeout(dialog audioDialogRunner, sigChan chan os.Signal, events <-chan voiceEvent, listenTimeout time.Duration) ([]int16, bool) {
	frameSamples := dialog.VADFrameSamples()
	if frameSamples <= 0 {
		log.Printf("[listen] invalid VAD frame size: %d\n", frameSamples)
		return nil, false
	}

	vadPending := make([]int16, 0, frameSamples*10)
	captured := make([]int16, 0, frameSamples*100)
	var hasPendingByte bool
	var pendingByte byte
	startedAt := time.Now()
	nextVADLogAt := startedAt.Add(time.Second)
	hasVADState := false
	speechDetected := false

	for {
		select {
		case <-sigChan:
			return nil, true
		case <-events:
			if speechDetected && len(captured) > 0 {
				log.Println("[listen] manual stop: wakeup pressed during recording, returning captured audio")
				utterance := dialog.FinishManualUtterance(vadPending)
				if len(utterance) > 0 {
					return utterance, false
				}
				return captured, false
			}
			ignoreWakeupWhileListening()
			continue
		default:
		}

		if listenTimeout > 0 && time.Since(startedAt) >= listenTimeout {
			log.Printf("[listen] No complete utterance within %s\n", listenTimeout)
			if len(captured) > 0 && (!hasVADState || speechDetected) {
				log.Printf("[listen] Returning %d buffered samples after VAD timeout\n", len(captured))
				return captured, false
			}
			if len(captured) > 0 {
				log.Printf("[listen] Discarding %d buffered samples without detected speech\n", len(captured))
			}
			return nil, false
		}

		chunk, err := dialog.ReadRecordChunk(200)
		if err != nil {
			log.Printf("[listen] read_record_chunk error: %v\n", err)
			return nil, false
		}

		select {
		case <-sigChan:
			return nil, true
		case <-events:
			if speechDetected && len(captured) > 0 {
				log.Println("[listen] manual stop: wakeup pressed during recording, returning captured audio")
				utterance := dialog.FinishManualUtterance(vadPending)
				if len(utterance) > 0 {
					return utterance, false
				}
				return captured, false
			}
			ignoreWakeupWhileListening()
		default:
		}

		if chunk == nil {
			continue
		}

		if chunk.EndOfStream {
			log.Println("[listen] record session closed by service")
			return nil, false
		}

		if len(chunk.PCM) == 0 {
			continue
		}

		pendingStart := len(vadPending)
		agent.AppendPCM16Samples(chunk.PCM, &vadPending, &hasPendingByte, &pendingByte)
		if len(vadPending) > pendingStart {
			captured = append(captured, vadPending[pendingStart:]...)
		}

		consumed := 0
		for consumed+frameSamples <= len(vadPending) {
			utterance, err := dialog.ProcessVADFrame(vadPending[consumed : consumed+frameSamples])
			consumed += frameSamples
			if err != nil {
				log.Printf("[vad] RKNN processing failed: %v\n", err)
				return nil, false
			}
			if reporter, ok := dialog.(vadDebugReporter); ok {
				state := reporter.VADDebugState()
				hasVADState = true
				if state.Speaking || state.SpeechFrames > 0 {
					speechDetected = true
				}
				if time.Now().After(nextVADLogAt) {
					log.Print(formatVADDebugLog(state))
					nextVADLogAt = time.Now().Add(time.Second)
				}
			}
			if utterance != nil {
				select {
				case <-sigChan:
					return nil, true
				case <-events:
					ignoreWakeupWhileListening()
				default:
				}
				log.Println("[utterance] VAD detected end of speech")
				return utterance, false
			}
		}

		if consumed > 0 {
			vadPending = vadPending[consumed:]
		}
	}
}

func processAudioUntilUtterance(dialog audioDialogRunner, runtime *agent.Runtime, ctx context.Context, sigChan chan os.Signal) bool {
	return processAudioUntilUtteranceWithTimeout(dialog, runtime, ctx, sigChan, wakeupListenTimeout)
}

func processAudioUntilUtteranceWithTimeout(dialog audioDialogRunner, runtime *agent.Runtime, ctx context.Context, sigChan chan os.Signal, listenTimeout time.Duration) bool {
	// Wakeup interruption is disabled when wakeupEvents is nil, so interrupted is always false here.
	exit, _ := processAudioUntilUtteranceWithWakeupInterrupt(dialog, runtime, ctx, sigChan, nil, false, listenTimeout)
	return exit
}

func processAudioUntilUtteranceWithWakeupInterrupt(
	dialog audioDialogRunner,
	runtime *agent.Runtime,
	ctx context.Context,
	sigChan chan os.Signal,
	wakeupEvents <-chan struct{},
	interruptOnWakeup bool,
	listenTimeout time.Duration,
) (bool, bool) {
	frameSamples := dialog.VADFrameSamples()
	if frameSamples <= 0 {
		log.Printf("[listen] invalid VAD frame size: %d\n", frameSamples)
		return false, false
	}

	vadPending := make([]int16, 0, frameSamples*10)
	captured := make([]int16, 0, frameSamples*100)
	var hasPendingByte bool
	var pendingByte byte
	startedAt := time.Now()
	nextVADLogAt := startedAt.Add(time.Second)

	for {
		// Read audio chunk
		select {
		case <-sigChan:
			return true, false
		default:
		}
		drainWakeupEventsWhileListening(wakeupEvents)

		if listenTimeout > 0 && time.Since(startedAt) >= listenTimeout {
			log.Printf("[listen] No complete utterance within %s, stopping recording\n", listenTimeout)
			if len(captured) > 0 {
				log.Printf("[listen] Sending %d buffered samples after VAD timeout\n", len(captured))
				if err := dialog.StopRecording(); err != nil {
					log.Printf("[listen] stop recording before timeout utterance: %v\n", err)
				}
				exit, interrupted := processUtteranceWithWakeupInterrupt(dialog, runtime, ctx, captured, sigChan, wakeupEvents, interruptOnWakeup)
				if exit || interrupted {
					return exit, interrupted
				}
			}
			return false, false
		}

		chunk, err := dialog.ReadRecordChunk(200)
		if err != nil {
			log.Printf("[listen] read_record_chunk error: %v\n", err)
			return false, false
		}

		if chunk == nil {
			continue
		}

		if chunk.EndOfStream {
			log.Println("[listen] record session closed by service")
			return false, false
		}

		if len(chunk.PCM) == 0 {
			continue
		}

		// Convert PCM bytes to int16 samples
		pendingStart := len(vadPending)
		agent.AppendPCM16Samples(chunk.PCM, &vadPending, &hasPendingByte, &pendingByte)
		if len(vadPending) > pendingStart {
			captured = append(captured, vadPending[pendingStart:]...)
		}

		// Feed VAD in 32ms Silero frames.
		consumed := 0
		for consumed+frameSamples <= len(vadPending) {
			utterance, err := dialog.ProcessVADFrame(vadPending[consumed : consumed+frameSamples])
			consumed += frameSamples
			if err != nil {
				log.Printf("[vad] RKNN processing failed: %v\n", err)
				return false, false
			}
			if reporter, ok := dialog.(vadDebugReporter); ok && time.Now().After(nextVADLogAt) {
				state := reporter.VADDebugState()
				log.Print(formatVADDebugLog(state))
				nextVADLogAt = time.Now().Add(time.Second)
			}
			if utterance != nil {
				log.Println("[utterance] VAD detected end of speech")
				if err := dialog.StopRecording(); err != nil {
					log.Printf("[listen] stop recording before utterance processing: %v\n", err)
				}
				return processUtteranceWithWakeupInterrupt(dialog, runtime, ctx, utterance, sigChan, wakeupEvents, interruptOnWakeup)
			}
		}

		if consumed > 0 {
			vadPending = vadPending[consumed:]
		}
	}
}

func drainWakeupEventsWhileListening(wakeupEvents <-chan struct{}) {
	if wakeupEvents == nil {
		return
	}
	drainedCount := 0
	for {
		select {
		case <-wakeupEvents:
			drainedCount++
		default:
			if drainedCount > 0 {
				log.Printf("[listen] %d duplicate wakeup(s) received while listening, ignoring", drainedCount)
			}
			return
		}
	}
}

func processUtteranceWithWakeupInterrupt(
	dialog audioDialogRunner,
	runtime *agent.Runtime,
	ctx context.Context,
	utterance []int16,
	sigChan chan os.Signal,
	wakeupEvents <-chan struct{},
	interruptOnWakeup bool,
) (bool, bool) {
	if wakeupEvents == nil {
		if err := dialog.ProcessUtterance(ctx, utterance, runtime); err != nil {
			log.Printf("[error] %v\n", err)
		}
		return false, false
	}

	turnCtx, cancel := context.WithCancel(ctx)
	resultCh := make(chan error, 1)
	go func() {
		resultCh <- dialog.ProcessUtterance(turnCtx, utterance, runtime)
	}()

	for {
		select {
		case <-sigChan:
			cancel()
			waitForLegacyUtteranceCancel(resultCh)
			return true, false
		case <-wakeupEvents:
			if interruptOnWakeup {
				log.Println("[interrupt] wakeup received during legacy turn, canceling current turn")
				cancel()
				waitForLegacyUtteranceCancel(resultCh)
				return false, true
			}
			log.Println("[interrupt] wakeup received during legacy turn, ignoring because interrupt is disabled")
		case err := <-resultCh:
			cancel()
			if err != nil && !errors.Is(err, context.Canceled) {
				log.Printf("[error] %v\n", err)
			}
			return false, false
		}
	}
}

func waitForLegacyUtteranceCancel(resultCh <-chan error) {
	select {
	case err := <-resultCh:
		if err != nil && !errors.Is(err, context.Canceled) {
			log.Printf("[error] %v\n", err)
		}
	case <-time.After(voiceTurnCancelWaitTimeout):
		log.Println("[interrupt] legacy turn did not finish after cancellation; continuing")
	}
}

func processAudioLoop(dialog audioDialogRunner, runtime *agent.Runtime, ctx context.Context, manualStop chan<- manualStopResult) {
	frameSamples := dialog.VADFrameSamples()
	if frameSamples <= 0 {
		log.Printf("[listen] invalid VAD frame size: %d\n", frameSamples)
		if manualStop != nil {
			manualStop <- manualStopResult{}
		}
		return
	}

	vadPending := make([]int16, 0, frameSamples*10)
	var hasPendingByte bool
	var pendingByte byte
	vadHandled := false

	for {
		select {
		case <-ctx.Done():
			goto stopped
		default:
		}

		// Read audio chunk
		chunk, err := dialog.ReadRecordChunk(200)
		if err != nil {
			log.Printf("[listen] read_record_chunk error: %v\n", err)
			break
		}

		if chunk == nil {
			continue
		}

		if chunk.EndOfStream {
			log.Println("[listen] record session closed by service")
			break
		}

		if len(chunk.PCM) == 0 {
			continue
		}

		// Convert PCM bytes to int16 samples
		agent.AppendPCM16Samples(chunk.PCM, &vadPending, &hasPendingByte, &pendingByte)

		// Feed VAD in 32ms Silero frames.
		consumed := 0
		for consumed+frameSamples <= len(vadPending) {
			utterance, err := dialog.ProcessVADFrame(vadPending[consumed : consumed+frameSamples])
			consumed += frameSamples
			if err != nil {
				log.Printf("[vad] processing failed: %v\n", err)
				if stopErr := dialog.StopRecording(); stopErr != nil {
					log.Printf("[listen] stop recording after VAD failure: %v\n", stopErr)
				}
				goto stopped
			}
			if utterance != nil {
				log.Println("[utterance] VAD detected end of speech")
				vadHandled = true
				if err := dialog.StopRecording(); err != nil {
					log.Printf("[listen] stop recording before utterance processing: %v\n", err)
				}
				if err := dialog.ProcessUtterance(ctx, utterance, runtime); err != nil {
					log.Printf("[error] %v\n", err)
				}
				log.Println("\n[ready] Waiting for next trigger...")
				goto stopped
			}
		}

		if consumed > 0 {
			vadPending = vadPending[consumed:]
		}
	}

stopped:
	if manualStop == nil {
		return
	}
	if vadHandled {
		manualStop <- manualStopResult{vadHandled: true}
		return
	}
	manualStop <- manualStopResult{utterance: dialog.FinishManualUtterance(vadPending)}
}
