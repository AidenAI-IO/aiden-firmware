package main

import (
	"context"
	"encoding/binary"
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
	"aiden-agent/internal/agent/realtimevoice"
	"aiden-agent/internal/agent/tts"
	"aiden-agent/internal/agenttask"

	langtools "github.com/tmc/langchaingo/tools"
)

const (
	realtimeAudioReadTimeoutMs        = 200
	realtimeConnectTimeout            = 15 * time.Second
	realtimePlaybackKeepAlive         = 10 * time.Second
	realtimeToolCallTimeout           = 30 * time.Second
	realtimeTaskResultDebounce        = 500 * time.Millisecond
	realtimeContextReplayTurns        = 10
	realtimeClientTurnEndpointSilence = 800 * time.Millisecond
	realtimeClientTurnSpeechThreshold = 500
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
	call   realtimevoice.Event
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
			log.Println("\n[exit] Stopped.")
			return
		case <-events:
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

func realtimeProviderType(cfg agent.Config) string {
	provider := strings.ToLower(strings.TrimSpace(cfg.VoiceModel.Provider))
	if record, ok := cfg.VoiceModelProviders[provider]; ok && strings.TrimSpace(record.Type) != "" {
		provider = strings.ToLower(strings.TrimSpace(record.Type))
	}
	if provider == "" {
		return "qwen"
	}
	return provider
}

func realtimeProviderSessionConfig(cfg agent.Config) realtimevoice.SessionConfig {
	voice := cfg.VoiceModel.Voice
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
	instructions := strings.TrimSpace(cfg.VoiceModel.Instructions)
	if instructions == "" {
		instructions = agent.DefaultRealtimeVoiceInstructions
	}
	enableEmotion := cfg.VoiceModel.EnableSpeechEmotion
	if enableEmotion == nil {
		v := true
		enableEmotion = &v
	}
	return realtimevoice.SessionConfig{APIKey: cfg.VoiceModel.APIKey, Model: cfg.VoiceModel.Model, Voice: voice, Instructions: instructions,
		InputAudioFormat: inputFormat, OutputAudioFormat: outputFormat, MaxHistoryTurns: realtimeContextReplayTurns,
		TurnDetection: turnType, TurnDetectionThresh: cfg.VoiceModel.TurnDetectionThreshold, TurnDetectionSilenceMs: cfg.VoiceModel.TurnDetectionSilenceMs,
		EnableSpeechEmotion: enableEmotion, Tools: realtimeVoiceToolDefinitions()}
}

// realtimeClientTurnEndpoint tracks a local speech turn when a provider
// requires client-side endpointing. The provider contract exposes commit()
// but does not expose a server VAD stream, so a small energy gate is needed
// to delimit turns on the hardware.
type realtimeClientTurnEndpoint struct {
	silenceDuration time.Duration
	speechActive    bool
	lastSpeechAt    time.Time
}

func newRealtimeClientTurnEndpoint(silenceMs int) *realtimeClientTurnEndpoint {
	silence := realtimeClientTurnEndpointSilence
	if silenceMs > 0 {
		silence = time.Duration(silenceMs) * time.Millisecond
	}
	return &realtimeClientTurnEndpoint{silenceDuration: silence}
}

func (e *realtimeClientTurnEndpoint) Observe(pcm []byte, now time.Time) bool {
	if e == nil || pcm16MeanAbs(pcm) < realtimeClientTurnSpeechThreshold {
		return false
	}
	e.speechActive = true
	e.lastSpeechAt = now
	return true
}

func (e *realtimeClientTurnEndpoint) Due(now time.Time) bool {
	return e != nil && e.speechActive && !e.lastSpeechAt.IsZero() && now.Sub(e.lastSpeechAt) >= e.silenceDuration
}

func (e *realtimeClientTurnEndpoint) Reset() {
	if e == nil {
		return
	}
	e.speechActive = false
	e.lastSpeechAt = time.Time{}
}

func pcm16MeanAbs(pcm []byte) int {
	const sampleBytes = 2
	if len(pcm) < sampleBytes {
		return 0
	}
	var total uint64
	samples := len(pcm) / sampleBytes
	for i := 0; i < samples; i++ {
		value := int64(int16(binary.LittleEndian.Uint16(pcm[i*sampleBytes:])))
		if value < 0 {
			value = -value
		}
		total += uint64(value)
	}
	return int(total / uint64(samples))
}

const (
	realtimeCurrentTimeTool        = "get_current_time"
	realtimeRecallTool             = "recall_memory"
	realtimeCreateTaskTool         = "create_agent_task"
	realtimeCancelTaskTool         = "cancel_agent_task"
	realtimeQueryTaskTool          = "query_agent_task"
	realtimeResponseUserActionTool = "response_user_action"
)

func realtimeVoiceToolDefinitions() []realtimevoice.Tool {
	return []realtimevoice.Tool{
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

func realtimeVoiceToolDefinition(name, description string, parameters map[string]any) realtimevoice.Tool {
	encoded, err := json.Marshal(parameters)
	if err != nil {
		encoded = json.RawMessage(`{"type":"object","properties":{}}`)
	}
	return realtimevoice.Tool{Name: name, Description: description, Parameters: encoded}
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

func startRealtimeToolCall(ctx context.Context, executor realtimeVoiceToolExecutor, call realtimevoice.Event, results chan<- realtimeToolResult) {
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

func realtimePlaybackOutputFormat(cfg agent.Config) agent.AudioFormat {
	return agent.AudioFormat{
		SampleRate: uint32(cfg.Audio.SampleRateOrDefault()),
		Channels:   uint32(cfg.Audio.ChannelsOrDefault()),
		BitWidth:   uint32(cfg.Audio.BitWidthOrDefault()),
	}
}

func realtimeAgentAudioFormat(format realtimevoice.AudioFormat) agent.AudioFormat {
	return agent.AudioFormat{SampleRate: uint32(format.SampleRate), Channels: uint32(format.Channels), BitWidth: uint32(format.BitDepth)}
}

func realtimeVoiceDeviceFormat(cfg agent.Config) realtimevoice.AudioFormat {
	return realtimevoice.AudioFormat{
		Encoding:   "pcm_s16le",
		SampleRate: int(cfg.Audio.SampleRateOrDefault()),
		Channels:   int(cfg.Audio.ChannelsOrDefault()),
		BitDepth:   int(cfg.Audio.BitWidthOrDefault()),
	}
}

func realtimeProviderAudioFormat(format realtimevoice.AudioFormat, legacyRate, defaultRate int) agent.AudioFormat {
	return realtimeAgentAudioFormat(format.OrDefault(positiveRate(legacyRate, defaultRate)))
}

func positiveRate(primary, fallback int) int {
	if primary > 0 {
		return primary
	}
	return fallback
}

func runRealtimeSession(cfg agent.Config, sigChan chan os.Signal, runtime *agent.Runtime, tasks *agenttask.Manager, chatBridges ...*realtimeChatBridge) error {
	return runRealtimeSessionWithRegistry(cfg, sigChan, runtime, tasks, realtimevoice.DefaultProviderRegistry(), chatBridges...)
}

func runRealtimeSessionWithRegistry(cfg agent.Config, sigChan chan os.Signal, runtime *agent.Runtime, tasks *agenttask.Manager, registry *realtimevoice.ProviderRegistry, chatBridges ...*realtimeChatBridge) error {
	var chatBridge *realtimeChatBridge
	if len(chatBridges) > 0 {
		chatBridge = chatBridges[0]
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sessionConfig := realtimeProviderSessionConfig(cfg)
	if runtime != nil {
		unregisterReset := runtime.RegisterUserContextResetHook(cancel)
		defer unregisterReset()
	}
	userContext, err := agent.InitializeContextManager(
		sessionConfig.Instructions,
		agentpath.UserContextManagerSessionFolder(cfg.ConfigDir),
		nil,
	)
	if err != nil {
		return fmt.Errorf("initialize realtime user context: %w", err)
	}

	providerName := realtimeProviderType(cfg)
	log.Printf("[realtime] Connecting: provider=%s model=%s region=%s workspace_configured=%t endpoint_override=%t",
		providerName, cfg.VoiceModel.Model, cfg.VoiceModel.Region, cfg.VoiceModel.WorkspaceID != "", cfg.VoiceModel.Endpoint != "")
	connectCtx, connectCancel := context.WithTimeout(ctx, realtimeConnectTimeout)
	session, err := registry.Open(connectCtx, providerName, realtimevoice.ProviderConfig{Endpoint: cfg.VoiceModel.Endpoint, BaseURL: cfg.VoiceModel.BaseURL, AgentID: cfg.VoiceModel.AgentID, UpstreamProvider: cfg.VoiceModel.UpstreamProvider, WorkspaceID: cfg.VoiceModel.WorkspaceID, Region: cfg.VoiceModel.Region}, sessionConfig, realtimevoice.DeviceMediaConfig{Input: realtimeVoiceDeviceFormat(cfg), Output: realtimeVoiceDeviceFormat(cfg)})
	connectCancel()
	if err != nil {
		return fmt.Errorf("connect realtime voice model: %w", err)
	}
	defer session.Close()
	info := session.Info()
	turnCommitter, _ := session.(realtimevoice.TurnCommitter)
	responseInterrupter, _ := session.(realtimevoice.ResponseInterrupter)
	canInterrupt := info.Capabilities.CanInterruptResponse
	toolResultSender, _ := session.(realtimevoice.ToolResultSender)
	canSendToolResult := info.Capabilities.CanSendToolResult
	textSession, _ := realtimeTextSession(session)
	supportsText := info.Capabilities.CanSendText
	contextReplayer, _ := session.(realtimevoice.ContextReplayer)
	canReplayContext := info.Capabilities.CanReplayContext
	if supportsText && canReplayContext {
		if err := replayRealtimeContext(ctx, contextReplayer, userContext); err != nil {
			return fmt.Errorf("restore realtime user context: %w", err)
		}
	}
	log.Printf("[realtime] Session ready: id=%s input_rate=%d output_rate=%d text_input=%t", info.ID, info.InputSampleRate, info.OutputSampleRate, supportsText)
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
	inputFormat := realtimeAgentAudioFormat(info.InputFormatOrDefault(16000))
	recording, err := recordingAudio.StartRecording(inputFormat)
	if err != nil {
		return fmt.Errorf("start realtime recording: %w", err)
	}
	defer func() { _ = recordingAudio.StopRecording(recording.SessionID) }()
	log.Printf("[realtime] Microphone opened: session_id=%d format=pcm/%d/%d/%d", recording.SessionID, inputFormat.SampleRate, inputFormat.Channels, inputFormat.BitWidth)

	chunks := make(chan []byte, 8)
	readErrs := make(chan error, 1)
	go streamRealtimeAudio(ctx, recordingAudio, recording.SessionID, chunks, readErrs)

	realtimeOutputFormat := realtimeAgentAudioFormat(info.OutputFormatOrDefault(24000))
	outputFormat := realtimePlaybackOutputFormat(cfg)
	if outputFormat.SampleRate == 0 {
		outputFormat = realtimeOutputFormat
	}
	log.Printf("[realtime] Playback format: source=pcm/%d/%d/%d target=pcm/%d/%d/%d resample=%t",
		realtimeOutputFormat.SampleRate,
		realtimeOutputFormat.Channels,
		realtimeOutputFormat.BitWidth,
		outputFormat.SampleRate,
		outputFormat.Channels,
		outputFormat.BitWidth,
		uint32(info.ProviderOutputAudioFormat.SampleRate) != outputFormat.SampleRate,
	)
	playback := realtimePlaybackState{finalizeResponses: cfg.AudioBackendOrDefault() == agent.AudioBackendLocal}
	if err := playback.open(playbackAudio, outputFormat); err != nil {
		return fmt.Errorf("open realtime playback: %w", err)
	}
	log.Printf("[realtime] Speaker opened: session_id=%d", playback.session.SessionID)
	defer func() { _ = playback.stop(playbackAudio) }()
	playbackKeepAlive := time.NewTicker(realtimePlaybackKeepAlive)
	defer playbackKeepAlive.Stop()
	var clientTurnEndpoint *realtimeClientTurnEndpoint
	var clientTurnCommitTimer *time.Timer
	var clientTurnCommitTimerCh <-chan time.Time
	if info.Capabilities.ClientSideTurnDetection {
		clientTurnEndpoint = newRealtimeClientTurnEndpoint(cfg.VoiceModel.TurnDetectionSilenceMs)
	}
	stopClientTurnCommitTimer := func() {
		if clientTurnCommitTimer == nil {
			clientTurnCommitTimerCh = nil
			return
		}
		if !clientTurnCommitTimer.Stop() {
			select {
			case <-clientTurnCommitTimer.C:
			default:
			}
		}
		clientTurnCommitTimerCh = nil
	}
	defer stopClientTurnCommitTimer()
	armClientTurnCommitTimer := func() {
		if clientTurnEndpoint == nil || !clientTurnEndpoint.speechActive {
			return
		}
		delay := clientTurnEndpoint.silenceDuration
		if !clientTurnEndpoint.lastSpeechAt.IsZero() {
			delay = clientTurnEndpoint.silenceDuration - time.Since(clientTurnEndpoint.lastSpeechAt)
			if delay < 0 {
				delay = 0
			}
		}
		if clientTurnCommitTimer == nil {
			clientTurnCommitTimer = time.NewTimer(delay)
		} else {
			if !clientTurnCommitTimer.Stop() {
				select {
				case <-clientTurnCommitTimer.C:
				default:
				}
			}
			clientTurnCommitTimer.Reset(delay)
		}
		clientTurnCommitTimerCh = clientTurnCommitTimer.C
	}
	commitClientTurn := func() error {
		if clientTurnEndpoint == nil || !clientTurnEndpoint.speechActive {
			return nil
		}
		stopClientTurnCommitTimer()
		if err := turnCommitter.Commit(ctx); err != nil {
			return fmt.Errorf("commit realtime input turn: %w", err)
		}
		clientTurnEndpoint.Reset()
		return nil
	}
	firstAudioChunk := true
	var activeChat *realtimeChatCommand
	var chatMode string
	var chatText strings.Builder
	var chatTranscript strings.Builder
	var responseText strings.Builder
	var responseTranscript strings.Builder
	assistantPersisted := false
	var realtimeResponseUsage messages.Usage
	hasRealtimeResponseUsage := false
	responseActive := false
	inputSpeechActive := false
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
		if !taskUpdatesReady || len(pendingTaskUpdates) == 0 || responseActive || inputSpeechActive || activeChat != nil || chatBridgeHasPending(chatBridge) {
			return nil
		}
		message := formatRealtimeTaskUpdates(pendingTaskUpdates)
		if !supportsText {
			return nil
		}
		if err := appendRealtimeNoticeMessage(userContext, message); err != nil {
			return fmt.Errorf("persist task update in realtime context: %w", err)
		}
		if err := textSession.SendText(ctx, message); err != nil {
			return fmt.Errorf("inject background task update: %w", err)
		}
		if err := textSession.CreateResponse(ctx); err != nil {
			return fmt.Errorf("respond to background task update: %w", err)
		}
		pendingTaskUpdates = nil
		taskUpdatesReady = false
		responseActive = true
		return nil
	}
	defer func() {
		if activeChat != nil {
			sendRealtimeChatEvent(*activeChat, agent.RealtimeChatEvent{Type: agent.RealtimeChatEventError, Error: "realtime voice session ended"})
			close(activeChat.events)
		}
	}()

	sessionErrors := session.Errors()
	// The event stream is the authoritative completion signal. Providers may
	// close Done before the final buffered transcript or response event is read.
	sessionEvents := session.Events()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-sigChan:
			return errRealtimeShutdown
		case err, ok := <-sessionErrors:
			if !ok {
				sessionErrors = nil
				continue
			}
			if err != nil {
				return err
			}
		case err := <-readErrs:
			if err != nil {
				return err
			}
		case result := <-toolResults:
			call := result.call
			if !canSendToolResult {
				return fmt.Errorf("realtime provider %s cannot send tool results", providerName)
			}
			if err := toolResultSender.SendToolResult(ctx, call.CallID, result.output); err != nil {
				return fmt.Errorf("send realtime tool result: %w", err)
			}
			if err := appendRealtimeToolExecution(userContext, call, result.output); err != nil {
				return fmt.Errorf("persist realtime tool call and result: %w", err)
			}
			if info.Capabilities.ExplicitToolContinuation && toolTracker.complete(call.ResponseID) {
				responseActive = true
				if err := textSession.CreateResponse(ctx); err != nil {
					return fmt.Errorf("continue realtime response after tool call: %w", err)
				}
			}
		case pcm, ok := <-chunks:
			if !ok {
				if err := commitClientTurn(); err != nil {
					return err
				}
				return nil
			}
			if err := session.SendAudio(ctx, pcm); err != nil {
				return err
			}
			if clientTurnEndpoint != nil && clientTurnEndpoint.Observe(pcm, time.Now()) {
				armClientTurnCommitTimer()
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
		case <-clientTurnCommitTimerCh:
			if clientTurnEndpoint != nil && clientTurnEndpoint.Due(time.Now()) {
				if err := commitClientTurn(); err != nil {
					return err
				}
			} else if clientTurnEndpoint != nil && clientTurnEndpoint.speechActive {
				armClientTurnCommitTimer()
			}
		case command := <-chatBridgeCommands(chatBridge):
			if !supportsText {
				sendRealtimeChatEvent(command, agent.RealtimeChatEvent{Type: agent.RealtimeChatEventError, Error: fmt.Sprintf("realtime provider %s does not support text injection", providerName)})
				close(command.events)
				continue
			}
			if activeChat != nil || responseActive || inputSpeechActive {
				sendRealtimeChatEvent(command, agent.RealtimeChatEvent{Type: agent.RealtimeChatEventError, Error: "realtime response is busy"})
				close(command.events)
				continue
			}
			activeChat = &command
			chatMode = ""
			chatText.Reset()
			chatTranscript.Reset()
			if err := textSession.SendText(command.ctx, command.request.Message); err != nil {
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
			if err := textSession.CreateResponse(command.ctx); err != nil {
				responseActive = false
				sendRealtimeChatEvent(command, agent.RealtimeChatEvent{Type: agent.RealtimeChatEventError, Error: err.Error()})
				close(command.events)
				activeChat = nil
			}
		case <-chatCommandDone(activeChat):
			if canInterrupt {
				_ = interruptRealtimeResponse(ctx, responseActive, responseInterrupter)
			}
			sendRealtimeChatEvent(*activeChat, agent.RealtimeChatEvent{Type: agent.RealtimeChatEventError, Error: "request canceled"})
			close(activeChat.events)
			activeChat = nil
		case event, ok := <-sessionEvents:
			if !ok {
				if err := realtimeSessionTerminationError(sessionErrors); err != nil {
					return err
				}
				return nil
			}
			switch event.Kind {
			case realtimevoice.EventReady:
				// Providers may emit a ready frame after Open; negotiated rates
				// are already available in Session.Info().
			case realtimevoice.EventSpeechStarted:
				inputSpeechActive = true
				log.Println("[realtime] Speech started, interrupting local playback")
				if canInterrupt {
					if err := interruptRealtimeResponse(ctx, responseActive, responseInterrupter); err != nil {
						return fmt.Errorf("interrupt realtime provider response: %w", err)
					}
				}
				// Clear the queued response immediately, then reopen the speaker
				// session so mic and speaker remain active for the next turn.
				if err := playback.interrupt(playbackAudio, outputFormat); err != nil {
					return fmt.Errorf("interrupt realtime playback: %w", err)
				}
			case realtimevoice.EventSpeechStopped:
				inputSpeechActive = false
				if clientTurnEndpoint != nil {
					if err := commitClientTurn(); err != nil {
						return err
					}
				}
				if err := tryInjectTaskUpdates(); err != nil {
					return err
				}
			case realtimevoice.EventTranscriptFinal:
				if event.Role == "user" {
					if err := appendRealtimeUserMessage(userContext, event.Text); err != nil {
						return fmt.Errorf("persist realtime user transcript: %w", err)
					}
				} else if event.Role == "assistant" {
					if recordRealtimeFinalTranscript(event.Text, &responseTranscript, &chatText, &chatTranscript) && activeChat != nil {
						chatMode = "transcript"
						sendRealtimeChatEvent(*activeChat, agent.RealtimeChatEvent{Type: agent.RealtimeChatEventDelta, Delta: event.Text})
					}
				}
			case realtimevoice.EventResponseStarted:
				log.Println("[realtime] Response created")
				responseActive = true
				responseText.Reset()
				responseTranscript.Reset()
				assistantPersisted = false
				// Ignore any late deltas from the interrupted response until the
				// server explicitly starts the next one. The speaker session stays
				// open across responses.
				if err := playback.beginResponse(playbackAudio, outputFormat); err != nil {
					return fmt.Errorf("begin realtime playback response: %w", err)
				}
			case realtimevoice.EventTranscriptDelta:
				if event.Role != "assistant" {
					continue
				}
				if !supportsText && (!responseActive || playback.suppressDeltas) {
					if err := ensureImplicitRealtimeResponse(&responseActive, &assistantPersisted, &responseText, &responseTranscript, &playback, playbackAudio, outputFormat); err != nil {
						return err
					}
				}
				if event.TextSource == "text" {
					responseText.WriteString(event.Text)
					if activeChat != nil {
						if chatMode == "" {
							chatMode = "text"
						}
						if chatMode == "text" && event.Text != "" {
							chatText.WriteString(event.Text)
							sendRealtimeChatEvent(*activeChat, agent.RealtimeChatEvent{Type: agent.RealtimeChatEventDelta, Delta: event.Text})
						}
					}
				} else {
					responseTranscript.WriteString(event.Text)
					if activeChat != nil {
						if chatMode == "" {
							chatMode = "transcript"
						}
						if chatMode == "transcript" && event.Text != "" {
							chatTranscript.WriteString(event.Text)
							sendRealtimeChatEvent(*activeChat, agent.RealtimeChatEvent{Type: agent.RealtimeChatEventDelta, Delta: event.Text})
						}
					}
				}
			case realtimevoice.EventAudio:
				if !supportsText {
					if err := ensureImplicitRealtimeResponse(&responseActive, &assistantPersisted, &responseText, &responseTranscript, &playback, playbackAudio, outputFormat); err != nil {
						return err
					}
				}
				pcm := event.PCM
				if err := playback.append(playbackAudio, outputFormat, pcm); err != nil {
					return err
				}
			case realtimevoice.EventToolCall:
				log.Printf("[realtime] Tool call: %s", event.Name)
				if info.Capabilities.ExplicitToolContinuation {
					toolTracker.start(event.ResponseID)
				}
				startRealtimeToolCall(ctx, toolExecutor, event, toolResults)
			case realtimevoice.EventUsage:
				realtimeResponseUsage.TotalTokens += event.Usage.TotalTokens
				realtimeResponseUsage.InputTokens += event.Usage.InputTokens
				realtimeResponseUsage.OutputTokens += event.Usage.OutputTokens
				hasRealtimeResponseUsage = true
			case realtimevoice.EventResponseDone, realtimevoice.EventResponseCancelled:
				if event.Usage.TotalTokens > 0 || event.Usage.InputTokens > 0 || event.Usage.OutputTokens > 0 {
					realtimeResponseUsage.TotalTokens += event.Usage.TotalTokens
					realtimeResponseUsage.InputTokens += event.Usage.InputTokens
					realtimeResponseUsage.OutputTokens += event.Usage.OutputTokens
					hasRealtimeResponseUsage = true
				}
				if err := playback.finishResponse(playbackAudio); err != nil {
					return fmt.Errorf("finish realtime playback response: %w", err)
				}
				if hasTools, continueNow := toolTracker.done(event.ResponseID); hasTools {
					if !continueNow {
						continue
					}
					responseActive = true
					if !supportsText {
						continue
					}
					if err := textSession.CreateResponse(ctx); err != nil {
						return fmt.Errorf("continue realtime response after tool call: %w", err)
					}
					continue
				}
				if !assistantPersisted && event.Kind != realtimevoice.EventResponseCancelled && (event.Status == "" || event.Status == "completed") {
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
				assistantPersisted = false
				if activeChat != nil {
					content := chatText.String()
					if content == "" {
						content = chatTranscript.String()
					}
					sendRealtimeChatEvent(*activeChat, agent.RealtimeChatEvent{Type: agent.RealtimeChatEventDone, Response: content})
					close(activeChat.events)
					activeChat = nil
				}
				if err := tryInjectTaskUpdates(); err != nil {
					return err
				}
			case realtimevoice.EventInterruption:
				responseActive = false
				if err := playback.interrupt(playbackAudio, outputFormat); err != nil {
					return err
				}
			case realtimevoice.EventError:
				if event.Error != nil {
					return event.Error
				}
			}
		}
	}
}

func realtimeTextSession(session realtimevoice.Session) (realtimevoice.TextSession, bool) {
	textSession, ok := session.(realtimevoice.TextSession)
	return textSession, ok
}

func realtimeSessionTerminationError(errs <-chan error) error {
	if errs == nil {
		return nil
	}
	select {
	case err, ok := <-errs:
		if ok && err != nil {
			return err
		}
	default:
	}
	return nil
}

func interruptRealtimeResponse(ctx context.Context, responseActive bool, interrupter realtimevoice.ResponseInterrupter) error {
	if !responseActive || interrupter == nil {
		return nil
	}
	return interrupter.Interrupt(ctx)
}

func ensureImplicitRealtimeResponse(
	active *bool,
	assistantPersisted *bool,
	responseText *strings.Builder,
	responseTranscript *strings.Builder,
	playback *realtimePlaybackState,
	audio realtimePlaybackAudio,
	format agent.AudioFormat,
) error {
	if active == nil || assistantPersisted == nil || responseText == nil || responseTranscript == nil || playback == nil {
		return errors.New("realtime response state is unavailable")
	}
	if *active && !playback.suppressDeltas {
		return nil
	}
	*active = true
	*assistantPersisted = false
	responseText.Reset()
	responseTranscript.Reset()
	if err := playback.beginResponse(audio, format); err != nil {
		return err
	}
	return nil
}

func recordRealtimeFinalTranscript(text string, responseTranscript, chatText, chatTranscript *strings.Builder) bool {
	if responseTranscript == nil || chatText == nil || chatTranscript == nil {
		return false
	}
	if responseTranscript.Len() == 0 {
		responseTranscript.WriteString(text)
	}
	if chatText.Len() == 0 && chatTranscript.Len() == 0 && strings.TrimSpace(text) != "" {
		chatTranscript.WriteString(text)
		return true
	}
	return false
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
func appendRealtimeToolExecution(manager *contextmanager.ContextManager, call realtimevoice.Event, output string) error {
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

func replayRealtimeContext(ctx context.Context, session realtimevoice.ContextReplayer, manager *contextmanager.ContextManager) error {
	if session == nil || manager == nil {
		return nil
	}
	var items []realtimevoice.ContextItem
	for _, message := range recentRealtimeContextMessages(manager.MessageListDump().Messages, realtimeContextReplayTurns) {
		content := strings.TrimSpace(message.Content)
		if message.Role == messages.MessageRoleSystem {
			continue
		}
		if message.Role == messages.MessageRoleToolCall {
			for _, call := range message.ToolCalls {
				items = append(items, realtimevoice.ContextItem{Type: "function_call", CallID: call.ID, Name: call.Name, Arguments: call.Arguments})
			}
			continue
		}
		if message.Role == messages.MessageRoleToolResult {
			for _, result := range message.ToolResults {
				items = append(items, realtimevoice.ContextItem{Type: "function_call_output", CallID: result.ToolCallID, Output: result.Content})
			}
			continue
		}
		if content == "" {
			continue
		}
		role := "user"
		if message.Role == messages.MessageRoleAssistant {
			role = "assistant"
		}
		items = append(items, realtimevoice.ContextItem{Type: "message", Role: role, Content: content})
	}
	return session.ReplayContext(ctx, items)
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

type realtimePlaybackAudio interface {
	StartPlayback(agent.AudioFormat) (*agent.PlaybackStartResult, error)
	WritePlayChunk(uint64, []byte, bool) error
	StopPlayback(uint64) error
}

type realtimePlaybackAudioAdapter struct {
	backend tts.AudioServiceBackend
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
	return a.backend.StopPlayback(sessionID)
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
	if p.finalizeResponses {
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

func (p *realtimePlaybackState) finishResponse(audio realtimePlaybackAudio) error {
	if !p.finalizeResponses || p.session == nil || p.finalized {
		return nil
	}
	if err := audio.WritePlayChunk(p.session.SessionID, nil, true); err != nil {
		return err
	}
	p.finalized = true
	return nil
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
