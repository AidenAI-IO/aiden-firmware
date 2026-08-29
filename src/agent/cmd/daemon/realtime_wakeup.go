package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"strings"
	"sync"
	"time"

	"aiden-agent/internal/agent"
	"aiden-agent/internal/agent/agentpath"
	"aiden-agent/internal/agent/contextmanager"
	"aiden-agent/internal/agent/messages"
	"aiden-agent/internal/agent/rtclient"
	"aiden-agent/internal/agent/tts"
	"aiden-agent/internal/agenttask"

	langtools "github.com/tmc/langchaingo/tools"
)

const (
	realtimeAudioReadTimeoutMs = 200
	realtimeConnectTimeout     = 15 * time.Second
	realtimeEventTimeout       = 10 * time.Second
	realtimeUpdateTimeout      = 5 * time.Second
	realtimePlaybackKeepAlive  = 10 * time.Second
	realtimeToolCallTimeout    = 30 * time.Second
	realtimeNotificationPoll   = 500 * time.Millisecond
	realtimeNotificationDrain  = 30 * time.Second
	realtimeTaskResultDebounce = 500 * time.Millisecond
	realtimeContextReplayTurns = 10
)

var errRealtimeShutdown = errors.New("realtime shutdown requested")

// runRealtimeWakeupMode owns the realtime voice path. The legacy wakeup
// runners remain separate so they can be restored without changing this path.
type realtimeChatCommand struct {
	ctx     context.Context
	request agent.RealtimeChatRequest
	events  chan agent.RealtimeChatEvent
}

type realtimeToolResult struct {
	call   rtclient.FunctionCallEvent
	output string
}

type realtimeToolTracker struct {
	pending      map[string]int
	seen         map[string]bool
	responseDone map[string]bool
}

func newRealtimeToolTracker() *realtimeToolTracker {
	return &realtimeToolTracker{
		pending:      make(map[string]int),
		seen:         make(map[string]bool),
		responseDone: make(map[string]bool),
	}
}

func (t *realtimeToolTracker) start(responseID string) {
	t.seen[responseID] = true
	t.pending[responseID]++
}

// complete returns true when response.done was already received and this was
// the final pending tool result, so the caller should create the continuation.
func (t *realtimeToolTracker) complete(responseID string) bool {
	if t.pending[responseID] > 0 {
		t.pending[responseID]--
	}
	if t.pending[responseID] != 0 || !t.responseDone[responseID] {
		return false
	}
	t.clear(responseID)
	return true
}

// done returns true when a tool-bearing response can continue immediately.
// A false return means either no tool was seen or pending tool work remains.
func (t *realtimeToolTracker) done(responseID string) (hasTools bool, continueNow bool) {
	if !t.seen[responseID] {
		return false, false
	}
	if t.pending[responseID] > 0 {
		t.responseDone[responseID] = true
		return true, false
	}
	t.clear(responseID)
	return true, true
}

func (t *realtimeToolTracker) clear(responseID string) {
	delete(t.pending, responseID)
	delete(t.seen, responseID)
	delete(t.responseDone, responseID)
}

type realtimeChatBridge struct {
	commands chan realtimeChatCommand
	wakeup   func()

	mu     sync.RWMutex
	active bool
	done   chan struct{}
}

func newRealtimeChatBridge(wakeups ...func()) *realtimeChatBridge {
	bridge := &realtimeChatBridge{commands: make(chan realtimeChatCommand, 8)}
	if len(wakeups) > 0 {
		bridge.wakeup = wakeups[0]
	}
	return bridge
}

func (b *realtimeChatBridge) Handle(ctx context.Context, request agent.RealtimeChatRequest) (<-chan agent.RealtimeChatEvent, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	events := make(chan agent.RealtimeChatEvent, 16)
	b.mu.RLock()
	active, done := b.active, b.done
	b.mu.RUnlock()
	command := realtimeChatCommand{ctx: ctx, request: request, events: events}
	var sessionDone <-chan struct{}
	if active {
		sessionDone = done
	}
	select {
	case b.commands <- command:
		if !active && b.wakeup != nil {
			b.wakeup()
		}
		return events, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-sessionDone:
		return nil, fmt.Errorf("realtime voice session is unavailable")
	}
}

func (b *realtimeChatBridge) activate() {
	b.mu.Lock()
	b.active = true
	b.done = make(chan struct{})
	b.mu.Unlock()
}

func (b *realtimeChatBridge) deactivate() {
	b.mu.Lock()
	if b.active && b.done != nil {
		close(b.done)
	}
	b.active = false
	b.done = nil
	b.mu.Unlock()
}

func (b *realtimeChatBridge) failQueued(message string) {
	for {
		select {
		case command := <-b.commands:
			sendRealtimeChatEvent(command, agent.RealtimeChatEvent{Type: agent.RealtimeChatEventError, Error: message})
			close(command.events)
		default:
			return
		}
	}
}

func sendRealtimeChatEvent(command realtimeChatCommand, event agent.RealtimeChatEvent) bool {
	select {
	case command.events <- event:
		return true
	case <-command.ctx.Done():
		return false
	}
}

func chatBridgeCommands(bridge *realtimeChatBridge) <-chan realtimeChatCommand {
	if bridge == nil {
		return nil
	}
	return bridge.commands
}

func chatCommandDone(command *realtimeChatCommand) <-chan struct{} {
	if command == nil || command.ctx == nil {
		return nil
	}
	return command.ctx.Done()
}

func runRealtimeWakeupMode(cfg agent.Config, sigChan chan os.Signal, newWatcher wakeupWatcherFactory) {
	runRealtimeWakeupModeWithServer(cfg, sigChan, nil, nil, nil, newWatcher)
}

func runRealtimeWakeupModeWithServer(cfg agent.Config, sigChan chan os.Signal, server *agent.Server, runtime *agent.Runtime, tasks *agenttask.Manager, newWatcher wakeupWatcherFactory) {
	events := make(chan struct{}, 1)
	bridge := newRealtimeChatBridge(func() {
		signalWakeupEvent(events)
	})
	voiceNotificationTicker := time.NewTicker(realtimeNotificationPoll)
	defer voiceNotificationTicker.Stop()
	var notificationFallbackCancel context.CancelFunc
	var notificationFallbackDone chan struct{}
	stopNotificationFallback := func() {
		if notificationFallbackCancel != nil {
			notificationFallbackCancel()
		}
		if notificationFallbackDone != nil {
			<-notificationFallbackDone
		}
		notificationFallbackCancel = nil
		notificationFallbackDone = nil
	}
	defer stopNotificationFallback()
	startNotificationFallback := func() {
		if server == nil || runtime == nil || !server.CanSpeakVoiceNotification() || notificationFallbackDone != nil {
			return
		}
		fallbackCtx, cancel := context.WithCancel(context.Background())
		done := make(chan struct{})
		notificationFallbackCancel = cancel
		notificationFallbackDone = done
		go func() {
			defer close(done)
			deliverPendingVoiceNotification(fallbackCtx, runtime, server.SpeakVoiceNotification)
		}()
	}
	notificationFallbackFinished := func() <-chan struct{} {
		if notificationFallbackDone == nil {
			return nil
		}
		return notificationFallbackDone
	}
	if server != nil {
		server.SetRealtimeChatHandler(bridge.Handle)
		defer server.SetRealtimeChatHandler(nil)
	}
	log.Printf("\n[ready] Starting realtime GPIO wakeup listeners on %s...", wakeupGPIOPinsLabel())
	watchers, err := startWakeupWatchers(newWatcher, func() {
		signalWakeupEvent(events)
	})
	if err != nil {
		log.Printf("[realtime] GPIO wakeup unavailable: %v; /api/chat activation remains enabled", err)
	} else {
		defer stopWakeupWatchers(watchers)
	}

	log.Printf("[ready] Waiting for realtime activation (/api/chat or GPIO %s)... Ctrl+C to quit", wakeupGPIOPinsLabel())
	for {
		select {
		case <-sigChan:
			stopNotificationFallback()
			log.Println("\n[exit] Stopped.")
			return
		case <-voiceNotificationTicker.C:
			startNotificationFallback()
		case <-notificationFallbackFinished():
			notificationFallbackCancel = nil
			notificationFallbackDone = nil
		case <-events:
			// Realtime is the preferred consumer. Stop an idle standalone TTS
			// fallback before opening the Realtime audio session.
			stopNotificationFallback()
			log.Println("\n[realtime] Activation requested, connecting realtime voice model...")
			if err := runRealtimeSession(cfg, sigChan, runtime, tasks, bridge); errors.Is(err, errRealtimeShutdown) {
				return
			} else if err != nil {
				log.Printf("[realtime] session ended: %v", err)
				bridge.failQueued(err.Error())
			}
			drainRealtimeWakeups(events)
			// An API request can arrive while the previous session is tearing
			// down. Preserve that activation after clearing stale GPIO events.
			if chatBridgeHasPending(bridge) {
				signalWakeupEvent(events)
			}
			log.Println("[ready] Waiting for realtime activation...")
		}
	}
}

func drainRealtimeWakeups(events <-chan struct{}) {
	for {
		select {
		case <-events:
		default:
			return
		}
	}
}

func realtimeSessionConfig(cfg agent.Config) rtclient.SessionConfig {
	voice := cfg.VoiceModel.Voice
	if voice == "" {
		voice = rtclient.DefaultVoice
	}
	inputFormat := cfg.VoiceModel.InputAudioFormat
	if inputFormat == "" {
		inputFormat = "pcm"
	}
	outputFormat := cfg.VoiceModel.OutputAudioFormat
	if outputFormat == "" {
		outputFormat = "pcm"
	}
	turnType := cfg.VoiceModel.TurnDetection
	if turnType == "" {
		turnType = "server_vad"
	}
	turn := &rtclient.TurnDetection{Type: turnType, Threshold: cfg.VoiceModel.TurnDetectionThreshold}
	if cfg.VoiceModel.TurnDetectionSilenceMs > 0 {
		turn.SilenceDurationMS = cfg.VoiceModel.TurnDetectionSilenceMs
	}
	instructions := strings.TrimSpace(cfg.VoiceModel.Instructions)
	if instructions == "" {
		instructions = agent.DefaultRealtimeVoiceInstructions
	}
	enableEmotion := cfg.VoiceModel.EnableSpeechEmotion
	if enableEmotion == nil {
		v := true
		enableEmotion = &v
	}
	return rtclient.SessionConfig{
		Modalities:          []string{"audio", "text"},
		Voice:               voice,
		EnableSpeechEmotion: enableEmotion,
		Instructions:        instructions,
		InputAudioFormat:    inputFormat,
		OutputAudioFormat:   outputFormat,
		MaxHistoryTurns:     realtimeContextReplayTurns,
		Tools:               realtimeVoiceToolDefinitions(),
		TurnDetection:       turn,
	}
}

const (
	realtimeCurrentTimeTool        = "get_current_time"
	realtimeRecallTool             = "recall_memory"
	realtimeCreateTaskTool         = "create_agent_task"
	realtimeCancelTaskTool         = "cancel_agent_task"
	realtimeQueryTaskTool          = "query_agent_task"
	realtimeResponseUserActionTool = "response_user_action"
)

func realtimeVoiceToolDefinitions() []rtclient.Tool {
	return []rtclient.Tool{
		realtimeVoiceToolDefinition(
			realtimeCurrentTimeTool,
			"Get the current local date, time, timezone, and UTC offset.",
			map[string]any{"type": "object", "properties": map[string]any{}},
		),
		realtimeVoiceToolDefinition(
			realtimeRecallTool,
			"Recall saved long-term user preferences, rules, facts, procedures, or profile information.",
			map[string]any{
				"type": "object",
				"properties": map[string]any{
					"tags":     map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
					"entities": map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
					"types":    map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
					"limit":    map[string]any{"type": "integer", "minimum": 1},
				},
			},
		),
		realtimeVoiceToolDefinition(
			realtimeCreateTaskTool,
			"Handle any request you cannot directly and reliably answer or complete with the realtime conversation tools, including device state, visual inspection, external actions, lookups, or longer multi-step work. Present the work to the user as your own responsibility.",
			map[string]any{
				"type": "object",
				"properties": map[string]any{
					"task": map[string]any{"type": "string", "description": "A self-contained description of the work you will handle."},
				},
				"required": []string{"task"},
			},
		),
		realtimeVoiceToolDefinition(
			realtimeCancelTaskTool,
			"Cancel work that you previously started when the user asks you to stop it.",
			map[string]any{
				"type": "object",
				"properties": map[string]any{
					"task_id": map[string]any{"type": "string"},
				},
				"required": []string{"task_id"},
			},
		),
		realtimeVoiceToolDefinition(
			realtimeQueryTaskTool,
			"Check the current status and result of work you are handling.",
			map[string]any{
				"type": "object",
				"properties": map[string]any{
					"task_id": map[string]any{"type": "string"},
				},
				"required": []string{"task_id"},
			},
		),
		realtimeVoiceToolDefinition(
			realtimeResponseUserActionTool,
			"Continue work after the user completed a requested device action. Use the internal task reference and pass a concise description of what the user did; never expose the internal reference to the user.",
			map[string]any{"type": "object", "properties": map[string]any{"task_id": map[string]any{"type": "string"}, "user_message": map[string]any{"type": "string"}}, "required": []string{"task_id", "user_message"}},
		),
	}
}

func realtimeVoiceToolDefinition(name, description string, parameters map[string]any) rtclient.Tool {
	encoded, err := json.Marshal(parameters)
	if err != nil {
		encoded = json.RawMessage(`{"type":"object","properties":{}}`)
	}
	return rtclient.Tool{
		Type: "function",
		Function: rtclient.FunctionDefinition{
			Name:        name,
			Description: description,
			Parameters:  encoded,
		},
	}
}

type realtimeVoiceToolExecutor struct {
	recall langtools.Tool
	now    func() time.Time
	tasks  *agenttask.Manager
}

func newRealtimeVoiceToolExecutor(runtime *agent.Runtime, tasks *agenttask.Manager) realtimeVoiceToolExecutor {
	executor := realtimeVoiceToolExecutor{now: time.Now, tasks: tasks}
	if runtime != nil {
		executor.recall, _ = runtime.Tool(realtimeRecallTool)
	}
	return executor
}

func (e realtimeVoiceToolExecutor) call(ctx context.Context, name, arguments string) string {
	switch name {
	case realtimeCurrentTimeTool:
		now := e.now()
		zone, offsetSeconds := now.Zone()
		return realtimeToolJSON(map[string]any{
			"datetime":           now.Format(time.RFC3339),
			"timezone":           zone,
			"utc_offset_seconds": offsetSeconds,
		})
	case realtimeRecallTool:
		if e.recall == nil {
			return realtimeToolJSON(map[string]any{"error": "recall_memory is unavailable"})
		}
		output, err := e.recall.Call(ctx, arguments)
		if err != nil {
			return realtimeToolJSON(map[string]any{"error": err.Error()})
		}
		return output
	case realtimeCreateTaskTool:
		var input struct {
			Task string `json:"task"`
		}
		if err := json.Unmarshal([]byte(arguments), &input); err != nil {
			return realtimeToolJSON(map[string]any{"error": "invalid task arguments"})
		}
		if e.tasks == nil {
			return realtimeToolJSON(map[string]any{"error": "background agent is unavailable"})
		}
		task, err := e.tasks.Create(input.Task)
		if err != nil {
			return realtimeToolJSON(map[string]any{"error": err.Error()})
		}
		return realtimeToolJSON(task)
	case realtimeCancelTaskTool, realtimeQueryTaskTool:
		var input struct {
			TaskID string `json:"task_id"`
		}
		if err := json.Unmarshal([]byte(arguments), &input); err != nil {
			return realtimeToolJSON(map[string]any{"error": "invalid task arguments"})
		}
		if e.tasks == nil {
			return realtimeToolJSON(map[string]any{"error": "background agent is unavailable"})
		}
		if name == realtimeCancelTaskTool {
			task, err := e.tasks.Cancel(input.TaskID)
			if err != nil {
				return realtimeToolJSON(map[string]any{"error": err.Error()})
			}
			return realtimeToolJSON(task)
		}
		task, ok := e.tasks.Query(input.TaskID)
		if !ok {
			return realtimeToolJSON(map[string]any{"error": "agent task not found"})
		}
		return realtimeToolJSON(task)
	case realtimeResponseUserActionTool:
		var input struct {
			TaskID      string `json:"task_id"`
			UserMessage string `json:"user_message"`
		}
		if err := json.Unmarshal([]byte(arguments), &input); err != nil {
			return realtimeToolJSON(map[string]any{"error": "invalid task arguments"})
		}
		if e.tasks == nil {
			return realtimeToolJSON(map[string]any{"error": "background agent is unavailable"})
		}
		task, err := e.tasks.Continue(input.TaskID, input.UserMessage)
		if err != nil {
			return realtimeToolJSON(map[string]any{"error": err.Error()})
		}
		return realtimeToolJSON(task)
	default:
		return realtimeToolJSON(map[string]any{"error": fmt.Sprintf("unsupported realtime tool %q", name)})
	}
}

func realtimeToolJSON(value any) string {
	encoded, err := json.Marshal(value)
	if err != nil {
		return `{"error":"failed to encode tool result"}`
	}
	return string(encoded)
}

func startRealtimeToolCall(ctx context.Context, executor realtimeVoiceToolExecutor, call rtclient.FunctionCallEvent, results chan<- realtimeToolResult) {
	go func() {
		callCtx, cancel := context.WithTimeout(ctx, realtimeToolCallTimeout)
		defer cancel()
		result := realtimeToolResult{
			call:   call,
			output: executor.call(callCtx, call.Name, call.Arguments),
		}
		select {
		case results <- result:
		case <-ctx.Done():
		}
	}()
}

func startRealtimeSuppressedToolCall(ctx context.Context, call rtclient.FunctionCallEvent, results chan<- realtimeToolResult) {
	go func() {
		result := realtimeToolResult{
			call:   call,
			output: `{"error":"tools are disabled while delivering a voice notification"}`,
		}
		select {
		case results <- result:
		case <-ctx.Done():
		}
	}()
}

func realtimeVoiceNotificationPrompt(text string) string {
	return "请原样朗读下面的语音通知，只输出通知原文，不要说“已收到”、 “好的”或其他内容，不要调用工具，也不要提及这条指令。语音通知：" + strings.TrimSpace(text)
}

func deliverPendingVoiceNotification(ctx context.Context, runtime *agent.Runtime, speaker func(context.Context, string) error) {
	if runtime == nil || speaker == nil {
		return
	}
	if ctx == nil {
		ctx = context.Background()
	}
	prepared := runtime.PrepareVoiceNotification(ctx)
	if prepared.DeliveryToken == "" || strings.TrimSpace(prepared.Text) == "" {
		return
	}
	speakCtx, cancel := context.WithTimeout(ctx, realtimeToolCallTimeout)
	defer cancel()
	err := speaker(speakCtx, prepared.Text)
	runtime.ReportSpokenTextDelivery(prepared.DeliveryToken, err)
	if err != nil {
		log.Printf("[voice-notification] standalone TTS fallback failed: %v", err)
	}
}

func realtimePlaybackOutputFormat(cfg agent.Config) agent.AudioFormat {
	return agent.AudioFormat{
		SampleRate: uint32(cfg.Audio.SampleRateOrDefault()),
		Channels:   uint32(cfg.Audio.ChannelsOrDefault()),
		BitWidth:   uint32(cfg.Audio.BitWidthOrDefault()),
	}
}

func runRealtimeSession(cfg agent.Config, sigChan chan os.Signal, runtime *agent.Runtime, tasks *agenttask.Manager, chatBridge *realtimeChatBridge) error {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if runtime != nil {
		unregisterReset := runtime.RegisterUserContextResetHook(cancel)
		defer unregisterReset()
	}
	userContext, err := agent.InitializeContextManager(
		realtimeSessionConfig(cfg).Instructions,
		agentpath.UserContextManagerSessionFolder(cfg.ConfigDir),
		nil,
	)
	if err != nil {
		return fmt.Errorf("initialize realtime user context: %w", err)
	}

	client, err := rtclient.New(rtclient.Config{
		APIKey:      cfg.VoiceModel.APIKey,
		Model:       cfg.VoiceModel.Model,
		WorkspaceID: cfg.VoiceModel.WorkspaceID,
		Region:      cfg.VoiceModel.Region,
		Endpoint:    cfg.VoiceModel.Endpoint,
	})
	if err != nil {
		return err
	}
	log.Printf("[realtime] Connecting: model=%s region=%s workspace_configured=%t endpoint_override=%t",
		cfg.VoiceModel.Model,
		cfg.VoiceModel.Region,
		cfg.VoiceModel.WorkspaceID != "",
		cfg.VoiceModel.Endpoint != "",
	)
	connectCtx, connectCancel := context.WithTimeout(ctx, realtimeConnectTimeout)
	session, err := client.Connect(connectCtx)
	connectCancel()
	if err != nil {
		return fmt.Errorf("connect realtime voice model: %w", err)
	}
	defer session.Close()
	log.Println("[realtime] WebSocket connected, waiting for session.created...")
	if err := waitForRealtimeEvent(ctx, session, sigChan, "session.created", realtimeEventTimeout); err != nil {
		return fmt.Errorf("initialize realtime session: %w", err)
	}
	log.Println("[realtime] session.created received, sending session.update...")
	updateCtx, updateCancel := context.WithTimeout(ctx, realtimeUpdateTimeout)
	err = session.Update(updateCtx, realtimeSessionConfig(cfg))
	updateCancel()
	if err != nil {
		return fmt.Errorf("update realtime session: %w", err)
	}
	if err := waitForRealtimeEvent(ctx, session, sigChan, "session.updated", realtimeEventTimeout); err != nil {
		return fmt.Errorf("apply realtime session config: %w", err)
	}
	if err := replayRealtimeContext(ctx, session, userContext); err != nil {
		return fmt.Errorf("restore realtime user context: %w", err)
	}
	log.Println("[realtime] session.updated received, opening microphone...")
	if chatBridge != nil {
		chatBridge.activate()
		defer func() {
			chatBridge.deactivate()
			chatBridge.failQueued("realtime voice session ended")
		}()
	}

	audioService := agent.NewAudioServiceClient(cfg.Audio.SocketOrDefault())
	recordingAudio := agent.NewConfiguredAudioRecordingBackend(cfg, audioService, nil)
	playbackAudio := realtimePlaybackAudioAdapter{
		backend: agent.NewConfiguredAudioPlaybackBackend(cfg, audioService, nil),
	}
	inputFormat := agent.AudioFormat{
		// Qwen realtime currently accepts only 16 kHz, 16-bit mono PCM.
		SampleRate: 16000,
		Channels:   1,
		BitWidth:   16,
	}
	recording, err := recordingAudio.StartRecording(inputFormat)
	if err != nil {
		return fmt.Errorf("start realtime recording: %w", err)
	}
	defer func() { _ = recordingAudio.StopRecording(recording.SessionID) }()
	log.Printf("[realtime] Microphone opened: session_id=%d format=pcm/16000/mono/16", recording.SessionID)

	chunks := make(chan []byte, 8)
	readErrs := make(chan error, 1)
	go streamRealtimeAudio(ctx, recordingAudio, recording.SessionID, chunks, readErrs)

	// Qwen realtime emits 24 kHz PCM16 mono, while the device playback
	// hardware is commonly configured for audio.sample_rate (16 kHz on the
	// target board). Resample before opening audio_service playback; passing
	// 24 kHz directly makes tinyalsa reject hw_params with EINVAL.
	realtimeOutputFormat := agent.AudioFormat{SampleRate: 24000, Channels: 1, BitWidth: 16}
	outputFormat := realtimePlaybackOutputFormat(cfg)
	if outputFormat.SampleRate == 0 {
		outputFormat = realtimeOutputFormat
	}
	var outputResampler *tts.PCM16MonoResampler
	if realtimeOutputFormat.Channels == outputFormat.Channels &&
		realtimeOutputFormat.BitWidth == outputFormat.BitWidth &&
		realtimeOutputFormat.SampleRate != outputFormat.SampleRate {
		outputResampler = tts.NewPCM16MonoResampler(int(realtimeOutputFormat.SampleRate), int(outputFormat.SampleRate))
	}
	log.Printf("[realtime] Playback format: source=pcm/%d/%d/%d target=pcm/%d/%d/%d resample=%t",
		realtimeOutputFormat.SampleRate,
		realtimeOutputFormat.Channels,
		realtimeOutputFormat.BitWidth,
		outputFormat.SampleRate,
		outputFormat.Channels,
		outputFormat.BitWidth,
		outputResampler != nil,
	)
	playback := realtimePlaybackState{finalizeResponses: cfg.AudioBackendOrDefault() == agent.AudioBackendLocal}
	if err := playback.open(playbackAudio, outputFormat); err != nil {
		return fmt.Errorf("open realtime playback: %w", err)
	}
	log.Printf("[realtime] Speaker opened: session_id=%d", playback.session.SessionID)
	defer func() { _ = playback.stop(playbackAudio) }()
	playbackKeepAlive := time.NewTicker(realtimePlaybackKeepAlive)
	defer playbackKeepAlive.Stop()
	voiceNotificationTicker := time.NewTicker(realtimeNotificationPoll)
	defer voiceNotificationTicker.Stop()
	firstAudioChunk := true
	var activeChat *realtimeChatCommand
	var chatMode string
	var chatText strings.Builder
	var chatTranscript strings.Builder
	var responseText strings.Builder
	var responseTranscript strings.Builder
	var realtimeResponseUsage messages.Usage
	hasRealtimeResponseUsage := false
	responseActive := false
	inputSpeechActive := false
	responseSuppressed := false
	activeNotificationToken := ""
	activeNotificationResponseID := ""
	activeNotificationAudioWritten := false
	// A notification can be interrupted before response.created supplies its
	// response ID. Keep a marker so the eventual response is still suppressed.
	suppressedNotificationResponsePending := false
	suppressedNotificationResponseIDs := make(map[string]struct{})
	var notificationDrainDone <-chan error
	var notificationDrainCancel context.CancelFunc
	defer func() {
		if notificationDrainCancel != nil {
			notificationDrainCancel()
		}
	}()
	defer func() {
		// A transport/session failure can happen before response.done. Return the
		// lease so the outer TTS fallback can retry the notification.
		if activeNotificationToken != "" && runtime != nil {
			runtime.ReportSpokenTextDelivery(activeNotificationToken, context.Canceled)
		}
	}()
	toolExecutor := newRealtimeVoiceToolExecutor(runtime, tasks)
	toolResults := make(chan realtimeToolResult, 16)
	toolTracker := newRealtimeToolTracker()
	var pendingTaskUpdates []agenttask.Task
	var taskDebounceTimer *time.Timer
	var taskDebounce <-chan time.Time
	taskUpdatesReady := false
	defer func() {
		if taskDebounceTimer != nil {
			taskDebounceTimer.Stop()
		}
		if tasks != nil && len(pendingTaskUpdates) > 0 {
			var terminal, actions []agenttask.Task
			for _, task := range pendingTaskUpdates {
				if task.PendingUserAction != nil {
					actions = append(actions, task)
				} else {
					terminal = append(terminal, task)
				}
			}
			tasks.RestoreTerminalTasks(terminal)
			tasks.RestoreUserActionTasks(actions)
		}
		if tasks != nil {
			tasks.RestoreUserActionTasks(tasks.PendingUserActionTasks())
		}
	}()
	tryInjectTaskUpdates := func() error {
		if !taskUpdatesReady || len(pendingTaskUpdates) == 0 || responseActive || inputSpeechActive || activeNotificationToken != "" || notificationDrainDone != nil || activeChat != nil || chatBridgeHasPending(chatBridge) {
			return nil
		}
		message := formatRealtimeTaskUpdates(pendingTaskUpdates)
		if err := appendRealtimeNoticeMessage(userContext, message); err != nil {
			return fmt.Errorf("persist task update in realtime context: %w", err)
		}
		if err := session.SendText(ctx, message, ""); err != nil {
			return fmt.Errorf("inject background task update: %w", err)
		}
		if err := session.CreateResponse(ctx, nil); err != nil {
			return fmt.Errorf("respond to background task update: %w", err)
		}
		pendingTaskUpdates = nil
		taskUpdatesReady = false
		responseActive = true
		return nil
	}
	tryInjectVoiceNotification := func() error {
		if runtime == nil || activeNotificationToken != "" || notificationDrainDone != nil || responseActive || inputSpeechActive || activeChat != nil || chatBridgeHasPending(chatBridge) {
			return nil
		}
		prepared := runtime.PrepareVoiceNotification(ctx)
		if prepared.DeliveryToken == "" || strings.TrimSpace(prepared.Text) == "" {
			return nil
		}
		item := rtclient.ConversationItem{
			Type: "message",
			// Realtime treats a response as a reaction to the latest user-facing
			// item. A system item is accepted by the protocol but Qwen may only
			// acknowledge it (for example, with “已收到”) instead of synthesizing
			// the requested notification audio.
			Role:    "user",
			Content: []rtclient.ContentPart{{Type: "input_text", Text: realtimeVoiceNotificationPrompt(prepared.Text)}},
		}
		if err := session.CreateItem(ctx, item, ""); err != nil {
			runtime.ReportSpokenTextDelivery(prepared.DeliveryToken, err)
			return fmt.Errorf("inject realtime voice notification: %w", err)
		}
		if err := session.CreateResponse(ctx, nil); err != nil {
			runtime.ReportSpokenTextDelivery(prepared.DeliveryToken, err)
			return fmt.Errorf("respond to realtime voice notification: %w", err)
		}
		activeNotificationToken = prepared.DeliveryToken
		activeNotificationResponseID = ""
		activeNotificationAudioWritten = false
		responseActive = true
		return nil
	}
	defer func() {
		if activeChat != nil {
			sendRealtimeChatEvent(*activeChat, agent.RealtimeChatEvent{Type: agent.RealtimeChatEventError, Error: "realtime voice session ended"})
			close(activeChat.events)
		}
	}()

	for {
		select {
		case <-ctx.Done():
			return nil
		case <-sigChan:
			return errRealtimeShutdown
		case <-session.Done():
			return nil
		case err := <-session.Errors():
			if err != nil {
				return err
			}
		case err := <-readErrs:
			if err != nil {
				return err
			}
		case <-voiceNotificationTicker.C:
			if err := tryInjectVoiceNotification(); err != nil {
				return err
			}
		case result := <-toolResults:
			call := result.call
			if err := session.SendFunctionOutput(ctx, call.CallID, result.output); err != nil {
				return fmt.Errorf("send realtime tool result: %w", err)
			}
			if activeNotificationToken == "" && !hasSuppressedNotificationResponse(suppressedNotificationResponseIDs, call.ResponseID) {
				if err := appendRealtimeToolExecution(userContext, call, result.output); err != nil {
					return fmt.Errorf("persist realtime tool call and result: %w", err)
				}
			}
			if toolTracker.complete(call.ResponseID) {
				responseActive = true
				if err := session.CreateResponse(ctx, nil); err != nil {
					return fmt.Errorf("continue realtime response after tool call: %w", err)
				}
			}
		case pcm, ok := <-chunks:
			if !ok {
				return nil
			}
			if err := session.AppendAudio(ctx, pcm); err != nil {
				return err
			}
			if firstAudioChunk {
				firstAudioChunk = false
				log.Printf("[realtime] Audio streaming started: first_chunk_bytes=%d", len(pcm))
			}
		case <-playbackKeepAlive.C:
			// audio_service reaps sessions after 30 seconds without a write.
			// A zero-length non-final write keeps the speaker hardware open
			// without adding audible silence to the playback queue.
			if err := playback.keepAlive(playbackAudio, outputFormat); err != nil {
				return fmt.Errorf("keep realtime playback alive: %w", err)
			}
		case err := <-notificationDrainDone:
			notificationDrainDone = nil
			if notificationDrainCancel != nil {
				notificationDrainCancel()
				notificationDrainCancel = nil
			}
			if activeNotificationToken == "" {
				continue
			}
			if err != nil {
				runtime.ReportSpokenTextDelivery(activeNotificationToken, err)
				if stopErr := playback.stop(playbackAudio); stopErr != nil {
					return fmt.Errorf("stop failed realtime notification playback: %w", stopErr)
				}
			} else {
				runtime.ReportSpokenTextDelivery(activeNotificationToken, nil)
				playback.markDrained()
			}
			activeNotificationToken = ""
			activeNotificationResponseID = ""
			activeNotificationAudioWritten = false
			responseActive = false
			if err := tryInjectVoiceNotification(); err != nil {
				return err
			}
			if err := tryInjectTaskUpdates(); err != nil {
				return err
			}
		case <-agentTaskNotifications(tasks):
			pendingTaskUpdates = append(pendingTaskUpdates, tasks.DrainTerminalTasks()...)
			taskUpdatesReady = false
			if taskDebounceTimer == nil {
				taskDebounceTimer = time.NewTimer(realtimeTaskResultDebounce)
			} else {
				if !taskDebounceTimer.Stop() {
					select {
					case <-taskDebounceTimer.C:
					default:
					}
				}
				taskDebounceTimer.Reset(realtimeTaskResultDebounce)
			}
			taskDebounce = taskDebounceTimer.C
		case <-agentTaskUserActionNotifications(tasks):
			for _, task := range tasks.DrainUserActionTasks() {
				pendingTaskUpdates = append(pendingTaskUpdates, task)
			}
			// User-action requests should be announced as soon as the
			// foreground is idle. The normal guards in tryInjectTaskUpdates
			// defer delivery while speech or a response is active.
			taskUpdatesReady = true
			if err := tryInjectTaskUpdates(); err != nil {
				return err
			}
		case <-taskDebounce:
			taskDebounce = nil
			taskUpdatesReady = true
			if err := tryInjectTaskUpdates(); err != nil {
				return err
			}
		case command := <-chatBridgeCommands(chatBridge):
			if activeChat != nil || responseActive || inputSpeechActive || activeNotificationToken != "" || notificationDrainDone != nil {
				sendRealtimeChatEvent(command, agent.RealtimeChatEvent{Type: agent.RealtimeChatEventError, Error: "realtime response is busy"})
				close(command.events)
				continue
			}
			activeChat = &command
			chatMode = ""
			chatText.Reset()
			chatTranscript.Reset()
			if err := session.SendText(command.ctx, command.request.Message, ""); err != nil {
				sendRealtimeChatEvent(command, agent.RealtimeChatEvent{Type: agent.RealtimeChatEventError, Error: err.Error()})
				close(command.events)
				activeChat = nil
				continue
			}
			if err := appendRealtimeUserMessage(userContext, command.request.Message); err != nil {
				sendRealtimeChatEvent(command, agent.RealtimeChatEvent{Type: agent.RealtimeChatEventError, Error: err.Error()})
				close(command.events)
				activeChat = nil
				continue
			}
			responseActive = true
			if err := session.CreateResponse(command.ctx, nil); err != nil {
				responseActive = false
				sendRealtimeChatEvent(command, agent.RealtimeChatEvent{Type: agent.RealtimeChatEventError, Error: err.Error()})
				close(command.events)
				activeChat = nil
			}
		case <-chatCommandDone(activeChat):
			_ = session.CancelResponse(ctx)
			sendRealtimeChatEvent(*activeChat, agent.RealtimeChatEvent{Type: agent.RealtimeChatEventError, Error: "request canceled"})
			close(activeChat.events)
			activeChat = nil
		case event, ok := <-session.Events():
			if !ok {
				return nil
			}
			switch event.Type {
			case "input_audio_buffer.speech_started":
				inputSpeechActive = true
				if activeNotificationToken != "" {
					if notificationDrainDone == nil {
						markSuppressedRealtimeNotificationResponse(activeNotificationResponseID, &suppressedNotificationResponsePending, suppressedNotificationResponseIDs)
						responseSuppressed = activeNotificationResponseID != ""
					}
					if runtime != nil {
						runtime.ReportSpokenTextDelivery(activeNotificationToken, context.Canceled)
					}
					if notificationDrainCancel != nil {
						notificationDrainCancel()
						notificationDrainCancel = nil
						notificationDrainDone = nil
						responseActive = false
					}
					activeNotificationToken = ""
					activeNotificationResponseID = ""
					activeNotificationAudioWritten = false
				}
				log.Println("[realtime] Speech started, interrupting local playback")
				// Clear the queued response immediately, then reopen the speaker
				// session so mic and speaker remain active for the next turn.
				if err := playback.interrupt(playbackAudio, outputFormat); err != nil {
					return fmt.Errorf("interrupt realtime playback: %w", err)
				}
			case "input_audio_buffer.speech_stopped", "input_audio_buffer.committed":
				inputSpeechActive = false
				if err := tryInjectTaskUpdates(); err != nil {
					return err
				}
			case "conversation.item.input_audio_transcription.completed":
				var transcript rtclient.TranscriptEvent
				if err := event.Decode(&transcript); err != nil {
					return err
				}
				if err := appendRealtimeUserMessage(userContext, transcript.Transcript); err != nil {
					return fmt.Errorf("persist realtime user transcript: %w", err)
				}
			case "response.created":
				var created rtclient.ResponseEvent
				if err := event.Decode(&created); err != nil {
					return err
				}
				log.Println("[realtime] Response created")
				responseActive = true
				responseSuppressed = false
				if bindSuppressedRealtimeNotificationResponse(created.Response.ID, &suppressedNotificationResponsePending, suppressedNotificationResponseIDs) {
					responseSuppressed = true
				} else if activeNotificationToken != "" && activeNotificationResponseID == "" {
					activeNotificationResponseID = created.Response.ID
				}
				responseText.Reset()
				responseTranscript.Reset()
				if realtimeOutputFormat.SampleRate != outputFormat.SampleRate &&
					realtimeOutputFormat.Channels == outputFormat.Channels &&
					realtimeOutputFormat.BitWidth == outputFormat.BitWidth {
					outputResampler = tts.NewPCM16MonoResampler(int(realtimeOutputFormat.SampleRate), int(outputFormat.SampleRate))
				}
				// Ignore any late deltas from the interrupted response until the
				// server explicitly starts the next one. The speaker session stays
				// open across responses.
				if err := playback.beginResponse(playbackAudio, outputFormat); err != nil {
					return fmt.Errorf("begin realtime playback response: %w", err)
				}
				if responseSuppressed {
					playback.suppressDeltas = true
				}
			case "response.text.delta":
				var delta rtclient.ResponseDeltaEvent
				if err := event.Decode(&delta); err != nil {
					return err
				}
				responseText.WriteString(delta.Delta)
				if activeChat != nil {
					if chatMode == "" {
						chatMode = "text"
					}
					if chatMode == "text" && delta.Delta != "" {
						chatText.WriteString(delta.Delta)
						sendRealtimeChatEvent(*activeChat, agent.RealtimeChatEvent{Type: agent.RealtimeChatEventDelta, Delta: delta.Delta})
					}
				}
			case "response.audio_transcript.delta":
				var delta rtclient.ResponseDeltaEvent
				if err := event.Decode(&delta); err != nil {
					return err
				}
				responseTranscript.WriteString(delta.Delta)
				if activeChat != nil {
					if chatMode == "" {
						chatMode = "transcript"
					}
					if chatMode == "transcript" && delta.Delta != "" {
						chatTranscript.WriteString(delta.Delta)
						sendRealtimeChatEvent(*activeChat, agent.RealtimeChatEvent{Type: agent.RealtimeChatEventDelta, Delta: delta.Delta})
					}
				}
			case "response.audio.delta":
				pcm, err := event.AudioDelta()
				if err != nil {
					return err
				}
				if outputResampler != nil {
					pcm = outputResampler.Write(pcm)
				}
				if responseSuppressed || suppressedNotificationResponsePending {
					continue
				}
				if err := playback.append(playbackAudio, outputFormat, pcm); err != nil {
					return err
				}
				if activeNotificationToken != "" && len(pcm) > 0 {
					activeNotificationAudioWritten = true
				}
			case "response.audio_transcript.done":
				var done rtclient.TranscriptEvent
				if err := event.Decode(&done); err == nil && responseTranscript.Len() == 0 {
					responseTranscript.WriteString(done.Transcript)
				}
				if activeChat != nil && chatTranscript.Len() == 0 {
					if err := event.Decode(&done); err == nil && done.Transcript != "" {
						chatTranscript.WriteString(done.Transcript)
					}
				}
			case "response.function_call_arguments.done":
				var call rtclient.FunctionCallEvent
				if err := event.Decode(&call); err != nil {
					return err
				}
				log.Printf("[realtime] Tool call: %s", call.Name)
				toolTracker.start(call.ResponseID)
				if activeNotificationToken != "" || suppressedNotificationResponsePending || hasSuppressedNotificationResponse(suppressedNotificationResponseIDs, call.ResponseID) {
					startRealtimeSuppressedToolCall(ctx, call, toolResults)
				} else {
					startRealtimeToolCall(ctx, toolExecutor, call, toolResults)
				}
			case "response.done":
				var done rtclient.ResponseEvent
				if err := event.Decode(&done); err != nil {
					return err
				}
				if done.Response.Usage != nil {
					realtimeResponseUsage.TotalTokens += done.Response.Usage.TotalTokens
					realtimeResponseUsage.InputTokens += done.Response.Usage.InputTokens
					realtimeResponseUsage.OutputTokens += done.Response.Usage.OutputTokens
					hasRealtimeResponseUsage = true
				}
				if suppressedNotificationResponsePending && done.Response.ID != "" {
					bindSuppressedRealtimeNotificationResponse(done.Response.ID, &suppressedNotificationResponsePending, suppressedNotificationResponseIDs)
				}
				_, suppressedResponse := suppressedNotificationResponseIDs[done.Response.ID]
				notificationResponse := activeNotificationToken != "" &&
					(activeNotificationResponseID == "" || activeNotificationResponseID == done.Response.ID)
				if suppressedResponse {
					delete(suppressedNotificationResponseIDs, done.Response.ID)
					toolTracker.clear(done.Response.ID)
				}
				if !suppressedResponse && !notificationResponse {
					if hasTools, continueNow := toolTracker.done(done.Response.ID); hasTools {
						if !continueNow {
							continue
						}
						responseActive = true
						if err := session.CreateResponse(ctx, nil); err != nil {
							return fmt.Errorf("continue realtime response after tool call: %w", err)
						}
						continue
					}
				}
				if notificationResponse {
					toolTracker.clear(done.Response.ID)
				}
				if !suppressedResponse {
					if err := playback.finishResponse(playbackAudio, false); err != nil {
						return fmt.Errorf("finish realtime playback response: %w", err)
					}
				}
				if !notificationResponse && !suppressedResponse && (done.Response.Status == "" || done.Response.Status == "completed") {
					assistantText := strings.TrimSpace(responseText.String())
					if assistantText == "" {
						assistantText = strings.TrimSpace(responseTranscript.String())
					}
					if assistantText != "" {
						var usage *messages.Usage
						if hasRealtimeResponseUsage {
							usageCopy := realtimeResponseUsage
							usage = &usageCopy
						}
						if err := appendRealtimeAssistantMessage(userContext, assistantText, usage); err != nil {
							return fmt.Errorf("persist realtime assistant response: %w", err)
						}
					}
				}
				realtimeResponseUsage = messages.Usage{}
				hasRealtimeResponseUsage = false
				responseActive = false
				if activeChat != nil {
					content := chatText.String()
					if content == "" {
						content = chatTranscript.String()
					}
					sendRealtimeChatEvent(*activeChat, agent.RealtimeChatEvent{Type: agent.RealtimeChatEventDone, Response: content})
					close(activeChat.events)
					activeChat = nil
				}
				if notificationResponse {
					var deliveryErr error
					if done.Response.Status != "" && done.Response.Status != "completed" {
						deliveryErr = fmt.Errorf("realtime notification response ended with status %q", done.Response.Status)
					} else if !activeNotificationAudioWritten {
						deliveryErr = errors.New("realtime notification response produced no audio")
					}
					if deliveryErr == nil && activeNotificationAudioWritten {
						if err := playback.finishResponse(playbackAudio, true); err != nil {
							deliveryErr = fmt.Errorf("finish realtime notification playback: %w", err)
						}
					}
					if deliveryErr != nil || !activeNotificationAudioWritten {
						runtime.ReportSpokenTextDelivery(activeNotificationToken, deliveryErr)
						if stopErr := playback.stop(playbackAudio); stopErr != nil {
							return fmt.Errorf("stop failed realtime notification playback: %w", stopErr)
						}
						activeNotificationToken = ""
						activeNotificationResponseID = ""
						activeNotificationAudioWritten = false
					} else {
						drainCtx, cancel := context.WithTimeout(ctx, realtimeNotificationDrain)
						notificationDrainCancel = cancel
						doneCh := make(chan error, 1)
						notificationDrainDone = doneCh
						go func() {
							doneCh <- playbackAudio.WaitForPlaybackDrain(drainCtx)
						}()
					}
				}
				responseSuppressed = false
				if err := tryInjectVoiceNotification(); err != nil {
					return err
				}
				if err := tryInjectTaskUpdates(); err != nil {
					return err
				}
			case "error":
				var serverErr rtclient.ErrorEvent
				if err := event.Decode(&serverErr); err != nil {
					return err
				}
				return &realtimeServerError{message: serverErr.Error.Message}
			}
		}
	}
}

func agentTaskNotifications(tasks *agenttask.Manager) <-chan struct{} {
	if tasks == nil {
		return nil
	}
	return tasks.TerminalNotifications()
}

func agentTaskUserActionNotifications(tasks *agenttask.Manager) <-chan struct{} {
	if tasks == nil {
		return nil
	}
	return tasks.UserActionNotifications()
}

func chatBridgeHasPending(bridge *realtimeChatBridge) bool {
	return bridge != nil && len(bridge.commands) > 0
}

func hasSuppressedNotificationResponse(responses map[string]struct{}, responseID string) bool {
	if responseID == "" {
		return false
	}
	_, ok := responses[responseID]
	return ok
}

func markSuppressedRealtimeNotificationResponse(responseID string, pending *bool, responses map[string]struct{}) {
	if responseID != "" {
		responses[responseID] = struct{}{}
		return
	}
	if pending != nil {
		*pending = true
	}
}

func bindSuppressedRealtimeNotificationResponse(responseID string, pending *bool, responses map[string]struct{}) bool {
	if pending != nil && *pending && responseID != "" {
		responses[responseID] = struct{}{}
		*pending = false
		return true
	}
	return hasSuppressedNotificationResponse(responses, responseID)
}

func formatRealtimeTaskUpdates(tasks []agenttask.Task) string {
	var output strings.Builder
	output.WriteString("Private work-status update for the assistant. Present these outcomes naturally as your own work; never mention agents, background execution, queues, orchestration, tools, or task IDs. For pending_user_action, explain what the user must do on the device, ask them to say when it is complete, and then continue using the internal response action:\n")
	for _, task := range tasks {
		fmt.Fprintf(&output, "- task_id=%s status=%s task=%q", task.ID, task.Status, limitRealtimeTaskField(task.Prompt, 300))
		if task.Result != "" {
			fmt.Fprintf(&output, " result=%q", limitRealtimeTaskField(task.Result, 2000))
		}
		if task.Error != "" {
			fmt.Fprintf(&output, " error=%q", limitRealtimeTaskField(task.Error, 500))
		}
		if task.PendingUserAction != nil {
			fmt.Fprintf(&output, " pending_user_action={reason:%q details:%q suggested_action:%q}",
				task.PendingUserAction.Reason,
				task.PendingUserAction.Details,
				task.PendingUserAction.SuggestedAction)
		}
		output.WriteByte('\n')
	}
	return strings.TrimSpace(output.String())
}

func appendRealtimeUserMessage(manager *contextmanager.ContextManager, content string) error {
	content = strings.TrimSpace(content)
	if manager == nil || content == "" {
		return nil
	}
	return manager.AppendMessage(messages.Message{Role: messages.MessageRoleUser, Content: content})
}

func appendRealtimeNoticeMessage(manager *contextmanager.ContextManager, content string) error {
	content = strings.TrimSpace(content)
	if manager == nil || content == "" {
		return nil
	}
	return manager.AppendMessage(messages.Message{Role: messages.MessageRoleNotice, Content: content})
}

func appendRealtimeAssistantMessage(manager *contextmanager.ContextManager, content string, usage *messages.Usage) error {
	content = strings.TrimSpace(content)
	if manager == nil || content == "" {
		return nil
	}
	return manager.AppendMessage(messages.Message{Role: messages.MessageRoleAssistant, Content: content, Usage: usage})
}

// appendRealtimeToolExecution persists the protocol pair only after the tool
// has produced an output, so an interrupted call cannot remain in user context.
func appendRealtimeToolExecution(manager *contextmanager.ContextManager, call rtclient.FunctionCallEvent, output string) error {
	if manager == nil {
		return nil
	}
	return manager.AppendMessages([]messages.Message{
		{
			Role: messages.MessageRoleToolCall,
			ToolCalls: []messages.ToolCall{{
				ID:        strings.TrimSpace(call.CallID),
				Name:      strings.TrimSpace(call.Name),
				Arguments: strings.TrimSpace(call.Arguments),
			}},
		},
		{
			Role: messages.MessageRoleToolResult,
			ToolResults: []messages.ToolResult{{
				ToolCallID: strings.TrimSpace(call.CallID),
				Name:       strings.TrimSpace(call.Name),
				Content:    output,
			}},
		},
	})
}

func replayRealtimeContext(ctx context.Context, session *rtclient.Session, manager *contextmanager.ContextManager) error {
	if session == nil || manager == nil {
		return nil
	}
	var previousID string
	for _, message := range recentRealtimeContextMessages(manager.MessageListDump().Messages, realtimeContextReplayTurns) {
		content := strings.TrimSpace(message.Content)
		if message.Role == messages.MessageRoleSystem {
			continue
		}
		if message.Role == messages.MessageRoleToolCall {
			for _, call := range message.ToolCalls {
				if err := session.CreateItem(ctx, rtclient.ConversationItem{
					Type:      "function_call",
					CallID:    call.ID,
					Name:      call.Name,
					Arguments: call.Arguments,
				}, previousID); err != nil {
					return err
				}
			}
			continue
		}
		if message.Role == messages.MessageRoleToolResult {
			for _, result := range message.ToolResults {
				if err := session.CreateItem(ctx, rtclient.ConversationItem{
					Type:   "function_call_output",
					CallID: result.ToolCallID,
					Output: result.Content,
				}, previousID); err != nil {
					return err
				}
			}
			continue
		}
		if content == "" {
			continue
		}
		role := "user"
		contentType := "input_text"
		if message.Role == messages.MessageRoleAssistant {
			role = "assistant"
			contentType = "output_text"
		}
		item := rtclient.ConversationItem{
			Type: "message",
			Role: role,
			Content: []rtclient.ContentPart{{
				Type: contentType,
				Text: content,
			}},
		}
		if err := session.CreateItem(ctx, item, previousID); err != nil {
			return err
		}
	}
	return nil
}

func recentRealtimeContextMessages(all []messages.Message, maxTurns int) []messages.Message {
	// A turn starts at a user message and owns every following assistant,
	// tool-call, and tool-result message until the next user message. Slice only
	// at these boundaries so a tool call can never be replayed without its
	// matching result (or vice versa).
	if len(all) == 0 || maxTurns <= 0 {
		return nil
	}
	userCount := 0
	for _, message := range all {
		if message.Role == messages.MessageRoleUser {
			userCount++
		}
	}
	if userCount <= maxTurns {
		return all
	}
	cutoff := userCount - maxTurns
	seen := 0
	start := len(all)
	for i, message := range all {
		if message.Role != messages.MessageRoleUser {
			continue
		}
		if seen == cutoff {
			start = i
			break
		}
		seen++
	}
	return all[start:]
}

func limitRealtimeTaskField(value string, maxRunes int) string {
	runes := []rune(strings.TrimSpace(value))
	if len(runes) <= maxRunes {
		return string(runes)
	}
	return string(runes[:maxRunes]) + "…"
}

type realtimeEventSource interface {
	Events() <-chan rtclient.Event
	Errors() <-chan error
	Done() <-chan struct{}
}

func waitForRealtimeEvent(ctx context.Context, session realtimeEventSource, sigChan <-chan os.Signal, want string, timeout time.Duration) error {
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-sigChan:
			return errRealtimeShutdown
		case <-timer.C:
			return fmt.Errorf("timed out after %s waiting for %s", timeout, want)
		case <-session.Done():
			return errors.New("WebSocket closed")
		case err, ok := <-session.Errors():
			if ok && err != nil {
				return err
			}
		case event, ok := <-session.Events():
			if !ok {
				return errors.New("server event stream closed")
			}
			if event.Type == "error" {
				var serverErr rtclient.ErrorEvent
				if err := event.Decode(&serverErr); err != nil {
					return err
				}
				return &realtimeServerError{message: serverErr.Error.Message}
			}
			if event.Type == want {
				return nil
			}
		}
	}
}

type realtimePlaybackAudio interface {
	StartPlayback(agent.AudioFormat) (*agent.PlaybackStartResult, error)
	WritePlayChunk(uint64, []byte, bool) error
	StopPlayback(uint64) error
}

type realtimePlaybackAudioAdapter struct {
	backend tts.AudioServiceBackend
}

type realtimePlaybackDrainWaiter interface {
	WaitForPlaybackDrain(context.Context) error
}

func (a realtimePlaybackAudioAdapter) StartPlayback(format agent.AudioFormat) (*agent.PlaybackStartResult, error) {
	if a.backend == nil {
		return nil, errors.New("realtime playback backend is unavailable")
	}
	sessionID, err := a.backend.StartPlayback(tts.AudioFormat{
		SampleRate: int(format.SampleRate),
		Channels:   int(format.Channels),
		BitWidth:   int(format.BitWidth),
	})
	if err != nil {
		return nil, err
	}
	return &agent.PlaybackStartResult{SessionID: sessionID}, nil
}

func (a realtimePlaybackAudioAdapter) WritePlayChunk(sessionID uint64, data []byte, final bool) error {
	return a.backend.WritePlayChunk(sessionID, data, final)
}

func (a realtimePlaybackAudioAdapter) StopPlayback(sessionID uint64) error {
	err := a.backend.StopPlayback(sessionID)
	if err == nil {
		return nil
	}
	message := strings.ToLower(err.Error())
	if strings.Contains(message, "session_not_found") ||
		strings.Contains(message, "session not found") {
		return nil
	}
	return err
}

func (a realtimePlaybackAudioAdapter) WaitForPlaybackDrain(ctx context.Context) error {
	if waiter, ok := a.backend.(realtimePlaybackDrainWaiter); ok {
		return waiter.WaitForPlaybackDrain(ctx)
	}
	return errors.New("realtime playback backend cannot confirm drain")
}

type realtimePlaybackState struct {
	session        *agent.PlaybackStartResult
	suppressDeltas bool
	finalized      bool
	// Host players consume a finalized WAV response, while audio_service keeps
	// one low-latency PCM session open across responses.
	finalizeResponses bool
}

func (p *realtimePlaybackState) open(audio realtimePlaybackAudio, format agent.AudioFormat) error {
	if p.session != nil {
		return nil
	}
	session, err := audio.StartPlayback(format)
	if err != nil {
		return err
	}
	p.session = session
	return nil
}

func (p *realtimePlaybackState) append(audio realtimePlaybackAudio, format agent.AudioFormat, pcm []byte) error {
	if p.suppressDeltas || len(pcm) == 0 {
		return nil
	}
	if err := p.open(audio, format); err != nil {
		return err
	}
	return audio.WritePlayChunk(p.session.SessionID, pcm, false)
}

func (p *realtimePlaybackState) keepAlive(audio realtimePlaybackAudio, format agent.AudioFormat) error {
	if p.finalizeResponses || p.finalized {
		return nil
	}
	if err := p.open(audio, format); err != nil {
		return err
	}
	return audio.WritePlayChunk(p.session.SessionID, nil, false)
}

func (p *realtimePlaybackState) interrupt(audio realtimePlaybackAudio, format agent.AudioFormat) error {
	p.suppressDeltas = true
	p.finalized = false
	if err := p.stop(audio); err != nil {
		return err
	}
	return p.open(audio, format)
}

func (p *realtimePlaybackState) beginResponse(audio realtimePlaybackAudio, format agent.AudioFormat) error {
	p.suppressDeltas = false
	if p.finalized {
		if err := p.stop(audio); err != nil {
			return err
		}
		p.finalized = false
	}
	return p.open(audio, format)
}

func (p *realtimePlaybackState) finishResponse(audio realtimePlaybackAudio, forceOverride ...bool) error {
	force := len(forceOverride) > 0 && forceOverride[0]
	if (!p.finalizeResponses && !force) || p.session == nil || p.finalized {
		return nil
	}
	if err := audio.WritePlayChunk(p.session.SessionID, nil, true); err != nil {
		return err
	}
	p.finalized = true
	return nil
}

func (p *realtimePlaybackState) markDrained() {
	p.session = nil
	p.finalized = false
	p.suppressDeltas = false
}

func (p *realtimePlaybackState) stop(audio realtimePlaybackAudio) error {
	if p.session == nil {
		return nil
	}
	sessionID := p.session.SessionID
	p.session = nil
	p.finalized = false
	return audio.StopPlayback(sessionID)
}

type realtimeServerError struct{ message string }

func (e *realtimeServerError) Error() string { return "rtclient: " + e.message }

type realtimeRecordingAudio interface {
	ReadRecordChunk(uint64, uint32) (*agent.AudioChunkResult, error)
}

type realtimeRecordChunkReader interface {
	Read(uint32) (*agent.AudioChunkResult, error)
	Close() error
}

type realtimeRecordChunkReaderOpener interface {
	OpenRecordChunkReader(uint64) (*agent.AudioRecordChunkReader, error)
}

func streamRealtimeAudio(ctx context.Context, audio realtimeRecordingAudio, sessionID uint64, chunks chan<- []byte, errs chan<- error) {
	defer close(chunks)
	var reader realtimeRecordChunkReader = realtimeRecordingBackendReader{audio: audio, sessionID: sessionID}
	if opener, ok := audio.(realtimeRecordChunkReaderOpener); ok {
		serviceReader, err := opener.OpenRecordChunkReader(sessionID)
		if err != nil {
			select {
			case errs <- err:
			case <-ctx.Done():
			}
			return
		}
		reader = serviceReader
	}
	defer reader.Close()
	for {
		chunk, err := reader.Read(realtimeAudioReadTimeoutMs)
		if err != nil {
			select {
			case errs <- err:
			case <-ctx.Done():
			}
			return
		}
		if chunk.EndOfStream {
			return
		}
		if len(chunk.PCM) == 0 {
			continue
		}
		select {
		case chunks <- chunk.PCM:
		case <-ctx.Done():
			return
		}
	}
}

type realtimeRecordingBackendReader struct {
	audio     realtimeRecordingAudio
	sessionID uint64
}

func (r realtimeRecordingBackendReader) Read(timeoutMs uint32) (*agent.AudioChunkResult, error) {
	return r.audio.ReadRecordChunk(r.sessionID, timeoutMs)
}

func (realtimeRecordingBackendReader) Close() error { return nil }
