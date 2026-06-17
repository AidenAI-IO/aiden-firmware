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
	"sync"
	"syscall"
	"time"

	"aiden-agent/internal/agent"
	"aiden-agent/internal/ota"
)

const (
	wakeupListenTimeout        = 10 * time.Second
	voiceTurnCancelWaitTimeout = 2 * time.Second
)

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
			fmt.Fprintln(os.Stderr, "config-test subcommand not yet implemented")
			os.Exit(1)
		}
	}

	// Default: run as daemon
	var (
		configDir    = flag.String("config", "", "path to config directory (required)")
		addr         = flag.String("addr", "0.0.0.0:8080", "HTTP server address")
		benchmarkDir = flag.String("benchmark-dir", "", "Override benchmark root directory (default: auto-detect)")
	)
	flag.Parse()

	if *configDir == "" {
		fmt.Fprintln(os.Stderr, "missing -config flag: must specify config directory")
		os.Exit(1)
	}

	cfg, err := agent.LoadRuntimeConfigFromDir(*configDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "load config: %v\n", err)
		os.Exit(1)
	}

	proxyConfig := agent.ProxyConfigFromEnvironment()
	if err := proxyConfig.Validate(); err != nil {
		fmt.Fprintf(os.Stderr, "proxy environment: %v\n", err)
		os.Exit(1)
	}
	cfg.SkillMergeModel = agent.NewLLMSkillMergeModel(agent.NewModelManager(cfg.Model, proxyConfig))

	runtime, err := agent.NewRuntime(cfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "create runtime: %v\n", err)
		os.Exit(1)
	}
	defer runtime.Close()
	if wrote, err := ota.WriteHealthMarkerIfPending("/userdata/ota/pending_boot.json", "/userdata/ota/health.ok"); err != nil {
		log.Printf("[ota] health marker not written: %v", err)
	} else if wrote {
		log.Printf("[ota] health marker written")
	}

	inputMode := cfg.InputModeOrDefault()

	// Resolve benchmark directory (allow override via flag, default to auto-detect)
	resolvedBenchmarkDir, err := agent.ResolveBenchmarkDir(*benchmarkDir, cfg.Benchmark)
	if err != nil {
		log.Printf("[benchmark] Failed to resolve benchmark directory: %v. Benchmark routes will return 503.", err)
		resolvedBenchmarkDir = "" // Pass empty string to NewServer
	}

	// HTTP server runs in all input modes so the web UI is available even
	// during voice (audio/stt) interactions.
	server := agent.NewServer(runtime, *addr, resolvedBenchmarkDir)

	fmt.Printf("🚀 Aiden Agent daemon starting on %s\n", *addr)
	fmt.Printf("📂 Config directory: %s\n", *configDir)
	if resolvedBenchmarkDir != "" {
		fmt.Printf("📊 Benchmark directory: %s\n", resolvedBenchmarkDir)
	}
	if _, port, err := net.SplitHostPort(*addr); err == nil && port != "" {
		fmt.Printf("🌐 Web UI: http://localhost:%s\n", port)
	}
	fmt.Printf("📝 Logs: %s/log/\n", *configDir)

	serverErr := make(chan error, 1)
	go func() {
		serverErr <- server.Start()
	}()

	if inputMode == "audio" || inputMode == "stt" {
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
			fmt.Fprintf(os.Stderr, "server error: %v\n", err)
			os.Exit(1)
		}
		return
	}

	if err := <-serverErr; err != nil {
		fmt.Fprintf(os.Stderr, "server error: %v\n", err)
		os.Exit(1)
	}
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
	if inputMode != "audio" && inputMode != "stt" {
		return false
	}
	return cfg.TriggerModeOrDefault() != "manual" || stdinInteractive
}

func runAudioMode(cfg agent.Config, runtime *agent.Runtime, server *agent.Server) {
	dialog, err := agent.NewAudioDialog(cfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "create audio dialog: %v\n", err)
		os.Exit(1)
	}
	dialog.SetHistoryAppender(server.AppendHistory)

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
	Speak(ctx context.Context, text string, interrupt <-chan struct{}) error
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

type wakeupWatcherFactory func(pin int, callback func()) (wakeupWatcher, error)

func newGPIOWatcher(pin int, callback func()) (wakeupWatcher, error) {
	return agent.NewGPIOWatcher(pin, callback)
}

func wakeupGPIOPinsLabel() string {
	return "GPIO 33/GPIO 32"
}

func startWakeupWatchers(newWatcher wakeupWatcherFactory, callback func()) ([]wakeupWatcher, error) {
	watchers := make([]wakeupWatcher, 0, len(wakeupGPIOPins))
	for _, pin := range wakeupGPIOPins {
		pin := pin
		watcher, err := newWatcher(pin, func() {
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

type voiceEvent int

const (
	voiceEventWakeup voiceEvent = iota + 1
)

type voiceTurnResult struct {
	interrupted    bool
	sleepRequested bool
	exit           bool
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
			var loopCtx context.Context
			loopCtx, cancelRecording = context.WithCancel(ctx)
			go processAudioLoop(dialog, runtime, loopCtx, manualStop)
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
		}
		dialog.StopRecording()
	}

	log.Println("\n[exit] Stopped.")
}

func runWakeupMode(cfg agent.Config, dialog audioDialogRunner, runtime *agent.Runtime, sigChan chan os.Signal, newWatcher wakeupWatcherFactory) {
	if cfg.InputModeOrDefault() != "stt" || !cfg.VoiceFollowupEnabledOrDefault() {
		runLegacyWakeupMode(cfg, dialog, runtime, sigChan, newWatcher)
		return
	}

	log.Printf("\n[ready] Starting GPIO wakeup listeners on %s...", wakeupGPIOPinsLabel())

	events := make(chan voiceEvent, 8)

	watchers, err := startWakeupWatchers(newWatcher, func() {
		select {
		case events <- voiceEventWakeup:
		default:
			log.Println("[wakeup] event queue full, dropping GPIO wakeup event")
		}
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
	wakeupTriggered := false
	var wakeupMutex sync.Mutex

	watchers, err := startWakeupWatchers(newWatcher, func() {
		wakeupMutex.Lock()
		defer wakeupMutex.Unlock()
		if !wakeupTriggered {
			log.Println("\n[wakeup] GPIO wakeup triggered, starting to listen...")
			wakeupTriggered = true
		}
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
			if dialog != nil {
				dialog.StopRecording()
			}
			log.Println("\n[exit] Stopped.")
			return
		default:
		}

		wakeupMutex.Lock()
		triggered := wakeupTriggered
		wakeupMutex.Unlock()

		if !triggered {
			time.Sleep(100 * time.Millisecond)
			continue
		}

		// Start recording
		log.Println("[listen] Recording audio...")
		if err := dialog.StartRecording(); err != nil {
			log.Printf("[error] Failed to start recording: %v\n", err)
			wakeupMutex.Lock()
			wakeupTriggered = false
			wakeupMutex.Unlock()
			continue
		}

		dialog.ResetVAD()
		if exit := processAudioUntilUtterance(dialog, runtime, ctx, sigChan); exit {
			dialog.StopRecording()
			log.Println("\n[exit] Stopped.")
			return
		}

		// Stop recording
		dialog.StopRecording()

		// Reset wakeup flag
		wakeupMutex.Lock()
		wakeupTriggered = false
		wakeupMutex.Unlock()

		log.Println("[ready] Waiting for next wakeup event...")
	}
}

func runVoiceSession(cfg agent.Config, dialog audioDialogRunner, runtime *agent.Runtime, sigChan chan os.Signal, events <-chan voiceEvent) bool {
	maxTurns := cfg.VoiceMaxTurns
	turns := 0
	firstTurn := true

	for {
		if maxTurns > 0 && turns >= maxTurns {
			log.Printf("[session] max turns reached (%d), closing voice session\n", maxTurns)
			return false
		}

		timeout := cfg.VoiceFollowupTimeoutOrDefault()
		if firstTurn {
			timeout = cfg.VoiceFirstTurnTimeoutOrDefault()
		}

		utterance, exit := listenOneUtterance(dialog, sigChan, events, timeout)
		if exit {
			return true
		}
		if len(utterance) == 0 {
			log.Println("[session] listen timeout, closing voice session")
			return false
		}

		input, err := dialog.PrepareTurnInput(utterance)
		if err != nil {
			log.Printf("[error] prepare turn input failed: %v\n", err)
			firstTurn = false
			continue
		}

		turns++
		result := runVoiceTurnWithInput(cfg, dialog, runtime, input, utterance, sigChan, events)
		if result.exit {
			return true
		}
		firstTurn = false
		if result.sleepRequested {
			log.Println("[session] agent requested sleep, closing voice session")
			return false
		}
		if result.interrupted {
			log.Println("[session] turn interrupted, listening for follow-up")
		}
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
	turnCtx, cancel := context.WithCancel(context.Background())
	resultCh := make(chan struct {
		result agent.RunResult
		err    error
	}, 1)
	go func() {
		result, err := dialog.RunVoiceTurn(turnCtx, input, utterance, runtime)
		resultCh <- struct {
			result agent.RunResult
			err    error
		}{result: result, err: err}
	}()

	var result agent.RunResult
thinking:
	for {
		select {
		case <-sigChan:
			cancel()
			waitForTurnCancel(resultCh)
			return voiceTurnResult{exit: true}
		case <-events:
			if cfg.VoiceInterruptOnWakeupOrDefault() {
				log.Println("[interrupt] wakeup received during thinking, canceling current turn")
				cancel()
				select {
				case turnResult := <-resultCh:
					if turnResult.err != nil {
						if !errors.Is(turnResult.err, context.Canceled) {
							log.Printf("[error] %v\n", turnResult.err)
						}
					}
				case <-time.After(2 * time.Second):
					log.Println("[interrupt] current turn did not finish after cancellation; continuing session")
				}
				return voiceTurnResult{interrupted: true}
			}
		case turnResult := <-resultCh:
			cancel()
			if turnResult.err != nil {
				log.Printf("[error] %v\n", turnResult.err)
				return voiceTurnResult{}
			}
			result = turnResult.result
			break thinking
		}
	}

	if result.SleepRequested {
		return voiceTurnResult{sleepRequested: true}
	}

	speechText := result.SpokenTextForConfig(cfg)
	if speechText == "" || result.SpeechStreamed {
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
		speakCh <- dialog.Speak(speakCtx, speechText, nil)
	}()

speaking:
	for {
		select {
		case <-sigChan:
			cancelSpeak()
			waitForSpeakCancel(speakCh)
			return voiceTurnResult{exit: true}
		case <-events:
			if cfg.VoiceInterruptOnWakeupOrDefault() {
				log.Println("[interrupt] wakeup received during speaking, stopping playback")
				cancelSpeak()
				waitForSpeakCancel(speakCh)
				return voiceTurnResult{interrupted: true}
			}
		case err := <-speakCh:
			cancelSpeak()
			if err != nil && !errors.Is(err, context.Canceled) {
				log.Printf("[error] speak failed: %v\n", err)
			}
			break speaking
		}
	}

	return voiceTurnResult{}
}

func waitForTurnCancel(resultCh <-chan struct {
	result agent.RunResult
	err    error
}) {
	select {
	case <-resultCh:
	case <-time.After(voiceTurnCancelWaitTimeout):
		log.Println("[interrupt] current turn did not finish after signal cancellation; exiting")
	}
}

func waitForSpeakCancel(speakCh <-chan error) {
	select {
	case err := <-speakCh:
		if err != nil && !errors.Is(err, context.Canceled) {
			log.Printf("[error] speak failed after signal cancellation: %v\n", err)
		}
	case <-time.After(voiceTurnCancelWaitTimeout):
		log.Println("[interrupt] speech did not finish after signal cancellation; exiting")
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
					log.Printf("[vad] probability=%.3f threshold=%.3f speaking=%v speech_frames=%d silence_frames=%d last_error=%q\n",
						state.Probability,
						state.Threshold,
						state.Speaking,
						state.SpeechFrames,
						state.SilenceFrames,
						state.LastError,
					)
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
	frameSamples := dialog.VADFrameSamples()
	if frameSamples <= 0 {
		log.Printf("[listen] invalid VAD frame size: %d\n", frameSamples)
		return false
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
			return true
		default:
		}

		if listenTimeout > 0 && time.Since(startedAt) >= listenTimeout {
			log.Printf("[listen] No complete utterance within %s, stopping recording\n", listenTimeout)
			if len(captured) > 0 {
				log.Printf("[listen] Sending %d buffered samples after VAD timeout\n", len(captured))
				if err := dialog.StopRecording(); err != nil {
					log.Printf("[listen] stop recording before timeout utterance: %v\n", err)
				}
				if err := dialog.ProcessUtterance(ctx, captured, runtime); err != nil {
					log.Printf("[error] %v\n", err)
				}
			}
			return false
		}

		chunk, err := dialog.ReadRecordChunk(200)
		if err != nil {
			log.Printf("[listen] read_record_chunk error: %v\n", err)
			return false
		}

		if chunk == nil {
			continue
		}

		if chunk.EndOfStream {
			log.Println("[listen] record session closed by service")
			return false
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
				return false
			}
			if reporter, ok := dialog.(vadDebugReporter); ok && time.Now().After(nextVADLogAt) {
				state := reporter.VADDebugState()
				log.Printf("[vad] probability=%.3f threshold=%.3f speaking=%v speech_frames=%d silence_frames=%d last_error=%q\n",
					state.Probability,
					state.Threshold,
					state.Speaking,
					state.SpeechFrames,
					state.SilenceFrames,
					state.LastError,
				)
				nextVADLogAt = time.Now().Add(time.Second)
			}
			if utterance != nil {
				log.Println("[utterance] VAD detected end of speech")
				if err := dialog.StopRecording(); err != nil {
					log.Printf("[listen] stop recording before utterance processing: %v\n", err)
				}
				if err := dialog.ProcessUtterance(ctx, utterance, runtime); err != nil {
					log.Printf("[error] %v\n", err)
				}
				return false
			}
		}

		if consumed > 0 {
			vadPending = vadPending[consumed:]
		}
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
