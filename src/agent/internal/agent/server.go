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
	s.mu.Lock()
	s.history = append(s.history, Message{
		Type:      "user",
		Content:   req.Message,
		Timestamp: time.Now(),
	})
	s.mu.Unlock()

	if s.logger != nil {
		s.logger.Info("Chat request: %s", req.Message)
	}

	// Run agent
	result, err := s.runtime.Run(context.Background(), RunRequest{
		Input:  req.Message,
		Skills: req.Skills,
	})

	if err != nil {
		if s.logger != nil {
			s.logger.Error("Agent run failed: %v", err)
		}
		http.Error(w, fmt.Sprintf("Agent error: %v", err), http.StatusInternalServerError)
		return
	}

	// Add assistant response to history
	s.mu.Lock()
	s.history = append(s.history, Message{
		Type:      "assistant",
		Content:   result.Output,
		Timestamp: time.Now(),
	})
	historySnapshot := make([]Message, len(s.history))
	copy(historySnapshot, s.history)
	s.mu.Unlock()

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

	s.mu.Lock()
	historySnapshot := make([]Message, len(s.history))
	copy(historySnapshot, s.history)
	s.mu.Unlock()

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
            font-family: -apple-system, BlinkMacSystemFont, "Segoe UI", Roboto, sans-serif;
            background: #f5f5f5;
            height: 100vh;
            display: flex;
            flex-direction: column;
        }
        .header {
            background: #2c3e50;
            color: white;
            padding: 1rem 2rem;
            box-shadow: 0 2px 4px rgba(0,0,0,0.1);
        }
        .header h1 { font-size: 1.5rem; }
        .container {
            flex: 1;
            display: flex;
            flex-direction: column;
            max-width: 1200px;
            width: 100%;
            margin: 0 auto;
            padding: 1rem;
            overflow: hidden;
        }
        .messages {
            flex: 1;
            overflow-y: auto;
            background: white;
            border-radius: 8px;
            padding: 1rem;
            margin-bottom: 1rem;
            box-shadow: 0 2px 4px rgba(0,0,0,0.1);
        }
        .message {
            margin-bottom: 1rem;
            padding: 0.75rem 1rem;
            border-radius: 8px;
            max-width: 80%;
        }
        .message.user {
            background: #3498db;
            color: white;
            margin-left: auto;
        }
        .message.assistant {
            background: #ecf0f1;
            color: #2c3e50;
        }
        .message.tool_call {
            background: #f39c12;
            color: white;
            font-family: monospace;
            font-size: 0.9rem;
        }
        .message.tool_result {
            background: #27ae60;
            color: white;
            font-family: monospace;
            font-size: 0.9rem;
        }
        .message-time {
            font-size: 0.75rem;
            opacity: 0.7;
            margin-top: 0.25rem;
        }
        .input-area {
            display: flex;
            gap: 0.5rem;
            background: white;
            padding: 1rem;
            border-radius: 8px;
            box-shadow: 0 2px 4px rgba(0,0,0,0.1);
        }
        input {
            flex: 1;
            padding: 0.75rem;
            border: 1px solid #ddd;
            border-radius: 4px;
            font-size: 1rem;
        }
        button {
            padding: 0.75rem 1.5rem;
            background: #3498db;
            color: white;
            border: none;
            border-radius: 4px;
            cursor: pointer;
            font-size: 1rem;
        }
        button:hover { background: #2980b9; }
        button:disabled {
            background: #95a5a6;
            cursor: not-allowed;
        }
        .clear-btn {
            background: #e74c3c;
        }
        .clear-btn:hover { background: #c0392b; }
        .loading {
            display: none;
            text-align: center;
            padding: 1rem;
            color: #7f8c8d;
        }
        .loading.active { display: block; }
    </style>
</head>
<body>
    <div class="header">
        <h1>🤖 Aiden Agent</h1>
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
                messagesDiv.innerHTML = '';
                history.forEach(msg => addMessage(msg));
                scrollToBottom();
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

            // Add user message immediately
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

                if (!res.ok) throw new Error('Request failed');

                const data = await res.json();

                // Add assistant response
                addMessage({
                    type: 'assistant',
                    content: data.response,
                    timestamp: new Date().toISOString()
                });
            } catch (err) {
                console.error('Failed to send message:', err);
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
                messagesDiv.innerHTML = '';
            } catch (err) {
                console.error('Failed to clear history:', err);
            }
        }

        function addMessage(msg) {
            const div = document.createElement('div');
            div.className = 'message ' + msg.type;

            let content = msg.content;
            if (msg.tool_name) {
                content = '🔧 ' + msg.tool_name + '\n' + (msg.tool_input || msg.content);
            }

            const contentDiv = document.createElement('div');
            contentDiv.innerHTML = escapeHtml(content);

            const timeDiv = document.createElement('div');
            timeDiv.className = 'message-time';
            timeDiv.textContent = formatTime(msg.timestamp);

            div.appendChild(contentDiv);
            div.appendChild(timeDiv);
            messagesDiv.appendChild(div);
        }

        function scrollToBottom() {
            messagesDiv.scrollTop = messagesDiv.scrollHeight;
        }

        function formatTime(timestamp) {
            const date = new Date(timestamp);
            return date.toLocaleTimeString();
        }

        function escapeHtml(text) {
            const div = document.createElement('div');
            div.textContent = text;
            return div.innerHTML.replace(/\n/g, '<br>');
        }
    </script>
</body>
</html>
`

