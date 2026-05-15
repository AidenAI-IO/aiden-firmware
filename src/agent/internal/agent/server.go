package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
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
	ttsClient   TTSClient
	audioClient *AudioServiceClient
}

// Message represents a chat message or tool call
type Message struct {
	Type      string    `json:"type"` // "user", "assistant", "tool_call", "tool_result"
	Content   string    `json:"content"`
	ToolName  string    `json:"tool_name,omitempty"`
	ToolInput string    `json:"tool_input,omitempty"`
	Timestamp time.Time `json:"timestamp"`
	IsError   bool      `json:"is_error,omitempty"`
}

// ChatRequest represents an incoming chat request
type ChatRequest struct {
	Message string   `json:"message"`
	Skills  []string `json:"skills,omitempty"`
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
	if cfg.TTS.Provider != "" {
		switch cfg.TTS.Provider {
		case "minimax":
			s.ttsClient = NewMinimaxTTS(cfg.TTS.APIKey, cfg.TTS.VoiceID, cfg.TTS.Emotion, cfg.TTS.Speed)
			s.audioClient = NewAudioServiceClient(cfg.Audio.SocketOrDefault())
			if s.logger != nil {
				s.logger.Info("TTS enabled: provider=%s voice=%s", cfg.TTS.Provider, cfg.TTS.VoiceID)
			}
		default:
			if s.logger != nil {
				s.logger.Error("Unknown TTS provider: %s", cfg.TTS.Provider)
			}
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

	if req.Message == "" {
		http.Error(w, "Message is required", http.StatusBadRequest)
		return
	}

	// Add user message to history
	s.appendHistory(Message{
		Type:      "user",
		Content:   req.Message,
		Timestamp: time.Now(),
	})

	if s.logger != nil {
		s.logger.Info("Chat request: %s", req.Message)
	}

	// Run agent
	result, err := s.runtime.Run(context.Background(), RunRequest{
		Input:  req.Message,
		Skills: req.Skills,
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
        * { margin: 0; padding: 0; box-sizing: border-box; }
        body {
            font-family: "SF Pro Display", "Segoe UI", sans-serif;
            background:
                radial-gradient(circle at top left, rgba(24, 119, 242, 0.08), transparent 28%),
                linear-gradient(180deg, #eef4fb 0%, #f7f9fc 48%, #edf1f7 100%);
            height: 100vh;
            display: flex;
            flex-direction: column;
            color: #16202a;
        }
        .header {
            background: rgba(13, 27, 42, 0.92);
            color: #f8fbff;
            padding: 1rem 1.5rem;
            box-shadow: 0 12px 40px rgba(13, 27, 42, 0.16);
            backdrop-filter: blur(12px);
        }
        .header h1 {
            font-size: 1.25rem;
            font-weight: 700;
            letter-spacing: 0.02em;
        }
        .container {
            flex: 1;
            display: flex;
            flex-direction: column;
            max-width: 1120px;
            width: 100%;
            margin: 0 auto;
            padding: 1rem;
            overflow: hidden;
        }
        .messages {
            flex: 1;
            overflow-y: auto;
            background: rgba(255, 255, 255, 0.84);
            border: 1px solid rgba(24, 39, 75, 0.08);
            border-radius: 20px;
            padding: 1rem;
            margin-bottom: 1rem;
            box-shadow: 0 18px 48px rgba(18, 38, 63, 0.1);
            backdrop-filter: blur(14px);
        }
        .message {
            margin-bottom: 0.9rem;
            border-radius: 18px;
            max-width: 88%;
            overflow: hidden;
        }
        .message.user {
            background: linear-gradient(135deg, #1570ef 0%, #0f5ac7 100%);
            color: white;
            margin-left: auto;
            box-shadow: 0 12px 28px rgba(21, 112, 239, 0.22);
        }
        .message.assistant {
            background: linear-gradient(180deg, rgba(255,255,255,0.98) 0%, rgba(243,247,251,0.98) 100%);
            color: #142033;
            border: 1px solid rgba(22, 32, 42, 0.08);
            box-shadow: 0 10px 28px rgba(18, 38, 63, 0.08);
        }
        .message.tool_call,
        .message.tool_result {
            max-width: 100%;
            background: rgba(255, 255, 255, 0.92);
            border: 1px solid rgba(22, 32, 42, 0.08);
            box-shadow: 0 8px 24px rgba(18, 38, 63, 0.06);
        }
        .message-inner {
            padding: 0.85rem 1rem;
        }
        .message-copy {
            line-height: 1.55;
            white-space: pre-wrap;
            word-break: break-word;
        }
        .message.user .message-copy {
            color: inherit;
        }
        .message-time {
            font-size: 0.75rem;
            opacity: 0.68;
            margin-top: 0.45rem;
        }
        .tool-card {
            display: flex;
            flex-direction: column;
            gap: 0.8rem;
        }
        .tool-card-header {
            display: flex;
            align-items: center;
            justify-content: space-between;
            gap: 0.75rem;
            flex-wrap: wrap;
        }
        .tool-card-title {
            display: flex;
            align-items: center;
            gap: 0.65rem;
        }
        .tool-label {
            display: inline-flex;
            align-items: center;
            padding: 0.24rem 0.58rem;
            border-radius: 999px;
            font-size: 0.72rem;
            font-weight: 700;
            letter-spacing: 0.05em;
            text-transform: uppercase;
        }
        .tool-call-label {
            background: rgba(217, 119, 6, 0.12);
            color: #b45309;
        }
        .tool-result-label {
            background: rgba(22, 163, 74, 0.12);
            color: #15803d;
        }
        .tool-result-label.error {
            background: rgba(220, 38, 38, 0.12);
            color: #b91c1c;
        }
        .tool-name {
            font-size: 1rem;
            font-weight: 700;
            color: #12263f;
        }
        .tool-section {
            display: flex;
            flex-direction: column;
            gap: 0.35rem;
        }
        .tool-section-label {
            font-size: 0.72rem;
            font-weight: 700;
            letter-spacing: 0.05em;
            text-transform: uppercase;
            color: #667085;
        }
        .tool-block {
            background: #0f172a;
            color: #e5eef9;
            border-radius: 12px;
            padding: 0.8rem 0.9rem;
            font-family: "SFMono-Regular", Consolas, "Liberation Mono", monospace;
            font-size: 0.86rem;
            line-height: 1.55;
            white-space: pre-wrap;
            word-break: break-word;
            overflow-x: auto;
        }
        .tool-block.result {
            background: #f8fafc;
            color: #0f172a;
            border: 1px solid rgba(15, 23, 42, 0.08);
        }
        .tool-meta-grid {
            display: grid;
            grid-template-columns: repeat(auto-fit, minmax(140px, 1fr));
            gap: 0.6rem;
        }
        .tool-meta-item {
            background: #f8fafc;
            border: 1px solid rgba(15, 23, 42, 0.08);
            border-radius: 12px;
            padding: 0.7rem 0.8rem;
        }
        .tool-meta-key {
            font-size: 0.72rem;
            font-weight: 700;
            letter-spacing: 0.05em;
            text-transform: uppercase;
            color: #667085;
        }
        .tool-meta-value {
            margin-top: 0.2rem;
            font-size: 0.95rem;
            font-weight: 700;
            color: #12263f;
        }
        .screenshot-preview {
            display: flex;
            flex-direction: column;
            gap: 0.6rem;
        }
        .screenshot-preview img {
            max-width: min(100%, 720px);
            width: 100%;
            border-radius: 14px;
            border: 1px solid rgba(15, 23, 42, 0.12);
            box-shadow: 0 12px 24px rgba(18, 38, 63, 0.12);
            background: #ffffff;
        }
        details.tool-details {
            border-top: 1px solid rgba(15, 23, 42, 0.08);
            padding-top: 0.7rem;
        }
        details.tool-details summary {
            cursor: pointer;
            color: #475467;
            font-size: 0.9rem;
            font-weight: 600;
        }
        .input-area {
            display: flex;
            gap: 0.5rem;
            background: rgba(255, 255, 255, 0.9);
            padding: 1rem;
            border-radius: 18px;
            border: 1px solid rgba(24, 39, 75, 0.08);
            box-shadow: 0 14px 38px rgba(18, 38, 63, 0.08);
        }
        input {
            flex: 1;
            padding: 0.75rem;
            border: 1px solid rgba(102, 112, 133, 0.24);
            border-radius: 12px;
            font-size: 1rem;
            background: rgba(248, 250, 252, 0.92);
        }
        button {
            padding: 0.75rem 1.5rem;
            background: linear-gradient(135deg, #1570ef 0%, #175cd3 100%);
            color: white;
            border: none;
            border-radius: 12px;
            cursor: pointer;
            font-size: 0.98rem;
            font-weight: 700;
        }
        button:hover { filter: brightness(0.98); }
        button:disabled {
            background: #95a5a6;
            cursor: not-allowed;
        }
        .clear-btn {
            background: linear-gradient(135deg, #ef4444 0%, #dc2626 100%);
        }
        .loading {
            display: none;
            text-align: center;
            padding: 1rem;
            color: #667085;
        }
        .loading.active { display: block; }
        @media (max-width: 720px) {
            .container { padding: 0.75rem; }
            .message { max-width: 100%; }
            .input-area { flex-direction: column; }
            button { width: 100%; }
        }
    </style>
</head>
<body>
    <div class="header">
        <h1>Aiden Agent</h1>
    </div>
    <div class="container">
        <div class="messages" id="messages"></div>
        <div class="loading" id="loading">Processing...</div>
        <div class="input-area">
            <input type="text" id="input" placeholder="Type your message..." />
            <button onclick="sendMessage()" id="sendBtn">Send</button>
            <button onclick="clearHistory()" class="clear-btn">Clear</button>
        </div>
    </div>
    <script>
        const messagesDiv = document.getElementById('messages');
        const inputEl = document.getElementById('input');
        const sendBtn = document.getElementById('sendBtn');
        const loadingDiv = document.getElementById('loading');

        // Load history on page load
        loadHistory();

        inputEl.addEventListener('keypress', (e) => {
            if (e.key === 'Enter') sendMessage();
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
            const message = inputEl.value.trim();
            if (!message) return;

            inputEl.value = '';
            sendBtn.disabled = true;
            loadingDiv.classList.add('active');

            addMessage({
                type: 'user',
                content: message,
                timestamp: new Date().toISOString()
            });

            try {
                const res = await fetch('/api/chat', {
                    method: 'POST',
                    headers: { 'Content-Type': 'application/json' },
                    body: JSON.stringify({ message })
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
                sendBtn.disabled = false;
                loadingDiv.classList.remove('active');
                scrollToBottom();
            }
        }

        async function clearHistory() {
            if (!confirm('Clear conversation history?')) return;

            try {
                await fetch('/api/clear', { method: 'POST' });
                renderHistory([]);
            } catch (err) {
                console.error('Failed to clear history:', err);
            }
        }

        function renderHistory(history) {
            messagesDiv.innerHTML = '';
            history.forEach(function(msg) { addMessage(msg); });
            scrollToBottom();
        }

        function addMessage(msg) {
            const card = document.createElement('article');
            card.className = 'message ' + (msg.type || 'assistant');

            const inner = document.createElement('div');
            inner.className = 'message-inner';

            if (msg.type === 'tool_call') {
                inner.appendChild(renderToolCall(msg));
            } else if (msg.type === 'tool_result') {
                inner.appendChild(renderToolResult(msg));
            } else {
                const contentDiv = document.createElement('div');
                contentDiv.className = 'message-copy';
                contentDiv.textContent = msg.content || '';
                inner.appendChild(contentDiv);
            }

            const timeDiv = document.createElement('div');
            timeDiv.className = 'message-time';
            timeDiv.textContent = formatTime(msg.timestamp);
            inner.appendChild(timeDiv);

            card.appendChild(inner);
            messagesDiv.appendChild(card);
        }

        function scrollToBottom() {
            messagesDiv.scrollTop = messagesDiv.scrollHeight;
        }

        function formatTime(timestamp) {
            const date = new Date(timestamp);
            return date.toLocaleTimeString();
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
                rawBlock.className = 'tool-block result';
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
            resultBlock.className = 'tool-block result';
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
