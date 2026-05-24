package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
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

func main() {
	var (
		configDir = flag.String("config", "", "path to config directory (required)")
		addr      = flag.String("addr", ":8080", "HTTP server address")
	)
	flag.Parse()

	if *configDir == "" {
		fmt.Fprintln(os.Stderr, "missing -config flag: must specify config directory")
		os.Exit(1)
	}

	cfg, err := agent.LoadConfigFromDir(*configDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "load config: %v\n", err)
		os.Exit(1)
	}

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

	// If input mode is audio or stt, run audio dialog loop
	if inputMode == "audio" || inputMode == "stt" {
		runAudioMode(cfg, runtime)
		return
	}

	// Otherwise run HTTP server
	server := agent.NewServer(runtime, *addr)

	fmt.Printf("🚀 Aiden Agent daemon starting on %s\n", *addr)
	fmt.Printf("📂 Config directory: %s\n", *configDir)
	fmt.Printf("🌐 Web UI: http://localhost%s\n", *addr)
	fmt.Printf("📝 Logs: %s/log/\n", *configDir)

	if err := server.Start(); err != nil {
		fmt.Fprintf(os.Stderr, "server error: %v\n", err)
		os.Exit(1)
	}
}

func runAudioMode(cfg agent.Config, runtime *agent.Runtime) {
	dialog, err := agent.NewAudioDialog(cfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "create audio dialog: %v\n", err)
		os.Exit(1)
	}

	inputMode := cfg.InputModeOrDefault()
	triggerMode := cfg.TriggerModeOrDefault()

	log.Printf("[init] Config loaded: model=%s, input_mode=%s, trigger_mode=%s\n",
		cfg.Model.Model, inputMode, triggerMode)
	log.Printf("[init] VAD: threshold=%d, silence=%dms, min_speech=%dms\n",
		cfg.EnergyThreshold, cfg.SilenceMs, cfg.MinSpeechMs)

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
	ProcessVADFrame(samples []int16) []int16
	ProcessUtterance(ctx context.Context, utterance []int16, runtime *agent.Runtime) error
	PrepareTurnInput(utterance []int16) (agent.TurnInput, error)
	RunAgentTurn(ctx context.Context, input agent.TurnInput, runtime *agent.Runtime) (agent.RunResult, error)
	Speak(ctx context.Context, text string, interrupt <-chan struct{}) error
	FlushVAD() []int16
	ResetVAD()
	VADFrameSamples() int
}

type wakeupWatcher interface {
	Start() error
	Stop()
}

type wakeupWatcherFactory func(pin int, callback func()) (wakeupWatcher, error)

func newGPIOWatcher(pin int, callback func()) (wakeupWatcher, error) {
	return agent.NewGPIOWatcher(pin, callback)
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
	ctx := context.Background()

	for {
		select {
		case <-sigChan:
			if recording {
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

			// Start VAD processing in background
			go processAudioLoop(dialog, runtime, &recording, ctx)
		} else {
			if !scanner.Scan() {
				break
			}

			log.Println("[manual] Stop triggered by user")
			recording = false

			// Flush VAD, close capture, then process so TTS playback is never
			// fed back into the active recording session.
			utterance := dialog.FlushVAD()
			if err := dialog.StopRecording(); err != nil {
				log.Printf("[listen] stop recording before manual utterance processing: %v\n", err)
			}
			if utterance != nil && len(utterance) > 0 {
				log.Println("[manual] Sending buffered audio without waiting for VAD")
				if err := dialog.ProcessUtterance(ctx, utterance, runtime); err != nil {
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

	log.Println("\n[exit] Stopped.")
}

func runWakeupMode(cfg agent.Config, dialog audioDialogRunner, runtime *agent.Runtime, sigChan chan os.Signal, newWatcher wakeupWatcherFactory) {
	if cfg.InputModeOrDefault() != "stt" || !cfg.VoiceSessionEnabledOrDefault() {
		runLegacyWakeupMode(cfg, dialog, runtime, sigChan, newWatcher)
		return
	}

	log.Println("\n[ready] Starting GPIO wakeup listener on GPIO 33...")

	events := make(chan voiceEvent, 8)

	// Create GPIO watcher
	watcher, err := newWatcher(33, func() {
		select {
		case events <- voiceEventWakeup:
		default:
			log.Println("[wakeup] event queue full, dropping GPIO wakeup event")
		}
	})
	if err != nil {
		log.Printf("[error] Failed to create GPIO watcher: %v\n", err)
		os.Exit(1)
	}

	if err := watcher.Start(); err != nil {
		log.Printf("[error] Failed to start GPIO watcher: %v\n", err)
		os.Exit(1)
	}
	defer watcher.Stop()

	log.Println("[ready] Waiting for wakeup event (GPIO 33)... Ctrl+C to quit")

	for {
		select {
		case <-sigChan:
			log.Println("\n[exit] Stopped.")
			return
		case <-events:
			log.Println("\n[wakeup] GPIO 33 triggered, opening voice session...")
			if exit := runVoiceSession(cfg, dialog, runtime, sigChan, events); exit {
				log.Println("\n[exit] Stopped.")
				return
			}
			log.Println("[ready] Waiting for wakeup event...")
		}
	}
}

func runLegacyWakeupMode(cfg agent.Config, dialog audioDialogRunner, runtime *agent.Runtime, sigChan chan os.Signal, newWatcher wakeupWatcherFactory) {
	log.Println("\n[ready] Starting GPIO wakeup listener on GPIO 33...")

	ctx := context.Background()
	wakeupTriggered := false
	var wakeupMutex sync.Mutex

	watcher, err := newWatcher(33, func() {
		wakeupMutex.Lock()
		defer wakeupMutex.Unlock()
		if !wakeupTriggered {
			log.Println("\n[wakeup] GPIO 33 triggered, starting to listen...")
			wakeupTriggered = true
		}
	})
	if err != nil {
		log.Printf("[error] Failed to create GPIO watcher: %v\n", err)
		os.Exit(1)
	}

	if err := watcher.Start(); err != nil {
		log.Printf("[error] Failed to start GPIO watcher: %v\n", err)
		os.Exit(1)
	}
	defer watcher.Stop()

	log.Println("[ready] Waiting for wakeup event (GPIO 33)... Ctrl+C to quit")

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
		result := runVoiceTurnWithInput(cfg, dialog, runtime, input, sigChan, events)
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

	return runVoiceTurnWithInput(cfg, dialog, runtime, input, sigChan, events)
}

func runVoiceTurnWithInput(cfg agent.Config, dialog audioDialogRunner, runtime *agent.Runtime, input agent.TurnInput, sigChan chan os.Signal, events <-chan voiceEvent) voiceTurnResult {
	turnCtx, cancel := context.WithCancel(context.Background())
	resultCh := make(chan struct {
		result agent.RunResult
		err    error
	}, 1)
	go func() {
		result, err := dialog.RunAgentTurn(turnCtx, input, runtime)
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
				case <-resultCh:
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

	if result.Output == "" || result.SpeechStreamed {
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
		speakCh <- dialog.Speak(speakCtx, result.Output, nil)
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
	return utterance, exit
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
			log.Println("[listen] wakeup received while listening, extending listen window")
			startedAt = time.Now()
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
			utterance := dialog.ProcessVADFrame(vadPending[consumed : consumed+frameSamples])
			consumed += frameSamples
			if reporter, ok := dialog.(vadDebugReporter); ok {
				state := reporter.VADDebugState()
				hasVADState = true
				if state.Speaking || state.SpeechFrames > 0 {
					speechDetected = true
				}
				if time.Now().After(nextVADLogAt) {
					log.Printf("[vad] energy=%d config_threshold=%d adaptive_threshold=%d noise_floor=%d speaking=%v speech_frames=%d silence_frames=%d\n",
						state.Energy,
						state.ConfigThreshold,
						state.AdaptiveThreshold,
						state.NoiseFloor,
						state.Speaking,
						state.SpeechFrames,
						state.SilenceFrames,
					)
					nextVADLogAt = time.Now().Add(time.Second)
				}
			}
			if utterance != nil {
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

		// Feed VAD in 30ms frames.
		consumed := 0
		for consumed+frameSamples <= len(vadPending) {
			utterance := dialog.ProcessVADFrame(vadPending[consumed : consumed+frameSamples])
			consumed += frameSamples
			if reporter, ok := dialog.(vadDebugReporter); ok && time.Now().After(nextVADLogAt) {
				state := reporter.VADDebugState()
				log.Printf("[vad] energy=%d config_threshold=%d adaptive_threshold=%d noise_floor=%d speaking=%v speech_frames=%d silence_frames=%d\n",
					state.Energy,
					state.ConfigThreshold,
					state.AdaptiveThreshold,
					state.NoiseFloor,
					state.Speaking,
					state.SpeechFrames,
					state.SilenceFrames,
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

func processAudioLoop(dialog audioDialogRunner, runtime *agent.Runtime, recording *bool, ctx context.Context) {
	frameSamples := dialog.VADFrameSamples()
	if frameSamples <= 0 {
		log.Printf("[listen] invalid VAD frame size: %d\n", frameSamples)
		*recording = false
		return
	}

	vadPending := make([]int16, 0, frameSamples*10)
	var hasPendingByte bool
	var pendingByte byte

	for *recording {
		// Read audio chunk
		chunk, err := dialog.ReadRecordChunk(200)
		if err != nil {
			log.Printf("[listen] read_record_chunk error: %v\n", err)
			*recording = false
			break
		}

		if chunk == nil {
			continue
		}

		if chunk.EndOfStream {
			log.Println("[listen] record session closed by service")
			*recording = false
			break
		}

		if len(chunk.PCM) == 0 {
			continue
		}

		// Convert PCM bytes to int16 samples
		agent.AppendPCM16Samples(chunk.PCM, &vadPending, &hasPendingByte, &pendingByte)

		// Feed VAD in 30ms frames.
		consumed := 0
		for consumed+frameSamples <= len(vadPending) {
			utterance := dialog.ProcessVADFrame(vadPending[consumed : consumed+frameSamples])
			consumed += frameSamples
			if utterance != nil {
				log.Println("[utterance] VAD detected end of speech")
				*recording = false
				if err := dialog.StopRecording(); err != nil {
					log.Printf("[listen] stop recording before utterance processing: %v\n", err)
				}
				if err := dialog.ProcessUtterance(ctx, utterance, runtime); err != nil {
					log.Printf("[error] %v\n", err)
				}
				log.Println("\n[ready] Waiting for next trigger...")
				break
			}
		}

		if consumed > 0 {
			vadPending = vadPending[consumed:]
		}
	}
}
