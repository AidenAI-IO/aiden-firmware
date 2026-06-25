package agent

import (
	"context"
	"encoding/binary"
	"fmt"
	"log"
	"os"
	"strings"
	"sync"
	"time"

	"aiden-agent/internal/agent/tts"
)

const (
	toolContentSpeechTimeout   = 15 * time.Second
	outputInterruptWaitTimeout = 250 * time.Millisecond
)

var (
	recordingStartRetryTimeout             = 5 * time.Second
	recordingStartRetryInterval            = 250 * time.Millisecond
	audioDialogStreamingSTTFinalizeTimeout = 2 * time.Second
)

// AudioDialog manages the audio conversation loop
type AudioDialog struct {
	config        Config
	audioClient   *AudioServiceClient
	sttClient     STTClient
	ttsManager    *tts.ProviderManager
	vad           *AudioVAD
	recordActive  bool
	sessionID     uint64
	recordReader  *AudioRecordChunkReader
	recordSTT     *streamingSTTSession
	recordText    string
	speechMu      sync.Mutex
	outputMu      sync.Mutex
	activeOutputs map[*activeTTSOutput]struct{}
	progressMu    sync.Mutex
	progress      *progressSpeaker
	runControl    voiceRunControl
	historyStore  *ChatHistoryStore
	historyAppend func(Message)
	audioArchive  *AudioArchiveManager
}

type activeTTSOutput struct {
	cancel context.CancelFunc
	done   chan struct{}

	mu     sync.Mutex
	stream *streamSessionWriter
}

func newActiveTTSOutput(cancel context.CancelFunc) *activeTTSOutput {
	return &activeTTSOutput{
		cancel: cancel,
		done:   make(chan struct{}),
	}
}

func (o *activeTTSOutput) setStream(stream *streamSessionWriter) {
	if o == nil {
		return
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	o.stream = stream
}

func (o *activeTTSOutput) clearStream(stream *streamSessionWriter) {
	if o == nil {
		return
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.stream == stream {
		o.stream = nil
	}
}

func (o *activeTTSOutput) interrupt() {
	if o == nil {
		return
	}
	o.mu.Lock()
	stream := o.stream
	o.mu.Unlock()
	if stream != nil {
		stream.interrupt()
	}
	if o.cancel != nil {
		o.cancel()
	}
}

func (o *activeTTSOutput) finish() {
	if o == nil {
		return
	}
	select {
	case <-o.done:
	default:
		close(o.done)
	}
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

	// Create pluggable TTS manager if configured.
	ttsManager, err := newTTSProviderManagerFromConfig(cfg, nil)
	if err != nil {
		log.Printf("[tts] init failed, continuing without TTS: %v\n", err)
	}

	silenceMs := cfg.SilenceMs
	if silenceMs == 0 {
		silenceMs = defaultSilenceMs
	}
	minSpeechMs := cfg.MinSpeechMs
	if minSpeechMs == 0 {
		minSpeechMs = defaultMinSpeechMs
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
		config:       cfg,
		audioClient:  audioClient,
		sttClient:    sttClient,
		ttsManager:   ttsManager,
		vad:          vad,
		audioArchive: NewAudioArchiveManager(cfg.AudioArchive),
	}, nil
}

// StartRecording starts an audio recording session
func (d *AudioDialog) StartRecording() error {
	if d.recordActive {
		return nil
	}

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
	if reader, err := d.audioClient.OpenRecordChunkReader(result.SessionID); err == nil {
		d.recordReader = reader
	} else {
		log.Printf("[audio] Persistent record reader unavailable, using per-request reads: %v\n", err)
	}
	d.recordText = ""
	recordSTT, err := beginStreamingSTTSession(context.Background(), d.sttClient, STTStreamConfig{
		SampleRate: d.config.Audio.SampleRateOrDefault(),
		Channels:   d.config.Audio.ChannelsOrDefault(),
		BitWidth:   d.config.Audio.BitWidthOrDefault(),
	})
	if err != nil {
		log.Printf("[stt] streaming upload unavailable, falling back to one-shot STT: %v\n", err)
	} else if recordSTT != nil {
		log.Println("[stt] streaming upload enabled for realtime transcription")
		d.recordSTT = recordSTT
	}
	d.recordActive = true
	if d.vad != nil {
		if err := d.vad.Reset(); err != nil {
			log.Printf("[vad] reset failed: %v\n", err)
		}
	}
	// Open the mic before the cue tone so speech right after Enter is captured.
	d.playPromptSoundAsync(promptSoundRecordingStart, "recording")
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
	reader := d.recordReader
	recordSTT := d.recordSTT
	if recordSTT != nil {
		defer func() {
			_ = recordSTT.Close()
		}()
	}
	d.recordActive = false
	d.sessionID = 0
	d.recordReader = nil
	d.recordSTT = nil

	if reader != nil {
		_ = reader.Close()
	}

	if err := d.audioClient.StopRecording(sessionID); err != nil {
		return fmt.Errorf("stop recording: %w", err)
	}
	if recordSTT != nil {
		transcript, err := recordSTT.FinalizeWithTimeout(audioDialogStreamingSTTFinalizeTimeout)
		if err != nil {
			log.Printf("[stt] finalize streaming transcript failed, falling back to one-shot STT: %v\n", err)
		} else {
			d.recordText = transcript
		}
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
	if d.recordReader != nil {
		chunk, err := d.recordReader.Read(timeoutMs)
		if err == nil {
			d.uploadRecordChunkToStreamingSTT(chunk)
			return chunk, nil
		}
		log.Printf("[audio] Persistent record reader failed, falling back to per-request reads: %v\n", err)
		_ = d.recordReader.Close()
		d.recordReader = nil
	}
	chunk, err := d.audioClient.ReadRecordChunk(d.sessionID, timeoutMs)
	if err == nil {
		d.uploadRecordChunkToStreamingSTT(chunk)
	}
	return chunk, err
}

func (d *AudioDialog) uploadRecordChunkToStreamingSTT(chunk *AudioChunkResult) {
	if d == nil || chunk == nil || len(chunk.PCM) == 0 || d.recordSTT == nil {
		return
	}
	if err := d.recordSTT.UploadPCM(chunk.PCM); err != nil {
		log.Printf("[stt] streaming upload failed, falling back to one-shot STT: %v\n", err)
		_ = d.recordSTT.Close()
		d.recordSTT = nil
	}
}

// ProcessVADFrame processes an audio frame through VAD.
func (d *AudioDialog) ProcessVADFrame(samples []int16) ([]int16, error) {
	return d.vad.Process(samples)
}

// FlushVAD flushes any buffered audio from VAD
func (d *AudioDialog) FlushVAD() []int16 {
	return d.vad.Flush()
}

// FinishManualUtterance flushes VAD state and appends any tail samples that were
// not yet aligned to a full Silero frame (legacy C++ agent_main behavior).
func (d *AudioDialog) FinishManualUtterance(pending []int16) []int16 {
	frameSamples := d.VADFrameSamples()
	consumed := 0
	for consumed+frameSamples <= len(pending) {
		if _, err := d.ProcessVADFrame(pending[consumed : consumed+frameSamples]); err != nil {
			log.Printf("[vad] finish manual utterance failed: %v\n", err)
			break
		}
		consumed += frameSamples
	}
	tail := pending[consumed:]
	utterance := d.FlushVAD()
	if len(tail) == 0 {
		return utterance
	}
	if len(utterance) == 0 {
		return append([]int16(nil), tail...)
	}
	return append(utterance, tail...)
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

func (d *AudioDialog) registerActiveOutput(output *activeTTSOutput) func() {
	if output == nil {
		return func() {}
	}
	d.outputMu.Lock()
	if d.activeOutputs == nil {
		d.activeOutputs = make(map[*activeTTSOutput]struct{})
	}
	d.activeOutputs[output] = struct{}{}
	d.outputMu.Unlock()

	var once sync.Once
	return func() {
		once.Do(func() {
			d.outputMu.Lock()
			delete(d.activeOutputs, output)
			d.outputMu.Unlock()
			output.finish()
		})
	}
}

func (d *AudioDialog) snapshotActiveOutputs() []*activeTTSOutput {
	d.outputMu.Lock()
	defer d.outputMu.Unlock()
	if len(d.activeOutputs) == 0 {
		return nil
	}
	outputs := make([]*activeTTSOutput, 0, len(d.activeOutputs))
	for output := range d.activeOutputs {
		outputs = append(outputs, output)
	}
	return outputs
}

func (d *AudioDialog) beginActiveManagedTTSStream(ctx context.Context) (*streamSessionWriter, func(), error) {
	if ctx == nil {
		ctx = context.Background()
	}
	streamCtx, streamCancel := context.WithCancel(ctx)
	stream, err := beginManagedTTSStream(streamCtx, d.ttsManager, d.audioClient, d.config)
	if err != nil {
		streamCancel()
		return nil, nil, err
	}
	stream.setCancel(streamCancel)
	output := newActiveTTSOutput(streamCancel)
	output.setStream(stream)
	unregister := d.registerActiveOutput(output)
	return stream, func() {
		unregister()
		streamCancel()
	}, nil
}

// InterruptOutput immediately stops any TTS output owned by this dialog.
func (d *AudioDialog) InterruptOutput() {
	if d == nil {
		return
	}
	d.progressMu.Lock()
	progress := d.progress
	d.progressMu.Unlock()
	if progress != nil {
		progress.Cancel()
	}

	outputs := d.snapshotActiveOutputs()
	if progress != nil || len(outputs) > 0 {
		log.Printf("[audio] interrupt output requested: active_outputs=%d progress_active=%t\n", len(outputs), progress != nil)
	}
	for _, output := range outputs {
		output.interrupt()
	}
	waitForActiveOutputs(outputs, outputInterruptWaitTimeout)
}

func waitForActiveOutputs(outputs []*activeTTSOutput, timeout time.Duration) {
	if len(outputs) == 0 || timeout <= 0 {
		return
	}
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	for _, output := range outputs {
		select {
		case <-output.done:
		case <-timer.C:
			return
		}
	}
}

// ProcessUtterance processes a detected utterance
func (d *AudioDialog) ProcessUtterance(ctx context.Context, utterance []int16, runtime *Runtime) error {
	input, err := d.PrepareTurnInput(utterance)
	if err != nil {
		return err
	}
	result, err := d.RunVoiceTurn(ctx, input, utterance, runtime)
	if err != nil {
		return err
	}
	if result.SpeechStreamed {
		return nil
	}
	return d.Speak(ctx, result.SpokenTextForConfig(d.config), nil)
}

// SetHistoryStore wires the chat history store. When non-nil, voice messages
// produced during RunVoiceTurn are appended with Source="voice".
func (d *AudioDialog) SetHistoryStore(store *ChatHistoryStore) {
	d.historyStore = store
}

// SetHistoryAppender wires a higher-level history appender. The server uses
// this so voice turns update both persistent history and the in-memory Web UI
// snapshot in the same path as HTTP chat messages.
func (d *AudioDialog) SetHistoryAppender(appender func(Message)) {
	d.historyAppend = appender
}

// persistVoiceTurn appends user (when transcript is available) and assistant
// messages to the chat history store, tagging both with Source="voice". It is
// best-effort: errors are logged and never break the voice loop. Uses
// context.Background since the caller's context may be cancelled by the time
// we persist.
func (d *AudioDialog) persistVoiceTurn(input TurnInput, result RunResult, utterance []int16) {
	d.PersistVoiceTurn(input, result, utterance)
}

func (d *AudioDialog) PersistVoiceTurn(input TurnInput, result RunResult, utterance []int16) {
	d.persistVoiceUserInput(input, utterance, result.EpisodeID, "")
	d.persistVoiceAssistantOutput(result, "")
}

func (d *AudioDialog) PersistVoiceUserInput(input TurnInput, utterance []int16) {
	d.persistVoiceUserInput(input, utterance, "", "")
}

func (d *AudioDialog) persistVoiceUserInput(input TurnInput, utterance []int16, episodeID, requestID string) {
	if d.historyAppend == nil && d.historyStore == nil {
		return
	}
	input = d.ensureVoiceInputAudioArtifact(input, utterance, nil)

	content := strings.TrimSpace(input.InputText)
	if content == "" {
		content = strings.TrimSpace(input.Transcript)
	}
	if content == "" {
		return
	}
	userMsg := messageFromTurnInput(input, episodeID, requestID, nil, time.Now())
	userMsg.Content = content
	if userMsg.Source == "" {
		userMsg.Source = "voice"
	}
	d.appendVoiceHistory(userMsg)
}

func (d *AudioDialog) PersistVoiceAssistantOutput(result RunResult) {
	d.persistVoiceAssistantOutput(result, "")
}

func (d *AudioDialog) persistVoiceAssistantOutput(result RunResult, requestID string) {
	if d.historyAppend == nil && d.historyStore == nil {
		return
	}
	now := time.Now()
	output := strings.TrimSpace(result.Output)
	if output != "" {
		assistantMsg := Message{
			Type:      "assistant",
			EpisodeID: result.EpisodeID,
			RequestID: requestID,
			Content:   output,
			Source:    "voice",
			Timestamp: now,
		}
		d.appendVoiceHistory(assistantMsg)
	}
}

func (d *AudioDialog) appendVoiceRunEvent(event RunEvent, requestID string) {
	if d.historyAppend == nil && d.historyStore == nil {
		return
	}
	message := messageFromRunEvent(event, event.EpisodeID, requestID)
	if message.Type == "" {
		return
	}
	message.Source = "voice"
	d.appendVoiceHistory(message)
}

func (d *AudioDialog) appendVoiceHistory(message Message) {
	if d.historyAppend != nil {
		d.historyAppend(message)
		return
	}
	if d.historyStore != nil {
		if err := d.historyStore.Append(context.Background(), message); err != nil {
			log.Printf("[history] persist voice message failed: %v", err)
		}
	}
}

func (d *AudioDialog) PrepareTurnInput(utterance []int16) (TurnInput, error) {
	duration := float64(len(utterance)) / float64(d.config.Audio.SampleRateOrDefault())
	log.Printf("[utterance] %.1fs of speech\n", duration)

	// Convert to WAV
	wavData := pcm16MonoToWAV(utterance, d.config.Audio.SampleRateOrDefault())
	log.Printf("[debug] WAV size: %d bytes\n", len(wavData))

	audioInput, err := PrepareAudioInput(d.config.InputModeOrDefault(), d.sttClient, wavData, d.consumeRecordingTranscript(), "", nil)
	if err != nil {
		return TurnInput{}, err
	}
	audioInput = d.ensureVoiceInputAudioArtifact(audioInput, utterance, wavData)
	if audioInput.Transcript != "" {
		log.Printf("[stt] Transcript: %s\n", audioInput.Transcript)
	}
	return audioInput, nil
}

func (d *AudioDialog) consumeRecordingTranscript() string {
	if d == nil {
		return ""
	}
	transcript := strings.TrimSpace(d.recordText)
	d.recordText = ""
	return transcript
}

func (d *AudioDialog) ensureVoiceInputAudioArtifact(input TurnInput, utterance []int16, wavData []byte) TurnInput {
	input = normalizeTurnInput(input)
	if artifact, ok := firstAudioArtifact(input); ok {
		if artifact.Path != "" {
			return input
		}
		if d.audioArchive == nil || !d.audioArchive.config.Enabled {
			if artifact.DurationMS > 0 || artifact.Size > 0 {
				return input
			}
		}
	}
	if d.audioArchive == nil {
		if input.Modality == TurnModalityAudio && len(wavData) > 0 {
			return withAudioArtifactPath(input, "", wavDurationMS(wavData), int64(len(wavData)))
		}
		return input
	}

	audioPath, audioDuration, err := d.audioArchive.SaveAudio(utterance, d.config.Audio.SampleRateOrDefault())
	if err != nil {
		log.Printf("[audio_archive] save failed: %v", err)
		if input.Modality == TurnModalityAudio && len(wavData) > 0 {
			return withAudioArtifactPath(input, "", wavDurationMS(wavData), int64(len(wavData)))
		}
		return input
	}
	size := int64(0)
	if audioPath != "" {
		if info, statErr := os.Stat(audioPath); statErr == nil {
			size = info.Size()
		}
	}
	if size == 0 && len(wavData) > 0 {
		size = int64(len(wavData))
	}
	if audioDuration == 0 && len(wavData) > 0 {
		audioDuration = int(wavDurationMS(wavData))
	}
	if audioPath == "" && input.Modality != TurnModalityAudio {
		return input
	}
	return withAudioArtifactPath(input, audioPath, int64(audioDuration), size)
}

func (d *AudioDialog) RunAgentTurn(ctx context.Context, input TurnInput, runtime *Runtime) (RunResult, error) {
	episodeID := ""
	if runtime != nil {
		episodeID = runtime.NewEpisodeID()
	}
	requestID := createVoiceRequestID()
	if !d.beginVoiceRunControl(requestID) {
		return RunResult{}, fmt.Errorf("voice run already active")
	}
	defer d.endVoiceRunControl(requestID)
	return d.runAgentTurnWithActiveRequest(ctx, input, runtime, episodeID, requestID, VoiceTurnContext{})
}

func (d *AudioDialog) RunVoiceTurn(ctx context.Context, input TurnInput, utterance []int16, runtime *Runtime) (RunResult, error) {
	return d.RunVoiceTurnWithContext(ctx, input, utterance, runtime, VoiceTurnContext{})
}

func (d *AudioDialog) RunVoiceTurnWithContext(ctx context.Context, input TurnInput, utterance []int16, runtime *Runtime, turnContext VoiceTurnContext) (RunResult, error) {
	episodeID := ""
	if runtime != nil {
		episodeID = runtime.NewEpisodeID()
	}
	requestID := createVoiceRequestID()
	if !d.beginVoiceRunControl(requestID) {
		return RunResult{}, fmt.Errorf("voice run already active")
	}
	defer d.endVoiceRunControl(requestID)
	d.persistVoiceUserInput(input, utterance, episodeID, requestID)
	result, err := d.runAgentTurnWithActiveRequest(ctx, input, runtime, episodeID, requestID, turnContext)
	if err != nil {
		return RunResult{}, err
	}
	d.persistVoiceAssistantOutput(result, requestID)
	return result, nil
}

func (d *AudioDialog) runAgentTurnWithActiveRequest(ctx context.Context, input TurnInput, runtime *Runtime, episodeID, requestID string, turnContext VoiceTurnContext) (RunResult, error) {
	turnContext = normalizeVoiceTurnContext(turnContext)

	d.playPromptSound(promptSoundAgentSend, "agent send", true)

	// Send to LLM
	log.Printf("[llm] Sending request to provider '%s' (model=%s)...\n",
		d.config.Model.Provider, d.config.Model.Model)

	progress := d.newRunProgressSpeaker()
	d.setProgressSpeaker(progress)
	defer func() {
		progress.Cancel()
		d.setProgressSpeaker(nil)
	}()

	req := RunRequest{
		Input:          input.InputText,
		Attachments:    input.Attachments,
		Turn:           input,
		RequestID:      requestID,
		RuntimeContext: turnContext.RuntimeContext,
		MaxTokens:      d.config.VoiceMaxResponseTokensOrDefault(),
		EventHandler: func(event RunEvent) {
			d.appendVoiceRunEvent(event, requestID)
			d.HandleRunEvent(ctx, event)
		},
		StreamFinalChunks: true,
		SteerProvider: func(ctx context.Context) (RunSteerMessage, bool) {
			return d.consumePendingSteer(requestID)
		},
		FinalSteerProvider: func(ctx context.Context) (RunSteerMessage, bool) {
			return d.consumeFinalPendingSteer(requestID)
		},
		SteerInterrupt: func() <-chan struct{} {
			return d.steerInterruptChannel(requestID)
		},
		SteerWaiter: func(ctx context.Context) (RunSteerMessage, bool, error) {
			return d.waitForSteerInterrupt(ctx, requestID)
		},
	}
	req.EpisodeID = strings.TrimSpace(episodeID)

	var newStream *streamSessionWriter
	unregisterStream := func() {}
	if d.config.VoiceStreamingTTSEnabledOrDefault() && d.ttsManager != nil {
		stream, unregister, err := d.beginActiveManagedTTSStream(ctx)
		if err != nil {
			log.Printf("[error] TTS BeginStream failed: %v\n", err)
		} else {
			newStream = stream
			unregisterStream = unregister
			defer unregisterStream()
			req.StreamWriter = newCancelOnFirstWriteWriter(speechStreamWriterForConfig(newStream, d.config), progress.Cancel)
		}
	}

	result, err := runtime.Run(ctx, req)
	d.stopAcceptingSteer(requestID)
	if newStream != nil {
		closeErr := newStream.closeAndWait()
		if closeErr != nil {
			log.Printf("[error] new TTS stream failed: %v", closeErr)
		}
		result.SpeechStreamed = closeErr == nil && newStream.spokeSuccessfully()
	}
	if err != nil {
		return RunResult{}, fmt.Errorf("LLM request failed: %w", err)
	}

	log.Printf("[llm] Response received\n")
	return result, nil
}

func (d *AudioDialog) beginVoiceRunControl(requestID string) bool {
	if d == nil {
		return false
	}
	return d.runControl.begin(requestID)
}

func (d *AudioDialog) endVoiceRunControl(requestID string) {
	if d == nil {
		return
	}
	d.runControl.end(requestID)
}

func (d *AudioDialog) QueueSteer(input TurnInput) bool {
	content := steerContentFromTurnInput(input)
	if content == "" {
		return false
	}
	queued, ok := d.runControl.queueSteer(content)
	if !ok {
		return false
	}
	if queued.interrupted {
		log.Printf("[steer] Queued interrupted voice steer: request_id=%s len=%d\n", queued.requestID, queued.contentLength)
		return true
	}
	log.Printf("[steer] Queued voice steer: request_id=%s len=%d\n", queued.requestID, queued.contentLength)
	return true
}

func (d *AudioDialog) BeginSteerInterrupt() bool {
	if d == nil {
		return false
	}
	requestID, started, ok := d.runControl.beginInterrupt()
	if !ok {
		return false
	}
	if started {
		log.Printf("[steer] Voice run interrupted, waiting for steering input: request_id=%s\n", requestID)
	}
	return true
}

func (d *AudioDialog) ResumeSteerInterrupt() bool {
	if d == nil {
		return false
	}
	requestID, ok := d.runControl.resumeInterrupt()
	if !ok {
		return false
	}
	log.Printf("[steer] Voice steer interruption resumed without input: request_id=%s\n", requestID)
	return true
}

func (d *AudioDialog) consumePendingSteer(requestID string) (RunSteerMessage, bool) {
	if d == nil {
		return RunSteerMessage{}, false
	}
	return d.runControl.consumePending(requestID)
}

func (d *AudioDialog) consumeFinalPendingSteer(requestID string) (RunSteerMessage, bool) {
	if d == nil {
		return RunSteerMessage{}, false
	}
	return d.runControl.consumeFinalPending(requestID)
}

func (d *AudioDialog) stopAcceptingSteer(requestID string) {
	if d == nil {
		return
	}
	d.runControl.stopAccepting(requestID)
}

func (d *AudioDialog) steerInterruptChannel(requestID string) <-chan struct{} {
	if d == nil {
		return nil
	}
	return d.runControl.interruptChannel(requestID)
}

func (d *AudioDialog) waitForSteerInterrupt(ctx context.Context, requestID string) (RunSteerMessage, bool, error) {
	if d == nil {
		return RunSteerMessage{}, false, nil
	}
	return d.runControl.waitForInterrupt(ctx, requestID)
}

func (d *AudioDialog) HandleRunEvent(ctx context.Context, event RunEvent) {
	if event.Type == runEventTodoUpdate {
		if !d.config.VoiceProgressSpeechEnabledOrDefault() {
			return
		}
		text, ok := progressSpeechTextForEvent(event)
		if !ok {
			return
		}
		d.currentProgressSpeaker().Schedule(ctx, text)
		return
	}
	if event.Type != runEventToolCall || !d.config.VoiceToolCallSpeechOrDefault() || event.ToolName == toolWaitForWakeup {
		return
	}
	// Tool-call speech is only explicit LLM-generated event content.
	go d.SpeakToolContent(event.Content)
}

func (d *AudioDialog) newRunProgressSpeaker() *progressSpeaker {
	return newProgressSpeaker(func(ctx context.Context, text string) error {
		return d.speak(ctx, text, nil, toolContentSpeechTimeout)
	}, progressSpeakerConfig{})
}

func (d *AudioDialog) setProgressSpeaker(speaker *progressSpeaker) {
	d.progressMu.Lock()
	defer d.progressMu.Unlock()
	d.progress = speaker
}

func (d *AudioDialog) currentProgressSpeaker() *progressSpeaker {
	d.progressMu.Lock()
	defer d.progressMu.Unlock()
	if d.progress == nil {
		d.progress = d.newRunProgressSpeaker()
	}
	return d.progress
}

func (d *AudioDialog) SpeakToolContent(content string) {
	content = strings.TrimSpace(content)
	if content == "" {
		return
	}
	// Detached from the agent turn context so tool TTS is not cut off when
	// runtime.Run returns or the parent context is cancelled.
	if err := d.speak(context.Background(), content, nil, toolContentSpeechTimeout); err != nil {
		log.Printf("[error] Tool content TTS failed: %v", err)
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
	if d.ttsManager != nil && text != "" {
		if err := baseCtx.Err(); err != nil {
			return err
		}
		outputCtx, cancelOutput := context.WithCancel(baseCtx)
		output := newActiveTTSOutput(cancelOutput)
		unregisterOutput := d.registerActiveOutput(output)
		defer func() {
			unregisterOutput()
			cancelOutput()
		}()
		d.speechMu.Lock()
		defer d.speechMu.Unlock()
		if err := outputCtx.Err(); err != nil {
			return err
		}
		speakCtx := outputCtx
		cancelTimeout := func() {}
		if timeoutAfterLock > 0 {
			speakCtx, cancelTimeout = context.WithTimeout(outputCtx, timeoutAfterLock)
			defer cancelTimeout()
		}
		if err := speakCtx.Err(); err != nil {
			return err
		}
		log.Printf("[reply] %s\n", text)
		log.Printf("[tts] Starting streaming playback...\n")
		_, err := speakWithTTSManagerObserved(speakCtx, d.ttsManager, d.audioClient, d.config, text, func(stream *streamSessionWriter) func() {
			stream.setCancel(cancelOutput)
			output.setStream(stream)
			return func() {
				output.clearStream(stream)
			}
		})
		if err != nil {
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
	if kind == promptSoundRecordingStart {
		d.playPromptSoundUninterruptible(kind, label, wait)
		return
	}

	outputCtx, cancelOutput := context.WithCancel(context.Background())
	output := newActiveTTSOutput(cancelOutput)
	unregisterOutput := d.registerActiveOutput(output)
	defer func() {
		unregisterOutput()
		cancelOutput()
	}()

	d.speechMu.Lock()
	defer d.speechMu.Unlock()
	if err := playPromptSound(outputCtx, d.audioClient, kind, wait); err != nil {
		log.Printf("[audio] %s prompt sound failed: %v\n", label, err)
	}
}

func (d *AudioDialog) playPromptSoundUninterruptible(kind promptSoundKind, label string, wait bool) {
	startedAt := time.Now()
	log.Printf("[audio] %s prompt sound requested (uninterruptible)\n", label)
	d.speechMu.Lock()
	defer d.speechMu.Unlock()
	if err := playPromptSound(context.Background(), d.audioClient, kind, wait); err != nil {
		log.Printf("[audio] %s prompt sound failed after %s: %v\n", label, time.Since(startedAt).Round(time.Millisecond), err)
		return
	}
	log.Printf("[audio] %s prompt sound completed in %s\n", label, time.Since(startedAt).Round(time.Millisecond))
}

// ProcessTextInput processes text input and speaks the response
func (d *AudioDialog) ProcessTextInput(ctx context.Context, text string, runtime *Runtime) error {
	log.Printf("[text] %s\n", text)
	d.playPromptSound(promptSoundAgentSend, "agent send", true)

	// Send to LLM
	log.Printf("[llm] Sending request to provider '%s' (model=%s)...\n",
		d.config.Model.Provider, d.config.Model.Model)

	progress := d.newRunProgressSpeaker()
	d.setProgressSpeaker(progress)
	defer func() {
		progress.Cancel()
		d.setProgressSpeaker(nil)
	}()

	req := RunRequest{
		Input: text,
		Turn:  NewTextTurnInput(text, nil),
		EventHandler: func(event RunEvent) {
			d.appendVoiceRunEvent(event, "")
			d.HandleRunEvent(ctx, event)
		},
		StreamFinalChunks: true,
	}
	var newStream *streamSessionWriter
	unregisterStream := func() {}
	if d.config.VoiceStreamingTTSEnabledOrDefault() && d.ttsManager != nil {
		stream, unregister, err := d.beginActiveManagedTTSStream(ctx)
		if err != nil {
			log.Printf("[error] TTS BeginStream failed: %v\n", err)
		} else {
			newStream = stream
			unregisterStream = unregister
			defer unregisterStream()
			req.StreamWriter = newCancelOnFirstWriteWriter(speechStreamWriterForConfig(newStream, d.config), progress.Cancel)
		}
	}

	result, err := runtime.Run(ctx, req)
	if newStream != nil {
		closeErr := newStream.closeAndWait()
		if closeErr != nil {
			log.Printf("[error] new TTS stream failed: %v", closeErr)
		}
		result.SpeechStreamed = closeErr == nil && newStream.spokeSuccessfully()
	}
	if err != nil {
		return fmt.Errorf("LLM request failed: %w", err)
	}

	log.Printf("[llm] Response received\n")

	// Speak response if TTS is available
	speechText := result.SpokenTextForConfig(d.config)
	if d.ttsManager != nil && speechText != "" && !result.SpeechStreamed {
		if err := d.Speak(ctx, speechText, nil); err != nil {
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
	if len(pcmBytes) == 0 {
		return
	}

	i := 0
	dst := *samples

	// Handle pending byte from previous chunk
	if *hasPending {
		word := uint16(*pending) | (uint16(pcmBytes[0]) << 8)
		dst = append(dst, int16(word))
		*hasPending = false
		i = 1
	}

	pairCount := (len(pcmBytes) - i) / 2
	if pairCount > 0 {
		oldLen := len(dst)
		needed := oldLen + pairCount
		if needed > cap(dst) {
			newCap := cap(dst) * 2
			if newCap < needed {
				newCap = needed
			}
			if newCap == 0 {
				newCap = pairCount
			}
			grown := make([]int16, oldLen, newCap)
			copy(grown, dst)
			dst = grown
		}
		dst = dst[:needed]
		for j := 0; j < pairCount; j++ {
			offset := i + j*2
			dst[oldLen+j] = int16(binary.LittleEndian.Uint16(pcmBytes[offset : offset+2]))
		}
		i += pairCount * 2
	}

	// Save odd byte for next chunk
	if i < len(pcmBytes) {
		*pending = pcmBytes[i]
		*hasPending = true
	}
	*samples = dst
}
