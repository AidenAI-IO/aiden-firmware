package agent

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	langtools "github.com/tmc/langchaingo/tools"

	"aiden-agent/internal/agent/tts"
	_ "aiden-agent/internal/agent/tts/adapters/alicloud"
	_ "aiden-agent/internal/agent/tts/adapters/fishaudio"
	_ "aiden-agent/internal/agent/tts/adapters/minimax"
	_ "aiden-agent/internal/agent/tts/adapters/volcengine"
)

// Server provides HTTP API for agent interactions
type Server struct {
	runtime                    *Runtime
	addr                       string
	logger                     *Logger
	benchmarkDir               string
	benchmarkPIDFile           string
	benchmarkLogPath           string
	benchmarkStatePath         string
	benchmarkLauncher          func(suite, judge, apiKey string) error
	benchmarkMobileGymLauncher func(suite, suiteType string, parallel, limit int) error
	benchmarkSuiteValidator    func(path string) error
	benchmarkSuiteLocks        sync.Map
	userFilesReportPath        string
	userFilesToolsDir          string
	mu                         sync.Mutex
	history                    []Message
	historyStore               *ChatHistoryStore
	episodeStore               *TaskEpisodeStore
	sttClient                  STTClient
	ttsManager                 *tts.ProviderManager
	ttsMu                      sync.RWMutex
	audioClient                *AudioServiceClient
	recordMu                   sync.Mutex
	webRecording               *webAudioRecording
	bridge                     *PhoneBridge
	pendingResults             map[string]*chatPendingResult
	pendingResultsMu           sync.Mutex
	activeRuns                 map[string]context.CancelFunc
	activeRunsMu               sync.Mutex
}

type webAudioRecording struct {
	sessionID  uint64
	sampleRate int
	done       chan struct{}

	mu         sync.Mutex
	samples    []int16
	hasPending bool
	pending    byte
	stopping   bool
	err        error
}

type AudioRecordStartResponse struct {
	Status     string `json:"status"`
	SampleRate int    `json:"sample_rate"`
}

type AudioRecordStopResponse struct {
	Attachment MessageAttachment `json:"attachment"`
}

type MessageAttachment struct {
	Kind       string `json:"kind"`
	Name       string `json:"name,omitempty"`
	MIMEType   string `json:"mime_type,omitempty"`
	Data       string `json:"data,omitempty"`
	Size       int    `json:"size,omitempty"`
	Transcript string `json:"transcript,omitempty"`
}

// Message represents a chat message or tool call
type Message struct {
	Type        string              `json:"type"` // "user", "assistant", "tool_call", "tool_result", "role_output"
	Role        string              `json:"role,omitempty"`
	EpisodeID   string              `json:"episode_id,omitempty"`
	RequestID   string              `json:"request_id,omitempty"`
	Status      string              `json:"status,omitempty"`
	Content     string              `json:"content"`
	ToolName    string              `json:"tool_name,omitempty"`
	ToolInput   string              `json:"tool_input,omitempty"`
	Description string              `json:"description,omitempty"`
	Attachments []MessageAttachment `json:"attachments,omitempty"`
	Timestamp   time.Time           `json:"timestamp"`
	IsError     bool                `json:"is_error,omitempty"`
}

// ChatRequest represents an incoming chat request
type ChatRequest struct {
	Message     string              `json:"message"`
	Skills      []string            `json:"skills,omitempty"`
	Attachments []MessageAttachment `json:"attachments,omitempty"`
	RequestID   string              `json:"request_id,omitempty"`
}

type ChatCancelRequest struct {
	RequestID string `json:"request_id"`
}

type ChatCancelResponse struct {
	RequestID string `json:"request_id"`
	Status    string `json:"status"`
}

// ChatResponse represents a chat response
type ChatResponse struct {
	Response string    `json:"response"`
	History  []Message `json:"history"`
}

// chatPendingResult stores intermediate results for async chat requests.
// The agent goroutine appends tool_call / assistant messages as they occur;
// the polling endpoint reads them back with an offset cursor.
type chatPendingResult struct {
	mu       sync.Mutex
	messages []Message
	done     bool
	err      string
	// Per-request history (separate from server-level history)
	history []Message
}

// ChatResultResponse is the JSON shape for GET /api/chat/result.
type ChatResultResponse struct {
	Status   string    `json:"status"`
	Messages []Message `json:"messages,omitempty"`
	Response string    `json:"response,omitempty"`
	History  []Message `json:"history,omitempty"`
	Error    string    `json:"error,omitempty"`
}

type ChatStreamEvent struct {
	Type     string    `json:"type"`
	Message  *Message  `json:"message,omitempty"`
	Response string    `json:"response,omitempty"`
	History  []Message `json:"history,omitempty"`
	Error    string    `json:"error,omitempty"`
}

type EpisodeResponse struct {
	Episode TaskEpisode `json:"episode"`
}

type ToolCatalogResponse struct {
	Tools []ToolDescriptor `json:"tools"`
}

type ToolSkillsResponse struct {
	Skills []ToolSkillDefinition `json:"skills"`
}

type ToolInvokeRequest struct {
	Input    json.RawMessage `json:"input,omitempty"`
	RawInput *string         `json:"raw_input,omitempty"`
}

type ToolInvokeResponse struct {
	Tool       ToolDescriptor `json:"tool"`
	RawInput   string         `json:"raw_input"`
	Output     string         `json:"output"`
	IsError    bool           `json:"is_error"`
	Error      string         `json:"error,omitempty"`
	DurationMs int64          `json:"duration_ms"`
	CalledAt   time.Time      `json:"called_at"`
}

// NewServer creates a new HTTP server
func NewServer(runtime *Runtime, addr string, benchmarkDir string) *Server {
	bridge := NewPhoneBridge(runtime.logger)
	s := &Server{
		runtime:             runtime,
		addr:                addr,
		logger:              runtime.logger,
		benchmarkDir:        benchmarkDir,
		benchmarkPIDFile:    "/tmp/benchmark_runner.pid",
		userFilesReportPath: "/userdata/agent/files_report.html",
		userFilesToolsDir:   "/userdata/agent_tools",
		history:             make([]Message, 0),
		bridge:              bridge,
		pendingResults:      make(map[string]*chatPendingResult),
		activeRuns:          make(map[string]context.CancelFunc),
	}
	if runtime.config.ConfigDir != "" {
		memoryDir := filepath.Join(runtime.config.ConfigDir, "memory")
		s.historyStore = NewChatHistoryStore(filepath.Join(memoryDir, "chat_history"))
		s.episodeStore = NewTaskEpisodeStore(filepath.Join(memoryDir, "episodes"))
	}
	loadAppMappingForConfig(runtime.config.ConfigDir, runtime.logger)
	runtime.tools.RegisterPhoneBridge(bridge)
	s.loadHistoryFromDisk()

	// Initialize speech clients if configured.
	cfg := runtime.config
	s.audioClient = NewAudioServiceClient(cfg.Audio.SocketOrDefault())

	sttClient, err := NewSTTClientFromConfig(cfg)
	if err != nil {
		if s.logger != nil {
			s.logger.Error("STT init failed: %v", err)
		}
	} else {
		s.sttClient = sttClient
	}

	// Initialize the pluggable TTS provider manager from agent.toml fields.
	if manager, err := newTTSProviderManagerFromConfig(cfg, s.logger); err != nil {
		if s.logger != nil {
			s.logger.Warn("TTS init failed: %v", err)
		}
	} else if manager != nil {
		s.ttsManager = manager
		if s.logger != nil {
			s.logger.Info("TTS enabled: provider=%s", manager.Current())
		}
	}

	return s
}

// Start starts the HTTP server
func (s *Server) Start() error {
	mux := http.NewServeMux()

	// API endpoints
	mux.HandleFunc("/api/chat", s.handleChat)
	mux.HandleFunc("/api/chat/cancel", s.handleChatCancel)
	mux.HandleFunc("/api/chat/result", s.handleChatResult)
	mux.HandleFunc("/api/history", s.handleHistory)
	mux.HandleFunc("/api/episodes/", s.handleEpisodes)
	mux.HandleFunc("/api/clear", s.handleClear)
	mux.HandleFunc("/api/clear-all", s.handleClearAll)
	mux.HandleFunc("/api/skills/reload", s.handleSkillsReload)
	mux.HandleFunc("/api/tools", s.handleTools)
	mux.HandleFunc("/api/tools/", s.handleTools)
	mux.HandleFunc("/api/mobilegym/episode/start", s.handleMobileGymEpisodeStart)
	mux.HandleFunc("/api/mobilegym/episode/end", s.handleMobileGymEpisodeEnd)
	mux.HandleFunc("/api/tool-skills", s.handleToolSkills)
	mux.HandleFunc("/api/audio/record/start", s.handleAudioRecordStart)
	mux.HandleFunc("/api/audio/record/stop", s.handleAudioRecordStop)
	mux.HandleFunc("/api/settings/tts", s.handleTTSSettings)
	mux.HandleFunc("/api/tts/providers", s.handleTTSProviders)
	mux.HandleFunc("/api/phone-bridge", s.bridge.HandleWebSocket)
	mux.HandleFunc("/api/phone-bridge/status", s.handleBridgeStatus)
	// HTTP queue endpoints for iOS background compatibility
	mux.HandleFunc("/api/phone-bridge/commands", s.handlePhoneBridgeCommands)
	mux.HandleFunc("/api/phone-bridge/results", s.handlePhoneBridgeResults)
	mux.HandleFunc("/api/phone-bridge/results/", s.handlePhoneBridgeResults)

	// Coordinate debug tool and its screenshot feed. Exposes live screen data,
	// so it is intentionally available on all listen addresses (including the
	// 0.0.0.0:8080 web UI) per deployment requirement.
	mux.HandleFunc("/api/screenshot.jpg", s.handleScreenshotJPEG)
	mux.HandleFunc("/coordinate-debug", s.handleCoordinateDebug)
	mux.HandleFunc("/coordinate-debug.html", s.handleCoordinateDebug)

	// Benchmark endpoints
	mux.HandleFunc("/benchmark", s.handleBenchmarkIndex)
	mux.HandleFunc("/benchmark/record", s.handleBenchmarkRecord)
	mux.HandleFunc("/benchmark/suites", s.handleBenchmarkSuites)
	mux.HandleFunc("/benchmark/runs", s.handleBenchmarkRuns)
	mux.HandleFunc("/benchmark/report/", s.handleBenchmarkReport)
	mux.HandleFunc("/benchmark/status", s.handleBenchmarkStatus)
	mux.HandleFunc("/benchmark/log", s.handleBenchmarkLog)
	mux.HandleFunc("/benchmark/run", s.handleBenchmarkRun)
	mux.HandleFunc("/benchmark/suites/import", s.handleBenchmarkImport)
	mux.HandleFunc("/benchmark/suites/delete", s.handleBenchmarkDelete)
	mux.HandleFunc("/benchmark/suites/generate", s.handleBenchmarkGenerate)
	mux.HandleFunc("/benchmark/suites/generate-perception", s.handleBenchmarkGeneratePerception)
	mux.HandleFunc("/benchmark/suites/import-with-assets", s.handleBenchmarkImportWithAssets)
	mux.HandleFunc("/benchmark/suites/append-perception", s.handleBenchmarkAppendPerception)
	mux.HandleFunc("/user_files", s.handleUserFiles)
	mux.HandleFunc("/user_files/regenerate", s.handleUserFilesRegenerate)

	// Static web UI
	mux.HandleFunc("/", s.handleIndex)

	if s.logger != nil {
		s.logger.Info("Starting HTTP server on %s", s.addr)
	}

	srv := &http.Server{
		Addr:    s.addr,
		Handler: mux,
	}

	errCh := make(chan error, 1)
	go func() {
		err := srv.ListenAndServe()
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
			return
		}
		errCh <- nil
	}()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(sigCh)

	select {
	case err := <-errCh:
		return err
	case sig := <-sigCh:
		if s.logger != nil {
			s.logger.Info("Shutting down HTTP server after signal %s", sig)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := srv.Shutdown(ctx); err != nil {
			return fmt.Errorf("shutdown HTTP server: %w", err)
		}
		return <-errCh
	}
}

func isLoopbackServerAddr(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		host = addr
	}
	host = strings.Trim(host, "[]")
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// handleChat handles chat requests.
// When req.RequestID is present, runs asynchronously: returns {request_id} immediately,
// agent runs in a background goroutine, and intermediate tool_call messages become
// available via GET /api/chat/result?request_id=xxx&offset=N.
func (s *Server) handleChat(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if wantsChatStream(r) {
		s.handleChatStream(w, r)
		return
	}

	var req ChatRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid request", http.StatusBadRequest)
		return
	}
	req.RequestID = strings.TrimSpace(req.RequestID)

	inputText, runAttachments, historyAttachments, err := s.resolveRequestInput(req)
	if err != nil {
		http.Error(w, "Invalid request: "+err.Error(), http.StatusBadRequest)
		return
	}
	episodeID := s.runtime.NewEpisodeID()

	userMsg := Message{
		Type:        "user",
		EpisodeID:   episodeID,
		RequestID:   req.RequestID,
		Content:     inputText,
		Attachments: historyAttachments,
		Timestamp:   time.Now(),
	}

	// Async path — when the client provides a request_id
	if req.RequestID != "" {
		s.handleChatAsync(w, r, req, inputText, runAttachments, userMsg)
		return
	}

	// Sync path — legacy behaviour for web UI and clients without request_id
	s.handleChatSync(w, r, req, inputText, runAttachments, userMsg)
}

func (s *Server) handleChatCancel(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req ChatCancelRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid request", http.StatusBadRequest)
		return
	}
	requestID := strings.TrimSpace(req.RequestID)
	if requestID == "" {
		http.Error(w, "missing request_id", http.StatusBadRequest)
		return
	}

	if s.cancelActiveRun(requestID) {
		if s.logger != nil {
			s.logger.Info("Chat request canceled: request_id=%s", requestID)
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(ChatCancelResponse{RequestID: requestID, Status: "canceled"})
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(ChatCancelResponse{RequestID: requestID, Status: "not_running"})
}

func (s *Server) registerActiveRun(requestID string, cancel context.CancelFunc) bool {
	if requestID == "" {
		return true
	}
	s.activeRunsMu.Lock()
	defer s.activeRunsMu.Unlock()
	if s.activeRuns == nil {
		s.activeRuns = make(map[string]context.CancelFunc)
	}
	if _, exists := s.activeRuns[requestID]; exists {
		return false
	}
	s.activeRuns[requestID] = cancel
	return true
}

func (s *Server) unregisterActiveRun(requestID string) {
	if requestID == "" {
		return
	}
	s.activeRunsMu.Lock()
	delete(s.activeRuns, requestID)
	s.activeRunsMu.Unlock()
}

func (s *Server) cancelActiveRun(requestID string) bool {
	s.activeRunsMu.Lock()
	cancel := s.activeRuns[requestID]
	s.activeRunsMu.Unlock()
	if cancel == nil {
		return false
	}
	cancel()
	return true
}

// handleChatAsync runs the agent in a background goroutine and returns
// {request_id} immediately. Intermediate results are pushed into
// chatPendingResult and served by handleChatResult.
func (s *Server) handleChatAsync(
	w http.ResponseWriter,
	r *http.Request,
	req ChatRequest,
	inputText string,
	runAttachments []InputAttachment,
	userMsg Message,
) {
	requestID := req.RequestID
	if s.logger != nil {
		s.logger.Info("Chat request (async): %s request_id=%s attachments=%d", inputText, requestID, len(runAttachments))
	}

	// Create a pending result slot for this request. Reject a request_id that
	// already has an active slot rather than overwriting it — otherwise the
	// in-flight run's poller loses its result stream and the two goroutines can
	// delete each other's state on exit.
	pending := &chatPendingResult{
		messages: make([]Message, 0),
		history:  []Message{userMsg},
	}
	s.pendingResultsMu.Lock()
	if _, exists := s.pendingResults[requestID]; exists {
		s.pendingResultsMu.Unlock()
		http.Error(w, "request_id already in use", http.StatusConflict)
		return
	}
	s.pendingResults[requestID] = pending
	s.pendingResultsMu.Unlock()

	s.playPromptSoundAsync(promptSoundAgentSend, "agent send")

	runCtx, cancel := context.WithCancel(context.Background())
	if !s.registerActiveRun(requestID, cancel) {
		cancel()
		s.pendingResultsMu.Lock()
		delete(s.pendingResults, requestID)
		s.pendingResultsMu.Unlock()
		http.Error(w, "request_id already in use", http.StatusConflict)
		return
	}
	s.appendHistory(userMsg)

	// Return {request_id} immediately
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(map[string]string{"request_id": requestID})

	// Run agent in background goroutine
	go func() {
		defer func() {
			s.unregisterActiveRun(requestID)
			// Keep the completed result available for polling for 60s,
			// then clean up. The client (PhoneBridge) polls /api/chat/result
			// every ~1s; immediate deletion creates a race where the app
			// gets status="not_found" and hangs forever waiting for "complete".
			time.AfterFunc(60*time.Second, func() {
				s.pendingResultsMu.Lock()
				delete(s.pendingResults, requestID)
				s.pendingResultsMu.Unlock()
			})
		}()

		// Event handler pushes intermediate messages into the pending result
		// so the client can poll them in near-realtime.
		eventHandler := func(event RunEvent) {
			episodeID := event.EpisodeID
			if episodeID == "" {
				episodeID = userMsg.EpisodeID
			}
			msg := Message{
				Type:        event.Type,
				Role:        event.Role,
				EpisodeID:   episodeID,
				RequestID:   requestID,
				Content:     event.Content,
				ToolName:    event.ToolName,
				ToolInput:   event.ToolInput,
				Description: event.Description,
				Timestamp:   event.Timestamp,
				IsError:     event.IsError,
			}
			s.appendHistory(msg)
			pending.mu.Lock()
			pending.history = append(pending.history, msg)
			pending.messages = append(pending.messages, msg)
			pending.mu.Unlock()

			if event.Type == "tool_call" && s.runtime.config.VoiceToolCallSpeechOrDefault() {
				go s.speakToolDescription(runCtx, event.Description)
			}
		}

		runReq := RunRequest{
			Input:          inputText,
			Attachments:    runAttachments,
			Skills:         req.Skills,
			EpisodeID:      userMsg.EpisodeID,
			RuntimeContext: s.runtimeContext(),
			EventHandler:   eventHandler,
		}

		var newStream *streamSessionWriter
		ttsManager := s.currentTTSManager()

		if s.runtime.config.VoiceStreamingTTSEnabledOrDefault() && s.audioClient != nil {
			if ttsManager != nil {
				stream, err := beginManagedTTSStream(runCtx, ttsManager, s.audioClient, s.runtime.config)
				if err != nil {
					if s.logger != nil {
						s.logger.Warn("TTS BeginStream failed: %v", err)
					}
				} else {
					newStream = stream
					runReq.StreamWriter = newStream
				}
			}
		}

		result, err := s.runtime.Run(runCtx, runReq)
		if newStream != nil {
			closeErr := newStream.closeAndWait()
			if closeErr != nil && s.logger != nil {
				s.logger.Error("new TTS stream failed: %v", closeErr)
			}
			result.SpeechStreamed = closeErr == nil && newStream.spoke
		}

		pending.mu.Lock()
		if err != nil {
			if errors.Is(runCtx.Err(), context.Canceled) {
				pending.err = "request canceled"
			} else {
				errorMsg := Message{
					Type:      "episode_status",
					EpisodeID: userMsg.EpisodeID,
					RequestID: requestID,
					Status:    "failed",
					Content:   "Agent error: " + err.Error(),
					Timestamp: time.Now(),
					IsError:   true,
				}
				s.appendHistory(errorMsg)
				pending.history = append(pending.history, errorMsg)
				pending.messages = append(pending.messages, errorMsg)
				pending.err = err.Error()
			}
			pending.done = true
			pending.mu.Unlock()
			if s.logger != nil {
				s.logger.Error("Agent run failed: request_id=%s error=%v", requestID, err)
			}
			return
		}

		// Add assistant message
		assistantMsg := Message{
			Type:      "assistant",
			EpisodeID: firstNonEmptyString([]string{result.EpisodeID, userMsg.EpisodeID}),
			RequestID: requestID,
			Status:    "completed",
			Content:   result.Output,
			Timestamp: time.Now(),
		}
		s.appendHistory(assistantMsg)
		pending.history = append(pending.history, assistantMsg)
		pending.messages = append(pending.messages, assistantMsg)
		pending.done = true
		pending.mu.Unlock()

		if s.logger != nil {
			s.logger.Info("Chat request completed: request_id=%s output_len=%d", requestID, len(result.Output))
		}

		// Play TTS in background
		if s.audioClient != nil && result.Output != "" && !result.SpeechStreamed {
			go func(text string) {
				if s.logger != nil {
					s.logger.Info("TTS playback: %q", text)
				}
				if err := s.speakText(runCtx, text, 0); err != nil && s.logger != nil {
					s.logger.Error("TTS playback failed: %v", err)
				}
			}(result.Output)
		}
	}()
}

// handleChatResult serves incremental results for an async chat request.
// GET /api/chat/result?request_id=xxx&offset=N
//
// When the agent is still running, returns:
//
//	{"status":"running","messages":[...messages since offset...]}
//
// When complete, returns:
//
//	{"status":"complete","response":"...","history":[...]}
func (s *Server) handleChatResult(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	requestID := r.URL.Query().Get("request_id")
	if requestID == "" {
		http.Error(w, "missing request_id", http.StatusBadRequest)
		return
	}

	offset := 0
	if offStr := r.URL.Query().Get("offset"); offStr != "" {
		n, err := strconv.Atoi(offStr)
		if err != nil || n < 0 {
			http.Error(w, "invalid offset", http.StatusBadRequest)
			return
		}
		offset = n
	}

	s.pendingResultsMu.Lock()
	pending := s.pendingResults[requestID]
	s.pendingResultsMu.Unlock()

	if pending == nil {
		// Request not found — may have completed and been cleaned up,
		// or may never have existed.
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(ChatResultResponse{Status: "not_found"})
		return
	}

	pending.mu.Lock()
	defer pending.mu.Unlock()

	if pending.err != "" {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(ChatResultResponse{
			Status: "error",
			Error:  pending.err,
		})
		return
	}

	// Return messages since the client's offset
	msgs := pending.messages
	if offset < len(msgs) {
		msgs = msgs[offset:]
	} else {
		msgs = nil
	}

	if pending.done {
		// Build a full ChatResponse-compatible shape
		historySnapshot := make([]Message, len(pending.history))
		copy(historySnapshot, pending.history)
		// Extract the assistant text from the last assistant message
		response := ""
		for i := len(pending.history) - 1; i >= 0; i-- {
			if pending.history[i].Type == "assistant" {
				response = pending.history[i].Content
				break
			}
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(ChatResultResponse{
			Status:   "complete",
			Response: response,
			History:  historySnapshot,
			Messages: msgs,
		})
	} else {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(ChatResultResponse{
			Status:   "running",
			Messages: msgs,
		})
	}
}

// handleChatSync is the original synchronous chat handler, kept for the web UI
// and any clients that don't provide a request_id.
func (s *Server) handleChatSync(
	w http.ResponseWriter,
	r *http.Request,
	req ChatRequest,
	inputText string,
	runAttachments []InputAttachment,
	userMsg Message,
) {
	ctx := r.Context()
	episodeID := userMsg.EpisodeID

	if s.logger != nil {
		s.logger.Info("Chat request (sync): %s attachments=%d", inputText, len(runAttachments))
	}

	s.playPromptSoundAsync(promptSoundAgentSend, "agent send")
	s.appendHistory(userMsg)

	runReq := RunRequest{
		Input:          inputText,
		Attachments:    runAttachments,
		Skills:         req.Skills,
		EpisodeID:      episodeID,
		RuntimeContext: s.runtimeContext(),
		EventHandler: func(event RunEvent) {
			eventEpisodeID := event.EpisodeID
			if eventEpisodeID == "" {
				eventEpisodeID = episodeID
			}
			s.appendHistory(Message{
				Type:        event.Type,
				Role:        event.Role,
				EpisodeID:   eventEpisodeID,
				Content:     event.Content,
				ToolName:    event.ToolName,
				ToolInput:   event.ToolInput,
				Description: event.Description,
				Timestamp:   event.Timestamp,
				IsError:     event.IsError,
			})
			if event.Type == "tool_call" && s.runtime.config.VoiceToolCallSpeechOrDefault() {
				go s.speakToolDescription(r.Context(), event.Description)
			}
		},
	}

	var newStream *streamSessionWriter
	ttsManager := s.currentTTSManager()

	if s.runtime.config.VoiceStreamingTTSEnabledOrDefault() && s.audioClient != nil {
		if ttsManager != nil {
			stream, err := beginManagedTTSStream(ctx, ttsManager, s.audioClient, s.runtime.config)
			if err != nil {
				if s.logger != nil {
					s.logger.Warn("TTS BeginStream failed: %v", err)
				}
			} else {
				newStream = stream
				runReq.StreamWriter = newStream
			}
		}
	}

	result, err := s.runtime.Run(ctx, runReq)
	if newStream != nil {
		closeErr := newStream.closeAndWait()
		if closeErr != nil && s.logger != nil {
			s.logger.Error("new TTS stream failed: %v", closeErr)
		}
		result.SpeechStreamed = closeErr == nil && newStream.spoke
	}

	if err != nil {
		s.appendHistory(Message{
			Type:      "episode_status",
			EpisodeID: episodeID,
			Status:    "failed",
			Content:   "Agent error: " + err.Error(),
			Timestamp: time.Now(),
			IsError:   true,
		})
		if s.logger != nil {
			s.logger.Error("Agent run failed: %v", err)
		}
		http.Error(w, fmt.Sprintf("Agent error: %v", err), http.StatusInternalServerError)
		return
	}

	s.appendHistory(Message{
		Type:      "assistant",
		EpisodeID: firstNonEmptyString([]string{result.EpisodeID, episodeID}),
		Status:    "completed",
		Content:   result.Output,
		Timestamp: time.Now(),
	})
	historySnapshot := s.historySnapshot()

	if s.audioClient != nil && result.Output != "" && !result.SpeechStreamed {
		go func(text string) {
			if s.logger != nil {
				s.logger.Info("TTS playback: %q", text)
			}
			if err := s.speakText(context.Background(), text, 0); err != nil && s.logger != nil {
				s.logger.Error("TTS playback failed: %v", err)
			}
		}(result.Output)
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(ChatResponse{
		Response: result.Output,
		History:  historySnapshot,
	})
}

func (s *Server) runtimeContext() string {
	if s.bridge == nil {
		return ""
	}
	return phoneBridgeRuntimeContext(s.bridge.Status())
}

func wantsChatStream(r *http.Request) bool {
	return strings.Contains(r.Header.Get("Accept"), "application/x-ndjson") ||
		strings.EqualFold(r.Header.Get("X-Aiden-Stream"), "ndjson") ||
		r.URL.Query().Get("stream") == "1"
}

func (s *Server) handleChatStream(w http.ResponseWriter, r *http.Request) {
	var req ChatRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid request", http.StatusBadRequest)
		return
	}
	req.RequestID = strings.TrimSpace(req.RequestID)

	inputText, runAttachments, historyAttachments, err := s.resolveRequestInput(req)
	if err != nil {
		http.Error(w, "Invalid request: "+err.Error(), http.StatusBadRequest)
		return
	}

	stream, ok := newChatStreamWriter(w)
	if !ok {
		http.Error(w, "Streaming unsupported", http.StatusInternalServerError)
		return
	}

	episodeID := s.runtime.NewEpisodeID()
	userMessage := Message{
		Type:        "user",
		EpisodeID:   episodeID,
		Content:     inputText,
		Attachments: historyAttachments,
		Timestamp:   time.Now(),
	}

	if s.logger != nil {
		s.logger.Info("Chat request: %s attachments=%d stream=ndjson", inputText, len(runAttachments))
	}

	s.playPromptSoundAsync(promptSoundAgentSend, "agent send")

	ctx := r.Context()
	if req.RequestID != "" {
		runCtx, cancel := context.WithCancel(ctx)
		if !s.registerActiveRun(req.RequestID, cancel) {
			cancel()
			http.Error(w, "request_id already in use", http.StatusConflict)
			return
		}
		defer func() {
			s.unregisterActiveRun(req.RequestID)
			cancel()
		}()
		ctx = runCtx
	}
	s.appendHistory(userMessage)

	result, err := s.runtime.Run(ctx, RunRequest{
		Input:          inputText,
		Attachments:    runAttachments,
		Skills:         req.Skills,
		EpisodeID:      episodeID,
		RuntimeContext: s.runtimeContext(),
		EventHandler: func(event RunEvent) {
			eventEpisodeID := event.EpisodeID
			if eventEpisodeID == "" {
				eventEpisodeID = episodeID
			}
			message := Message{
				Type:        event.Type,
				Role:        event.Role,
				EpisodeID:   eventEpisodeID,
				Content:     event.Content,
				ToolName:    event.ToolName,
				ToolInput:   event.ToolInput,
				Description: event.Description,
				Timestamp:   event.Timestamp,
				IsError:     event.IsError,
			}
			s.appendHistory(message)
			stream.Write(ChatStreamEvent{Type: "message", Message: &message})
			if event.Type == "tool_call" && s.runtime.config.VoiceToolCallSpeechOrDefault() {
				go s.speakToolDescription(ctx, event.Description)
			}
		},
	})
	if err != nil {
		errorMessage := Message{
			Type:      "episode_status",
			EpisodeID: episodeID,
			Status:    "failed",
			Content:   "Agent error: " + err.Error(),
			Timestamp: time.Now(),
			IsError:   true,
		}
		s.appendHistory(errorMessage)
		stream.Write(ChatStreamEvent{Type: "message", Message: &errorMessage})
		if s.logger != nil {
			s.logger.Error("Agent run failed: %v", err)
		}
		stream.Write(ChatStreamEvent{Type: "error", Error: err.Error(), History: s.historySnapshot()})
		return
	}

	assistantMessage := Message{
		Type:      "assistant",
		EpisodeID: firstNonEmptyString([]string{result.EpisodeID, episodeID}),
		Status:    "completed",
		Content:   result.Output,
		Timestamp: time.Now(),
	}
	s.appendHistory(assistantMessage)
	stream.Write(ChatStreamEvent{Type: "message", Message: &assistantMessage})
	historySnapshot := s.historySnapshot()

	if s.audioClient != nil && result.Output != "" && !result.SpeechStreamed {
		go func(text string) {
			if s.logger != nil {
				s.logger.Info("TTS playback: %q", text)
			}
			if err := s.speakText(context.Background(), text, 0); err != nil {
				if s.logger != nil {
					s.logger.Error("TTS playback failed: %v", err)
				}
			}
		}(result.Output)
	}

	stream.Write(ChatStreamEvent{
		Type:     "done",
		Response: result.Output,
		History:  historySnapshot,
	})
}

type chatStreamWriter struct {
	encoder *json.Encoder
	flusher http.Flusher
	mu      sync.Mutex
}

func newChatStreamWriter(w http.ResponseWriter) (*chatStreamWriter, bool) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		return nil, false
	}
	header := w.Header()
	header.Set("Content-Type", "application/x-ndjson")
	header.Set("Cache-Control", "no-cache")
	header.Set("X-Accel-Buffering", "no")
	return &chatStreamWriter{
		encoder: json.NewEncoder(w),
		flusher: flusher,
	}, true
}

func (w *chatStreamWriter) Write(event ChatStreamEvent) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if err := w.encoder.Encode(event); err == nil {
		w.flusher.Flush()
	}
}

func (s *Server) speakToolDescription(ctx context.Context, description string) {
	description = strings.TrimSpace(description)
	if description == "" || s.audioClient == nil {
		return
	}
	if ctx == nil {
		ctx = context.Background()
	}
	ttsCtx, cancel := context.WithTimeout(ctx, toolDescriptionSpeechTimeout)
	defer cancel()
	if s.logger != nil {
		s.logger.Info("Tool description TTS playback: %q", description)
	}
	if err := s.speakText(ttsCtx, description, 0); err != nil {
		if s.logger != nil {
			s.logger.Error("Tool description TTS playback failed: %v", err)
		}
	}
}

func (s *Server) currentTTSManager() *tts.ProviderManager {
	s.ttsMu.RLock()
	defer s.ttsMu.RUnlock()
	return s.ttsManager
}

func (s *Server) speakText(ctx context.Context, text string, timeoutAfterLock time.Duration) error {
	if ctx == nil {
		ctx = context.Background()
	}
	manager := s.currentTTSManager()
	if manager != nil {
		cfg := Config{}
		if s.runtime != nil {
			cfg = s.runtime.config
		}
		_, err := speakWithTTSManager(ctx, manager, s.audioClient, cfg, text)
		return err
	}
	return nil
}

func (s *Server) playPromptSoundAsync(kind promptSoundKind, label string) {
	if s.audioClient == nil {
		return
	}
	go func() {
		if err := playPromptSound(context.Background(), s.audioClient, kind, false); err != nil && s.logger != nil {
			s.logger.Error("%s prompt sound failed: %v", label, err)
		}
	}()
}

// handleHistory returns the conversation history
func (s *Server) handleHistory(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	historySnapshot := s.historySnapshot()

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(historySnapshot)
}

func (s *Server) handleEpisodes(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	store := s.episodeStore
	if store == nil && s.runtime != nil && s.runtime.config.ConfigDir != "" {
		store = NewTaskEpisodeStore(filepath.Join(s.runtime.config.ConfigDir, "memory", "episodes"))
	}
	if store == nil {
		http.Error(w, "Episode store is not configured", http.StatusNotFound)
		return
	}
	id := strings.TrimPrefix(r.URL.Path, "/api/episodes/")
	id = strings.Trim(id, "/")
	if id == "" || strings.Contains(id, "/") {
		http.Error(w, "Invalid episode id", http.StatusBadRequest)
		return
	}
	episode, err := store.Get(r.Context(), id)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(EpisodeResponse{Episode: episode})
}

// handleClear clears the conversation history
func (s *Server) handleClear(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	s.mu.Lock()
	s.history = make([]Message, 0)
	s.mu.Unlock()

	if s.logger != nil {
		s.logger.Info("Conversation history cleared")
	}
	if s.historyStore != nil {
		if err := s.historyStore.Clear(); err != nil {
			if s.logger != nil {
				s.logger.Error("Clear chat history failed: %v", err)
			}
			http.Error(w, fmt.Sprintf("Clear chat history failed: %v", err), http.StatusInternalServerError)
			return
		}
	}
	if err := s.runtime.ClearMemory(r.Context()); err != nil {
		if s.logger != nil {
			s.logger.Error("Clear memory failed: %v", err)
		}
		http.Error(w, fmt.Sprintf("Clear memory failed: %v", err), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}

func (s *Server) handleClearAll(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	if err := s.runtime.ClearAllMemory(r.Context()); err != nil {
		if s.logger != nil {
			s.logger.Error("Clear all memory failed: %v", err)
		}
		http.Error(w, fmt.Sprintf("Clear all memory failed: %v", err), http.StatusInternalServerError)
		return
	}

	s.mu.Lock()
	s.history = make([]Message, 0)
	s.mu.Unlock()
	if s.historyStore != nil {
		if err := s.historyStore.Clear(); err != nil {
			if s.logger != nil {
				s.logger.Error("Clear chat history failed: %v", err)
			}
			http.Error(w, fmt.Sprintf("Clear chat history failed: %v", err), http.StatusInternalServerError)
			return
		}
	}
	if s.logger != nil {
		s.logger.Info("All memory cleared")
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}

// handleSkillsReload marks the skill index as dirty so the next agent run
// reloads SKILL.md files from disk. Used by external tooling (e.g.
// benchmark/runner/skillopt) that swaps skill files without going through
// the agent's own skill_manage tool.
func (s *Server) handleSkillsReload(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	s.runtime.MarkSkillsDirty()
	if s.logger != nil {
		s.logger.Info("Skills marked dirty; will reload on next run")
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}

func (s *Server) handleAudioRecordStart(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if s.audioClient == nil {
		http.Error(w, "audio service client is unavailable", http.StatusServiceUnavailable)
		return
	}

	s.recordMu.Lock()
	if s.webRecording != nil {
		stale := s.webRecording
		s.webRecording = nil
		if s.logger != nil {
			s.logger.Warn("Clearing stale web audio recording before start")
		}
		if err := s.endStaleWebRecording(stale); err != nil && s.logger != nil {
			s.logger.Warn("Stale web audio recording cleanup failed: %v", err)
		}
	}
	s.recordMu.Unlock()

	sampleRate := s.runtime.config.Audio.SampleRateOrDefault()
	result, err := s.audioClient.StartRecording(AudioFormat{
		SampleRate: uint32(sampleRate),
		Channels:   1,
		BitWidth:   16,
	})
	if err != nil {
		if s.logger != nil {
			s.logger.Error("Web audio recording start failed: %v", err)
		}
		http.Error(w, "start device audio recording: "+err.Error(), http.StatusInternalServerError)
		return
	}
	go func() {
		if err := playPromptSound(context.Background(), s.audioClient, promptSoundRecordingStart, true); err != nil && s.logger != nil {
			s.logger.Error("Recording prompt sound failed: %v", err)
		}
	}()

	recording := &webAudioRecording{
		sessionID:  result.SessionID,
		sampleRate: sampleRate,
		done:       make(chan struct{}),
		samples:    make([]int16, 0, sampleRate),
	}

	s.recordMu.Lock()
	if s.webRecording != nil {
		s.recordMu.Unlock()
		if err := s.endStaleWebRecording(recording); err != nil && s.logger != nil {
			s.logger.Warn("Orphaned web audio recording cleanup failed: %v", err)
		}
		http.Error(w, "audio recording is already active", http.StatusConflict)
		return
	}
	s.webRecording = recording
	s.recordMu.Unlock()

	go s.readWebAudioRecording(recording)

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(AudioRecordStartResponse{
		Status:     "recording",
		SampleRate: sampleRate,
	})
}

func (s *Server) handleAudioRecordStop(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if s.audioClient == nil {
		http.Error(w, "audio service client is unavailable", http.StatusServiceUnavailable)
		return
	}

	s.recordMu.Lock()
	recording := s.webRecording
	if recording == nil {
		s.recordMu.Unlock()
		http.Error(w, "audio recording is not active", http.StatusBadRequest)
		return
	}
	s.recordMu.Unlock()

	if err := s.endWebRecording(recording); err != nil {
		if s.logger != nil {
			s.logger.Error("Web audio recording stop failed: %v", err)
		}
		http.Error(w, "stop device audio recording: "+err.Error(), http.StatusInternalServerError)
		s.recordMu.Lock()
		if s.webRecording == recording {
			s.webRecording = nil
		}
		s.recordMu.Unlock()
		return
	}

	s.recordMu.Lock()
	if s.webRecording == recording {
		s.webRecording = nil
	}
	s.recordMu.Unlock()

	samples := recording.snapshotSamples()
	wavData := pcm16MonoToWAV(samples, recording.sampleRate)
	attachment := MessageAttachment{
		Kind:     AttachmentKindAudio,
		Name:     "recording.wav",
		MIMEType: "audio/wav",
		Data:     base64.StdEncoding.EncodeToString(wavData),
		Size:     len(wavData),
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(AudioRecordStopResponse{Attachment: attachment})
}

const (
	webRecordingDrainTimeout        = 7 * time.Second
	webRecordingStaleCleanupTimeout = 200 * time.Millisecond
)

func (s *Server) endWebRecording(recording *webAudioRecording) error {
	return s.endWebRecordingWithTimeout(recording, webRecordingDrainTimeout)
}

func (s *Server) endStaleWebRecording(recording *webAudioRecording) error {
	return s.endWebRecordingWithTimeout(recording, webRecordingStaleCleanupTimeout)
}

func (s *Server) endWebRecordingWithTimeout(recording *webAudioRecording, timeout time.Duration) error {
	if recording == nil {
		return nil
	}
	recording.setStopping()
	stopErr := s.audioClient.StopRecording(recording.sessionID)
	select {
	case <-recording.done:
	case <-time.After(timeout):
		if stopErr == nil {
			stopErr = fmt.Errorf("timed out waiting for audio recording to drain")
		}
	}
	if err := recording.readError(); err != nil {
		return err
	}
	return stopErr
}

func (s *Server) readWebAudioRecording(recording *webAudioRecording) {
	defer close(recording.done)

	for {
		chunk, err := s.audioClient.ReadRecordChunk(recording.sessionID, 1000)
		if err != nil {
			if !recording.isStopping() {
				recording.setError(err)
			}
			return
		}
		if len(chunk.PCM) > 0 {
			recording.appendPCM(chunk.PCM)
		}
		if chunk.EndOfStream {
			return
		}
	}
}

func (r *webAudioRecording) appendPCM(pcm []byte) {
	r.mu.Lock()
	defer r.mu.Unlock()
	AppendPCM16Samples(pcm, &r.samples, &r.hasPending, &r.pending)
}

func (r *webAudioRecording) setStopping() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.stopping = true
}

func (r *webAudioRecording) isStopping() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.stopping
}

func (r *webAudioRecording) setError(err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.err = err
}

func (r *webAudioRecording) readError() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.err
}

func (r *webAudioRecording) snapshotSamples() []int16 {
	r.mu.Lock()
	defer r.mu.Unlock()
	samples := make([]int16, len(r.samples))
	copy(samples, r.samples)
	return samples
}

func (s *Server) handleTools(w http.ResponseWriter, r *http.Request) {
	switch {
	case r.URL.Path == "/api/tools":
		s.handleToolCatalog(w, r)
	case strings.HasPrefix(r.URL.Path, "/api/tools/"):
		s.handleToolInvoke(w, r)
	default:
		http.NotFound(w, r)
	}
}

func (s *Server) handleToolCatalog(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(ToolCatalogResponse{
		Tools: s.runtime.ToolDescriptors(),
	})
}

func (s *Server) handleToolInvoke(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	toolName := strings.TrimPrefix(r.URL.Path, "/api/tools/")
	toolName = strings.TrimSpace(toolName)
	if toolName == "" || strings.Contains(toolName, "/") || !isHTTPToolExposed(toolName) {
		http.NotFound(w, r)
		return
	}

	tool, descriptor, ok := s.lookupOwnedTool(toolName)
	if !ok {
		http.Error(w, fmt.Sprintf("Unknown tool: %s", toolName), http.StatusNotFound)
		return
	}

	rawInput, err := decodeToolInvokeInput(r.Body)
	if err != nil {
		http.Error(w, "Invalid request: "+err.Error(), http.StatusBadRequest)
		return
	}

	startedAt := time.Now()
	output, callErr := tool.Call(r.Context(), rawInput)
	duration := time.Since(startedAt).Milliseconds()

	response := ToolInvokeResponse{
		Tool:       descriptor,
		RawInput:   rawInput,
		Output:     output,
		IsError:    callErr != nil || toolOutputLooksLikeError(output),
		DurationMs: duration,
		CalledAt:   startedAt,
	}
	if callErr != nil {
		response.Error = callErr.Error()
		if response.Output == "" {
			response.Output = callErr.Error()
		}
	}

	if s.logger != nil {
		s.logger.Info("HTTP tool invoke: name=%s error=%v", toolName, response.IsError)
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(response)
}

func (s *Server) handleToolSkills(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(ToolSkillsResponse{
		Skills: s.runtime.HTTPToolSkills(requestBaseURL(r)),
	})
}

type mobileGymEpisodeStartRequest struct {
	EpisodeID   string `json:"episode_id"`
	BridgeURL   string `json:"bridge_url"`
	BridgeToken string `json:"bridge_token"`
}

type mobileGymEpisodeEndRequest struct {
	EpisodeID string `json:"episode_id"`
}

func (s *Server) handleMobileGymEpisodeStart(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !s.authorizeMobileGymControl(w, r) {
		return
	}

	var req mobileGymEpisodeStartRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid request: "+err.Error(), http.StatusBadRequest)
		return
	}
	req.EpisodeID = strings.TrimSpace(req.EpisodeID)
	req.BridgeURL = strings.TrimSpace(req.BridgeURL)
	req.BridgeToken = strings.TrimSpace(req.BridgeToken)
	if req.EpisodeID == "" || req.BridgeURL == "" || req.BridgeToken == "" {
		http.Error(w, "episode_id, bridge_url, and bridge_token are required", http.StatusBadRequest)
		return
	}
	if s.runtime.mobileGym == nil {
		s.runtime.mobileGym = &mobileGymSessionStore{}
	}
	s.runtime.mobileGym.Set(mobileGymSession{EpisodeID: req.EpisodeID, BridgeURL: req.BridgeURL, BridgeToken: req.BridgeToken})

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}

func (s *Server) handleMobileGymEpisodeEnd(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !s.authorizeMobileGymControl(w, r) {
		return
	}

	var req mobileGymEpisodeEndRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid request: "+err.Error(), http.StatusBadRequest)
		return
	}
	req.EpisodeID = strings.TrimSpace(req.EpisodeID)
	if req.EpisodeID == "" {
		http.Error(w, "episode_id is required", http.StatusBadRequest)
		return
	}
	if s.runtime.mobileGym != nil {
		s.runtime.mobileGym.Clear(req.EpisodeID)
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}

func (s *Server) authorizeMobileGymControl(w http.ResponseWriter, r *http.Request) bool {
	path := strings.TrimSpace(s.runtime.config.Device.ControlTokenFile)
	if path == "" {
		http.Error(w, "mobilegym control token is not configured", http.StatusUnauthorized)
		return false
	}
	data, err := os.ReadFile(path)
	if err != nil {
		http.Error(w, "mobilegym control token is unavailable", http.StatusUnauthorized)
		return false
	}
	expected := strings.TrimSpace(string(data))
	if expected == "" {
		http.Error(w, "mobilegym control token is empty", http.StatusUnauthorized)
		return false
	}
	auth := strings.TrimSpace(r.Header.Get("Authorization"))
	if !strings.HasPrefix(auth, "Bearer ") || strings.TrimSpace(strings.TrimPrefix(auth, "Bearer ")) != expected {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return false
	}
	return true
}

func requestBaseURL(r *http.Request) string {
	if r == nil {
		return ""
	}

	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	if forwardedProto := firstForwardedHeaderValue(r.Header.Get("X-Forwarded-Proto")); forwardedProto != "" {
		scheme = forwardedProto
	}

	host := firstForwardedHeaderValue(r.Header.Get("X-Forwarded-Host"))
	if host == "" {
		host = strings.TrimSpace(r.Host)
	}
	if host == "" {
		return ""
	}
	return scheme + "://" + host
}

func firstForwardedHeaderValue(value string) string {
	if value == "" {
		return ""
	}
	parts := strings.Split(value, ",")
	if len(parts) == 0 {
		return ""
	}
	return strings.TrimSpace(parts[0])
}

func (s *Server) lookupOwnedTool(name string) (tool langtools.Tool, descriptor ToolDescriptor, ok bool) {
	for _, candidate := range s.runtime.OwnedTools() {
		if candidate.Name() != name {
			continue
		}
		descriptor, _ = s.runtime.ToolDescriptorByName(name)
		return candidate, descriptor, true
	}
	return nil, ToolDescriptor{}, false
}

func decodeToolInvokeInput(body io.Reader) (string, error) {
	if body == nil {
		return "", nil
	}

	var req ToolInvokeRequest
	decoder := json.NewDecoder(body)
	if err := decoder.Decode(&req); err != nil {
		if errors.Is(err, io.EOF) {
			return "", nil
		}
		return "", err
	}

	if req.RawInput != nil {
		return *req.RawInput, nil
	}

	trimmed := strings.TrimSpace(string(req.Input))
	if trimmed == "" || trimmed == "null" {
		return "", nil
	}

	if strings.HasPrefix(trimmed, `"`) {
		var plain string
		if err := json.Unmarshal(req.Input, &plain); err == nil {
			return plain, nil
		}
	}

	return trimmed, nil
}

func toolOutputLooksLikeError(output string) bool {
	return strings.HasPrefix(strings.ToLower(strings.TrimSpace(output)), "error:")
}

func (s *Server) appendHistory(message Message) {
	s.mu.Lock()
	s.history = append(s.history, message)
	s.mu.Unlock()
	if s.historyStore != nil {
		if err := s.historyStore.Append(context.Background(), message); err != nil && s.logger != nil {
			s.logger.Warn("Persist chat history failed: %v", err)
		}
	}
}

func (s *Server) historySnapshot() []Message {
	s.mu.Lock()
	defer s.mu.Unlock()
	historySnapshot := make([]Message, len(s.history))
	copy(historySnapshot, s.history)
	return historySnapshot
}

func (s *Server) loadHistoryFromDisk() {
	if s.runtime.config.ConfigDir == "" {
		return
	}
	if s.historyStore != nil {
		messages, err := s.historyStore.Load(context.Background())
		if err == nil && len(messages) > 0 {
			s.history = append(s.history, messages...)
			return
		}
	}
	eventsPath := filepath.Join(s.runtime.config.ConfigDir, "memory", "session", "events.jsonl")
	store := NewSessionMemoryStore(filepath.Join(s.runtime.config.ConfigDir, "memory", "session"))
	events, err := store.readEvents(eventsPath)
	if err != nil {
		return
	}
	for _, evt := range events {
		msgType := "user"
		switch evt.Role {
		case "assistant":
			msgType = "assistant"
		case "tool":
			msgType = "tool_result"
		case "system":
			msgType = "system"
		}
		ts, _ := time.Parse(time.RFC3339Nano, evt.Ts)
		s.history = append(s.history, Message{
			Type:      msgType,
			ToolName:  evt.Source,
			ToolInput: evt.ToolCallID,
			Content:   evt.Content,
			Timestamp: ts,
		})
	}
}

func (s *Server) resolveRequestInput(req ChatRequest) (string, []InputAttachment, []MessageAttachment, error) {
	decodedAttachments, historyAttachments, err := decodeMessageAttachments(req.Attachments)
	if err != nil {
		return "", nil, nil, err
	}

	audioAttachment, nonAudioAttachments, err := splitAudioAttachment(decodedAttachments)
	if err != nil {
		return "", nil, nil, err
	}

	trimmedMessage := strings.TrimSpace(req.Message)
	if audioAttachment != nil {
		audioInput, err := PrepareAudioInput(s.webAudioInputMode(), s.sttClient, audioAttachment.Data, trimmedMessage, nonAudioAttachments)
		if err != nil {
			return "", nil, nil, err
		}
		if audioInput.Transcript != "" {
			for i := range historyAttachments {
				if historyAttachments[i].Kind == AttachmentKindAudio {
					historyAttachments[i].Transcript = audioInput.Transcript
					break
				}
			}
		}
		return audioInput.InputText, audioInput.Attachments, historyAttachments, nil
	}

	inputText := normalizeRunInput(trimmedMessage, nonAudioAttachments)
	if inputText == "" {
		return "", nil, nil, fmt.Errorf("message or attachment is required")
	}
	return inputText, nonAudioAttachments, historyAttachments, nil
}

func (s *Server) webAudioInputMode() string {
	switch s.runtime.config.InputModeOrDefault() {
	case "stt", "audio":
		return s.runtime.config.InputModeOrDefault()
	default:
		if s.sttClient != nil {
			return "stt"
		}
		return "audio"
	}
}

func decodeMessageAttachments(payloads []MessageAttachment) ([]InputAttachment, []MessageAttachment, error) {
	decoded := make([]InputAttachment, 0, len(payloads))
	history := make([]MessageAttachment, 0, len(payloads))

	for _, payload := range payloads {
		data := strings.TrimSpace(payload.Data)
		if data == "" {
			return nil, nil, fmt.Errorf("attachment %q is missing data", payload.Name)
		}

		raw, err := base64.StdEncoding.DecodeString(data)
		if err != nil {
			return nil, nil, fmt.Errorf("attachment %q is not valid base64", payload.Name)
		}

		kind := strings.ToLower(strings.TrimSpace(payload.Kind))
		if kind == "" {
			switch {
			case strings.HasPrefix(payload.MIMEType, "image/"):
				kind = AttachmentKindImage
			case strings.HasPrefix(payload.MIMEType, "audio/"):
				kind = AttachmentKindAudio
			default:
				kind = "file"
			}
		}

		decoded = append(decoded, InputAttachment{
			Kind:     kind,
			Name:     payload.Name,
			MIMEType: payload.MIMEType,
			Data:     raw,
		})

		history = append(history, MessageAttachment{
			Kind:     kind,
			Name:     payload.Name,
			MIMEType: payload.MIMEType,
			Data:     payload.Data,
			Size:     len(raw),
		})
	}

	return decoded, history, nil
}

func splitAudioAttachment(attachments []InputAttachment) (*InputAttachment, []InputAttachment, error) {
	var audio *InputAttachment
	others := make([]InputAttachment, 0, len(attachments))

	for i := range attachments {
		attachment := attachments[i]
		if attachment.Kind == AttachmentKindAudio {
			if audio != nil {
				return nil, nil, fmt.Errorf("only one audio attachment is supported per request")
			}
			copyAttachment := attachment
			audio = &copyAttachment
			continue
		}
		others = append(others, attachment)
	}

	return audio, others, nil
}

func (s *Server) handleBridgeStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	if s.bridge == nil {
		json.NewEncoder(w).Encode(PhoneBridgeStatus{})
		return
	}
	json.NewEncoder(w).Encode(s.bridge.Status())
}

// handlePhoneBridgeCommands routes /api/phone-bridge/commands
func (s *Server) handlePhoneBridgeCommands(w http.ResponseWriter, r *http.Request) {
	if s.bridge == nil {
		http.Error(w, `{"error":"phone bridge not initialized"}`, http.StatusServiceUnavailable)
		return
	}
	switch r.Method {
	case http.MethodPost:
		s.bridge.handleEnqueueCommand(w, r)
	case http.MethodGet:
		s.bridge.handlePollCommands(w, r)
	default:
		http.Error(w, `{"error":"Method not allowed"}`, http.StatusMethodNotAllowed)
	}
}

// handlePhoneBridgeResults routes /api/phone-bridge/results and /api/phone-bridge/results/:id
func (s *Server) handlePhoneBridgeResults(w http.ResponseWriter, r *http.Request) {
	if s.bridge == nil {
		http.Error(w, `{"error":"phone bridge not initialized"}`, http.StatusServiceUnavailable)
		return
	}
	if r.URL.Path == "/api/phone-bridge/results" {
		if r.Method == http.MethodPost {
			s.bridge.handleSubmitResult(w, r)
		} else {
			http.Error(w, `{"error":"Method not allowed"}`, http.StatusMethodNotAllowed)
		}
	} else if strings.HasPrefix(r.URL.Path, "/api/phone-bridge/results/") {
		if r.Method == http.MethodGet {
			s.bridge.handleQueryResult(w, r)
		} else {
			http.Error(w, `{"error":"Method not allowed"}`, http.StatusMethodNotAllowed)
		}
	} else {
		http.NotFound(w, r)
	}
}

// handleIndex serves the web UI
func (s *Server) handleIndex(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	html := webUI
	w.Write([]byte(html))
}

const coordinateDebugNavLink = `<a href="/coordinate-debug" class="new-chat-btn" style="text-decoration:none;display:inline-flex;align-items:center;">🎯 坐标调试</a>`

const webUI = `<!DOCTYPE html>
<html lang="en">
<head>
    <meta charset="UTF-8">
    <meta name="viewport" content="width=device-width, initial-scale=1.0">
    <title>Aiden Agent</title>
    <style>
        :root {
            color-scheme: light;
            --bg: #f1ede2;
            --panel: rgba(255, 251, 245, 0.92);
            --panel-strong: rgba(255, 254, 250, 0.97);
            --line: rgba(52, 56, 49, 0.14);
            --text: #1e241d;
            --muted: #697063;
            --muted-strong: #43493d;
            --accent: #1f7a63;
            --accent-strong: #155646;
            --surface-soft: #efe7da;
            --surface-code: #f6f0e6;
            --surface-contrast: #20241f;
            --user-bubble: #fffdf8;
            --tool-call: rgba(183, 107, 47, 0.15);
            --tool-call-text: #8e4d1d;
            --tool-ok: rgba(31, 122, 99, 0.14);
            --tool-ok-text: #155646;
            --tool-error: rgba(190, 67, 52, 0.14);
            --tool-error-text: #a0382a;
            --shadow: 0 24px 48px rgba(43, 47, 40, 0.14);
            --shadow-soft: 0 12px 26px rgba(43, 47, 40, 0.08);
            --radius-xl: 26px;
            --radius-lg: 20px;
            --radius-md: 14px;
            --dock-width: 380px;
        }

        * {
            box-sizing: border-box;
            margin: 0;
            padding: 0;
        }

        html, body {
            min-height: 100%;
        }

        body {
            font-family: "IBM Plex Sans", "Avenir Next", "Segoe UI", sans-serif;
            background:
                linear-gradient(rgba(255, 255, 255, 0.3) 1px, transparent 1px),
                linear-gradient(90deg, rgba(255, 255, 255, 0.3) 1px, transparent 1px),
                radial-gradient(circle at top left, rgba(31, 122, 99, 0.12), transparent 22%),
                radial-gradient(circle at bottom right, rgba(183, 107, 47, 0.12), transparent 20%),
                var(--bg);
            background-size: 28px 28px, 28px 28px, auto, auto, auto;
            background-position: 0 0, 0 0, 0 0, 100% 100%, 0 0;
            color: var(--text);
        }

        button,
        textarea,
        select {
            font: inherit;
        }

        button:not(:focus-visible),
        select:not(:focus-visible),
        textarea:not(:focus-visible) {
            outline: none;
        }

        button:focus-visible,
        select:focus-visible,
        textarea:focus-visible {
            outline: 2px solid var(--accent);
            outline-offset: 2px;
        }

        .app-shell {
            height: 100vh;
            display: grid;
            grid-template-columns: minmax(0, 1fr) var(--dock-width);
            gap: 12px;
            padding: 12px;
        }

        .inspector {
            display: flex;
            flex-direction: column;
            gap: 12px;
            min-height: 0;
            overflow-y: auto;
            padding: 14px;
            border-radius: var(--radius-xl);
            background: linear-gradient(180deg, rgba(255, 254, 250, 0.9), rgba(241, 235, 224, 0.82));
            border: 1px solid var(--line);
            backdrop-filter: blur(18px) saturate(1.05);
            box-shadow: var(--shadow-soft);
        }

        .message-role,
        .tool-section-label,
        .tool-meta-key {
            font-size: 0.68rem;
            font-weight: 700;
            letter-spacing: 0.12em;
            text-transform: uppercase;
        }

        .hidden {
            display: none !important;
        }

        .sidebar-card {
            padding: 14px;
            border-radius: 18px;
            background: rgba(255, 251, 245, 0.82);
            border: 1px solid rgba(52, 56, 49, 0.08);
            box-shadow: inset 0 1px 0 rgba(255, 255, 255, 0.32);
        }

        .sidebar-note,
        .composer-hint,
        .message-time,
        .empty-state p {
            color: var(--muted);
        }

        .sidebar-note {
            line-height: 1.45;
            font-size: 0.82rem;
        }

        .sidebar-kicker {
            font-size: 0.68rem;
            font-weight: 700;
            letter-spacing: 0.12em;
            text-transform: uppercase;
        }

        .sidebar-card strong {
            display: block;
            margin: 0.22rem 0 0.45rem;
            font-size: 0.95rem;
            letter-spacing: -0.01em;
        }

        .tool-lab,
        .skill-export {
            display: flex;
            flex-direction: column;
            gap: 9px;
        }

        .tool-lab-label {
            font-size: 0.74rem;
            font-weight: 600;
            color: var(--muted-strong);
        }

        .tool-lab-select,
        .tool-lab-input {
            width: 100%;
            border-radius: 14px;
            border: 1px solid rgba(52, 56, 49, 0.12);
            background: rgba(255, 255, 255, 0.88);
            color: var(--text);
        }

        .tool-lab-select {
            padding: 0.68rem 0.78rem;
        }

        .tool-lab-input {
            min-height: 104px;
            padding: 11px 12px;
            resize: vertical;
            line-height: 1.5;
            font-family: "IBM Plex Mono", "SFMono-Regular", Consolas, monospace;
            font-size: 0.82rem;
        }

        .tool-lab-input[readonly] {
            background: rgba(248, 244, 238, 0.95);
        }

        .tool-lab-row {
            display: flex;
            gap: 8px;
            flex-wrap: wrap;
        }

        .tool-secondary-btn,
        .tool-run-btn {
            flex: 1 1 0;
            justify-content: center;
        }

        .tool-lab-status {
            font-size: 0.76rem;
            color: var(--muted);
            line-height: 1.5;
        }

        .tool-lab-status.error {
            color: var(--tool-error-text);
        }

        .tool-lab-result {
            display: flex;
            flex-direction: column;
            gap: 10px;
        }

        .tool-lab-result-meta {
            font-size: 0.76rem;
            color: var(--muted-strong);
        }

        .tool-lab-preview img {
            width: 100%;
            border-radius: 14px;
            border: 1px solid rgba(52, 56, 49, 0.08);
            background: #ffffff;
        }

        .main-panel {
            min-height: 0;
            display: flex;
            flex-direction: column;
            border-radius: calc(var(--radius-xl) + 2px);
            background: linear-gradient(180deg, rgba(255, 252, 247, 0.96), rgba(248, 242, 233, 0.9));
            border: 1px solid rgba(255, 255, 255, 0.48);
            backdrop-filter: blur(22px);
            box-shadow: var(--shadow);
            overflow: hidden;
        }

        .topbar {
            display: flex;
            justify-content: space-between;
            gap: 14px;
            align-items: center;
            padding: 12px 20px;
            border-bottom: 1px solid var(--line);
            background:
                linear-gradient(120deg, rgba(255, 255, 255, 0.84), rgba(255, 248, 239, 0.76)),
                linear-gradient(90deg, rgba(31, 122, 99, 0.06), rgba(183, 107, 47, 0.08));
        }

        .topbar h1 {
            font-size: 1.15rem;
            font-weight: 700;
            letter-spacing: -0.02em;
        }

        .topbar-actions {
            display: flex;
            align-items: center;
            gap: 8px;
        }

        .new-chat-btn {
            display: inline-flex;
            align-items: center;
            padding: 0.55rem 0.9rem;
            border: 1px solid var(--line);
            border-radius: 999px;
            background: linear-gradient(180deg, #20241f 0%, #31382f 100%);
            color: #f5efe6;
            font-weight: 600;
            font-size: 0.82rem;
            cursor: pointer;
            transition: transform 120ms ease, box-shadow 120ms ease;
        }

        .new-chat-btn:hover,
        .new-chat-btn:focus-visible,
        .send-btn:hover,
        .send-btn:focus-visible {
            transform: translateY(-1px);
            box-shadow: 0 12px 20px rgba(43, 47, 40, 0.12);
        }

        .conversation {
            flex: 1;
            min-height: 0;
            overflow-y: auto;
            padding: 10px 18px 0;
            background:
                linear-gradient(180deg, rgba(255, 255, 255, 0.24), transparent 120px),
                transparent;
        }

        .conversation-inner {
            width: 100%;
            min-height: 100%;
            display: flex;
            flex-direction: column;
        }

        .empty-state {
            margin: auto;
            width: min(100%, 640px);
            padding: 32px 30px;
            text-align: center;
        }

        .empty-state h3 {
            font-size: 1.4rem;
            letter-spacing: -0.03em;
            color: var(--muted-strong);
        }

        .empty-state p {
            margin-top: 0.4rem;
            color: var(--muted);
            font-size: 0.92rem;
            line-height: 1.45;
        }

        .empty-state.hidden {
            display: none;
        }

        .message-list {
            display: grid;
            gap: 14px;
            padding: 10px 0 16px;
        }

        .message {
            width: 100%;
        }

        .message-shell {
            display: flex;
            align-items: flex-start;
            gap: 10px;
        }

        .message.user .message-shell {
            justify-content: flex-end;
        }

        .message.user .message-avatar {
            order: 2;
            background: rgba(16, 163, 127, 0.12);
            color: var(--accent-strong);
        }

        .message.user .message-body {
            order: 1;
            max-width: min(78%, 860px);
            background: var(--user-bubble);
            border: 1px solid var(--line);
            border-radius: 18px 18px 8px 18px;
            padding: 13px 15px;
            box-shadow: var(--shadow-soft);
        }

        .message.assistant .message-body {
            max-width: min(100%, 1120px);
            flex: 1;
            padding: 1px 2px 0 0;
        }

        .message.tool_call .message-body,
        .message.role_output .message-body,
        .message.tool_result .message-body {
            flex: 1;
            background: rgba(255, 255, 255, 0.68);
            border: 1px solid var(--line);
            border-radius: 18px;
            padding: 14px 15px;
            box-shadow: var(--shadow-soft);
        }

        .message.role_output .message-body {
            background: rgba(248, 251, 255, 0.82);
        }

        .message-avatar {
            flex: 0 0 34px;
            width: 34px;
            height: 34px;
            display: inline-flex;
            align-items: center;
            justify-content: center;
            border-radius: 11px;
            background: var(--surface-contrast);
            color: #f9f4eb;
            font-size: 0.78rem;
            font-weight: 700;
        }

        .message-role {
            margin-bottom: 0.42rem;
            color: var(--muted-strong);
        }

        .message-copy {
            line-height: 1.62;
            white-space: pre-wrap;
            word-break: break-word;
            font-size: 0.95rem;
        }

        .message.assistant .message-copy {
            font-size: 0.96rem;
        }

        .message-attachments {
            display: grid;
            gap: 10px;
            margin-bottom: 10px;
        }

        .message-attachments:last-child {
            margin-bottom: 0;
        }

        .attachment-card {
            display: flex;
            flex-direction: column;
            gap: 8px;
            padding: 10px;
            border-radius: 14px;
            background: rgba(243, 237, 228, 0.78);
            border: 1px solid rgba(52, 56, 49, 0.06);
        }

        .attachment-card img,
        .attachment-card audio {
            width: 100%;
            border-radius: 14px;
            background: #ffffff;
        }

        .attachment-card img {
            max-width: min(100%, 420px);
            border: 1px solid rgba(52, 56, 49, 0.08);
        }

        .attachment-meta {
            display: flex;
            align-items: center;
            justify-content: space-between;
            gap: 8px;
            flex-wrap: wrap;
            font-size: 0.8rem;
            color: var(--muted-strong);
        }

        .attachment-kind {
            display: inline-flex;
            align-items: center;
            padding: 0.22rem 0.5rem;
            border-radius: 999px;
            background: rgba(16, 163, 127, 0.12);
            color: var(--accent-strong);
            font-size: 0.68rem;
            font-weight: 700;
            letter-spacing: 0.08em;
            text-transform: uppercase;
        }

        .attachment-transcript {
            padding: 9px 10px;
            border-radius: 12px;
            background: rgba(255, 255, 255, 0.85);
            border: 1px solid rgba(52, 56, 49, 0.06);
            line-height: 1.55;
            color: var(--text);
        }

        .message-time {
            margin-top: 0.55rem;
            font-size: 0.74rem;
        }

        .tool-card {
            display: flex;
            flex-direction: column;
            gap: 12px;
        }

        .tool-card-header,
        .tool-card-title,
        .composer-footer,
        .composer-actions,
        .loading {
            display: flex;
            align-items: center;
        }

        .tool-card-header,
        .composer-footer {
            justify-content: space-between;
            gap: 10px;
            flex-wrap: wrap;
        }

        .tool-card-title,
        .composer-actions {
            gap: 8px;
        }

        .tool-label {
            display: inline-flex;
            align-items: center;
            padding: 0.26rem 0.54rem;
            border-radius: 999px;
            font-size: 0.68rem;
            font-weight: 700;
            letter-spacing: 0.08em;
            text-transform: uppercase;
        }

        .tool-call-label {
            background: var(--tool-call);
            color: var(--tool-call-text);
        }

        .tool-result-label {
            background: var(--tool-ok);
            color: var(--tool-ok-text);
        }

        .tool-result-label.error {
            background: var(--tool-error);
            color: var(--tool-error-text);
        }

        .tool-name {
            font-size: 0.94rem;
            font-weight: 700;
        }

        .tool-section {
            display: flex;
            flex-direction: column;
            gap: 7px;
        }

        .tool-section-label,
        .tool-meta-key {
            color: var(--muted);
        }

        .tool-block {
            overflow-x: auto;
            padding: 12px 13px;
            border-radius: 14px;
            border: 1px solid rgba(52, 56, 49, 0.08);
            background: var(--surface-code);
            color: var(--text);
            font-family: "IBM Plex Mono", "SFMono-Regular", Consolas, monospace;
            font-size: 0.81rem;
            line-height: 1.5;
            white-space: pre-wrap;
            word-break: break-word;
        }

        .tool-meta-grid {
            display: grid;
            grid-template-columns: repeat(auto-fit, minmax(110px, 1fr));
            gap: 8px;
        }

        .tool-meta-item {
            padding: 10px 12px;
            border-radius: 14px;
            background: var(--surface-soft);
            border: 1px solid rgba(52, 56, 49, 0.06);
        }

        .tool-meta-value {
            margin-top: 0.2rem;
            font-size: 0.92rem;
            font-weight: 700;
        }

        .screenshot-preview {
            display: flex;
            flex-direction: column;
            gap: 8px;
        }

        .screenshot-preview img {
            width: 100%;
            max-width: min(100%, 880px);
            border-radius: 14px;
            border: 1px solid rgba(52, 56, 49, 0.08);
            box-shadow: var(--shadow-soft);
            background: #ffffff;
        }

        details.tool-details {
            border-top: 1px solid var(--line);
            padding-top: 10px;
        }

        details.tool-details summary {
            cursor: pointer;
            color: var(--muted-strong);
            font-weight: 600;
            font-size: 0.84rem;
        }

        .episode-link {
            margin-top: 0.65rem;
            display: inline-flex;
            align-items: center;
            gap: 6px;
            padding: 0.42rem 0.7rem;
            border: 1px solid var(--line);
            border-radius: 999px;
            background: rgba(255, 255, 255, 0.82);
            color: var(--muted-strong);
            cursor: pointer;
            font-size: 0.78rem;
            font-weight: 600;
        }

        .episode-panel {
            margin-top: 0.7rem;
            padding: 12px;
            border-radius: 14px;
            background: var(--surface-soft);
            border: 1px solid rgba(52, 56, 49, 0.08);
            display: grid;
            gap: 10px;
        }

        .episode-meta {
            color: var(--muted);
            font-size: 0.78rem;
        }

        .episode-events {
            display: grid;
            gap: 8px;
        }

        .episode-event {
            padding: 9px 10px;
            border-radius: 12px;
            background: rgba(255, 255, 255, 0.78);
            border: 1px solid rgba(52, 56, 49, 0.06);
        }

        .episode-event-title {
            font-size: 0.75rem;
            font-weight: 700;
            color: var(--muted-strong);
        }

        .episode-event-content {
            margin-top: 0.35rem;
            font-size: 0.82rem;
            line-height: 1.45;
            white-space: pre-wrap;
            word-break: break-word;
        }

        .composer {
            padding: 10px 18px 18px;
            border-top: 1px solid rgba(52, 56, 49, 0.08);
            background: linear-gradient(180deg, rgba(255, 252, 247, 0.1), rgba(255, 252, 247, 0.76));
        }

        .composer-shell {
            width: 100%;
            padding: 12px 13px 11px;
            border-radius: 18px;
            background: var(--panel-strong);
            border: 1px solid var(--line);
            box-shadow: var(--shadow);
        }

        .composer-shell:focus-within {
            border-color: rgba(31, 122, 99, 0.4);
            box-shadow: 0 16px 34px rgba(43, 47, 40, 0.12);
        }

        .composer-input {
            width: 100%;
            min-height: 26px;
            max-height: 168px;
            border: none;
            outline: none;
            resize: none;
            background: transparent;
            color: var(--text);
            line-height: 1.5;
            font-size: 0.95rem;
        }

        .composer-input::placeholder {
            color: #8a9085;
        }

        .hidden-input {
            display: none;
        }

        .draft-attachments {
            display: grid;
            gap: 8px;
            margin-bottom: 10px;
        }

        .draft-attachment {
            display: flex;
            align-items: flex-start;
            gap: 8px;
            padding: 9px 10px;
            border-radius: 14px;
            background: rgba(243, 237, 228, 0.8);
            border: 1px solid rgba(52, 56, 49, 0.06);
        }

        .draft-attachment img {
            width: 58px;
            height: 58px;
            object-fit: cover;
            border-radius: 10px;
            border: 1px solid rgba(52, 56, 49, 0.06);
            background: #ffffff;
        }

        .draft-attachment audio {
            width: min(100%, 280px);
        }

        .draft-attachment-copy {
            flex: 1;
            min-width: 0;
        }

        .draft-attachment-name {
            font-size: 0.88rem;
            font-weight: 600;
            word-break: break-word;
        }

        .draft-attachment-meta {
            margin-top: 0.22rem;
            font-size: 0.74rem;
            color: var(--muted);
        }

        .draft-remove {
            flex: 0 0 auto;
            padding: 0.38rem 0.62rem;
            border: none;
            border-radius: 999px;
            background: rgba(220, 38, 38, 0.1);
            color: #b91c1c;
            cursor: pointer;
            font-size: 0.76rem;
        }

        .composer-footer {
            margin-top: 8px;
        }

        .composer-toolbar {
            display: flex;
            align-items: center;
            gap: 8px;
            flex-wrap: wrap;
        }

        .composer-btn {
            padding: 0.54rem 0.8rem;
            border: 1px solid var(--line);
            border-radius: 999px;
            background: rgba(255, 255, 255, 0.9);
            color: var(--text);
            cursor: pointer;
            transition: border-color 120ms ease, background 120ms ease;
            font-size: 0.82rem;
        }

        .composer-btn.recording {
            border-color: rgba(220, 38, 38, 0.25);
            background: rgba(220, 38, 38, 0.1);
            color: #b91c1c;
        }

        .composer-btn:disabled,
        .draft-remove:disabled {
            opacity: 0.55;
            cursor: not-allowed;
        }

        .composer-hint {
            font-size: 0.76rem;
        }

        .send-btn {
            padding: 0.64rem 1rem;
            border: none;
            border-radius: 999px;
            background: linear-gradient(135deg, var(--accent) 0%, var(--accent-strong) 100%);
            color: #ffffff;
            cursor: pointer;
            transition: transform 120ms ease, box-shadow 120ms ease, background 120ms ease;
            font-size: 0.84rem;
            font-weight: 600;
        }

        .send-btn:disabled {
            background: #9ca3af;
            cursor: not-allowed;
            transform: none;
            box-shadow: none;
        }

        .loading {
            margin: 0 18px;
            padding: 0 0 4px;
            gap: 8px;
            color: var(--muted);
            visibility: hidden;
            opacity: 0;
            transition: opacity 140ms ease;
            font-size: 0.82rem;
        }

        .stop-run-btn {
            padding: 0.32rem 0.62rem;
            border: 1px solid rgba(185, 28, 28, 0.26);
            border-radius: 999px;
            background: rgba(220, 38, 38, 0.08);
            color: #b91c1c;
            font-size: 0.76rem;
            font-weight: 700;
            cursor: pointer;
        }

        .stop-run-btn:disabled {
            opacity: 0.55;
            cursor: not-allowed;
        }

        .loading.active {
            visibility: visible;
            opacity: 1;
        }

        .spinner {
            width: 14px;
            height: 14px;
            border-radius: 999px;
            border: 2px solid rgba(31, 122, 99, 0.16);
            border-top-color: var(--accent);
            animation: spin 0.8s linear infinite;
        }

        @keyframes spin {
            to { transform: rotate(360deg); }
        }

        @media (max-width: 1100px) {
            .app-shell {
                grid-template-columns: 1fr;
                height: auto;
                min-height: 100vh;
            }

            .main-panel {
                min-height: 72vh;
            }

            .inspector {
                max-height: none;
            }

            .conversation {
                padding: 10px 14px 0;
            }
        }

        @media (max-width: 720px) {
            .app-shell {
                padding: 8px;
                gap: 8px;
            }

            .inspector {
                padding: 10px;
            }

            .topbar {
                padding: 14px 14px 12px;
            }

            .composer-shell,
            .empty-state {
                border-radius: 16px;
            }

            .message.user .message-body {
                max-width: 100%;
            }

            .message-shell {
                gap: 8px;
            }

            .message-avatar {
                width: 30px;
                height: 30px;
                flex-basis: 30px;
                font-size: 0.7rem;
            }

            .conversation {
                padding: 8px 10px 0;
            }

            .composer {
                padding: 10px;
            }

            .composer-footer {
                align-items: flex-start;
            }

            .composer-actions {
                width: 100%;
                justify-content: flex-end;
            }
        }
    </style>
</head>
<body>
    <div class="app-shell">
        <main class="main-panel">
            <header class="topbar">
                <h1>Aiden Agent</h1>
                <div class="topbar-actions">
                    <a href="/coordinate-debug" class="new-chat-btn" style="text-decoration:none;display:inline-flex;align-items:center;">🎯 坐标调试</a>
                    <a href="/benchmark" class="new-chat-btn" style="text-decoration:none;display:inline-flex;align-items:center;">📋 Benchmark</a>
                    <a href="/user_files" class="new-chat-btn" style="text-decoration:none;display:inline-flex;align-items:center;">📁 User files</a>
                    <button type="button" class="new-chat-btn" onclick="clearHistory()">New chat</button>
                    <button type="button" class="new-chat-btn" onclick="resetAllMemory()" style="background:#c0392b;">Reset all memory</button>
                </div>
            </header>

            <section class="conversation" id="conversation">
                <div class="conversation-inner">
                    <div class="empty-state" id="emptyState">
                        <h3>Aiden Agent</h3>
                        <p>Ask a question or run a task.</p>
                    </div>
                    <div class="message-list" id="messages"></div>
                </div>
            </section>

            <div class="loading" id="loading">
                <span class="spinner" aria-hidden="true"></span>
                <span>Working on it...</span>
                <button type="button" class="stop-run-btn" id="stopRunBtn" onclick="cancelCurrentRun()" disabled>Stop</button>
            </div>

            <form class="composer" onsubmit="event.preventDefault(); sendMessage();">
                <div class="composer-shell">
                    <input type="file" id="imageInput" class="hidden-input" accept="image/*" multiple />
                    <div class="draft-attachments" id="draftAttachments"></div>
                    <textarea
                        id="input"
                        class="composer-input"
                        rows="1"
                        placeholder="Message Aiden Agent..."
                    ></textarea>
                    <div class="composer-footer">
                        <div class="composer-toolbar">
                            <button type="button" class="composer-btn" id="imageBtn" onclick="openImagePicker()">Add image</button>
                            <button type="button" class="composer-btn" id="recordBtn" onclick="toggleRecording()">Record audio</button>
                            <div class="composer-hint">Enter to send, Shift+Enter for newline</div>
                        </div>
                        <div class="composer-actions">
                            <button type="submit" class="send-btn" id="sendBtn">Send</button>
                        </div>
                    </div>
                </div>
            </form>
        </main>

        <aside class="inspector">
            <div class="sidebar-card tool-lab">
                <div class="sidebar-kicker">Tool Lab</div>
                <strong>Manual tool test</strong>
                <label class="tool-lab-label" for="toolSelect">Tool</label>
                <select id="toolSelect" class="tool-lab-select">
                    <option value="">Loading tools...</option>
                </select>
                <div class="sidebar-note" id="toolDescription"></div>
                <textarea
                    id="toolInput"
                    class="tool-lab-input"
                    rows="7"
                    spellcheck="false"
                    placeholder='{"command":"pwd"}'
                ></textarea>
                <div class="tool-lab-row">
                    <button type="button" class="composer-btn tool-secondary-btn" id="toolExampleBtn" onclick="loadSelectedToolExample()">Use example</button>
                    <button type="button" class="send-btn tool-run-btn" id="toolInvokeBtn" onclick="invokeSelectedTool()">Run tool</button>
                </div>
                <div class="tool-lab-status" id="toolStatus"></div>
                <div class="tool-lab-result hidden" id="toolResultPanel">
                    <div class="tool-lab-result-meta" id="toolResultMeta"></div>
                    <div class="tool-lab-preview hidden" id="toolResultPreviewWrap">
                        <img id="toolResultPreview" alt="Tool output preview" />
                    </div>
                    <pre class="tool-block" id="toolResultOutput"></pre>
                </div>
            </div>

            <div class="sidebar-card skill-export">
                <div class="sidebar-kicker">Skill Export</div>
                <strong>HTTP skill bundle</strong>
                <label class="tool-lab-label" for="skillSelect">Skill bundle</label>
                <select id="skillSelect" class="tool-lab-select">
                    <option value="">Loading skills...</option>
                </select>
                <textarea
                    id="skillMarkdown"
                    class="tool-lab-input"
                    rows="12"
                    readonly
                    spellcheck="false"
                    placeholder="Generated skill markdown will appear here."
                ></textarea>
                <div class="tool-lab-row">
                    <button type="button" class="composer-btn tool-secondary-btn" id="copySkillBtn" onclick="copySelectedSkill()">Copy skill</button>
                </div>
                <div class="tool-lab-status" id="skillStatus"></div>
            </div>
        </aside>
    </div>

    <script>
        const conversationEl = document.getElementById('conversation');
        const messagesDiv = document.getElementById('messages');
        const inputEl = document.getElementById('input');
        const sendBtn = document.getElementById('sendBtn');
        const imageInputEl = document.getElementById('imageInput');
        const imageBtn = document.getElementById('imageBtn');
        const recordBtn = document.getElementById('recordBtn');
        const draftAttachmentsEl = document.getElementById('draftAttachments');
        const loadingDiv = document.getElementById('loading');
        const stopRunBtn = document.getElementById('stopRunBtn');
        const emptyStateEl = document.getElementById('emptyState');
        const toolSelectEl = document.getElementById('toolSelect');
        const toolDescriptionEl = document.getElementById('toolDescription');
        const toolInputEl = document.getElementById('toolInput');
        const toolExampleBtnEl = document.getElementById('toolExampleBtn');
        const toolInvokeBtnEl = document.getElementById('toolInvokeBtn');
        const toolStatusEl = document.getElementById('toolStatus');
        const toolResultPanelEl = document.getElementById('toolResultPanel');
        const toolResultMetaEl = document.getElementById('toolResultMeta');
        const toolResultPreviewWrapEl = document.getElementById('toolResultPreviewWrap');
        const toolResultPreviewEl = document.getElementById('toolResultPreview');
        const toolResultOutputEl = document.getElementById('toolResultOutput');
        const skillSelectEl = document.getElementById('skillSelect');
        const skillMarkdownEl = document.getElementById('skillMarkdown');
        const skillStatusEl = document.getElementById('skillStatus');
        const copySkillBtnEl = document.getElementById('copySkillBtn');
        const targetAudioSampleRate = 16000;

        let nextAttachmentId = 1;
        let draftAttachments = [];
        let recorderState = createRecorderState();
        let toolCatalog = [];
        let toolSkills = [];
        let currentChatRequestId = '';
        let currentChatAbortController = null;
        let currentChatCancelRequested = false;
        let episodeCache = {};

        loadHistory();
        loadToolCatalog();
        loadToolSkills();
        autoResizeInput();

        inputEl.addEventListener('input', autoResizeInput);
        imageInputEl.addEventListener('change', handleImageSelection);
        toolSelectEl.addEventListener('change', syncSelectedTool);
        skillSelectEl.addEventListener('change', syncSelectedSkill);
        inputEl.addEventListener('keydown', function(event) {
            if (event.key === 'Enter' && !event.shiftKey && !event.isComposing) {
                event.preventDefault();
                sendMessage();
            }
        });

        async function loadHistory() {
            try {
                const res = await fetch('/api/history');
                const history = await res.json();
                renderHistory(history);
            } catch (err) {
                console.error('Failed to load history:', err);
            }
        }

        async function loadToolCatalog() {
            try {
                const res = await fetch('/api/tools');
                if (!res.ok) {
                    throw new Error(await res.text() || 'Failed to load tools');
                }
                const data = await res.json();
                toolCatalog = data.tools || [];
                renderToolCatalog();
            } catch (err) {
                console.error('Failed to load tools:', err);
                toolCatalog = [];
                toolSelectEl.innerHTML = '<option value="">Tools unavailable</option>';
                toolSelectEl.disabled = true;
                toolInputEl.disabled = true;
                toolExampleBtnEl.disabled = true;
                toolInvokeBtnEl.disabled = true;
                toolDescriptionEl.textContent = 'Tool metadata failed to load: ' + err.message;
                toolStatusEl.textContent = 'Failed to load tools.';
                toolStatusEl.classList.add('error');
            }
        }

        function renderToolCatalog() {
            toolSelectEl.innerHTML = '';

            if (toolCatalog.length === 0) {
                toolSelectEl.innerHTML = '<option value="">No tools exposed</option>';
                toolSelectEl.disabled = true;
                toolInputEl.disabled = true;
                toolExampleBtnEl.disabled = true;
                toolInvokeBtnEl.disabled = true;
                return;
            }

            toolSelectEl.disabled = false;
            toolInputEl.disabled = false;
            toolExampleBtnEl.disabled = false;
            toolInvokeBtnEl.disabled = false;

            toolCatalog.forEach(function(tool) {
                const option = document.createElement('option');
                option.value = tool.name;
                option.textContent = tool.name;
                toolSelectEl.appendChild(option);
            });

            syncSelectedTool();
        }

        function getSelectedTool() {
            const selectedName = toolSelectEl.value;
            for (let i = 0; i < toolCatalog.length; i++) {
                if (toolCatalog[i].name === selectedName) {
                    return toolCatalog[i];
                }
            }
            return toolCatalog.length > 0 ? toolCatalog[0] : null;
        }

        function syncSelectedTool() {
            const tool = getSelectedTool();
            if (!tool) return;

            const switched = toolInputEl.dataset.toolName !== tool.name;
            if (switched) {
                clearToolResultPreview();
            }

            if (toolSelectEl.value !== tool.name) {
                toolSelectEl.value = tool.name;
            }

            toolDescriptionEl.textContent = tool.description || '';
            toolInputEl.placeholder = tool.example_input || '';
            if (switched) {
                toolInputEl.value = tool.example_input || '';
                toolInputEl.dataset.toolName = tool.name;
            }
            toolStatusEl.classList.remove('error');
        }

        function loadSelectedToolExample() {
            const tool = getSelectedTool();
            if (!tool) return;
            toolInputEl.value = tool.example_input || '';
            toolStatusEl.textContent = 'Example loaded.';
            toolStatusEl.classList.remove('error');
        }

        async function invokeSelectedTool() {
            const tool = getSelectedTool();
            if (!tool || toolInvokeBtnEl.disabled) return;

            toolInvokeBtnEl.disabled = true;
            toolExampleBtnEl.disabled = true;
            toolStatusEl.textContent = 'Running...';
            toolStatusEl.classList.remove('error');

            try {
                const res = await fetch(tool.http.path, {
                    method: 'POST',
                    headers: { 'Content-Type': 'application/json' },
                    body: JSON.stringify({
                        raw_input: toolInputEl.value
                    })
                });

                if (!res.ok) {
                    throw new Error(await res.text() || 'Tool call failed');
                }

                const data = await res.json();
                renderToolInvokeResult(data);
            } catch (err) {
                console.error('Failed to invoke tool:', err);
                toolStatusEl.textContent = 'Tool call failed: ' + err.message;
                toolStatusEl.classList.add('error');
                toolResultPanelEl.classList.add('hidden');
            } finally {
                toolInvokeBtnEl.disabled = false;
                toolExampleBtnEl.disabled = false;
            }
        }

        function renderToolInvokeResult(result) {
            toolResultPanelEl.classList.remove('hidden');
            toolResultMetaEl.textContent = [
                result.tool && result.tool.name ? result.tool.name : 'tool',
                result.duration_ms + ' ms',
                result.is_error ? 'error' : 'ok'
            ].join(' · ');
            toolResultOutputEl.textContent = formatToolPayload(result.output || '');

            const screenshot = parseScreenshotOutput(result.tool ? result.tool.name : '', result.output || '');
            if (screenshot) {
                toolResultPreviewWrapEl.classList.remove('hidden');
                toolResultPreviewEl.src = 'data:image/' + (screenshot.format || 'jpeg') + ';base64,' + screenshot.data;
            } else {
                clearToolResultPreview();
            }

            if (result.is_error) {
                toolStatusEl.textContent = result.error || 'Error';
                toolStatusEl.classList.add('error');
            } else {
                toolStatusEl.textContent = 'Done at ' + formatTime(result.called_at) + '.';
                toolStatusEl.classList.remove('error');
            }
        }

        function clearToolResultPreview() {
            toolResultPreviewWrapEl.classList.add('hidden');
            toolResultPreviewEl.removeAttribute('src');
        }

        async function loadToolSkills() {
            try {
                const res = await fetch('/api/tool-skills');
                if (!res.ok) {
                    throw new Error(await res.text() || 'Failed to load tool skills');
                }
                const data = await res.json();
                toolSkills = data.skills || [];
                renderToolSkills();
            } catch (err) {
                console.error('Failed to load tool skills:', err);
                toolSkills = [];
                skillSelectEl.innerHTML = '<option value="">Skills unavailable</option>';
                skillSelectEl.disabled = true;
                skillMarkdownEl.value = '';
                copySkillBtnEl.disabled = true;
                skillStatusEl.textContent = 'Skill export unavailable: ' + err.message;
                skillStatusEl.classList.add('error');
            }
        }

        function renderToolSkills() {
            skillSelectEl.innerHTML = '';

            if (toolSkills.length === 0) {
                skillSelectEl.innerHTML = '<option value="">No generated skills</option>';
                skillSelectEl.disabled = true;
                skillMarkdownEl.value = '';
                copySkillBtnEl.disabled = true;
                return;
            }

            skillSelectEl.disabled = false;
            copySkillBtnEl.disabled = false;

            toolSkills.forEach(function(skill) {
                const option = document.createElement('option');
                option.value = skill.name;
                option.textContent = skill.name;
                skillSelectEl.appendChild(option);
            });

            syncSelectedSkill();
        }

        function getSelectedSkill() {
            const selectedName = skillSelectEl.value;
            for (let i = 0; i < toolSkills.length; i++) {
                if (toolSkills[i].name === selectedName) {
                    return toolSkills[i];
                }
            }
            return toolSkills.length > 0 ? toolSkills[0] : null;
        }

        function syncSelectedSkill() {
            const skill = getSelectedSkill();
            if (!skill) return;
            if (skillSelectEl.value !== skill.name) {
                skillSelectEl.value = skill.name;
            }
            skillMarkdownEl.value = skill.markdown || '';
            skillStatusEl.textContent = skill.description || '';
            skillStatusEl.classList.remove('error');
        }

        async function copySelectedSkill() {
            const skill = getSelectedSkill();
            if (!skill || !skill.markdown) return;

            try {
                if (navigator.clipboard && navigator.clipboard.writeText) {
                    await navigator.clipboard.writeText(skill.markdown);
                } else {
                    skillMarkdownEl.focus();
                    skillMarkdownEl.select();
                    document.execCommand('copy');
                }
                skillStatusEl.textContent = 'Copied.';
                skillStatusEl.classList.remove('error');
            } catch (err) {
                console.error('Failed to copy skill:', err);
                skillStatusEl.textContent = 'Copy failed.';
                skillStatusEl.classList.add('error');
            }
        }

        async function sendMessage() {
            if (sendBtn.disabled) return;
            if (recorderState.isRecording || recorderState.isStopping) {
                await stopRecording();
            }

            const message = inputEl.value.trim();
            const attachments = cloneAttachmentsForTransport(draftAttachments);
            if (!message && attachments.length === 0) return;

            const pendingAttachments = cloneAttachmentsForMessage(draftAttachments);

            inputEl.value = '';
            autoResizeInput();
            setComposerState(true);
            clearDraftAttachments();
            currentChatRequestId = createRequestId();
            currentChatAbortController = new AbortController();
            currentChatCancelRequested = false;

            addMessage({
                type: 'user',
                content: message,
                attachments: pendingAttachments,
                timestamp: new Date().toISOString()
            });

            try {
                const res = await fetch('/api/chat', {
                    method: 'POST',
                    headers: {
                        'Content-Type': 'application/json',
                        'Accept': 'application/x-ndjson',
                        'X-Aiden-Stream': 'ndjson'
                    },
                    signal: currentChatAbortController.signal,
                    body: JSON.stringify({
                        message: message,
                        attachments: attachments,
                        request_id: currentChatRequestId
                    })
                });

                if (!res.ok) {
                    const errorText = await res.text();
                    throw new Error(errorText || 'Request failed');
                }

                await consumeChatStream(res);
            } catch (err) {
                if (currentChatCancelRequested || err.name === 'AbortError') {
                    addMessage({
                        type: 'assistant',
                        content: 'Interrupted.',
                        timestamp: new Date().toISOString()
                    });
                    return;
                }
                console.error('Failed to send message:', err);
                try {
                    await loadHistory();
                } catch (_) {}

                addMessage({
                    type: 'assistant',
                    content: 'Error: ' + err.message,
                    timestamp: new Date().toISOString()
                });
            } finally {
                currentChatRequestId = '';
                currentChatAbortController = null;
                currentChatCancelRequested = false;
                setComposerState(false);
                scrollToBottom();
            }
        }

        function createRequestId() {
            if (window.crypto && window.crypto.randomUUID) {
                return window.crypto.randomUUID();
            }
            return 'web-' + Date.now().toString(36) + '-' + Math.random().toString(36).slice(2);
        }

        async function cancelCurrentRun() {
            if (!currentChatRequestId) return;
            const requestId = currentChatRequestId;
            currentChatCancelRequested = true;
            stopRunBtn.disabled = true;
            stopRunBtn.textContent = 'Stopping...';

			if (currentChatAbortController) {
				currentChatAbortController.abort();
			}

			fetch('/api/chat/cancel', {
				method: 'POST',
				headers: { 'Content-Type': 'application/json' },
				body: JSON.stringify({ request_id: requestId }),
				keepalive: true
			}).catch(function(err) {
				console.error('Failed to cancel chat request:', err);
			});
		}

        async function consumeChatStream(res) {
            let sawDone = false;
            if (!res.body) {
                sawDone = consumeChatStreamText(await res.text());
                if (!sawDone) {
                    throw new Error('Chat stream ended before done event.');
                }
                return;
            }

            const reader = res.body.getReader();
            const decoder = new TextDecoder();
            let buffer = '';

            while (true) {
                const chunk = await reader.read();
                if (chunk.done) break;
                buffer += decoder.decode(chunk.value, { stream: true });
                const result = consumeChatStreamLines(buffer);
                buffer = result.buffer;
                sawDone = sawDone || result.sawDone;
            }

            buffer += decoder.decode();
            const result = consumeChatStreamLines(buffer, true);
            sawDone = sawDone || result.sawDone;
            if (!sawDone) {
                throw new Error('Chat stream ended before done event.');
            }
        }

        function consumeChatStreamText(text) {
            const trimmed = text.trim();
            if (!trimmed) return false;
            try {
                const payload = JSON.parse(trimmed);
                if (payload.history || payload.response) {
                    renderHistory(payload.history || []);
                    return true;
                }
                if (payload.type) {
                    return handleChatStreamEvent(payload);
                }
            } catch (_) {
                // Not a single JSON response; parse it below as NDJSON.
            }
            return consumeChatStreamLines(text, true).sawDone;
        }

        function consumeChatStreamLines(buffer, flush) {
            const lines = buffer.split('\n');
            if (!flush) {
                buffer = lines.pop() || '';
            } else {
                buffer = '';
            }

            let sawDone = false;
            lines.forEach(function(line) {
                line = line.trim();
                if (!line) return;
                let event;
                try {
                    event = JSON.parse(line);
                } catch (err) {
                    throw new Error('Invalid chat stream event: ' + err.message);
                }
                sawDone = handleChatStreamEvent(event) || sawDone;
            });

            return { buffer, sawDone };
        }

        function handleChatStreamEvent(event) {
            if (event.type === 'message' && event.message) {
                addMessage(event.message);
                return false;
            }
            if (event.type === 'done') {
                return true;
            }
            if (event.type === 'error') {
                throw new Error(event.error || 'Agent error');
            }
            return false;
        }

        async function clearHistory() {
            if (!confirm('Start a new chat and clear the current conversation?')) return;

            try {
                await fetch('/api/clear', { method: 'POST' });
                clearDraftAttachments();
                renderHistory([]);
            } catch (err) {
                console.error('Failed to clear history:', err);
            }
        }

        async function resetAllMemory() {
            if (!confirm('This will permanently delete ALL memory including long-term memories and user profile. Continue?')) return;

            try {
                const res = await fetch('/api/clear-all', { method: 'POST' });
                if (!res.ok) {
                    throw new Error(await res.text() || 'Failed to reset all memory.');
                }
                clearDraftAttachments();
                renderHistory([]);
            } catch (err) {
                console.error('Failed to reset all memory:', err);
            }
        }

        function renderHistory(history) {
            messagesDiv.innerHTML = '';

            const fragment = document.createDocumentFragment();
            history.forEach(function(msg) {
                fragment.appendChild(createMessageNode(msg));
            });

            messagesDiv.appendChild(fragment);
            updateEmptyState();
            scrollToBottom();
        }

        function addMessage(msg) {
            messagesDiv.appendChild(createMessageNode(msg));
            updateEmptyState();
            scrollToBottom();
        }

        function createMessageNode(msg) {
            const card = document.createElement('article');
            card.className = 'message ' + normalizeType(msg.type);

            const shell = document.createElement('div');
            shell.className = 'message-shell';

            const avatar = document.createElement('div');
            avatar.className = 'message-avatar';
            avatar.textContent = getAvatarLabel(msg.type);

            const body = document.createElement('div');
            body.className = 'message-body';

            const role = document.createElement('div');
            role.className = 'message-role';
            role.textContent = getRoleLabel(msg.type, msg.tool_name, msg.role);
            body.appendChild(role);

            if (msg.type === 'tool_call') {
                body.appendChild(renderToolCall(msg));
            } else if (msg.type === 'tool_result') {
                body.appendChild(renderToolResult(msg));
            } else {
                const attachmentsEl = renderMessageAttachments(msg.attachments || []);
                if (attachmentsEl) {
                    body.appendChild(attachmentsEl);
                }

                if (msg.content) {
                    const contentDiv = document.createElement('div');
                    contentDiv.className = 'message-copy';
                    contentDiv.textContent = msg.content || '';
                    body.appendChild(contentDiv);
                }
            }

            const episodeLink = renderEpisodeLink(msg);
            if (episodeLink) {
                body.appendChild(episodeLink);
            }

            const timeDiv = document.createElement('div');
            timeDiv.className = 'message-time';
            timeDiv.textContent = formatTime(msg.timestamp);
            body.appendChild(timeDiv);

            shell.appendChild(avatar);
            shell.appendChild(body);
            card.appendChild(shell);
            return card;
        }

        function renderEpisodeLink(msg) {
            if (!msg.episode_id) return null;
            if (msg.type !== 'user' && msg.type !== 'assistant' && msg.type !== 'episode_status') return null;

            const wrap = document.createElement('div');
            const button = document.createElement('button');
            button.type = 'button';
            button.className = 'episode-link';
            button.textContent = 'Trace' + (msg.status ? ' · ' + msg.status : '');
            wrap.appendChild(button);

            let panel = null;
            button.addEventListener('click', async function() {
                if (panel) {
                    panel.classList.toggle('hidden');
                    return;
                }
                panel = document.createElement('div');
                panel.className = 'episode-panel';
                panel.textContent = 'Loading trace...';
                wrap.appendChild(panel);

                try {
                    const episode = await loadEpisode(msg.episode_id);
                    renderEpisodePanel(panel, episode);
                } catch (err) {
                    panel.textContent = 'Trace unavailable: ' + err.message;
                }
            });
            return wrap;
        }

        async function loadEpisode(id) {
            if (episodeCache[id]) return episodeCache[id];
            const res = await fetch('/api/episodes/' + encodeURIComponent(id));
            if (!res.ok) {
                throw new Error(await res.text() || 'Episode not found');
            }
            const data = await res.json();
            episodeCache[id] = data.episode;
            return data.episode;
        }

        function renderEpisodePanel(panel, episode) {
            panel.innerHTML = '';
            const meta = document.createElement('div');
            meta.className = 'episode-meta';
            const outcome = episode.outcome || {};
            meta.textContent = [
                episode.id || 'episode',
                episode.status || 'unknown',
                outcome.success ? 'success' : (outcome.failure_reason ? 'failed' : 'pending')
            ].join(' · ');
            panel.appendChild(meta);

            const events = (episode.events || []).slice(-12);
            if (events.length === 0) {
                const empty = document.createElement('div');
                empty.className = 'episode-meta';
                empty.textContent = 'No trace events recorded.';
                panel.appendChild(empty);
                return;
            }

            const list = document.createElement('div');
            list.className = 'episode-events';
            events.forEach(function(event) {
                list.appendChild(renderEpisodeEvent(event));
            });
            panel.appendChild(list);
        }

        function renderEpisodeEvent(event) {
            const item = document.createElement('div');
            item.className = 'episode-event';

            const title = document.createElement('div');
            title.className = 'episode-event-title';
            const parts = [event.type || 'event'];
            if (event.role) parts.push(event.role);
            if (event.tool_name) parts.push(event.tool_name);
            title.textContent = parts.join(' · ');
            item.appendChild(title);

            const content = document.createElement('div');
            content.className = 'episode-event-content';
            content.textContent = episodeEventText(event);
            item.appendChild(content);
            return item;
        }

        function episodeEventText(event) {
            if (event.next_step) return event.next_step;
            if (event.content) return event.content;
            if (event.observation) return formatToolPayload(event.observation);
            if (event.reason) return event.reason;
            if (event.plan && event.plan.length) return event.plan.join('\n');
            return '';
        }

        function updateEmptyState() {
            emptyStateEl.classList.toggle('hidden', messagesDiv.children.length > 0);
        }

        function setComposerState(isLoading) {
            sendBtn.disabled = isLoading || recorderState.isStopping;
            imageBtn.disabled = isLoading || recorderState.isRecording || recorderState.isStopping;
            recordBtn.disabled = isLoading || recorderState.isStopping;
            stopRunBtn.disabled = !isLoading;
            stopRunBtn.textContent = 'Stop';
            loadingDiv.classList.toggle('active', isLoading);
            updateRecordButton();
        }

        function autoResizeInput() {
            inputEl.style.height = 'auto';
            inputEl.style.height = Math.min(inputEl.scrollHeight, 180) + 'px';
        }

        function scrollToBottom() {
            conversationEl.scrollTop = conversationEl.scrollHeight;
        }

        function normalizeType(type) {
            return type || 'assistant';
        }

        function getRoleLabel(type, toolName, role) {
            if (type === 'user') return 'You';
            if (type === 'episode_status') return 'Task Trace';
            if (type === 'role_output') return 'Role · ' + (role || 'agent');
            if (type === 'tool_call') return toolName ? 'Tool Call · ' + toolName : 'Tool Call';
            if (type === 'tool_result') return toolName ? 'Tool Result · ' + toolName : 'Tool Result';
            return 'Aiden';
        }

        function getAvatarLabel(type) {
            if (type === 'user') return 'You';
            if (type === 'episode_status') return 'Task';
            if (type === 'role_output') return 'Role';
            if (type === 'tool_call') return 'Call';
            if (type === 'tool_result') return 'Tool';
            return 'AI';
        }

        function formatTime(timestamp) {
            const date = new Date(timestamp);
            return date.toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' });
        }

        function openImagePicker() {
            if (imageBtn.disabled) return;
            imageInputEl.click();
        }

        async function handleImageSelection(event) {
            const files = Array.from(event.target.files || []);
            imageInputEl.value = '';
            if (files.length === 0) return;

            for (const file of files) {
                if (!file.type || file.type.indexOf('image/') !== 0) {
                    continue;
                }
                const dataUrl = await readFileAsDataURL(file);
                draftAttachments.push({
                    id: nextAttachmentId++,
                    kind: 'image',
                    name: file.name,
                    mime_type: file.type,
                    data: extractBase64(dataUrl),
                    size: file.size
                });
            }

            renderDraftAttachments();
        }

        async function toggleRecording() {
            if (recordBtn.disabled && !recorderState.isRecording) return;

            try {
                if (recorderState.isRecording) {
                    await stopRecording();
                } else {
                    await startRecording();
                }
            } catch (err) {
                console.error('Audio recording error:', err);
                alert('Audio recording failed: ' + err.message);
                await teardownRecorder();
                updateRecordButton();
            }
        }

        async function startRecording() {
            if (recorderState.isRecording) return;

            const startedOnServer = await startServerRecording();
            if (startedOnServer) {
                recorderState = {
                    isRecording: true,
                    isStopping: false,
                    mode: 'server',
                    stream: null,
                    context: null,
                    source: null,
                    processor: null,
                    sink: null,
                    chunks: [],
                    sampleRate: targetAudioSampleRate
                };
                updateRecordButton();
                return;
            }

            const AudioContextClass = window.AudioContext || window.webkitAudioContext;
            if (!navigator.mediaDevices || !navigator.mediaDevices.getUserMedia || !AudioContextClass) {
                throw new Error('Device audio recording is unavailable and this browser cannot record audio from the page.');
            }

            const stream = await navigator.mediaDevices.getUserMedia({ audio: true });
            const context = new AudioContextClass();
            const source = context.createMediaStreamSource(stream);
            const processor = context.createScriptProcessor(4096, 1, 1);
            const sink = context.createGain();
            sink.gain.value = 0;

            const chunks = [];
            processor.onaudioprocess = function(event) {
                if (!recorderState.isRecording) return;
                const channelData = event.inputBuffer.getChannelData(0);
                chunks.push(new Float32Array(channelData));
            };

            source.connect(processor);
            processor.connect(sink);
            sink.connect(context.destination);

            recorderState = {
                isRecording: true,
                isStopping: false,
                mode: 'browser',
                stream: stream,
                context: context,
                source: source,
                processor: processor,
                sink: sink,
                chunks: chunks,
                sampleRate: context.sampleRate
            };

            updateRecordButton();
        }

        async function stopRecording() {
            if (!recorderState.isRecording || recorderState.isStopping) return;

            const mode = recorderState.mode;
            recorderState.isStopping = true;
            setComposerState(loadingDiv.classList.contains('active'));

            try {
                if (mode === 'server') {
                    const attachment = await stopServerRecording();
                    await teardownRecorder({ forceServerStop: false });
                    if (attachment) {
                        upsertAudioAttachment({
                            id: nextAttachmentId++,
                            kind: attachment.kind || 'audio',
                            name: attachment.name || 'recording.wav',
                            mime_type: attachment.mime_type || 'audio/wav',
                            data: attachment.data || '',
                            size: attachment.size || 0
                        });
                    }
                    return;
                }

                const chunks = recorderState.chunks.slice();
                const sampleRate = recorderState.sampleRate;
                await teardownRecorder({ forceServerStop: false });

                const wavBlob = createWavBlob(chunks, sampleRate, targetAudioSampleRate);
                const dataUrl = await readBlobAsDataURL(wavBlob);

                upsertAudioAttachment({
                    id: nextAttachmentId++,
                    kind: 'audio',
                    name: 'recording.wav',
                    mime_type: 'audio/wav',
                    data: extractBase64(dataUrl),
                    size: wavBlob.size,
                    preview_url: URL.createObjectURL(wavBlob)
                });
            } finally {
                recorderState.isRecording = false;
                recorderState.isStopping = false;
                setComposerState(loadingDiv.classList.contains('active'));
            }
        }

        async function forceStopServerRecording() {
            try {
                await fetch('/api/audio/record/stop', { method: 'POST' });
            } catch (err) {
                console.warn('Force stop device recording failed:', err);
            }
        }

        async function startServerRecording() {
            const res = await fetch('/api/audio/record/start', { method: 'POST' });
            if (res.status === 404) {
                return false;
            }
            if (res.status === 409) {
                await forceStopServerRecording();
                const retry = await fetch('/api/audio/record/start', { method: 'POST' });
                if (retry.status === 404) {
                    return false;
                }
                if (!retry.ok) {
                    throw new Error(await retry.text() || 'Device audio recording failed to start.');
                }
                return true;
            }
            if (!res.ok) {
                const detail = await res.text() || 'Device audio recording failed to start.';
                if (detail.indexOf('audio service') !== -1 || detail.indexOf('audio_service') !== -1 || detail.indexOf('no such file') !== -1) {
                    throw new Error('Audio service is not running. Run: /etc/init.d/S53audio_service start');
                }
                throw new Error(detail);
            }
            return true;
        }

        async function stopServerRecording() {
            const res = await fetch('/api/audio/record/stop', { method: 'POST' });
            if (res.status === 400) {
                return null;
            }
            if (!res.ok) {
                throw new Error(await res.text() || 'Device audio recording failed to stop.');
            }
            const data = await res.json();
            if (!data.attachment || !data.attachment.data) {
                throw new Error('Device audio recording returned no audio data.');
            }
            return data.attachment;
        }

        function upsertAudioAttachment(attachment) {
            const nextDrafts = [];
            draftAttachments.forEach(function(item) {
                if (item.kind === 'audio') {
                    revokeAttachmentPreview(item);
                    return;
                }
                nextDrafts.push(item);
            });
            nextDrafts.push(attachment);
            draftAttachments = nextDrafts;
            renderDraftAttachments();
        }

        async function teardownRecorder(options) {
            const opts = options || {};
            const wasServerMode = recorderState.mode === 'server';
            if (recorderState.processor) {
                recorderState.processor.disconnect();
            }
            if (recorderState.source) {
                recorderState.source.disconnect();
            }
            if (recorderState.sink) {
                recorderState.sink.disconnect();
            }
            if (recorderState.stream) {
                recorderState.stream.getTracks().forEach(function(track) { track.stop(); });
            }
            if (recorderState.context && recorderState.context.state !== 'closed') {
                await recorderState.context.close();
            }
            recorderState = createRecorderState();
            if (wasServerMode && opts.forceServerStop !== false) {
                await forceStopServerRecording();
            }
        }

        function createRecorderState() {
            return {
                isRecording: false,
                isStopping: false,
                mode: '',
                stream: null,
                context: null,
                source: null,
                processor: null,
                sink: null,
                chunks: [],
                sampleRate: targetAudioSampleRate
            };
        }

        function updateRecordButton() {
            const active = recorderState.isRecording;
            recordBtn.classList.toggle('recording', active);
            if (recorderState.isStopping) {
                recordBtn.textContent = 'Processing recording...';
            } else {
                recordBtn.textContent = active ? 'Stop recording' : 'Record audio';
            }
        }

        function renderDraftAttachments() {
            draftAttachmentsEl.innerHTML = '';

            draftAttachments.forEach(function(attachment) {
                const row = document.createElement('div');
                row.className = 'draft-attachment';

                if (attachment.kind === 'image') {
                    const image = document.createElement('img');
                    image.alt = attachment.name || 'Image attachment';
                    image.src = attachmentDataURL(attachment);
                    row.appendChild(image);
                } else if (attachment.kind === 'audio') {
                    const audio = document.createElement('audio');
                    audio.controls = true;
                    audio.src = attachmentDataURL(attachment);
                    row.appendChild(audio);
                }

                const copy = document.createElement('div');
                copy.className = 'draft-attachment-copy';

                const name = document.createElement('div');
                name.className = 'draft-attachment-name';
                name.textContent = attachment.name || getAttachmentTitle(attachment.kind);
                copy.appendChild(name);

                const meta = document.createElement('div');
                meta.className = 'draft-attachment-meta';
                meta.textContent = getAttachmentTitle(attachment.kind) + ' · ' + formatBytes(attachment.size || 0);
                copy.appendChild(meta);
                row.appendChild(copy);

                const removeBtn = document.createElement('button');
                removeBtn.type = 'button';
                removeBtn.className = 'draft-remove';
                removeBtn.textContent = 'Remove';
                removeBtn.onclick = function() {
                    removeDraftAttachment(attachment.id);
                };
                row.appendChild(removeBtn);

                draftAttachmentsEl.appendChild(row);
            });
        }

        function removeDraftAttachment(id) {
            draftAttachments = draftAttachments.filter(function(attachment) {
                if (attachment.id === id) {
                    revokeAttachmentPreview(attachment);
                    return false;
                }
                return true;
            });
            renderDraftAttachments();
        }

        function clearDraftAttachments() {
            draftAttachments.forEach(revokeAttachmentPreview);
            draftAttachments = [];
            renderDraftAttachments();
        }

        function revokeAttachmentPreview(attachment) {
            if (attachment && attachment.preview_url && attachment.preview_url.indexOf('blob:') === 0) {
                URL.revokeObjectURL(attachment.preview_url);
            }
        }

        function renderMessageAttachments(attachments) {
            if (!attachments || attachments.length === 0) return null;

            const wrapper = document.createElement('div');
            wrapper.className = 'message-attachments';

            attachments.forEach(function(attachment) {
                const card = document.createElement('div');
                card.className = 'attachment-card';

                if (attachment.kind === 'image') {
                    const image = document.createElement('img');
                    image.alt = attachment.name || 'Image attachment';
                    image.src = attachmentDataURL(attachment);
                    card.appendChild(image);
                } else if (attachment.kind === 'audio') {
                    const audio = document.createElement('audio');
                    audio.controls = true;
                    audio.src = attachmentDataURL(attachment);
                    card.appendChild(audio);
                }

                const meta = document.createElement('div');
                meta.className = 'attachment-meta';

                const badge = document.createElement('span');
                badge.className = 'attachment-kind';
                badge.textContent = getAttachmentTitle(attachment.kind);
                meta.appendChild(badge);

                const name = document.createElement('span');
                name.textContent = attachment.name || getAttachmentTitle(attachment.kind);
                meta.appendChild(name);
                card.appendChild(meta);

                if (attachment.transcript) {
                    const transcript = document.createElement('div');
                    transcript.className = 'attachment-transcript';
                    transcript.textContent = 'Transcript: ' + attachment.transcript;
                    card.appendChild(transcript);
                }

                wrapper.appendChild(card);
            });

            return wrapper;
        }

        function cloneAttachmentsForTransport(attachments) {
            return attachments.map(function(attachment) {
                return {
                    kind: attachment.kind,
                    name: attachment.name,
                    mime_type: attachment.mime_type,
                    data: attachment.data,
                    size: attachment.size
                };
            });
        }

        function cloneAttachmentsForMessage(attachments) {
            return attachments.map(function(attachment) {
                return {
                    kind: attachment.kind,
                    name: attachment.name,
                    mime_type: attachment.mime_type,
                    data: attachment.data,
                    size: attachment.size,
                    preview_url: attachment.preview_url || ''
                };
            });
        }

        function attachmentDataURL(attachment) {
            if (attachment.preview_url) return attachment.preview_url;
            if (!attachment.data) return '';
            return 'data:' + (attachment.mime_type || 'application/octet-stream') + ';base64,' + attachment.data;
        }

        function getAttachmentTitle(kind) {
            if (kind === 'audio') return 'Audio';
            if (kind === 'image') return 'Image';
            return 'Attachment';
        }

        function formatBytes(size) {
            if (!size) return '0 B';
            const units = ['B', 'KB', 'MB', 'GB'];
            let value = size;
            let unitIndex = 0;
            while (value >= 1024 && unitIndex < units.length - 1) {
                value = value / 1024;
                unitIndex++;
            }
            const rounded = unitIndex === 0 ? String(Math.round(value)) : value.toFixed(1);
            return rounded + ' ' + units[unitIndex];
        }

        function extractBase64(dataUrl) {
            const parts = String(dataUrl || '').split(',');
            return parts.length > 1 ? parts[1] : '';
        }

        function readFileAsDataURL(file) {
            return new Promise(function(resolve, reject) {
                const reader = new FileReader();
                reader.onload = function() { resolve(reader.result); };
                reader.onerror = function() { reject(reader.error || new Error('Failed to read file.')); };
                reader.readAsDataURL(file);
            });
        }

        function readBlobAsDataURL(blob) {
            return new Promise(function(resolve, reject) {
                const reader = new FileReader();
                reader.onload = function() { resolve(reader.result); };
                reader.onerror = function() { reject(reader.error || new Error('Failed to read audio blob.')); };
                reader.readAsDataURL(blob);
            });
        }

        function createWavBlob(chunks, sourceSampleRate, targetSampleRate) {
            const merged = mergeFloat32Chunks(chunks);
            const downsampled = downsampleBuffer(merged, sourceSampleRate, targetSampleRate);
            const wavBuffer = encodeWAV(downsampled, targetSampleRate);
            return new Blob([wavBuffer], { type: 'audio/wav' });
        }

        function mergeFloat32Chunks(chunks) {
            let totalLength = 0;
            chunks.forEach(function(chunk) {
                totalLength += chunk.length;
            });

            const merged = new Float32Array(totalLength);
            let offset = 0;
            chunks.forEach(function(chunk) {
                merged.set(chunk, offset);
                offset += chunk.length;
            });
            return merged;
        }

        function downsampleBuffer(buffer, inputRate, outputRate) {
            if (!buffer || buffer.length === 0) return new Float32Array(0);
            if (inputRate === outputRate) return buffer;

            const ratio = inputRate / outputRate;
            const newLength = Math.round(buffer.length / ratio);
            const result = new Float32Array(newLength);
            let offsetResult = 0;
            let offsetBuffer = 0;

            while (offsetResult < newLength) {
                const nextOffsetBuffer = Math.round((offsetResult + 1) * ratio);
                let accum = 0;
                let count = 0;

                for (let i = offsetBuffer; i < nextOffsetBuffer && i < buffer.length; i++) {
                    accum += buffer[i];
                    count++;
                }

                result[offsetResult] = count > 0 ? accum / count : 0;
                offsetResult++;
                offsetBuffer = nextOffsetBuffer;
            }

            return result;
        }

        function encodeWAV(samples, sampleRate) {
            const buffer = new ArrayBuffer(44 + samples.length * 2);
            const view = new DataView(buffer);

            writeAscii(view, 0, 'RIFF');
            view.setUint32(4, 36 + samples.length * 2, true);
            writeAscii(view, 8, 'WAVE');
            writeAscii(view, 12, 'fmt ');
            view.setUint32(16, 16, true);
            view.setUint16(20, 1, true);
            view.setUint16(22, 1, true);
            view.setUint32(24, sampleRate, true);
            view.setUint32(28, sampleRate * 2, true);
            view.setUint16(32, 2, true);
            view.setUint16(34, 16, true);
            writeAscii(view, 36, 'data');
            view.setUint32(40, samples.length * 2, true);

            let offset = 44;
            for (let i = 0; i < samples.length; i++) {
                const sample = Math.max(-1, Math.min(1, samples[i]));
                view.setInt16(offset, sample < 0 ? sample * 0x8000 : sample * 0x7fff, true);
                offset += 2;
            }

            return buffer;
        }

        function writeAscii(view, offset, text) {
            for (let i = 0; i < text.length; i++) {
                view.setUint8(offset + i, text.charCodeAt(i));
            }
        }

        function renderToolCall(msg) {
            const card = createToolCard(msg, 'Tool Call', 'tool-call-label');
            const inputSection = document.createElement('div');
            inputSection.className = 'tool-section';

            const inputLabel = document.createElement('div');
            inputLabel.className = 'tool-section-label';
            inputLabel.textContent = 'Input';
            inputSection.appendChild(inputLabel);

            const inputBlock = document.createElement('pre');
            inputBlock.className = 'tool-block';
            inputBlock.textContent = formatToolPayload(msg.tool_input || '');
            inputSection.appendChild(inputBlock);

            card.appendChild(inputSection);
            return card;
        }

        function renderToolResult(msg) {
            const card = createToolCard(msg, 'Tool Result', msg.is_error ? 'tool-result-label error' : 'tool-result-label');
            const screenshot = parseScreenshotPayload(msg);

            if (screenshot) {
                const metaSection = document.createElement('div');
                metaSection.className = 'tool-section';

                const metaLabel = document.createElement('div');
                metaLabel.className = 'tool-section-label';
                metaLabel.textContent = 'Screenshot';
                metaSection.appendChild(metaLabel);

                const grid = document.createElement('div');
                grid.className = 'tool-meta-grid';

                const metaEntries = [
                    ['Format', screenshot.format || 'jpeg'],
                    ['Width', String(screenshot.width)],
                    ['Height', String(screenshot.height)],
                    ['Bytes', String(screenshot.size)]
                ];
                if (screenshot.action_output) {
                    metaEntries.unshift(['Action', screenshot.action_output]);
                }
                metaEntries.forEach(function(entry) {
                    const item = document.createElement('div');
                    item.className = 'tool-meta-item';

                    const key = document.createElement('div');
                    key.className = 'tool-meta-key';
                    key.textContent = entry[0];

                    const value = document.createElement('div');
                    value.className = 'tool-meta-value';
                    value.textContent = entry[1];

                    item.appendChild(key);
                    item.appendChild(value);
                    grid.appendChild(item);
                });

                metaSection.appendChild(grid);
                card.appendChild(metaSection);

                const preview = document.createElement('div');
                preview.className = 'screenshot-preview';

                const previewLabel = document.createElement('div');
                previewLabel.className = 'tool-section-label';
                previewLabel.textContent = 'Preview';
                preview.appendChild(previewLabel);

                const image = document.createElement('img');
                image.alt = 'Screenshot preview';
                image.src = 'data:image/' + (screenshot.format || 'jpeg') + ';base64,' + screenshot.data;
                preview.appendChild(image);
                card.appendChild(preview);

                const details = document.createElement('details');
                details.className = 'tool-details';

                const summary = document.createElement('summary');
                summary.textContent = 'Raw payload';
                details.appendChild(summary);

                const rawBlock = document.createElement('pre');
                rawBlock.className = 'tool-block';
                rawBlock.textContent = formatToolPayload(msg.content || '');
                details.appendChild(rawBlock);
                card.appendChild(details);

                return card;
            }

            const resultSection = document.createElement('div');
            resultSection.className = 'tool-section';

            const resultLabel = document.createElement('div');
            resultLabel.className = 'tool-section-label';
            resultLabel.textContent = msg.is_error ? 'Error' : 'Output';
            resultSection.appendChild(resultLabel);

            const resultBlock = document.createElement('pre');
            resultBlock.className = 'tool-block';
            resultBlock.textContent = formatToolPayload(msg.content || '');
            resultSection.appendChild(resultBlock);
            card.appendChild(resultSection);

            return card;
        }

        function createToolCard(msg, label, labelClass) {
            const wrapper = document.createElement('div');
            wrapper.className = 'tool-card';

            const header = document.createElement('div');
            header.className = 'tool-card-header';

            const title = document.createElement('div');
            title.className = 'tool-card-title';

            const badge = document.createElement('span');
            badge.className = 'tool-label ' + labelClass;
            badge.textContent = label;

            const name = document.createElement('span');
            name.className = 'tool-name';
            name.textContent = msg.tool_name || 'unknown';

            title.appendChild(badge);
            title.appendChild(name);
            header.appendChild(title);
            wrapper.appendChild(header);

            return wrapper;
        }

        function formatToolPayload(value) {
            if (!value) return '';
            try {
                return JSON.stringify(redactToolPayloadForDisplay(JSON.parse(value)), null, 2);
            } catch (_) {
                return value;
            }
        }

        function redactToolPayloadForDisplay(value) {
            if (Array.isArray(value)) {
                return value.map(redactToolPayloadForDisplay);
            }
            if (!value || typeof value !== 'object') {
                return value;
            }

            const clone = {};
            Object.keys(value).forEach(function(key) {
                clone[key] = redactToolPayloadForDisplay(value[key]);
            });
            if (isScreenshotPayload(value)) {
                const bytes = Number(value.size);
                const byteLabel = Number.isFinite(bytes) && bytes >= 0 ? bytes + ' bytes' : 'base64 omitted';
                clone.data = '[base64 screenshot omitted: ' + byteLabel + ']';
            }
            return clone;
        }

        function parseScreenshotPayload(msg) {
            return parseScreenshotOutput(msg.tool_name || '', msg.content || '');
        }

        function parseScreenshotOutput(toolName, content) {
            if (!content) return null;
            try {
                const parsed = JSON.parse(content);
                if (!isScreenshotPayload(parsed)) return null;
                return parsed;
            } catch (_) {
                return null;
            }
        }

        function isScreenshotPayload(value) {
            return !!value &&
                typeof value === 'object' &&
                typeof value.data === 'string' &&
                value.data.length > 0 &&
                (value.format === 'jpeg' || value.format === 'jpg' || value.format === 'png' || value.width || value.height || value.size);
        }
    </script>
</body>
</html>
`
