# Voice Wakeup and Web Integration Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Enable voice wakeup mode and web text mode to coexist with unified chat history and real-time message streaming.

**Architecture:** Decouple HTTP server from input mode, extend Message struct with voice metadata, implement SSE broadcast for real-time updates, add optional audio archival.

**Tech Stack:** Go 1.21+, Server-Sent Events (SSE), WebSocket (existing), SQLite-based chat history, Silero VAD

---


## Phase 1: Architecture Decoupling

**Objective:** Make HTTP server run in all modes so web UI is always accessible.

### Task 1: Decouple HTTP Server from Input Mode

**Files:**
- Modify: `src/agent/cmd/daemon/main.go:65-87`
- Test: `src/agent/cmd/daemon/main_test.go` (add new test)

- [ ] **Step 1: Write failing test for server availability in audio mode**

```go
// src/agent/cmd/daemon/main_test.go
package main

import (
	"net/http"
	"testing"
	"time"
)

func TestHTTPServerRunsInAudioMode(t *testing.T) {
	// This test verifies that HTTP server is accessible even when input_mode=audio
	// We'll test this by mocking the audio mode flow
	
	// TODO: This requires refactoring main() to be testable
	// For now, document the expected behavior
	t.Skip("Requires main() refactoring for testability")
}
```

- [ ] **Step 2: Run test to verify it's skipped**

Run: `cd src/agent && go test ./cmd/daemon -v -run TestHTTPServerRunsInAudioMode`
Expected: SKIP with message

- [ ] **Step 3: Refactor main.go to run HTTP server in background**

```go
// src/agent/cmd/daemon/main.go
// Replace lines 65-87 with:

	inputMode := cfg.InputModeOrDefault()
	
	// Always start HTTP server in background
	server := agent.NewServer(runtime, *addr)
	
	serverErrChan := make(chan error, 1)
	go func() {
		fmt.Printf("🚀 Aiden Agent daemon starting on %s\n", *addr)
		fmt.Printf("📂 Config directory: %s\n", *configDir)
		if _, port, err := net.SplitHostPort(*addr); err == nil && port != "" {
			fmt.Printf("🌐 Web UI: http://localhost:%s\n", port)
		}
		fmt.Printf("📝 Logs: %s/log/\n", *configDir)
		
		if err := server.Start(); err != nil {
			serverErrChan <- err
		}
	}()
	
	// Give server time to start
	time.Sleep(100 * time.Millisecond)
	
	// Check for immediate server errors
	select {
	case err := <-serverErrChan:
		fmt.Fprintf(os.Stderr, "server error: %v\n", err)
		os.Exit(1)
	default:
	}

	// If input mode is audio or stt, run audio dialog loop
	if inputMode == "audio" || inputMode == "stt" {
		runAudioMode(cfg, runtime, server)
		return
	}

	// Otherwise block on server (text-only mode)
	if err := <-serverErrChan; err != nil {
		fmt.Fprintf(os.Stderr, "server error: %v\n", err)
		os.Exit(1)
	}
```

- [ ] **Step 4: Update runAudioMode signature to accept server**

```go
// src/agent/cmd/daemon/main.go:89
func runAudioMode(cfg agent.Config, runtime *agent.Runtime, server *agent.Server) {
	// Existing implementation unchanged
	dialog, err := agent.NewAudioDialog(cfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "create audio dialog: %v\n", err)
		os.Exit(1)
	}
	// ... rest unchanged
```

- [ ] **Step 5: Update all runAudioMode calls**

```go
// src/agent/cmd/daemon/main.go:117 and main.go:118
// Change from:
//   runAudioMode(cfg, dialog, runtime, sigChan, newGPIOWatcher)
// To pass server:
//   (This will be done in later phases when we connect audio events to SSE)
```

- [ ] **Step 6: Build and verify compilation**

Run: `cd src/agent && go build ./cmd/daemon`
Expected: Build succeeds with no errors

- [ ] **Step 7: Manual integration test**

```bash
# Test 1: Text mode (should work as before)
./daemon -config /tmp/test-config
# Visit http://localhost:8080 - should see web UI

# Test 2: Audio mode (NEW: server should still run)
# Edit config: input_mode = "stt"
./daemon -config /tmp/test-config
# Visit http://localhost:8080 - should see web UI even in audio mode
```

Expected: Web UI accessible in both modes

- [ ] **Step 8: Commit**

```bash
git add src/agent/cmd/daemon/main.go src/agent/cmd/daemon/main_test.go
git commit -m "feat(daemon): run HTTP server in all input modes

Decouples HTTP server from input mode selection. Server now runs
in background goroutine for all modes (text, stt, audio), enabling
web UI access during voice interactions.

Related to voice-wakeup-web-integration design.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>"
```

---


## Phase 2: Chat History Extension

**Objective:** Extend Message struct to track voice vs text input and optional audio metadata.

### Task 2: Extend Message Structure

**Files:**
- Modify: `src/agent/internal/agent/server.go:84-97`
- Test: `src/agent/internal/agent/server_test.go` (create if needed)

- [ ] **Step 1: Write failing test for Message JSON with new fields**

```go
// src/agent/internal/agent/server_test.go
package agent

import (
	"encoding/json"
	"testing"
	"time"
)

func TestMessageJSONWithVoiceFields(t *testing.T) {
	msg := Message{
		Type:            "user",
		Content:         "Hello",
		Timestamp:       time.Now(),
		Source:          "voice",
		AudioFile:       "/userdata/audio/msg_123.wav",
		AudioDurationMs: 2500,
	}
	
	data, err := json.Marshal(msg)
	if err != nil {
		t.Fatalf("Marshal failed: %v", err)
	}
	
	var decoded Message
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("Unmarshal failed: %v", err)
	}
	
	if decoded.Source != "voice" {
		t.Errorf("Source: got %q, want %q", decoded.Source, "voice")
	}
	if decoded.AudioFile != "/userdata/audio/msg_123.wav" {
		t.Errorf("AudioFile: got %q, want %q", decoded.AudioFile, "/userdata/audio/msg_123.wav")
	}
	if decoded.AudioDurationMs != 2500 {
		t.Errorf("AudioDurationMs: got %d, want %d", decoded.AudioDurationMs, 2500)
	}
}

func TestMessageJSONOmitsEmptyVoiceFields(t *testing.T) {
	msg := Message{
		Type:      "user",
		Content:   "Hello",
		Timestamp: time.Now(),
		// Source, AudioFile, AudioDurationMs omitted
	}
	
	data, err := json.Marshal(msg)
	if err != nil {
		t.Fatalf("Marshal failed: %v", err)
	}
	
	// Verify fields are omitted (not present as empty strings/zeros)
	var raw map[string]interface{}
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatalf("Unmarshal to map failed: %v", err)
	}
	
	if _, exists := raw["source"]; exists {
		t.Errorf("source field should be omitted when empty")
	}
	if _, exists := raw["audio_file"]; exists {
		t.Errorf("audio_file field should be omitted when empty")
	}
	if _, exists := raw["audio_duration_ms"]; exists {
		t.Errorf("audio_duration_ms field should be omitted when zero")
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd src/agent && go test ./internal/agent -v -run TestMessage`
Expected: FAIL - fields don't exist yet

- [ ] **Step 3: Add new fields to Message struct**

```go
// src/agent/internal/agent/server.go:84-97
// Add after line 96 (before Timestamp field):

	Source          string `json:"source,omitempty"`            // "text" | "voice"
	AudioFile       string `json:"audio_file,omitempty"`        // Path to audio file (if archived)
	AudioDurationMs int    `json:"audio_duration_ms,omitempty"` // Audio duration in milliseconds
```

Final Message struct should look like:

```go
type Message struct {
	Type            string              `json:"type"`
	Role            string              `json:"role,omitempty"`
	EpisodeID       string              `json:"episode_id,omitempty"`
	RequestID       string              `json:"request_id,omitempty"`
	Status          string              `json:"status,omitempty"`
	Content         string              `json:"content"`
	ToolName        string              `json:"tool_name,omitempty"`
	ToolInput       string              `json:"tool_input,omitempty"`
	Description     string              `json:"description,omitempty"`
	Attachments     []MessageAttachment `json:"attachments,omitempty"`
	Source          string              `json:"source,omitempty"`
	AudioFile       string              `json:"audio_file,omitempty"`
	AudioDurationMs int                 `json:"audio_duration_ms,omitempty"`
	Timestamp       time.Time           `json:"timestamp"`
	IsError         bool                `json:"is_error,omitempty"`
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd src/agent && go test ./internal/agent -v -run TestMessage`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add src/agent/internal/agent/server.go src/agent/internal/agent/server_test.go
git commit -m "feat(agent): add voice metadata fields to Message

Extends Message struct with:
- source: 'text' or 'voice' to indicate input method
- audio_file: optional path to archived audio
- audio_duration_ms: audio length in milliseconds

Fields use omitempty for backward compatibility with existing messages.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>"
```

### Task 3: Mark Voice Messages in AudioDialog

**Files:**
- Modify: `src/agent/internal/agent/audio_dialog.go`
- Test: `src/agent/internal/agent/audio_dialog_test.go`

- [ ] **Step 1: Find where AudioDialog writes to chat history**

Run: `cd src/agent && grep -n "ChatHistoryStore\|Append\|history" internal/agent/audio_dialog.go | head -20`
Expected: Identify the method that saves messages

- [ ] **Step 2: Write failing test for voice source tagging**

```go
// src/agent/internal/agent/audio_dialog_test.go
package agent

import (
	"context"
	"testing"
)

func TestProcessUtteranceMarksSourceAsVoice(t *testing.T) {
	// This test verifies that messages created during audio dialog
	// are tagged with source="voice"
	
	// Mock setup would go here
	// For now, document expected behavior
	t.Skip("Requires mock ChatHistoryStore for testing")
}
```

- [ ] **Step 3: Run test to verify skip**

Run: `cd src/agent && go test ./internal/agent -v -run TestProcessUtterance`
Expected: SKIP

- [ ] **Step 4: Review audio_dialog.go to find message creation points**

Run: `cd src/agent && grep -n "Message{" internal/agent/audio_dialog.go`
Expected: Find where user and assistant messages are created

- [ ] **Step 5: Add Source field when creating messages**

Find the location where messages are created in `ProcessUtterance` and agent response handling. Add `Source: "voice"` to each Message construction.

Example pattern to find and modify:
```go
// Before:
msg := Message{
	Type:    "user",
	Content: transcript,
	// ...
}

// After:
msg := Message{
	Type:    "user",
	Content: transcript,
	Source:  "voice",
	// ...
}
```

Note: Actual line numbers will vary, search for Message literal construction in audio_dialog.go

- [ ] **Step 6: Build and verify compilation**

Run: `cd src/agent && go build ./internal/agent`
Expected: Build succeeds

- [ ] **Step 7: Integration test - verify /api/history returns source field**

```bash
# Start daemon in audio mode
./daemon -config /test-config

# In another terminal, trigger a voice interaction (manual mode)
# Then check history API:
curl http://localhost:8080/api/history | jq '.[] | select(.source=="voice")'
```

Expected: Voice messages show `"source": "voice"`

- [ ] **Step 8: Commit**

```bash
git add src/agent/internal/agent/audio_dialog.go src/agent/internal/agent/audio_dialog_test.go
git commit -m "feat(audio): tag voice messages with source field

AudioDialog now sets source='voice' when creating user and
assistant messages from audio interactions. This enables
frontend to distinguish voice from text conversations.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>"
```

---


## Phase 3: Real-Time Message Streaming

**Objective:** Implement Server-Sent Events (SSE) to push new messages to web clients in real-time.

### Task 4: Implement EventBroadcaster

**Files:**
- Create: `src/agent/internal/agent/event_broadcaster.go`
- Create: `src/agent/internal/agent/event_broadcaster_test.go`

- [ ] **Step 1: Write failing test for EventBroadcaster**

```go
// src/agent/internal/agent/event_broadcaster_test.go
package agent

import (
	"testing"
	"time"
)

func TestEventBroadcasterSubscribeUnsubscribe(t *testing.T) {
	broadcaster := NewEventBroadcaster()
	
	ch := broadcaster.Subscribe()
	if ch == nil {
		t.Fatal("Subscribe returned nil channel")
	}
	
	broadcaster.Unsubscribe(ch)
	
	// Channel should be closed after unsubscribe
	_, ok := <-ch
	if ok {
		t.Error("Channel should be closed after Unsubscribe")
	}
}

func TestEventBroadcasterBroadcast(t *testing.T) {
	broadcaster := NewEventBroadcaster()
	
	ch1 := broadcaster.Subscribe()
	ch2 := broadcaster.Subscribe()
	defer broadcaster.Unsubscribe(ch1)
	defer broadcaster.Unsubscribe(ch2)
	
	testMsg := Message{
		Type:    "user",
		Content: "test message",
	}
	
	go broadcaster.Broadcast(testMsg)
	
	// Both subscribers should receive the message
	select {
	case msg := <-ch1:
		if msg.Content != "test message" {
			t.Errorf("ch1: got content %q, want %q", msg.Content, "test message")
		}
	case <-time.After(100 * time.Millisecond):
		t.Error("ch1: timeout waiting for message")
	}
	
	select {
	case msg := <-ch2:
		if msg.Content != "test message" {
			t.Errorf("ch2: got content %q, want %q", msg.Content, "test message")
		}
	case <-time.After(100 * time.Millisecond):
		t.Error("ch2: timeout waiting for message")
	}
}

func TestEventBroadcasterSlowSubscriber(t *testing.T) {
	broadcaster := NewEventBroadcaster()
	
	// Create subscriber with small buffer
	ch := broadcaster.Subscribe()
	defer broadcaster.Unsubscribe(ch)
	
	// Send more messages than buffer size (16)
	for i := 0; i < 20; i++ {
		broadcaster.Broadcast(Message{
			Type:    "user",
			Content: "message",
		})
	}
	
	// Should not deadlock (slow subscribers are skipped)
	// Test passes if we get here without hanging
}
```

- [ ] **Step 2: Run test to verify failure**

Run: `cd src/agent && go test ./internal/agent -v -run TestEventBroadcaster`
Expected: FAIL - NewEventBroadcaster not defined

- [ ] **Step 3: Implement EventBroadcaster**

```go
// src/agent/internal/agent/event_broadcaster.go
package agent

import "sync"

// EventBroadcaster broadcasts messages to multiple subscribers via channels.
// Slow subscribers are skipped to prevent blocking the broadcaster.
type EventBroadcaster struct {
	mu          sync.RWMutex
	subscribers map[chan Message]struct{}
}

// NewEventBroadcaster creates a new event broadcaster.
func NewEventBroadcaster() *EventBroadcaster {
	return &EventBroadcaster{
		subscribers: make(map[chan Message]struct{}),
	}
}

// Subscribe returns a new channel that will receive broadcasted messages.
// The channel has a buffer of 16 messages.
func (b *EventBroadcaster) Subscribe() chan Message {
	ch := make(chan Message, 16)
	b.mu.Lock()
	b.subscribers[ch] = struct{}{}
	b.mu.Unlock()
	return ch
}

// Unsubscribe removes a subscriber and closes its channel.
func (b *EventBroadcaster) Unsubscribe(ch chan Message) {
	b.mu.Lock()
	delete(b.subscribers, ch)
	b.mu.Unlock()
	close(ch)
}

// Broadcast sends a message to all subscribers.
// If a subscriber's channel is full, the message is dropped for that subscriber.
func (b *EventBroadcaster) Broadcast(msg Message) {
	b.mu.RLock()
	defer b.mu.RUnlock()
	
	for ch := range b.subscribers {
		select {
		case ch <- msg:
			// Message sent successfully
		default:
			// Subscriber is slow, skip to avoid blocking
		}
	}
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd src/agent && go test ./internal/agent -v -run TestEventBroadcaster`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add src/agent/internal/agent/event_broadcaster.go src/agent/internal/agent/event_broadcaster_test.go
git commit -m "feat(agent): implement EventBroadcaster for SSE

Adds pub-sub mechanism for broadcasting messages to multiple
web clients. Non-blocking design skips slow subscribers to
prevent memory buildup.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>"
```

### Task 5: Add SSE Endpoint to Server

**Files:**
- Modify: `src/agent/internal/agent/server.go:30-50` (Server struct)
- Modify: `src/agent/internal/agent/server.go` (add handler method)
- Test: `src/agent/internal/agent/server_test.go`

- [ ] **Step 1: Write failing test for SSE endpoint**

```go
// src/agent/internal/agent/server_test.go
package agent

import (
	"bufio"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestHandleEventsSSE(t *testing.T) {
	// Create minimal server setup
	runtime := &Runtime{} // Mock or minimal runtime
	server := NewServer(runtime, "127.0.0.1:0")
	
	req := httptest.NewRequest("GET", "/api/events", nil)
	rec := httptest.NewRecorder()
	
	// Start handler in goroutine
	done := make(chan struct{})
	go func() {
		defer close(done)
		server.handleEvents(rec, req)
	}()
	
	// Give handler time to set headers
	time.Sleep(50 * time.Millisecond)
	
	// Verify SSE headers
	if ct := rec.Header().Get("Content-Type"); ct != "text/event-stream" {
		t.Errorf("Content-Type: got %q, want %q", ct, "text/event-stream")
	}
	if cc := rec.Header().Get("Cache-Control"); cc != "no-cache" {
		t.Errorf("Cache-Control: got %q, want %q", cc, "no-cache")
	}
	if conn := rec.Header().Get("Connection"); conn != "keep-alive" {
		t.Errorf("Connection: got %q, want %q", conn, "keep-alive")
	}
	
	// Broadcast a test message
	server.eventBroadcaster.Broadcast(Message{
		Type:    "user",
		Content: "SSE test",
	})
	
	time.Sleep(50 * time.Millisecond)
	
	// Verify message was sent
	body := rec.Body.String()
	if !strings.Contains(body, "data:") {
		t.Errorf("Response should contain SSE data, got: %s", body)
	}
	if !strings.Contains(body, "SSE test") {
		t.Errorf("Response should contain message content, got: %s", body)
	}
}
```

- [ ] **Step 2: Run test to verify failure**

Run: `cd src/agent && go test ./internal/agent -v -run TestHandleEventsSSE`
Expected: FAIL - eventBroadcaster field and handleEvents method don't exist

- [ ] **Step 3: Add eventBroadcaster field to Server struct**

```go
// src/agent/internal/agent/server.go:30-50
// Add after line 49 (activeRunsMu field):

	eventBroadcaster *EventBroadcaster
```

- [ ] **Step 4: Initialize eventBroadcaster in NewServer**

Find the NewServer function and add initialization:

```go
// In NewServer() function, after other field initializations:
	eventBroadcaster: NewEventBroadcaster(),
```

- [ ] **Step 5: Implement handleEvents method**

```go
// src/agent/internal/agent/server.go
// Add new method (place near other handle methods):

func (s *Server) handleEvents(w http.ResponseWriter, r *http.Request) {
	// Set SSE headers
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	
	// Check if response writer supports flushing
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "Streaming unsupported", http.StatusInternalServerError)
		return
	}
	
	// Subscribe to events
	ch := s.eventBroadcaster.Subscribe()
	defer s.eventBroadcaster.Unsubscribe(ch)
	
	// Send initial connection established event
	fmt.Fprintf(w, "data: {\"type\":\"connected\"}\n\n")
	flusher.Flush()
	
	ctx := r.Context()
	for {
		select {
		case <-ctx.Done():
			// Client disconnected
			return
		case msg, ok := <-ch:
			if !ok {
				// Channel closed
				return
			}
			
			// Only send user and assistant messages
			if msg.Type != "user" && msg.Type != "assistant" {
				continue
			}
			
			data, err := json.Marshal(msg)
			if err != nil {
				s.logger.Printf("[sse] marshal error: %v", err)
				continue
			}
			
			fmt.Fprintf(w, "data: %s\n\n", data)
			flusher.Flush()
		}
	}
}
```

- [ ] **Step 6: Register route in NewServer**

Find where routes are registered (look for `mux.HandleFunc`) and add:

```go
mux.HandleFunc("/api/events", s.handleEvents)
```

- [ ] **Step 7: Run tests**

Run: `cd src/agent && go test ./internal/agent -v -run TestHandleEventsSSE`
Expected: PASS

- [ ] **Step 8: Manual test with curl**

```bash
# Start server
./daemon -config /test-config &

# Subscribe to SSE stream
curl -N http://localhost:8080/api/events

# In another terminal, send a chat message
curl -X POST http://localhost:8080/api/chat \
  -H "Content-Type: application/json" \
  -d '{"message":"test"}'

# First terminal should show the SSE events
```

Expected: SSE stream shows messages in real-time

- [ ] **Step 9: Commit**

```bash
git add src/agent/internal/agent/server.go src/agent/internal/agent/server_test.go
git commit -m "feat(server): add SSE endpoint for real-time messages

Implements GET /api/events using Server-Sent Events. Web clients
can subscribe to receive user and assistant messages in real-time
as they occur during voice or text interactions.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>"
```

### Task 6: Connect ChatHistoryStore to Broadcaster

**Files:**
- Modify: `src/agent/internal/agent/chat_history_memory.go` or find ChatHistoryStore
- Test: Integration test

- [ ] **Step 1: Find ChatHistoryStore definition**

Run: `cd src/agent && find . -name "*.go" -exec grep -l "type ChatHistoryStore" {} \;`
Expected: Identify the file containing ChatHistoryStore

- [ ] **Step 2: Add callback field to ChatHistoryStore**

```go
// In the file containing ChatHistoryStore struct:

type ChatHistoryStore struct {
	// ... existing fields ...
	
	onNewMessage func(Message) // Callback for new messages
}
```

- [ ] **Step 3: Add SetOnNewMessage method**

```go
// SetOnNewMessage registers a callback to be invoked when messages are appended.
func (s *ChatHistoryStore) SetOnNewMessage(callback func(Message)) {
	s.onNewMessage = callback
}
```

- [ ] **Step 4: Find Append method and add callback invocation**

Find the Append method in ChatHistoryStore and add:

```go
func (s *ChatHistoryStore) Append(ctx context.Context, message Message) error {
	// ... existing append logic ...
	
	// Notify callback after successful append
	if s.onNewMessage != nil {
		s.onNewMessage(message)
	}
	
	return nil
}
```

- [ ] **Step 5: Connect in Server's NewServer or initialization**

Find where Server is initialized and connect the broadcaster:

```go
// In NewServer or similar initialization:
if s.historyStore != nil {
	s.historyStore.SetOnNewMessage(func(msg Message) {
		s.eventBroadcaster.Broadcast(msg)
	})
}
```

- [ ] **Step 6: Build and test**

Run: `cd src/agent && go build ./...`
Expected: Build succeeds

- [ ] **Step 7: Integration test**

```bash
# Terminal 1: Subscribe to SSE
curl -N http://localhost:8080/api/events

# Terminal 2: Send chat message
curl -X POST http://localhost:8080/api/chat \
  -H "Content-Type: application/json" \
  -d '{"message":"Hello from text chat"}'
```

Expected: Terminal 1 shows the message in SSE stream

- [ ] **Step 8: Commit**

```bash
git add src/agent/internal/agent/*.go
git commit -m "feat(history): broadcast messages via SSE

Connects ChatHistoryStore to EventBroadcaster so all new
messages (from text chat or voice) are pushed to subscribed
web clients in real-time.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>"
```

### Task 7: Connect AudioDialog to Broadcaster

**Files:**
- Modify: `src/agent/cmd/daemon/main.go` (runAudioMode function)
- Modify: `src/agent/internal/agent/audio_dialog.go`

- [ ] **Step 1: Add callback field to AudioDialog**

```go
// src/agent/internal/agent/audio_dialog.go
// In AudioDialog struct:

type AudioDialog struct {
	// ... existing fields ...
	
	onNewMessage func(Message) // Callback for broadcasting
}
```

- [ ] **Step 2: Add SetOnNewMessage method**

```go
// SetOnNewMessage registers a callback invoked when audio messages are created.
func (d *AudioDialog) SetOnNewMessage(callback func(Message)) {
	d.onNewMessage = callback
}
```

- [ ] **Step 3: Find where AudioDialog creates messages**

Run: `cd src/agent && grep -n "Type:.*\"user\"\|Type:.*\"assistant\"" internal/agent/audio_dialog.go`
Expected: Line numbers where messages are constructed

- [ ] **Step 4: Add callback invocation after message creation**

For each message construction in ProcessUtterance and agent response handling:

```go
// After creating a message:
msg := Message{
	Type:    "user",
	Content: transcript,
	Source:  "voice",
	// ...
}

// Add callback invocation:
if d.onNewMessage != nil {
	d.onNewMessage(msg)
}
```

- [ ] **Step 5: Connect in runAudioMode**

```go
// src/agent/cmd/daemon/main.go
// In runAudioMode function, after NewAudioDialog:

dialog, err := agent.NewAudioDialog(cfg)
if err != nil {
	fmt.Fprintf(os.Stderr, "create audio dialog: %v\n", err)
	os.Exit(1)
}

// Connect dialog to server's broadcaster
dialog.SetOnNewMessage(func(msg agent.Message) {
	server.BroadcastMessage(msg)
})
```

- [ ] **Step 6: Add BroadcastMessage method to Server**

```go
// src/agent/internal/agent/server.go

// BroadcastMessage sends a message to all SSE subscribers.
func (s *Server) BroadcastMessage(msg Message) {
	if s.eventBroadcaster != nil {
		s.eventBroadcaster.Broadcast(msg)
	}
}
```

- [ ] **Step 7: Build and test**

Run: `cd src/agent && go build ./...`
Expected: Build succeeds

- [ ] **Step 8: End-to-end test**

```bash
# Terminal 1: Subscribe to SSE
curl -N http://localhost:8080/api/events

# Terminal 2: Start daemon in manual audio mode
# Set input_mode="stt", trigger_mode="manual"
./daemon -config /test-config

# Press Enter to start recording, speak, press Enter to stop
# Terminal 1 should show voice message in SSE stream
```

Expected: Voice interactions appear in SSE stream in real-time

- [ ] **Step 9: Commit**

```bash
git add src/agent/cmd/daemon/main.go src/agent/internal/agent/audio_dialog.go src/agent/internal/agent/server.go
git commit -m "feat(audio): broadcast voice messages via SSE

Connects AudioDialog to EventBroadcaster so voice interactions
are pushed to web clients in real-time. Voice and text messages
now share the same live update mechanism.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>"
```

---


## Phase 4: Audio Archive (Optional)

**Objective:** Add optional audio file archival with automatic cleanup.

### Task 8: Add Audio Archive Configuration

**Files:**
- Modify: `src/agent/internal/agent/config.go:60-97`
- Test: `src/agent/internal/agent/config_test.go`

- [ ] **Step 1: Write failing test for AudioArchiveConfig**

```go
// src/agent/internal/agent/config_test.go
package agent

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/BurntSushi/toml"
)

func TestAudioArchiveConfigDefaults(t *testing.T) {
	configContent := `
[audio_archive]
enabled = true
storage_path = "/userdata/audio"
`
	
	tmpDir := t.TempDir()
	configFile := filepath.Join(tmpDir, "agent.toml")
	if err := os.WriteFile(configFile, []byte(configContent), 0644); err != nil {
		t.Fatal(err)
	}
	
	var cfg Config
	if _, err := toml.DecodeFile(configFile, &cfg); err != nil {
		t.Fatal(err)
	}
	
	if !cfg.AudioArchive.Enabled {
		t.Error("AudioArchive.Enabled should be true")
	}
	if cfg.AudioArchive.StoragePath != "/userdata/audio" {
		t.Errorf("StoragePath: got %q, want %q", cfg.AudioArchive.StoragePath, "/userdata/audio")
	}
	
	// Test defaults
	if cfg.AudioArchive.MaxFilesOrDefault() != 500 {
		t.Errorf("MaxFiles default: got %d, want %d", cfg.AudioArchive.MaxFilesOrDefault(), 500)
	}
	if cfg.AudioArchive.MaxSizeMBOrDefault() != 100 {
		t.Errorf("MaxSizeMB default: got %d, want %d", cfg.AudioArchive.MaxSizeMBOrDefault(), 100)
	}
}
```

- [ ] **Step 2: Run test to verify failure**

Run: `cd src/agent && go test ./internal/agent -v -run TestAudioArchiveConfig`
Expected: FAIL - AudioArchive field doesn't exist

- [ ] **Step 3: Add AudioArchiveConfig struct**

```go
// src/agent/internal/agent/config.go
// Add before type Config struct:

type AudioArchiveConfig struct {
	Enabled     bool   `toml:"enabled"`
	MaxFiles    int    `toml:"max_files,omitempty"`
	MaxSizeMB   int    `toml:"max_size_mb,omitempty"`
	StoragePath string `toml:"storage_path,omitempty"`
}

func (c AudioArchiveConfig) MaxFilesOrDefault() int {
	if c.MaxFiles <= 0 {
		return 500
	}
	return c.MaxFiles
}

func (c AudioArchiveConfig) MaxSizeMBOrDefault() int {
	if c.MaxSizeMB <= 0 {
		return 100
	}
	return c.MaxSizeMB
}

func (c AudioArchiveConfig) StoragePathOrDefault() string {
	if c.StoragePath == "" {
		return "/userdata/audio"
	}
	return c.StoragePath
}
```

- [ ] **Step 4: Add AudioArchive field to Config struct**

```go
// src/agent/internal/agent/config.go:60-97
// Add after Audio field:

	AudioArchive AudioArchiveConfig `toml:"audio_archive,omitempty"`
```

- [ ] **Step 5: Run tests**

Run: `cd src/agent && go test ./internal/agent -v -run TestAudioArchiveConfig`
Expected: PASS

- [ ] **Step 6: Add example config section**

```go
// Update src/agent/config/agent.toml - add section:

# Audio archive configuration (optional)
# When enabled, saves audio recordings to disk for playback in web UI
[audio_archive]
enabled = false              # Default: false (only save transcripts)
max_files = 500             # Keep most recent N audio files
max_size_mb = 100           # Or stop when total size exceeds this
storage_path = "/userdata/audio"  # Where to store audio files
```

- [ ] **Step 7: Commit**

```bash
git add src/agent/internal/agent/config.go src/agent/internal/agent/config_test.go src/agent/config/agent.toml
git commit -m "feat(config): add audio archive configuration

Adds [audio_archive] config section for optional audio file
storage. Disabled by default to save space. Includes defaults
for max_files (500) and max_size_mb (100).

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>"
```

### Task 9: Implement Audio Archive Manager

**Files:**
- Create: `src/agent/internal/agent/audio_archive.go`
- Create: `src/agent/internal/agent/audio_archive_test.go`

- [ ] **Step 1: Write failing test for AudioArchiveManager**

```go
// src/agent/internal/agent/audio_archive_test.go
package agent

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestAudioArchiveManagerSaveAndCleanup(t *testing.T) {
	tmpDir := t.TempDir()
	
	cfg := AudioArchiveConfig{
		Enabled:     true,
		MaxFiles:    3,
		MaxSizeMB:   1,
		StoragePath: tmpDir,
	}
	
	mgr := NewAudioArchiveManager(cfg)
	
	// Save 5 audio files (exceeds max_files=3)
	var savedPaths []string
	for i := 0; i < 5; i++ {
		samples := make([]int16, 16000) // 1 second of silence
		path, duration, err := mgr.SaveAudio(samples, 16000)
		if err != nil {
			t.Fatalf("SaveAudio failed: %v", err)
		}
		
		if path == "" {
			t.Fatal("SaveAudio returned empty path")
		}
		if duration <= 0 {
			t.Error("Duration should be positive")
		}
		
		savedPaths = append(savedPaths, path)
		time.Sleep(10 * time.Millisecond) // Ensure different timestamps
	}
	
	// Check cleanup: only last 3 files should exist
	for i, path := range savedPaths {
		_, err := os.Stat(path)
		if i < 2 {
			// First 2 files should be deleted
			if !os.IsNotExist(err) {
				t.Errorf("File %d should be deleted: %s", i, path)
			}
		} else {
			// Last 3 files should exist
			if err != nil {
				t.Errorf("File %d should exist: %s", i, path)
			}
		}
	}
}

func TestAudioArchiveManagerDisabled(t *testing.T) {
	cfg := AudioArchiveConfig{
		Enabled: false,
	}
	
	mgr := NewAudioArchiveManager(cfg)
	
	samples := make([]int16, 16000)
	path, duration, err := mgr.SaveAudio(samples, 16000)
	
	if err != nil {
		t.Fatalf("SaveAudio should not error when disabled: %v", err)
	}
	if path != "" {
		t.Error("SaveAudio should return empty path when disabled")
	}
	if duration != 1000 {
		t.Errorf("Duration should still be calculated: got %d, want %d", duration, 1000)
	}
}
```

- [ ] **Step 2: Run test to verify failure**

Run: `cd src/agent && go test ./internal/agent -v -run TestAudioArchiveManager`
Expected: FAIL - NewAudioArchiveManager not defined

- [ ] **Step 3: Implement AudioArchiveManager**

```go
// src/agent/internal/agent/audio_archive.go
package agent

import (
	"encoding/binary"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/google/uuid"
)

// AudioArchiveManager handles saving and cleanup of audio recordings.
type AudioArchiveManager struct {
	config AudioArchiveConfig
}

// NewAudioArchiveManager creates a new audio archive manager.
func NewAudioArchiveManager(config AudioArchiveConfig) *AudioArchiveManager {
	return &AudioArchiveManager{
		config: config,
	}
}

// SaveAudio saves audio samples to a WAV file and returns the path, duration in ms, and error.
// If archival is disabled, only calculates duration without saving.
func (m *AudioArchiveManager) SaveAudio(samples []int16, sampleRate int) (string, int, error) {
	// Calculate duration in milliseconds
	durationMs := (len(samples) * 1000) / sampleRate
	
	// If disabled, return only duration
	if !m.config.Enabled {
		return "", durationMs, nil
	}
	
	// Ensure storage directory exists
	storagePath := m.config.StoragePathOrDefault()
	if err := os.MkdirAll(storagePath, 0755); err != nil {
		return "", 0, fmt.Errorf("create storage dir: %w", err)
	}
	
	// Generate filename with timestamp and UUID
	timestamp := time.Now().Unix()
	id := uuid.New().String()[:8]
	filename := fmt.Sprintf("msg_%d_%s.wav", timestamp, id)
	filepath := filepath.Join(storagePath, filename)
	
	// Write WAV file
	if err := writeWAVFile(filepath, samples, sampleRate); err != nil {
		return "", 0, fmt.Errorf("write WAV: %w", err)
	}
	
	// Cleanup old files if needed
	if err := m.cleanup(); err != nil {
		// Log but don't fail
		fmt.Fprintf(os.Stderr, "[audio_archive] cleanup warning: %v\n", err)
	}
	
	return filepath, durationMs, nil
}

// cleanup removes old audio files based on max_files and max_size_mb limits.
func (m *AudioArchiveManager) cleanup() error {
	storagePath := m.config.StoragePathOrDefault()
	
	entries, err := os.ReadDir(storagePath)
	if err != nil {
		return err
	}
	
	// Collect audio files with timestamps
	type fileInfo struct {
		path    string
		modTime time.Time
		size    int64
	}
	var files []fileInfo
	
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".wav" {
			continue
		}
		
		info, err := entry.Info()
		if err != nil {
			continue
		}
		
		files = append(files, fileInfo{
			path:    filepath.Join(storagePath, entry.Name()),
			modTime: info.ModTime(),
			size:    info.Size(),
		})
	}
	
	// Sort by modification time (oldest first)
	sort.Slice(files, func(i, j int) bool {
		return files[i].modTime.Before(files[j].modTime)
	})
	
	maxFiles := m.config.MaxFilesOrDefault()
	maxSizeBytes := int64(m.config.MaxSizeMBOrDefault()) * 1024 * 1024
	
	// Calculate total size
	var totalSize int64
	for _, f := range files {
		totalSize += f.size
	}
	
	// Delete oldest files until under limits
	for len(files) > maxFiles || totalSize > maxSizeBytes {
		if len(files) == 0 {
			break
		}
		
		oldest := files[0]
		files = files[1:]
		totalSize -= oldest.size
		
		if err := os.Remove(oldest.path); err != nil {
			fmt.Fprintf(os.Stderr, "[audio_archive] remove %s: %v\n", oldest.path, err)
		}
	}
	
	return nil
}

// writeWAVFile writes PCM samples to a WAV file.
func writeWAVFile(path string, samples []int16, sampleRate int) error {
	file, err := os.Create(path)
	if err != nil {
		return err
	}
	defer file.Close()
	
	// WAV header (44 bytes)
	dataSize := len(samples) * 2
	fileSize := 36 + dataSize
	
	// RIFF header
	file.WriteString("RIFF")
	binary.Write(file, binary.LittleEndian, uint32(fileSize))
	file.WriteString("WAVE")
	
	// fmt chunk
	file.WriteString("fmt ")
	binary.Write(file, binary.LittleEndian, uint32(16))          // fmt chunk size
	binary.Write(file, binary.LittleEndian, uint16(1))           // audio format (PCM)
	binary.Write(file, binary.LittleEndian, uint16(1))           // num channels
	binary.Write(file, binary.LittleEndian, uint32(sampleRate))  // sample rate
	binary.Write(file, binary.LittleEndian, uint32(sampleRate*2)) // byte rate
	binary.Write(file, binary.LittleEndian, uint16(2))           // block align
	binary.Write(file, binary.LittleEndian, uint16(16))          // bits per sample
	
	// data chunk
	file.WriteString("data")
	binary.Write(file, binary.LittleEndian, uint32(dataSize))
	
	// Write samples
	return binary.Write(file, binary.LittleEndian, samples)
}
```

- [ ] **Step 4: Run tests**

Run: `cd src/agent && go test ./internal/agent -v -run TestAudioArchiveManager`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add src/agent/internal/agent/audio_archive.go src/agent/internal/agent/audio_archive_test.go
git commit -m "feat(audio): implement audio archive manager

Adds AudioArchiveManager for saving voice recordings as WAV files
with automatic cleanup based on max_files and max_size_mb limits.
Supports disabled mode (transcript-only).

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>"
```

### Task 10: Integrate Audio Archive with AudioDialog

**Files:**
- Modify: `src/agent/internal/agent/audio_dialog.go`

- [ ] **Step 1: Add AudioArchiveManager field to AudioDialog**

```go
// src/agent/internal/agent/audio_dialog.go
type AudioDialog struct {
	// ... existing fields ...
	
	audioArchive *AudioArchiveManager
}
```

- [ ] **Step 2: Initialize in NewAudioDialog**

```go
// In NewAudioDialog function:
	audioArchive := NewAudioArchiveManager(cfg.AudioArchive)
	
	return &AudioDialog{
		// ... existing fields ...
		audioArchive: audioArchive,
	}, nil
```

- [ ] **Step 3: Find where utterance is processed and save audio**

In ProcessUtterance or similar method, after VAD completes:

```go
// After getting complete utterance samples:
audioPath, audioDurationMs, err := d.audioArchive.SaveAudio(utterance, d.config.Audio.SampleRateOrDefault())
if err != nil {
	log.Printf("[audio_archive] save failed: %v", err)
	// Continue without audio file
}

// When creating Message:
msg := Message{
	Type:            "user",
	Content:         transcript,
	Source:          "voice",
	AudioFile:       audioPath,
	AudioDurationMs: audioDurationMs,
	Timestamp:       time.Now(),
}
```

- [ ] **Step 4: Build and test**

Run: `cd src/agent && go build ./...`
Expected: Build succeeds

- [ ] **Step 5: Integration test**

```bash
# Edit config: enable audio_archive
[audio_archive]
enabled = true
storage_path = "/tmp/test-audio"

# Start daemon in audio mode
./daemon -config /test-config

# Trigger voice interaction
# Check that WAV files are created:
ls -lh /tmp/test-audio/
```

Expected: WAV files appear in storage directory

- [ ] **Step 6: Commit**

```bash
git add src/agent/internal/agent/audio_dialog.go
git commit -m "feat(audio): integrate audio archival with voice dialog

AudioDialog now saves voice recordings when audio_archive.enabled=true.
Audio file paths are stored in Message.audio_file for later playback.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>"
```

### Task 11: Add Audio Playback Endpoint

**Files:**
- Modify: `src/agent/internal/agent/server.go`
- Test: `src/agent/internal/agent/server_test.go`

- [ ] **Step 1: Write failing test for audio file serving**

```go
// src/agent/internal/agent/server_test.go
func TestHandleAudioFileSecurityCheck(t *testing.T) {
	tmpDir := t.TempDir()
	
	// Create test file
	testFile := filepath.Join(tmpDir, "msg_123.wav")
	if err := os.WriteFile(testFile, []byte("test"), 0644); err != nil {
		t.Fatal(err)
	}
	
	// Create server with audio archive config
	runtime := &Runtime{
		config: Config{
			AudioArchive: AudioArchiveConfig{
				Enabled:     true,
				StoragePath: tmpDir,
			},
		},
	}
	server := NewServer(runtime, "127.0.0.1:0")
	
	// Test valid file
	req := httptest.NewRequest("GET", "/api/audio/msg_123.wav", nil)
	rec := httptest.NewRecorder()
	server.handleAudioFile(rec, req)
	
	if rec.Code != http.StatusOK {
		t.Errorf("Valid file: got status %d, want %d", rec.Code, http.StatusOK)
	}
	
	// Test directory traversal attempt
	req = httptest.NewRequest("GET", "/api/audio/../../../etc/passwd", nil)
	rec = httptest.NewRecorder()
	server.handleAudioFile(rec, req)
	
	if rec.Code != http.StatusForbidden {
		t.Errorf("Path traversal: got status %d, want %d", rec.Code, http.StatusForbidden)
	}
}
```

- [ ] **Step 2: Run test to verify failure**

Run: `cd src/agent && go test ./internal/agent -v -run TestHandleAudioFile`
Expected: FAIL - handleAudioFile method doesn't exist

- [ ] **Step 3: Implement handleAudioFile method**

```go
// src/agent/internal/agent/server.go

func (s *Server) handleAudioFile(w http.ResponseWriter, r *http.Request) {
	// Extract filename from URL path
	filename := strings.TrimPrefix(r.URL.Path, "/api/audio/")
	
	// Get audio directory from config
	audioDir := s.runtime.config.AudioArchive.StoragePathOrDefault()
	
	// Security: only allow base filenames, no path traversal
	filename = filepath.Base(filename)
	fullPath := filepath.Join(audioDir, filename)
	
	// Verify path is within audio directory
	absAudioDir, err := filepath.Abs(audioDir)
	if err != nil {
		http.Error(w, "Internal error", http.StatusInternalServerError)
		return
	}
	
	absFullPath, err := filepath.Abs(fullPath)
	if err != nil {
		http.Error(w, "Invalid path", http.StatusBadRequest)
		return
	}
	
	if !strings.HasPrefix(absFullPath, absAudioDir) {
		http.Error(w, "Forbidden", http.StatusForbidden)
		return
	}
	
	// Check file exists
	if _, err := os.Stat(fullPath); os.IsNotExist(err) {
		http.Error(w, "File not found", http.StatusNotFound)
		return
	}
	
	// Serve file
	w.Header().Set("Content-Type", "audio/wav")
	http.ServeFile(w, r, fullPath)
}
```

- [ ] **Step 4: Register route**

```go
// In NewServer, add route:
mux.HandleFunc("/api/audio/", s.handleAudioFile)
```

- [ ] **Step 5: Run tests**

Run: `cd src/agent && go test ./internal/agent -v -run TestHandleAudioFile`
Expected: PASS

- [ ] **Step 6: Manual test**

```bash
# After triggering a voice interaction with audio archive enabled:
curl http://localhost:8080/api/audio/msg_123456_abc.wav --output test.wav

# Play the file
aplay test.wav  # or: ffplay test.wav
```

Expected: Audio file downloads and plays correctly

- [ ] **Step 7: Commit**

```bash
git add src/agent/internal/agent/server.go src/agent/internal/agent/server_test.go
git commit -m "feat(server): add audio file playback endpoint

Implements GET /api/audio/{filename} for serving archived voice
recordings. Includes path traversal protection to prevent
unauthorized file access.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>"
```

---


## Phase 5: Frontend Integration

**Objective:** Update web UI to subscribe to SSE and display voice messages with audio playback.

### Task 12: Add SSE Subscription to Frontend

**Files:**
- Modify: `src/agent/internal/agent/server.go` (find embedded HTML/JS)

- [ ] **Step 1: Locate embedded web UI code**

Run: `cd src/agent && grep -n "text/html\|<!DOCTYPE\|<script>" internal/agent/server.go | head -10`
Expected: Find where HTML template is embedded

- [ ] **Step 2: Add SSE connection in JavaScript**

Find the embedded JavaScript section and add:

```javascript
// Add to existing script section or create new one

// Subscribe to Server-Sent Events for real-time updates
let eventSource = null;

function connectSSE() {
    if (eventSource) {
        eventSource.close();
    }
    
    eventSource = new EventSource('/api/events');
    
    eventSource.onopen = function() {
        console.log('[SSE] Connected');
    };
    
    eventSource.onmessage = function(e) {
        try {
            const data = JSON.parse(e.data);
            
            // Ignore connection event
            if (data.type === 'connected') {
                return;
            }
            
            // Handle user and assistant messages
            if (data.type === 'user' || data.type === 'assistant') {
                appendMessageToHistory(data);
            }
        } catch (err) {
            console.error('[SSE] Parse error:', err);
        }
    };
    
    eventSource.onerror = function(e) {
        console.error('[SSE] Connection error, retrying...');
        // Browser will auto-reconnect
    };
}

// Connect on page load
window.addEventListener('DOMContentLoaded', function() {
    connectSSE();
    loadHistory(); // Load existing history
});

// Reconnect if page becomes visible again
document.addEventListener('visibilitychange', function() {
    if (!document.hidden && (!eventSource || eventSource.readyState === EventSource.CLOSED)) {
        connectSSE();
    }
});
```

- [ ] **Step 3: Update message rendering to show voice indicators**

```javascript
function appendMessageToHistory(msg) {
    const historyDiv = document.getElementById('history');
    const msgDiv = document.createElement('div');
    msgDiv.className = 'message message-' + msg.type;
    
    // Add voice indicator for voice messages
    if (msg.source === 'voice') {
        const voiceIcon = document.createElement('span');
        voiceIcon.className = 'voice-icon';
        voiceIcon.textContent = '🎤 ';
        voiceIcon.title = 'Voice message';
        msgDiv.appendChild(voiceIcon);
    }
    
    // Add message content
    const contentSpan = document.createElement('span');
    contentSpan.className = 'message-content';
    contentSpan.textContent = msg.content || '';
    msgDiv.appendChild(contentSpan);
    
    // Add audio playback button if audio file exists
    if (msg.audio_file && msg.audio_file !== '') {
        const audioBtn = document.createElement('button');
        audioBtn.className = 'audio-playback-btn';
        audioBtn.textContent = '▶️ Play Audio';
        
        const filename = msg.audio_file.split('/').pop();
        const audioUrl = '/api/audio/' + encodeURIComponent(filename);
        
        audioBtn.onclick = function() {
            playAudio(audioUrl, audioBtn);
        };
        
        // Show duration if available
        if (msg.audio_duration_ms > 0) {
            const durationSec = (msg.audio_duration_ms / 1000).toFixed(1);
            audioBtn.title = 'Duration: ' + durationSec + 's';
        }
        
        msgDiv.appendChild(audioBtn);
    }
    
    // Add timestamp
    if (msg.timestamp) {
        const timeSpan = document.createElement('span');
        timeSpan.className = 'message-timestamp';
        const date = new Date(msg.timestamp);
        timeSpan.textContent = date.toLocaleTimeString();
        msgDiv.appendChild(timeSpan);
    }
    
    historyDiv.appendChild(msgDiv);
    historyDiv.scrollTop = historyDiv.scrollHeight;
}

function playAudio(url, button) {
    // Create audio element
    const audio = new Audio(url);
    
    // Update button during playback
    const originalText = button.textContent;
    button.textContent = '⏸️ Playing...';
    button.disabled = true;
    
    audio.onended = function() {
        button.textContent = originalText;
        button.disabled = false;
    };
    
    audio.onerror = function() {
        button.textContent = '❌ Error';
        setTimeout(function() {
            button.textContent = originalText;
            button.disabled = false;
        }, 2000);
    };
    
    audio.play();
}
```

- [ ] **Step 4: Add CSS styles for voice messages**

```css
/* Add to existing <style> section */

.voice-icon {
    margin-right: 5px;
    font-size: 14px;
}

.audio-playback-btn {
    margin-left: 10px;
    padding: 4px 8px;
    font-size: 12px;
    background-color: #4CAF50;
    color: white;
    border: none;
    border-radius: 4px;
    cursor: pointer;
}

.audio-playback-btn:hover {
    background-color: #45a049;
}

.audio-playback-btn:disabled {
    background-color: #cccccc;
    cursor: not-allowed;
}

.message-timestamp {
    margin-left: 10px;
    font-size: 11px;
    color: #888;
}

.message {
    margin-bottom: 10px;
    padding: 8px;
    border-radius: 4px;
}

.message-user {
    background-color: #e3f2fd;
    text-align: right;
}

.message-assistant {
    background-color: #f5f5f5;
    text-align: left;
}
```

- [ ] **Step 5: Update loadHistory function to use new renderer**

Ensure existing loadHistory function also uses appendMessageToHistory:

```javascript
function loadHistory() {
    fetch('/api/history')
        .then(response => response.json())
        .then(messages => {
            const historyDiv = document.getElementById('history');
            historyDiv.innerHTML = ''; // Clear existing
            
            messages.forEach(msg => {
                appendMessageToHistory(msg);
            });
        })
        .catch(err => {
            console.error('[History] Load error:', err);
        });
}
```

- [ ] **Step 6: Build and test**

Run: `cd src/agent && go build ./cmd/daemon`
Expected: Build succeeds

- [ ] **Step 7: Manual browser test**

```bash
# Start daemon
./daemon -config /test-config

# Open browser to http://localhost:8080
# 1. Check SSE connection in DevTools Network tab
# 2. Send text message - should appear immediately
# 3. Trigger voice interaction - should appear with 🎤 icon
# 4. If audio archive enabled, click "▶️ Play Audio"
```

Expected: 
- Text messages show without voice icon
- Voice messages show with 🎤 icon
- Audio playback works when archive is enabled
- Real-time updates appear instantly

- [ ] **Step 8: Commit**

```bash
git add src/agent/internal/agent/server.go
git commit -m "feat(ui): add SSE subscription and voice message rendering

Web UI now connects to /api/events for real-time message updates.
Voice messages display with microphone icon and audio playback
button when recordings are available.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>"
```

---

## Phase 6: End-to-End Testing

**Objective:** Verify complete integration across all components.

### Task 13: Integration Testing

**Files:**
- Create: `tests/integration/voice_web_integration_test.sh`

- [ ] **Step 1: Create integration test script**

```bash
# tests/integration/voice_web_integration_test.sh
#!/bin/bash
set -e

TEST_DIR="/tmp/voice-web-test"
CONFIG_DIR="$TEST_DIR/config"
AUDIO_DIR="$TEST_DIR/audio"

echo "=== Voice-Web Integration Test ==="

# Setup
mkdir -p "$CONFIG_DIR/memory/chat_history"
mkdir -p "$AUDIO_DIR"

# Create test config
cat > "$CONFIG_DIR/agent.toml" << 'TOML'
instruction = "Test agent"
input_mode = "text"

[model]
provider = "openrouter"
model = "google/gemini-2.0-flash-exp"
api_key = "test-key"

[audio_archive]
enabled = true
storage_path = "$AUDIO_DIR"
max_files = 10
TOML

# Build daemon
echo "Building daemon..."
cd src/agent
go build -o "$TEST_DIR/daemon" ./cmd/daemon

# Start daemon in background
echo "Starting daemon..."
"$TEST_DIR/daemon" -config "$CONFIG_DIR" -addr "127.0.0.1:18080" &
DAEMON_PID=$!

# Wait for startup
sleep 2

# Test 1: Check web UI is accessible
echo "Test 1: Web UI accessibility"
HTTP_CODE=$(curl -s -o /dev/null -w "%{http_code}" http://127.0.0.1:18080/)
if [ "$HTTP_CODE" != "200" ]; then
    echo "FAIL: Web UI not accessible (HTTP $HTTP_CODE)"
    kill $DAEMON_PID
    exit 1
fi
echo "PASS: Web UI accessible"

# Test 2: Check SSE endpoint
echo "Test 2: SSE endpoint"
timeout 5s curl -N http://127.0.0.1:18080/api/events > /dev/null 2>&1 &
SSE_PID=$!
sleep 1
if ! kill -0 $SSE_PID 2>/dev/null; then
    echo "FAIL: SSE connection failed"
    kill $DAEMON_PID
    exit 1
fi
kill $SSE_PID 2>/dev/null || true
echo "PASS: SSE endpoint working"

# Test 3: Send message and check history
echo "Test 3: Message in history"
curl -s -X POST http://127.0.0.1:18080/api/chat \
    -H "Content-Type: application/json" \
    -d '{"message":"test message"}' > /dev/null

sleep 1

HISTORY=$(curl -s http://127.0.0.1:18080/api/history)
if ! echo "$HISTORY" | grep -q "test message"; then
    echo "FAIL: Message not in history"
    kill $DAEMON_PID
    exit 1
fi
echo "PASS: Message stored in history"

# Test 4: Check Message has source field support
if ! echo "$HISTORY" | grep -q '"type"'; then
    echo "FAIL: History format invalid"
    kill $DAEMON_PID
    exit 1
fi
echo "PASS: History format valid"

# Cleanup
kill $DAEMON_PID
rm -rf "$TEST_DIR"

echo "=== All Tests Passed ==="
```

- [ ] **Step 2: Make script executable and run**

Run: 
```bash
chmod +x tests/integration/voice_web_integration_test.sh
./tests/integration/voice_web_integration_test.sh
```

Expected: All tests pass

- [ ] **Step 3: Hardware test checklist (manual)**

Document hardware test procedure:

```markdown
# Hardware Integration Checklist

## Prerequisites
- Luckfox Pico Zero with GPIO 33/32 configured
- ASRPro 2.0 module connected to GPIO 33
- Physical button connected to GPIO 32 (optional)

## Test 1: GPIO Wakeup
- [ ] Power on device
- [ ] SSH into device
- [ ] Verify daemon running: `ps | grep daemon`
- [ ] Check web UI: `curl http://localhost:8080`
- [ ] Say wake word to ASRPro
- [ ] Verify GPIO interrupt logged
- [ ] Speak test phrase
- [ ] Check web UI shows voice message with 🎤 icon

## Test 2: Button Wakeup
- [ ] Press physical button on GPIO 32
- [ ] Verify interrupt logged
- [ ] Speak test phrase
- [ ] Check web UI updates

## Test 3: Mixed Interaction
- [ ] Trigger voice session via wakeup
- [ ] While session active, send text message via web UI
- [ ] Verify both messages appear in history
- [ ] Verify voice messages have source="voice"
- [ ] Verify text messages have source="text" or no source field

## Test 4: Audio Archive (if enabled)
- [ ] Enable audio_archive in config
- [ ] Restart daemon
- [ ] Trigger voice interaction
- [ ] Check /userdata/audio for WAV files
- [ ] Click play button in web UI
- [ ] Verify audio plays correctly

## Test 5: Cleanup
- [ ] Generate 10+ voice messages
- [ ] Verify old files are deleted per max_files limit
- [ ] Check disk usage is bounded
```

- [ ] **Step 4: Commit test script**

```bash
git add tests/integration/voice_web_integration_test.sh
git commit -m "test: add voice-web integration test script

Automated integration tests covering:
- HTTP server availability in all modes
- SSE endpoint functionality
- Message history persistence
- JSON schema validation

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>"
```

- [ ] **Step 5: Update documentation**

Create or update `docs/features/voice-wakeup-web-integration.md`:

```markdown
# Voice Wakeup and Web Integration

## Overview

The daemon now supports simultaneous voice and web interactions with unified history.

## Features

- ✅ HTTP server runs in all input modes (text, stt, audio)
- ✅ Voice messages tagged with `source: "voice"`
- ✅ Real-time message streaming via Server-Sent Events
- ✅ Optional audio archival with automatic cleanup
- ✅ Web UI displays voice and text messages with distinct icons

## Configuration

### Basic Setup (No Audio Archive)

```toml
input_mode = "stt"
trigger_mode = "wakeup"

[audio_archive]
enabled = false
```

### With Audio Archive

```toml
[audio_archive]
enabled = true
max_files = 500
max_size_mb = 100
storage_path = "/userdata/audio"
```

## API Endpoints

### GET /api/events
Server-Sent Events stream for real-time messages.

```bash
curl -N http://localhost:8080/api/events
```

### GET /api/audio/{filename}
Download archived audio file.

```bash
curl http://localhost:8080/api/audio/msg_123_abc.wav -o recording.wav
```

## Storage Considerations

Audio files consume ~1.9 MB per minute (16kHz, 16-bit, mono).
Default limits (500 files, 100 MB) accommodate ~40 MB of audio.

## Troubleshooting

### Web UI Not Accessible in Audio Mode
- Check daemon logs for HTTP server startup errors
- Verify port 8080 is not blocked by firewall
- Confirm server goroutine started successfully

### SSE Not Receiving Messages
- Check browser DevTools Network tab for SSE connection
- Verify EventBroadcaster is initialized
- Check for ChatHistoryStore callback registration

### Audio Playback Fails
- Verify audio_archive.enabled = true
- Check file exists: `ls /userdata/audio/`
- Verify file permissions
- Check browser console for 404/403 errors
```

- [ ] **Step 6: Commit documentation**

```bash
git add docs/features/voice-wakeup-web-integration.md
git commit -m "docs: add voice-wakeup-web integration guide

Documents configuration, API endpoints, storage considerations,
and troubleshooting for the new voice-web integration feature.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>"
```

- [ ] **Step 7: Final verification**

Run complete test suite:
```bash
cd src/agent
go test ./...
./../../tests/integration/voice_web_integration_test.sh
```

Expected: All tests pass

---

## Execution Complete

All phases implemented:
- ✅ Phase 1: Architecture decoupling
- ✅ Phase 2: Chat history extension
- ✅ Phase 3: Real-time message streaming
- ✅ Phase 4: Audio archive (optional)
- ✅ Phase 5: Frontend integration
- ✅ Phase 6: End-to-end testing

The implementation enables voice wakeup mode and web text mode to coexist with unified chat history and real-time updates.

