package main

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"aiden-agent/internal/agent"
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
	ReadRecordChunk(timeoutMs uint32) (*agent.AudioChunkResult, error)
	ProcessVADFrame(samples []int16) []int16
	ProcessUtterance(ctx context.Context, utterance []int16, runtime *agent.Runtime) error
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

			// Flush VAD and process
			utterance := dialog.FlushVAD()
			if utterance != nil && len(utterance) > 0 {
				log.Println("[manual] Sending buffered audio without waiting for VAD")
				if err := dialog.ProcessUtterance(ctx, utterance, runtime); err != nil {
					log.Printf("[error] %v\n", err)
				}
			} else {
				log.Println("[manual] No buffered audio to send")
			}

			dialog.StopRecording()
			log.Println("\n[ready] Waiting for next trigger...")
		}
	}

	if err := scanner.Err(); err != nil {
		log.Printf("[error] %v\n", err)
	}

	log.Println("\n[exit] Stopped.")
}

func runWakeupMode(cfg agent.Config, dialog audioDialogRunner, runtime *agent.Runtime, sigChan chan os.Signal, newWatcher wakeupWatcherFactory) {
	log.Println("\n[ready] Starting GPIO wakeup listener on GPIO 33...")

	ctx := context.Background()
	wakeupTriggered := false
	var wakeupMutex sync.Mutex

	// Create GPIO watcher
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

		// Check if wakeup was triggered
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

func processAudioUntilUtterance(dialog audioDialogRunner, runtime *agent.Runtime, ctx context.Context, sigChan chan os.Signal) bool {
	frameSamples := dialog.VADFrameSamples()
	if frameSamples <= 0 {
		log.Printf("[listen] invalid VAD frame size: %d\n", frameSamples)
		return false
	}

	vadPending := make([]int16, 0, frameSamples*10)
	var hasPendingByte bool
	var pendingByte byte

	for {
		// Read audio chunk
		select {
		case <-sigChan:
			return true
		default:
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
		agent.AppendPCM16Samples(chunk.PCM, &vadPending, &hasPendingByte, &pendingByte)

		// Feed VAD in 30ms frames.
		consumed := 0
		for consumed+frameSamples <= len(vadPending) {
			utterance := dialog.ProcessVADFrame(vadPending[consumed : consumed+frameSamples])
			consumed += frameSamples
			if utterance != nil {
				log.Println("[utterance] VAD detected end of speech")
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
				if err := dialog.ProcessUtterance(ctx, utterance, runtime); err != nil {
					log.Printf("[error] %v\n", err)
				}
				*recording = false
				log.Println("\n[ready] Waiting for next trigger...")
				break
			}
		}

		if consumed > 0 {
			vadPending = vadPending[consumed:]
		}
	}
}
