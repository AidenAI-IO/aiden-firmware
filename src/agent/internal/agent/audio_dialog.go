package agent

import (
	"context"
	"encoding/binary"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"
)

type TurnInput = AudioInputResult

const toolDescriptionSpeechTimeout = 5 * time.Second

var (
	recordingStartRetryTimeout  = 5 * time.Second
	recordingStartRetryInterval = 250 * time.Millisecond
)

// AudioDialog manages the audio conversation loop
type AudioDialog struct {
	config       Config
	audioClient  *AudioServiceClient
	sttClient    STTClient
	ttsClient    TTSClient
	vad          *AudioVAD
	recordActive bool
	sessionID    uint64
	speechMu     sync.Mutex
}

// NewAudioDialog creates a new audio dialog manager
func NewAudioDialog(cfg Config) (*AudioDialog, error) {
	// Create audio client
	audioClient := NewAudioServiceClient(cfg.Audio.SocketOrDefault())

	// Create STT client if needed
	var sttClient STTClient
	if cfg.InputModeOrDefault() == "stt" {
		var err error
		sttClient, err = NewSTTClientFromConfig(cfg)
		if err != nil {
			return nil, err
		}
	}

	// Create TTS client
	ttsClient, err := NewTTSClientFromConfig(cfg)
	if err != nil {
		return nil, err
	}

	silenceMs := cfg.SilenceMs
	if silenceMs == 0 {
		silenceMs = 650
	}
	minSpeechMs := cfg.MinSpeechMs
	if minSpeechMs == 0 {
		minSpeechMs = 300
	}

	alwaysBuffer := cfg.TriggerModeOrDefault() == "manual"
	vad, err := NewAudioVAD(AudioVADConfig{
		SampleRate:      cfg.Audio.SampleRateOrDefault(),
		SilenceMs:       silenceMs,
		MinSpeechMs:     minSpeechMs,
		AlwaysBuffer:    alwaysBuffer,
		Backend:         cfg.VADBackend,
		ModelPath:       cfg.VADModelPath,
		HelperPath:      cfg.VADHelperPath,
		SpeechThreshold: cfg.VADSpeechThreshold,
	})
	if err != nil {
		return nil, err
	}

	return &AudioDialog{
		config:      cfg,
		audioClient: audioClient,
		sttClient:   sttClient,
		ttsClient:   ttsClient,
		vad:         vad,
	}, nil
}

// StartRecording starts an audio recording session
func (d *AudioDialog) StartRecording() error {
	if d.recordActive {
		return nil
	}

	d.playPromptSound(promptSoundRecordingStart, "recording", true)

	log.Println("[audio] Opening record session...")
	format := AudioFormat{
		SampleRate: uint32(d.config.Audio.SampleRateOrDefault()),
		Channels:   uint32(d.config.Audio.ChannelsOrDefault()),
		BitWidth:   uint32(d.config.Audio.BitWidthOrDefault()),
	}

	result, err := startRecordingWithRetry(d.audioClient, format, recordingStartRetryTimeout, recordingStartRetryInterval)
	if err != nil {
		return fmt.Errorf("start recording: %w", err)
	}
	d.sessionID = result.SessionID
	d.recordActive = true
	return nil
}

func startRecordingWithRetry(audio *AudioServiceClient, format AudioFormat, retryTimeout, retryInterval time.Duration) (*RecordStartResult, error) {
	if retryTimeout <= 0 {
		return audio.StartRecording(format)
	}
	if retryInterval <= 0 {
		retryInterval = 100 * time.Millisecond
	}

	deadline := time.Now().Add(retryTimeout)
	attempts := 0
	var lastErr error
	for {
		result, err := audio.StartRecording(format)
		if err == nil {
			if attempts > 0 {
				log.Printf("[audio] Record session opened after %d retries\n", attempts)
			}
			return result, nil
		}
		lastErr = err
		attempts++
		if attempts == 1 {
			log.Printf("[audio] Record session unavailable, retrying for up to %s: %v\n", retryTimeout, err)
		}

		remaining := time.Until(deadline)
		if remaining <= 0 {
			break
		}
		sleep := retryInterval
		if sleep > remaining {
			sleep = remaining
		}
		time.Sleep(sleep)
	}
	return nil, fmt.Errorf("after %s: %w", retryTimeout, lastErr)
}

// StopRecording stops the current recording session
func (d *AudioDialog) StopRecording() error {
	if !d.recordActive {
		return nil
	}

	sessionID := d.sessionID
	d.recordActive = false
	d.sessionID = 0

	if err := d.audioClient.StopRecording(sessionID); err != nil {
		return fmt.Errorf("stop recording: %w", err)
	}

	log.Println("[audio] Record session closed")
	return nil
}

func (d *AudioDialog) RecordingActive() bool {
	return d.recordActive
}

// ReadRecordChunk reads a PCM chunk from the recording session
func (d *AudioDialog) ReadRecordChunk(timeoutMs uint32) (*AudioChunkResult, error) {
	if !d.recordActive || d.sessionID == 0 {
		return nil, fmt.Errorf("recording is not active")
	}
	return d.audioClient.ReadRecordChunk(d.sessionID, timeoutMs)
}

// ProcessVADFrame processes an audio frame through VAD.
func (d *AudioDialog) ProcessVADFrame(samples []int16) ([]int16, error) {
	return d.vad.Process(samples)
}

// FlushVAD flushes any buffered audio from VAD
func (d *AudioDialog) FlushVAD() []int16 {
	return d.vad.Flush()
}

// ResetVAD resets the VAD state
func (d *AudioDialog) ResetVAD() {
	if err := d.vad.Reset(); err != nil {
		log.Printf("[vad] reset failed: %v\n", err)
	}
}

// VADFrameSamples returns the number of samples to feed into each VAD frame.
func (d *AudioDialog) VADFrameSamples() int {
	return d.vad.FrameSamples()
}

func (d *AudioDialog) VADDebugState() VADDebugState {
	return d.vad.DebugState()
}

// ProcessUtterance processes a detected utterance
func (d *AudioDialog) ProcessUtterance(ctx context.Context, utterance []int16, runtime *Runtime) error {
	input, err := d.PrepareTurnInput(utterance)
	if err != nil {
		return err
	}
	result, err := d.RunAgentTurn(ctx, input, runtime)
	if err != nil {
		return err
	}
	if result.SpeechStreamed {
		return nil
	}
	return d.Speak(ctx, result.Output, nil)
}

func (d *AudioDialog) PrepareTurnInput(utterance []int16) (TurnInput, error) {
	duration := float64(len(utterance)) / float64(d.config.Audio.SampleRateOrDefault())
	log.Printf("[utterance] %.1fs of speech\n", duration)

	// Convert to WAV
	wavData := pcm16MonoToWAV(utterance, d.config.Audio.SampleRateOrDefault())
	log.Printf("[debug] WAV size: %d bytes\n", len(wavData))

	audioInput, err := PrepareAudioInput(d.config.InputModeOrDefault(), d.sttClient, wavData, "", nil)
	if err != nil {
		return TurnInput{}, err
	}
	if audioInput.Transcript != "" {
		log.Printf("[stt] Transcript: %s\n", audioInput.Transcript)
	}
	return audioInput, nil
}

func (d *AudioDialog) RunAgentTurn(ctx context.Context, input TurnInput, runtime *Runtime) (RunResult, error) {
	d.playPromptSound(promptSoundAgentSend, "agent send", true)

	// Send to LLM
	log.Printf("[llm] Sending request to provider '%s' (model=%s)...\n",
		d.config.Model.Provider, d.config.Model.Model)

	req := RunRequest{
		Input:       input.InputText,
		Attachments: input.Attachments,
		MaxTokens:   d.config.VoiceMaxResponseTokensOrDefault(),
		EventHandler: func(event RunEvent) {
			d.HandleRunEvent(ctx, event)
		},
	}

	var speech *streamingSpeechWriter
	if d.ttsClient != nil && d.config.VoiceStreamingTTSEnabledOrDefault() {
		speech = newStreamingSpeechWriter(ctx, d)
		req.StreamWriter = speech
	}

	result, err := runtime.Run(ctx, req)
	if speech != nil {
		if closeErr := speech.CloseAndWait(); closeErr != nil {
			log.Printf("[error] streaming speech failed: %v", closeErr)
		}
		result.SpeechStreamed = speech.Spoke()
	}
	if err != nil {
		return RunResult{}, fmt.Errorf("LLM request failed: %w", err)
	}

	log.Printf("[llm] Response received\n")
	return result, nil
}

func (d *AudioDialog) HandleRunEvent(ctx context.Context, event RunEvent) {
	if event.Type != "tool_call" || !d.config.VoiceToolCallSpeechOrDefault() {
		return
	}
	if event.ToolName == "enter_sleep" {
		return
	}
	description := event.Description
	go d.SpeakToolDescription(ctx, description)
}

func (d *AudioDialog) SpeakToolDescription(ctx context.Context, description string) {
	description = strings.TrimSpace(description)
	if description == "" {
		return
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := d.speak(ctx, description, nil, toolDescriptionSpeechTimeout); err != nil {
		log.Printf("[error] Tool description TTS failed: %v", err)
	}
}

func (d *AudioDialog) Speak(ctx context.Context, text string, interrupt <-chan struct{}) error {
	return d.speak(ctx, text, interrupt, 0)
}

func (d *AudioDialog) speak(ctx context.Context, text string, interrupt <-chan struct{}, timeoutAfterLock time.Duration) error {
	if ctx == nil {
		ctx = context.Background()
	}
	baseCtx := ctx
	cancelInterrupt := func() {}
	if interrupt != nil {
		interruptCtx, cancel := context.WithCancel(ctx)
		baseCtx = interruptCtx
		cancelInterrupt = cancel
		defer cancelInterrupt()
		go func() {
			select {
			case <-interrupt:
				cancelInterrupt()
			case <-interruptCtx.Done():
			}
		}()
	}
	// Speak response if TTS is available
	if d.ttsClient != nil && text != "" {
		if err := baseCtx.Err(); err != nil {
			return err
		}
		d.speechMu.Lock()
		defer d.speechMu.Unlock()
		if err := baseCtx.Err(); err != nil {
			return err
		}
		speakCtx := baseCtx
		cancelTimeout := func() {}
		if timeoutAfterLock > 0 {
			speakCtx, cancelTimeout = context.WithTimeout(baseCtx, timeoutAfterLock)
			defer cancelTimeout()
		}
		if err := speakCtx.Err(); err != nil {
			return err
		}
		log.Printf("[reply] %s\n", text)
		log.Printf("[tts] Starting streaming playback...\n")
		if err := d.ttsClient.TextToSpeechStream(speakCtx, text, d.audioClient); err != nil {
			log.Printf("[error] TTS streaming failed: %v", err)
			return err
		} else {
			log.Printf("[tts] Streaming playback complete\n")
		}
	} else if text != "" {
		log.Printf("[reply] %s\n", text)
	}

	return nil
}

func (d *AudioDialog) playPromptSoundAsync(kind promptSoundKind, label string) {
	go func() {
		d.playPromptSound(kind, label, true)
	}()
}

func (d *AudioDialog) playPromptSound(kind promptSoundKind, label string, wait bool) {
	d.speechMu.Lock()
	defer d.speechMu.Unlock()
	if err := playPromptSound(context.Background(), d.audioClient, kind, wait); err != nil {
		log.Printf("[audio] %s prompt sound failed: %v\n", label, err)
	}
}

// ProcessTextInput processes text input and speaks the response
func (d *AudioDialog) ProcessTextInput(ctx context.Context, text string, runtime *Runtime) error {
	log.Printf("[text] %s\n", text)
	d.playPromptSound(promptSoundAgentSend, "agent send", true)

	// Send to LLM
	log.Printf("[llm] Sending request to provider '%s' (model=%s)...\n",
		d.config.Model.Provider, d.config.Model.Model)

	req := RunRequest{
		Input: text,
		EventHandler: func(event RunEvent) {
			d.HandleRunEvent(ctx, event)
		},
	}
	var speech *streamingSpeechWriter
	if d.ttsClient != nil && d.config.VoiceStreamingTTSEnabledOrDefault() {
		speech = newStreamingSpeechWriter(ctx, d)
		req.StreamWriter = speech
	}

	result, err := runtime.Run(ctx, req)
	if speech != nil {
		if closeErr := speech.CloseAndWait(); closeErr != nil {
			log.Printf("[error] streaming speech failed: %v", closeErr)
		}
		result.SpeechStreamed = speech.Spoke()
	}
	if err != nil {
		return fmt.Errorf("LLM request failed: %w", err)
	}

	log.Printf("[llm] Response received\n")

	// Speak response if TTS is available
	if d.ttsClient != nil && result.Output != "" && !result.SpeechStreamed {
		if err := d.Speak(ctx, result.Output, nil); err != nil {
			log.Printf("[error] TTS streaming failed: %v", err)
		}
	} else if result.Output != "" {
		log.Printf("[reply] %s\n", result.Output)
	}

	return nil
}

// pcm16MonoToWAV converts PCM16 mono samples to WAV format
func pcm16MonoToWAV(samples []int16, sampleRate int) []byte {
	dataSize := len(samples) * 2
	fileSize := 36 + dataSize

	wav := make([]byte, 44+dataSize)

	// RIFF header
	copy(wav[0:4], "RIFF")
	binary.LittleEndian.PutUint32(wav[4:8], uint32(fileSize))
	copy(wav[8:12], "WAVE")

	// fmt chunk
	copy(wav[12:16], "fmt ")
	binary.LittleEndian.PutUint32(wav[16:20], 16)                   // chunk size
	binary.LittleEndian.PutUint16(wav[20:22], 1)                    // PCM
	binary.LittleEndian.PutUint16(wav[22:24], 1)                    // mono
	binary.LittleEndian.PutUint32(wav[24:28], uint32(sampleRate))   // sample rate
	binary.LittleEndian.PutUint32(wav[28:32], uint32(sampleRate*2)) // byte rate
	binary.LittleEndian.PutUint16(wav[32:34], 2)                    // block align
	binary.LittleEndian.PutUint16(wav[34:36], 16)                   // bits per sample

	// data chunk
	copy(wav[36:40], "data")
	binary.LittleEndian.PutUint32(wav[40:44], uint32(dataSize))

	// PCM data
	for i, sample := range samples {
		binary.LittleEndian.PutUint16(wav[44+i*2:], uint16(sample))
	}

	return wav
}

// AppendPCM16Samples converts PCM bytes to int16 samples
func AppendPCM16Samples(pcmBytes []byte, samples *[]int16, hasPending *bool, pending *byte) {
	i := 0

	// Handle pending byte from previous chunk
	if *hasPending && len(pcmBytes) > 0 {
		word := uint16(*pending) | (uint16(pcmBytes[0]) << 8)
		*samples = append(*samples, int16(word))
		*hasPending = false
		i = 1
	}

	// Process pairs of bytes
	for i+1 < len(pcmBytes) {
		word := binary.LittleEndian.Uint16(pcmBytes[i : i+2])
		*samples = append(*samples, int16(word))
		i += 2
	}

	// Save odd byte for next chunk
	if i < len(pcmBytes) {
		*pending = pcmBytes[i]
		*hasPending = true
	}
}
