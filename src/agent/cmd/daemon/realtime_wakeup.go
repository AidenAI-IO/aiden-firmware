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
	realtimeTaskResultDebounce = 500 * time.Millisecond
)

// runRealtimeWakeupMode owns the realtime voice path. The legacy wakeup
// runners remain separate so they can be restored without changing this path.
type realtimeChatCommand struct {
	ctx     context.Context
	request agent.RealtimeChatRequest
	events  chan agent.RealtimeChatEvent
}

type realtimeChatBridge struct {
	commands chan realtimeChatCommand

	mu     sync.RWMutex
	active bool
	done   chan struct{}
}

func newRealtimeChatBridge() *realtimeChatBridge {
	return &realtimeChatBridge{commands: make(chan realtimeChatCommand, 8)}
}

func (b *realtimeChatBridge) Handle(ctx context.Context, request agent.RealtimeChatRequest) (<-chan agent.RealtimeChatEvent, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	events := make(chan agent.RealtimeChatEvent, 16)
	b.mu.RLock()
	active, done := b.active, b.done
	b.mu.RUnlock()
	if !active || done == nil {
		return nil, fmt.Errorf("realtime voice session is unavailable")
	}
	command := realtimeChatCommand{ctx: ctx, request: request, events: events}
	select {
	case b.commands <- command:
		return events, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-done:
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
	bridge := newRealtimeChatBridge()
	if server != nil {
		server.SetRealtimeChatHandler(bridge.Handle)
		defer server.SetRealtimeChatHandler(nil)
	}
	log.Printf("\n[ready] Starting realtime GPIO wakeup listeners on %s...", wakeupGPIOPinsLabel())
	events := make(chan struct{}, 1)
	watchers, err := startWakeupWatchers(newWatcher, func() {
		signalWakeupEvent(events)
	})
	if err != nil {
		log.Printf("[error] Failed to start GPIO wakeup listeners: %v\n", err)
		return
	}
	defer stopWakeupWatchers(watchers)

	log.Printf("[ready] Waiting for realtime wakeup event (%s)... Ctrl+C to quit", wakeupGPIOPinsLabel())
	for {
		select {
		case <-sigChan:
			log.Println("\n[exit] Stopped.")
			return
		case <-events:
			log.Println("\n[wakeup] GPIO wakeup triggered, connecting realtime voice model...")
			if err := runRealtimeSession(cfg, sigChan, runtime, tasks, bridge); err != nil {
				log.Printf("[realtime] session ended: %v", err)
			}
			drainRealtimeWakeups(events)
			log.Println("[ready] Waiting for realtime wakeup event...")
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
		Tools:               realtimeVoiceToolDefinitions(),
		TurnDetection:       turn,
	}
}

const (
	realtimeCurrentTimeTool = "get_current_time"
	realtimeRecallTool      = "recall_memory"
	realtimeCreateTaskTool  = "create_agent_task"
	realtimeCancelTaskTool  = "cancel_agent_task"
	realtimeQueryTaskTool   = "query_agent_task"
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
			"Create a non-blocking background agent task for device operations, external actions, or longer multi-step work.",
			map[string]any{
				"type": "object",
				"properties": map[string]any{
					"task": map[string]any{"type": "string", "description": "A self-contained description of the work for the background agent."},
				},
				"required": []string{"task"},
			},
		),
		realtimeVoiceToolDefinition(
			realtimeCancelTaskTool,
			"Cancel a queued or running background agent task.",
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
			"Query the current status and result of a background agent task.",
			map[string]any{
				"type": "object",
				"properties": map[string]any{
					"task_id": map[string]any{"type": "string"},
				},
				"required": []string{"task_id"},
			},
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

func realtimePlaybackOutputFormat(cfg agent.Config) agent.AudioFormat {
	return agent.AudioFormat{
		SampleRate: uint32(cfg.Audio.SampleRateOrDefault()),
		Channels:   uint32(cfg.Audio.ChannelsOrDefault()),
		BitWidth:   uint32(cfg.Audio.BitWidthOrDefault()),
	}
}

func runRealtimeSession(cfg agent.Config, sigChan chan os.Signal, runtime *agent.Runtime, tasks *agenttask.Manager, chatBridges ...*realtimeChatBridge) error {
	var chatBridge *realtimeChatBridge
	if len(chatBridges) > 0 {
		chatBridge = chatBridges[0]
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

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
	log.Println("[realtime] session.updated received, opening microphone...")
	if chatBridge != nil {
		chatBridge.activate()
		defer func() {
			chatBridge.deactivate()
			chatBridge.failQueued("realtime voice session ended")
		}()
	}

	audio := agent.NewAudioServiceClient(cfg.Audio.SocketOrDefault())
	inputFormat := agent.AudioFormat{
		// Qwen realtime currently accepts only 16 kHz, 16-bit mono PCM.
		SampleRate: 16000,
		Channels:   1,
		BitWidth:   16,
	}
	recording, err := audio.StartRecording(inputFormat)
	if err != nil {
		return fmt.Errorf("start realtime recording: %w", err)
	}
	defer func() { _ = audio.StopRecording(recording.SessionID) }()
	log.Printf("[realtime] Microphone opened: session_id=%d format=pcm/16000/mono/16", recording.SessionID)

	chunks := make(chan []byte, 8)
	readErrs := make(chan error, 1)
	go streamRealtimeAudio(ctx, audio, recording.SessionID, chunks, readErrs)

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
	playback := realtimePlaybackState{}
	if err := playback.open(audio, outputFormat); err != nil {
		return fmt.Errorf("open realtime playback: %w", err)
	}
	log.Printf("[realtime] Speaker opened: session_id=%d", playback.session.SessionID)
	defer func() { _ = playback.stop(audio) }()
	playbackKeepAlive := time.NewTicker(realtimePlaybackKeepAlive)
	defer playbackKeepAlive.Stop()
	firstAudioChunk := true
	var activeChat *realtimeChatCommand
	var chatMode string
	var chatText strings.Builder
	var chatTranscript strings.Builder
	responseActive := false
	inputSpeechActive := false
	toolExecutor := newRealtimeVoiceToolExecutor(runtime, tasks)
	toolResponsesPending := make(map[string]bool)
	var pendingTaskUpdates []agenttask.Task
	var taskDebounceTimer *time.Timer
	var taskDebounce <-chan time.Time
	taskUpdatesReady := false
	defer func() {
		if taskDebounceTimer != nil {
			taskDebounceTimer.Stop()
		}
		if tasks != nil && len(pendingTaskUpdates) > 0 {
			tasks.RestoreTerminalTasks(pendingTaskUpdates)
		}
	}()
	tryInjectTaskUpdates := func() error {
		if !taskUpdatesReady || len(pendingTaskUpdates) == 0 || responseActive || inputSpeechActive || activeChat != nil || chatBridgeHasPending(chatBridge) {
			return nil
		}
		message := formatRealtimeTaskUpdates(pendingTaskUpdates)
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
	defer func() {
		if activeChat != nil {
			sendRealtimeChatEvent(*activeChat, agent.RealtimeChatEvent{Type: agent.RealtimeChatEventError, Error: "realtime voice session ended"})
			close(activeChat.events)
		}
	}()

	for {
		select {
		case <-sigChan:
			return nil
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
			if err := playback.keepAlive(audio, outputFormat); err != nil {
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
		case <-taskDebounce:
			taskDebounce = nil
			taskUpdatesReady = true
			if err := tryInjectTaskUpdates(); err != nil {
				return err
			}
		case command := <-chatBridgeCommands(chatBridge):
			if activeChat != nil || responseActive || inputSpeechActive {
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
				log.Println("[realtime] Speech started, interrupting local playback")
				// Clear the queued response immediately, then reopen the speaker
				// session so mic and speaker remain active for the next turn.
				if err := playback.interrupt(audio, outputFormat); err != nil {
					return fmt.Errorf("interrupt realtime playback: %w", err)
				}
			case "input_audio_buffer.speech_stopped", "input_audio_buffer.committed":
				inputSpeechActive = false
			case "response.created":
				log.Println("[realtime] Response created")
				responseActive = true
				if realtimeOutputFormat.SampleRate != outputFormat.SampleRate &&
					realtimeOutputFormat.Channels == outputFormat.Channels &&
					realtimeOutputFormat.BitWidth == outputFormat.BitWidth {
					outputResampler = tts.NewPCM16MonoResampler(int(realtimeOutputFormat.SampleRate), int(outputFormat.SampleRate))
				}
				// Ignore any late deltas from the interrupted response until the
				// server explicitly starts the next one. The speaker session stays
				// open across responses.
				if err := playback.beginResponse(audio, outputFormat); err != nil {
					return fmt.Errorf("begin realtime playback response: %w", err)
				}
			case "response.text.delta":
				if activeChat != nil {
					var delta rtclient.ResponseDeltaEvent
					if err := event.Decode(&delta); err != nil {
						return err
					}
					if chatMode == "" {
						chatMode = "text"
					}
					if chatMode == "text" && delta.Delta != "" {
						chatText.WriteString(delta.Delta)
						sendRealtimeChatEvent(*activeChat, agent.RealtimeChatEvent{Type: agent.RealtimeChatEventDelta, Delta: delta.Delta})
					}
				}
			case "response.audio_transcript.delta":
				if activeChat != nil {
					var delta rtclient.ResponseDeltaEvent
					if err := event.Decode(&delta); err != nil {
						return err
					}
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
				if err := playback.append(audio, outputFormat, pcm); err != nil {
					return err
				}
			case "response.audio_transcript.done":
				if activeChat != nil && chatTranscript.Len() == 0 {
					var done rtclient.TranscriptEvent
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
				output := toolExecutor.call(ctx, call.Name, call.Arguments)
				if err := session.SendFunctionOutput(ctx, call.CallID, output); err != nil {
					return fmt.Errorf("send realtime tool result: %w", err)
				}
				toolResponsesPending[call.ResponseID] = true
			case "response.done":
				var done rtclient.ResponseEvent
				if err := event.Decode(&done); err != nil {
					return err
				}
				if toolResponsesPending[done.Response.ID] {
					delete(toolResponsesPending, done.Response.ID)
					responseActive = true
					if err := session.CreateResponse(ctx, nil); err != nil {
						return fmt.Errorf("continue realtime response after tool call: %w", err)
					}
					continue
				}
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

func chatBridgeHasPending(bridge *realtimeChatBridge) bool {
	return bridge != nil && len(bridge.commands) > 0
}

func formatRealtimeTaskUpdates(tasks []agenttask.Task) string {
	var output strings.Builder
	output.WriteString("Background agent task updates. Tell the user the outcomes naturally and concisely:\n")
	for _, task := range tasks {
		fmt.Fprintf(&output, "- task_id=%s status=%s task=%q", task.ID, task.Status, limitRealtimeTaskField(task.Prompt, 300))
		if task.Result != "" {
			fmt.Fprintf(&output, " result=%q", limitRealtimeTaskField(task.Result, 2000))
		}
		if task.Error != "" {
			fmt.Fprintf(&output, " error=%q", limitRealtimeTaskField(task.Error, 500))
		}
		output.WriteByte('\n')
	}
	return strings.TrimSpace(output.String())
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
			return context.Canceled
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

type realtimePlaybackState struct {
	session        *agent.PlaybackStartResult
	suppressDeltas bool
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
	if err := p.open(audio, format); err != nil {
		return err
	}
	return audio.WritePlayChunk(p.session.SessionID, nil, false)
}

func (p *realtimePlaybackState) interrupt(audio realtimePlaybackAudio, format agent.AudioFormat) error {
	p.suppressDeltas = true
	if err := p.stop(audio); err != nil {
		return err
	}
	return p.open(audio, format)
}

func (p *realtimePlaybackState) beginResponse(audio realtimePlaybackAudio, format agent.AudioFormat) error {
	p.suppressDeltas = false
	return p.open(audio, format)
}

func (p *realtimePlaybackState) stop(audio realtimePlaybackAudio) error {
	if p.session == nil {
		return nil
	}
	sessionID := p.session.SessionID
	p.session = nil
	return audio.StopPlayback(sessionID)
}

type realtimeServerError struct{ message string }

func (e *realtimeServerError) Error() string { return "rtclient: " + e.message }

func streamRealtimeAudio(ctx context.Context, audio *agent.AudioServiceClient, sessionID uint64, chunks chan<- []byte, errs chan<- error) {
	defer close(chunks)
	reader, err := audio.OpenRecordChunkReader(sessionID)
	if err != nil {
		select {
		case errs <- err:
		case <-ctx.Done():
		}
		return
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
