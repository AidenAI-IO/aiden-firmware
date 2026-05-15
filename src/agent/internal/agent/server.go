package agent

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"
)

// Server provides HTTP API for agent interactions
type Server struct {
	runtime     *Runtime
	addr        string
	logger      *Logger
	mu          sync.Mutex
	history     []Message
	sttClient   STTClient
	ttsClient   TTSClient
	audioClient *AudioServiceClient
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
	Type        string              `json:"type"` // "user", "assistant", "tool_call", "tool_result"
	Content     string              `json:"content"`
	ToolName    string              `json:"tool_name,omitempty"`
	ToolInput   string              `json:"tool_input,omitempty"`
	Attachments []MessageAttachment `json:"attachments,omitempty"`
	Timestamp   time.Time           `json:"timestamp"`
	IsError     bool                `json:"is_error,omitempty"`
}

// ChatRequest represents an incoming chat request
type ChatRequest struct {
	Message     string              `json:"message"`
	Skills      []string            `json:"skills,omitempty"`
	Attachments []MessageAttachment `json:"attachments,omitempty"`
}

// ChatResponse represents a chat response
type ChatResponse struct {
	Response string    `json:"response"`
	History  []Message `json:"history"`
}

// NewServer creates a new HTTP server
func NewServer(runtime *Runtime, addr string) *Server {
	s := &Server{
		runtime: runtime,
		addr:    addr,
		logger:  runtime.logger,
		history: make([]Message, 0),
	}

	// Initialize TTS client if configured
	cfg := runtime.config
	sttClient, err := NewSTTClientFromConfig(cfg)
	if err != nil {
		if s.logger != nil {
			s.logger.Error("STT init failed: %v", err)
		}
	} else {
		s.sttClient = sttClient
	}

	ttsClient, err := NewTTSClientFromConfig(cfg)
	if err != nil {
		if s.logger != nil {
			s.logger.Error("TTS init failed: %v", err)
		}
	} else if ttsClient != nil {
		s.ttsClient = ttsClient
		s.audioClient = NewAudioServiceClient(cfg.Audio.SocketOrDefault())
		if s.logger != nil {
			s.logger.Info("TTS enabled: provider=%s voice=%s", cfg.TTS.Provider, cfg.TTS.VoiceID)
		}
	}

	return s
}

// Start starts the HTTP server
func (s *Server) Start() error {
	mux := http.NewServeMux()

	// API endpoints
	mux.HandleFunc("/api/chat", s.handleChat)
	mux.HandleFunc("/api/history", s.handleHistory)
	mux.HandleFunc("/api/clear", s.handleClear)

	// Static web UI
	mux.HandleFunc("/", s.handleIndex)

	if s.logger != nil {
		s.logger.Info("Starting HTTP server on %s", s.addr)
	}

	return http.ListenAndServe(s.addr, mux)
}

// handleChat handles chat requests
func (s *Server) handleChat(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req ChatRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid request", http.StatusBadRequest)
		return
	}

	inputText, runAttachments, historyAttachments, err := s.resolveRequestInput(req)
	if err != nil {
		http.Error(w, "Invalid request: "+err.Error(), http.StatusBadRequest)
		return
	}

	// Add user message to history
	s.appendHistory(Message{
		Type:        "user",
		Content:     inputText,
		Attachments: historyAttachments,
		Timestamp:   time.Now(),
	})

	if s.logger != nil {
		s.logger.Info("Chat request: %s attachments=%d", inputText, len(runAttachments))
	}

	// Run agent
	result, err := s.runtime.Run(context.Background(), RunRequest{
		Input:       inputText,
		Attachments: runAttachments,
		Skills:      req.Skills,
		EventHandler: func(event RunEvent) {
			s.appendHistory(Message{
				Type:      event.Type,
				Content:   event.Content,
				ToolName:  event.ToolName,
				ToolInput: event.ToolInput,
				Timestamp: event.Timestamp,
				IsError:   event.IsError,
			})
		},
	})

	if err != nil {
		if s.logger != nil {
			s.logger.Error("Agent run failed: %v", err)
		}
		http.Error(w, fmt.Sprintf("Agent error: %v", err), http.StatusInternalServerError)
		return
	}

	// Add assistant response to history
	s.appendHistory(Message{
		Type:      "assistant",
		Content:   result.Output,
		Timestamp: time.Now(),
	})
	historySnapshot := s.historySnapshot()

	// Play TTS in background if configured
	if s.ttsClient != nil && s.audioClient != nil && result.Output != "" {
		go func(text string) {
			if s.logger != nil {
				s.logger.Info("TTS playback: %q", text)
			}
			if err := s.ttsClient.TextToSpeechStream(text, s.audioClient); err != nil {
				if s.logger != nil {
					s.logger.Error("TTS playback failed: %v", err)
				}
			}
		}(result.Output)
	}

	// Return response
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(ChatResponse{
		Response: result.Output,
		History:  historySnapshot,
	})
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

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}

func (s *Server) appendHistory(message Message) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.history = append(s.history, message)
}

func (s *Server) historySnapshot() []Message {
	s.mu.Lock()
	defer s.mu.Unlock()
	historySnapshot := make([]Message, len(s.history))
	copy(historySnapshot, s.history)
	return historySnapshot
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

// handleIndex serves the web UI
func (s *Server) handleIndex(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write([]byte(webUI))
}

const webUI = `<!DOCTYPE html>
<html lang="en">
<head>
    <meta charset="UTF-8">
    <meta name="viewport" content="width=device-width, initial-scale=1.0">
    <title>Aiden Agent</title>
    <style>
        :root {
            color-scheme: light;
            --bg: #f7f7f4;
            --bg-accent: rgba(16, 163, 127, 0.12);
            --panel: rgba(255, 255, 255, 0.78);
            --panel-strong: rgba(255, 255, 255, 0.92);
            --sidebar: rgba(236, 238, 232, 0.82);
            --line: rgba(17, 24, 39, 0.08);
            --line-strong: rgba(17, 24, 39, 0.14);
            --text: #1f2937;
            --muted: #6b7280;
            --muted-strong: #4b5563;
            --accent: #10a37f;
            --accent-strong: #0c8a6a;
            --surface-soft: #f3f4f6;
            --surface-code: #f6f7f8;
            --user-bubble: #ffffff;
            --tool-call: rgba(180, 83, 9, 0.1);
            --tool-call-text: #92400e;
            --tool-ok: rgba(16, 163, 127, 0.12);
            --tool-ok-text: #0f766e;
            --tool-error: rgba(220, 38, 38, 0.12);
            --tool-error-text: #b91c1c;
            --shadow: 0 20px 60px rgba(15, 23, 42, 0.08);
            --shadow-soft: 0 10px 30px rgba(15, 23, 42, 0.04);
            --radius-xl: 28px;
            --radius-lg: 22px;
            --radius-md: 16px;
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
            font-family: ui-sans-serif, -apple-system, BlinkMacSystemFont, "Segoe UI", sans-serif;
            background:
                radial-gradient(circle at top left, var(--bg-accent), transparent 26%),
                radial-gradient(circle at bottom right, rgba(15, 118, 110, 0.08), transparent 20%),
                var(--bg);
            color: var(--text);
        }

        button,
        textarea {
            font: inherit;
        }

        .app-shell {
            min-height: 100vh;
            display: grid;
            grid-template-columns: 280px minmax(0, 1fr);
            gap: 16px;
            padding: 16px;
        }

        .sidebar {
            display: flex;
            flex-direction: column;
            gap: 18px;
            padding: 22px 18px;
            border-radius: var(--radius-xl);
            background: var(--sidebar);
            border: 1px solid var(--line);
            backdrop-filter: blur(18px);
            box-shadow: var(--shadow-soft);
        }

        .brand {
            display: flex;
            align-items: center;
            gap: 12px;
        }

        .brand-mark {
            width: 40px;
            height: 40px;
            display: inline-flex;
            align-items: center;
            justify-content: center;
            border-radius: 14px;
            background: linear-gradient(135deg, #0f172a 0%, #1f2937 100%);
            color: #f9fafb;
            font-size: 1rem;
            font-weight: 700;
            letter-spacing: 0.02em;
        }

        .brand-copy h1 {
            font-size: 1rem;
            font-weight: 700;
        }

        .brand-copy p,
        .sidebar-note,
        .sidebar-kicker,
        .composer-hint,
        .message-time,
        .empty-state p,
        .topbar p {
            color: var(--muted);
        }

        .brand-copy p,
        .sidebar-note,
        .topbar p,
        .empty-state p {
            line-height: 1.5;
        }

        .sidebar-kicker,
        .message-role,
        .tool-section-label,
        .tool-meta-key {
            font-size: 0.74rem;
            font-weight: 700;
            letter-spacing: 0.08em;
            text-transform: uppercase;
        }

        .sidebar-action {
            display: inline-flex;
            align-items: center;
            justify-content: center;
            width: 100%;
            padding: 0.9rem 1rem;
            border: 1px solid var(--line);
            border-radius: 18px;
            background: var(--panel-strong);
            color: var(--text);
            cursor: pointer;
            transition: transform 120ms ease, box-shadow 120ms ease, border-color 120ms ease;
        }

        .sidebar-action:hover,
        .sidebar-action:focus-visible,
        .send-btn:hover,
        .send-btn:focus-visible {
            transform: translateY(-1px);
            box-shadow: 0 8px 20px rgba(15, 23, 42, 0.08);
        }

        .sidebar-card {
            padding: 16px;
            border-radius: 20px;
            background: rgba(255, 255, 255, 0.58);
            border: 1px solid rgba(255, 255, 255, 0.5);
        }

        .sidebar-card strong {
            display: block;
            margin: 0.35rem 0 0.5rem;
            font-size: 0.98rem;
        }

        .main-panel {
            min-height: 0;
            display: flex;
            flex-direction: column;
            border-radius: calc(var(--radius-xl) + 4px);
            background: var(--panel);
            border: 1px solid rgba(255, 255, 255, 0.58);
            backdrop-filter: blur(20px);
            box-shadow: var(--shadow);
            overflow: hidden;
        }

        .topbar {
            padding: 28px 28px 20px;
            border-bottom: 1px solid var(--line);
        }

        .topbar h2 {
            margin-top: 0.45rem;
            font-size: clamp(1.7rem, 2vw, 2.25rem);
            font-weight: 700;
            letter-spacing: -0.03em;
        }

        .topbar p {
            max-width: 700px;
            margin-top: 0.45rem;
        }

        .conversation {
            flex: 1;
            min-height: 0;
            overflow-y: auto;
            padding: 0 28px;
        }

        .conversation-inner {
            max-width: 880px;
            margin: 0 auto;
            min-height: 100%;
            display: flex;
            flex-direction: column;
        }

        .empty-state {
            margin: auto;
            width: min(100%, 620px);
            padding: 38px 30px;
            border-radius: 30px;
            background: rgba(255, 255, 255, 0.72);
            border: 1px solid rgba(255, 255, 255, 0.8);
            box-shadow: var(--shadow-soft);
            text-align: center;
        }

        .empty-state h3 {
            font-size: clamp(1.7rem, 4vw, 2.6rem);
            letter-spacing: -0.04em;
        }

        .empty-state p {
            margin-top: 0.75rem;
        }

        .empty-state.hidden {
            display: none;
        }

        .message-list {
            padding: 28px 0 20px;
        }

        .message {
            width: 100%;
            margin-bottom: 26px;
        }

        .message-shell {
            display: flex;
            align-items: flex-start;
            gap: 14px;
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
            max-width: min(76%, 640px);
            background: var(--user-bubble);
            border: 1px solid var(--line);
            border-radius: 26px;
            padding: 16px 18px;
            box-shadow: var(--shadow-soft);
        }

        .message.assistant .message-body {
            max-width: 100%;
            flex: 1;
            padding-top: 2px;
        }

        .message.tool_call .message-body,
        .message.tool_result .message-body {
            flex: 1;
            background: rgba(255, 255, 255, 0.72);
            border: 1px solid var(--line);
            border-radius: 24px;
            padding: 16px 18px;
            box-shadow: var(--shadow-soft);
        }

        .message-avatar {
            flex: 0 0 36px;
            width: 36px;
            height: 36px;
            display: inline-flex;
            align-items: center;
            justify-content: center;
            border-radius: 12px;
            background: #111827;
            color: #f9fafb;
            font-size: 0.84rem;
            font-weight: 700;
        }

        .message-role {
            margin-bottom: 0.55rem;
            color: var(--muted-strong);
        }

        .message-copy {
            line-height: 1.72;
            white-space: pre-wrap;
            word-break: break-word;
        }

        .message.assistant .message-copy {
            font-size: 1rem;
        }

        .message-attachments {
            display: grid;
            gap: 12px;
            margin-bottom: 12px;
        }

        .message-attachments:last-child {
            margin-bottom: 0;
        }

        .attachment-card {
            display: flex;
            flex-direction: column;
            gap: 10px;
            padding: 12px;
            border-radius: 18px;
            background: rgba(243, 244, 246, 0.9);
            border: 1px solid rgba(17, 24, 39, 0.06);
        }

        .attachment-card img,
        .attachment-card audio {
            width: 100%;
            border-radius: 14px;
            background: #ffffff;
        }

        .attachment-card img {
            max-width: min(100%, 420px);
            border: 1px solid rgba(17, 24, 39, 0.08);
        }

        .attachment-meta {
            display: flex;
            align-items: center;
            justify-content: space-between;
            gap: 10px;
            flex-wrap: wrap;
            font-size: 0.84rem;
            color: var(--muted-strong);
        }

        .attachment-kind {
            display: inline-flex;
            align-items: center;
            padding: 0.24rem 0.55rem;
            border-radius: 999px;
            background: rgba(16, 163, 127, 0.12);
            color: var(--accent-strong);
            font-size: 0.72rem;
            font-weight: 700;
            letter-spacing: 0.04em;
            text-transform: uppercase;
        }

        .attachment-transcript {
            padding: 10px 12px;
            border-radius: 14px;
            background: rgba(255, 255, 255, 0.85);
            border: 1px solid rgba(17, 24, 39, 0.06);
            line-height: 1.6;
            color: var(--text);
        }

        .message-time {
            margin-top: 0.75rem;
            font-size: 0.78rem;
        }

        .tool-card {
            display: flex;
            flex-direction: column;
            gap: 14px;
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
            gap: 12px;
            flex-wrap: wrap;
        }

        .tool-card-title,
        .composer-actions {
            gap: 10px;
        }

        .tool-label {
            display: inline-flex;
            align-items: center;
            padding: 0.34rem 0.65rem;
            border-radius: 999px;
            font-size: 0.72rem;
            font-weight: 700;
            letter-spacing: 0.06em;
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
            font-size: 1rem;
            font-weight: 700;
        }

        .tool-section {
            display: flex;
            flex-direction: column;
            gap: 8px;
        }

        .tool-section-label,
        .tool-meta-key {
            color: var(--muted);
        }

        .tool-block {
            overflow-x: auto;
            padding: 14px 15px;
            border-radius: 16px;
            border: 1px solid rgba(17, 24, 39, 0.06);
            background: var(--surface-code);
            color: var(--text);
            font-family: ui-monospace, SFMono-Regular, SF Mono, Menlo, Consolas, monospace;
            font-size: 0.86rem;
            line-height: 1.55;
            white-space: pre-wrap;
            word-break: break-word;
        }

        .tool-meta-grid {
            display: grid;
            grid-template-columns: repeat(auto-fit, minmax(140px, 1fr));
            gap: 10px;
        }

        .tool-meta-item {
            padding: 12px 14px;
            border-radius: 16px;
            background: var(--surface-soft);
            border: 1px solid rgba(17, 24, 39, 0.06);
        }

        .tool-meta-value {
            margin-top: 0.2rem;
            font-size: 0.98rem;
            font-weight: 700;
        }

        .screenshot-preview {
            display: flex;
            flex-direction: column;
            gap: 10px;
        }

        .screenshot-preview img {
            width: 100%;
            max-width: min(100%, 720px);
            border-radius: 18px;
            border: 1px solid rgba(17, 24, 39, 0.08);
            box-shadow: var(--shadow-soft);
            background: #ffffff;
        }

        details.tool-details {
            border-top: 1px solid var(--line);
            padding-top: 12px;
        }

        details.tool-details summary {
            cursor: pointer;
            color: var(--muted-strong);
            font-weight: 600;
        }

        .composer {
            padding: 0 28px 28px;
        }

        .composer-shell {
            max-width: 880px;
            margin: 0 auto;
            padding: 14px 16px 12px;
            border-radius: 30px;
            background: var(--panel-strong);
            border: 1px solid var(--line);
            box-shadow: var(--shadow);
        }

        .composer-shell:focus-within {
            border-color: rgba(16, 163, 127, 0.4);
            box-shadow: 0 24px 64px rgba(15, 23, 42, 0.1);
        }

        .composer-input {
            width: 100%;
            min-height: 30px;
            max-height: 180px;
            border: none;
            outline: none;
            resize: none;
            background: transparent;
            color: var(--text);
            line-height: 1.6;
        }

        .composer-input::placeholder {
            color: #9ca3af;
        }

        .hidden-input {
            display: none;
        }

        .draft-attachments {
            display: grid;
            gap: 10px;
            margin-bottom: 12px;
        }

        .draft-attachment {
            display: flex;
            align-items: flex-start;
            gap: 10px;
            padding: 10px 12px;
            border-radius: 16px;
            background: rgba(243, 244, 246, 0.9);
            border: 1px solid rgba(17, 24, 39, 0.06);
        }

        .draft-attachment img {
            width: 64px;
            height: 64px;
            object-fit: cover;
            border-radius: 12px;
            border: 1px solid rgba(17, 24, 39, 0.06);
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
            font-size: 0.92rem;
            font-weight: 600;
            word-break: break-word;
        }

        .draft-attachment-meta {
            margin-top: 0.3rem;
            font-size: 0.78rem;
            color: var(--muted);
        }

        .draft-remove {
            flex: 0 0 auto;
            padding: 0.45rem 0.7rem;
            border: none;
            border-radius: 999px;
            background: rgba(220, 38, 38, 0.1);
            color: #b91c1c;
            cursor: pointer;
        }

        .composer-footer {
            margin-top: 10px;
        }

        .composer-toolbar {
            display: flex;
            align-items: center;
            gap: 10px;
            flex-wrap: wrap;
        }

        .composer-btn {
            padding: 0.6rem 0.9rem;
            border: 1px solid var(--line);
            border-radius: 999px;
            background: rgba(255, 255, 255, 0.9);
            color: var(--text);
            cursor: pointer;
            transition: border-color 120ms ease, background 120ms ease;
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
            font-size: 0.82rem;
        }

        .send-btn {
            padding: 0.72rem 1.15rem;
            border: none;
            border-radius: 999px;
            background: var(--accent);
            color: #ffffff;
            cursor: pointer;
            transition: transform 120ms ease, box-shadow 120ms ease, background 120ms ease;
        }

        .send-btn:disabled {
            background: #9ca3af;
            cursor: not-allowed;
            transform: none;
            box-shadow: none;
        }

        .loading {
            max-width: 880px;
            margin: 0 auto;
            padding: 0 0 8px;
            gap: 10px;
            color: var(--muted);
            visibility: hidden;
            opacity: 0;
            transition: opacity 140ms ease;
        }

        .loading.active {
            visibility: visible;
            opacity: 1;
        }

        .spinner {
            width: 16px;
            height: 16px;
            border-radius: 999px;
            border: 2px solid rgba(16, 163, 127, 0.2);
            border-top-color: var(--accent);
            animation: spin 0.8s linear infinite;
        }

        @keyframes spin {
            to { transform: rotate(360deg); }
        }

        @media (max-width: 960px) {
            .app-shell {
                grid-template-columns: 1fr;
            }

            .sidebar {
                padding: 18px;
            }
        }

        @media (max-width: 720px) {
            .app-shell,
            .conversation,
            .composer,
            .topbar {
                padding-left: 16px;
                padding-right: 16px;
            }

            .app-shell {
                padding-top: 16px;
                padding-bottom: 16px;
            }

            .main-panel {
                border-radius: 24px;
            }

            .conversation {
                padding-left: 16px;
                padding-right: 16px;
            }

            .composer {
                padding-bottom: 20px;
            }

            .composer-shell,
            .empty-state {
                border-radius: 24px;
            }

            .message.user .message-body {
                max-width: 100%;
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
        <aside class="sidebar">
            <div class="brand">
                <div class="brand-mark">AI</div>
                <div class="brand-copy">
                    <h1>Aiden Agent</h1>
                    <p>Operator workspace for reasoning, tool use, and iteration.</p>
                </div>
            </div>

            <button type="button" class="sidebar-action" onclick="clearHistory()">New chat</button>

            <div class="sidebar-card">
                <div class="sidebar-kicker">Workspace</div>
                <strong>ChatGPT-inspired layout</strong>
                <div class="sidebar-note">Neutral canvas, centered conversation flow, compact user bubbles, and transparent tool traces.</div>
            </div>

            <div class="sidebar-card">
                <div class="sidebar-kicker">Tips</div>
                <div class="sidebar-note">Send with Enter. Use Shift+Enter for a new line. Tool calls and results appear inline so the full execution path stays visible.</div>
            </div>
        </aside>

        <main class="main-panel">
            <header class="topbar">
                <div class="sidebar-kicker">Agent Console</div>
                <h2>Ask the agent, inspect the work, continue the thread.</h2>
                <p>The interface keeps the conversation centered and lets execution details sit alongside the final answer instead of overwhelming it.</p>
            </header>

            <section class="conversation" id="conversation">
                <div class="conversation-inner">
                    <div class="empty-state" id="emptyState">
                        <h3>How can Aiden help?</h3>
                        <p>Start a conversation to run the agent, inspect tool activity, and iterate in a single thread.</p>
                    </div>
                    <div class="message-list" id="messages"></div>
                </div>
            </section>

            <div class="loading" id="loading">
                <span class="spinner" aria-hidden="true"></span>
                <span>Working on it...</span>
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
        const emptyStateEl = document.getElementById('emptyState');
        const targetAudioSampleRate = 16000;

        let nextAttachmentId = 1;
        let draftAttachments = [];
        let recorderState = createRecorderState();

        loadHistory();
        autoResizeInput();

        inputEl.addEventListener('input', autoResizeInput);
        imageInputEl.addEventListener('change', handleImageSelection);
        inputEl.addEventListener('keydown', function(event) {
            if (event.key === 'Enter' && !event.shiftKey) {
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

        async function sendMessage() {
            if (sendBtn.disabled) return;
            if (recorderState.isRecording) {
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

            addMessage({
                type: 'user',
                content: message,
                attachments: pendingAttachments,
                timestamp: new Date().toISOString()
            });

            try {
                const res = await fetch('/api/chat', {
                    method: 'POST',
                    headers: { 'Content-Type': 'application/json' },
                    body: JSON.stringify({
                        message: message,
                        attachments: attachments
                    })
                });

                if (!res.ok) {
                    const errorText = await res.text();
                    throw new Error(errorText || 'Request failed');
                }

                const data = await res.json();
                renderHistory(data.history || []);
            } catch (err) {
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
                setComposerState(false);
                scrollToBottom();
            }
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
            role.textContent = getRoleLabel(msg.type, msg.tool_name);
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

            const timeDiv = document.createElement('div');
            timeDiv.className = 'message-time';
            timeDiv.textContent = formatTime(msg.timestamp);
            body.appendChild(timeDiv);

            shell.appendChild(avatar);
            shell.appendChild(body);
            card.appendChild(shell);
            return card;
        }

        function updateEmptyState() {
            emptyStateEl.classList.toggle('hidden', messagesDiv.children.length > 0);
        }

        function setComposerState(isLoading) {
            sendBtn.disabled = isLoading;
            imageBtn.disabled = isLoading || recorderState.isRecording;
            recordBtn.disabled = isLoading;
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

        function getRoleLabel(type, toolName) {
            if (type === 'user') return 'You';
            if (type === 'tool_call') return toolName ? 'Tool Call · ' + toolName : 'Tool Call';
            if (type === 'tool_result') return toolName ? 'Tool Result · ' + toolName : 'Tool Result';
            return 'Aiden';
        }

        function getAvatarLabel(type) {
            if (type === 'user') return 'You';
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
            const AudioContextClass = window.AudioContext || window.webkitAudioContext;
            if (!navigator.mediaDevices || !navigator.mediaDevices.getUserMedia || !AudioContextClass) {
                throw new Error('This browser does not support audio recording.');
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
            if (!recorderState.isRecording) return;

            recorderState.isRecording = false;
            const chunks = recorderState.chunks.slice();
            const sampleRate = recorderState.sampleRate;
            await teardownRecorder();

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

        async function teardownRecorder() {
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
        }

        function createRecorderState() {
            return {
                isRecording: false,
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
            recordBtn.classList.toggle('recording', recorderState.isRecording);
            recordBtn.textContent = recorderState.isRecording ? 'Stop recording' : 'Record audio';
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

                [
                    ['Format', screenshot.format || 'jpeg'],
                    ['Width', String(screenshot.width)],
                    ['Height', String(screenshot.height)],
                    ['Bytes', String(screenshot.size)]
                ].forEach(function(entry) {
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
                return JSON.stringify(JSON.parse(value), null, 2);
            } catch (_) {
                return value;
            }
        }

        function parseScreenshotPayload(msg) {
            if (msg.tool_name !== 'screenshot' || !msg.content) return null;
            try {
                const parsed = JSON.parse(msg.content);
                if (!parsed || typeof parsed.data !== 'string') return null;
                return parsed;
            } catch (_) {
                return null;
            }
        }
    </script>
</body>
</html>
`
