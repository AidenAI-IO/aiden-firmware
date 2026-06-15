# Voice Wakeup & Web Integration - Integration Test Plan

## Test Environment Setup

```bash
# Build the daemon
cd src/agent
go build -o daemon ./cmd/daemon

# Create test config
mkdir -p /tmp/aiden-test/audio
cat > /tmp/aiden-test/agent.toml <<EOF
input_mode = "audio"
trigger_mode = "manual"

[audio]
sample_rate = 16000

[audio_archive]
enabled = true
max_files = 10
max_size_mb = 50
storage_path = "/tmp/aiden-test/audio"

[model]
provider = "openai"
model = "gpt-4"
EOF

# Start daemon
./daemon -config /tmp/aiden-test
```

## Test Scenarios

### ✅ Test 1: HTTP Server Runs in Audio Mode

**Goal:** Verify HTTP server is accessible when daemon runs in audio mode.

**Steps:**

1. Start daemon with `input_mode = "audio"`
2. Open browser to http://localhost:8080
3. Verify web UI loads successfully

**Expected:**

- ✅ Web UI loads (not 404)
- ✅ Console shows: "🚀 Aiden Agent daemon starting on 127.0.0.1:8080"
- ✅ Console shows: "🌐 Web UI: http://localhost:8080"

### ✅ Test 2: Voice Message Persistence

**Goal:** Verify voice messages are saved to chat_history.jsonl with source="voice".

**Steps:**

1. Trigger voice interaction (press Enter, speak, press Enter)
2. Wait for response
3. Check chat history file:
   ```bash
   cat /tmp/aiden-test/memory/chat_history/history.jsonl | jq .
   ```

**Expected:**

- ✅ User message with `"source": "voice"` exists
- ✅ Assistant message with `"source": "voice"` exists
- ✅ User message has `"content"` (transcript)

### ✅ Test 3: Audio Archive When Enabled

**Goal:** Verify audio files are saved when audio_archive.enabled=true.

**Steps:**

1. Enable audio archive in config
2. Trigger voice interaction
3. Check audio directory:
   ```bash
   ls -lh /tmp/aiden-test/audio/
   ```

**Expected:**

- ✅ WAV file exists: `msg_<timestamp>_<uuid>.wav`
- ✅ File size > 0 bytes
- ✅ User message in history has `"audio_file": "/tmp/aiden-test/audio/msg_*.wav"`
- ✅ User message has `"audio_duration_ms": <positive number>`

### ✅ Test 4: Audio Archive Cleanup

**Goal:** Verify old audio files are deleted when max_files exceeded.

**Steps:**

1. Set `max_files = 3` in config
2. Trigger 5 voice interactions
3. Check audio directory:
   ```bash
   ls /tmp/aiden-test/audio/ | wc -l
   ```

**Expected:**

- ✅ Only 3 most recent WAV files exist
- ✅ Oldest 2 files were deleted

### ✅ Test 5: SSE Real-Time Push

**Goal:** Verify SSE endpoint pushes messages in real-time.

**Steps:**

1. Start daemon
2. Open browser console on http://localhost:8080
3. Check network tab for `/api/events` connection
4. Trigger voice interaction
5. Watch console logs

**Expected:**

- ✅ SSE connection established: `[SSE] Connected`
- ✅ New messages appear in UI without page refresh
- ✅ Network tab shows `text/event-stream` connection

### ✅ Test 6: Voice Message UI Display

**Goal:** Verify voice messages display with 🎤 icon in web UI.

**Steps:**

1. Open http://localhost:8080 in browser
2. Trigger voice interaction
3. Wait for message to appear

**Expected:**

- ✅ User message shows "You 🎤" in header
- ✅ Assistant message shows "Assistant 🎤" in header
- ✅ Message content displays correctly

### ✅ Test 7: Audio Playback Button

**Goal:** Verify audio playback button works when archive enabled.

**Steps:**

1. Ensure `audio_archive.enabled = true`
2. Trigger voice interaction
3. Check user message in UI
4. Click "▶️ Play Audio" button

**Expected:**

- ✅ "▶️ Play Audio" button appears on user message
- ✅ Button shows duration tooltip
- ✅ Clicking button plays audio
- ✅ Button text changes to "⏸️ Playing..." during playback
- ✅ Audio file is audible

### ✅ Test 8: Audio Playback Endpoint Security

**Goal:** Verify path traversal protection on /api/audio/{filename}.

**Steps:**

```bash
# Attempt path traversal
curl -i http://localhost:8080/api/audio/../../../etc/passwd

# Attempt absolute path
curl -i http://localhost:8080/api/audio//etc/passwd

# Valid file
curl -i http://localhost:8080/api/audio/msg_1234_abcd.wav
```

**Expected:**

- ✅ Path traversal returns `403 Forbidden`
- ✅ Absolute path returns `403 Forbidden`
- ✅ Valid file returns `200 OK` with `Content-Type: audio/wav`
- ✅ Nonexistent file returns `404 Not Found`

### ✅ Test 9: Mixed Text and Voice History

**Goal:** Verify text and voice messages coexist in history.

**Steps:**

1. Send text message via web UI
2. Trigger voice interaction
3. Send another text message
4. Check `/api/history`:
   ```bash
   curl http://localhost:8080/api/history | jq '.[] | {type, source, content}'
   ```

**Expected:**

- ✅ Text messages have `"source": null` or missing
- ✅ Voice messages have `"source": "voice"`
- ✅ All messages display in UI with correct icons

### ✅ Test 10: Audio Archive Disabled Mode

**Goal:** Verify transcript-only mode when archive disabled.

**Steps:**

1. Set `audio_archive.enabled = false`
2. Restart daemon
3. Trigger voice interaction
4. Check history:
   ```bash
   cat /tmp/aiden-test/memory/chat_history/history.jsonl | tail -2 | jq .
   ```

**Expected:**

- ✅ User message has `"source": "voice"`
- ✅ User message has `"content"` (transcript)
- ✅ User message has `"audio_file": ""` (empty)
- ✅ User message has `"audio_duration_ms": 0` or positive (calculated but not saved)
- ✅ No WAV files created in audio directory
- ✅ No "▶️ Play Audio" button in UI

## Performance Tests

### Test 11: SSE Connection Stability

**Goal:** Verify SSE connection survives network hiccups.

**Steps:**

1. Open web UI
2. Put laptop to sleep for 1 minute
3. Wake up laptop
4. Trigger voice interaction

**Expected:**

- ✅ SSE reconnects automatically
- ✅ New messages appear after reconnect

### Test 12: Concurrent Users

**Goal:** Verify multiple browsers can connect simultaneously.

**Steps:**

1. Open 3 browser windows to http://localhost:8080
2. Trigger voice interaction
3. Watch all 3 windows

**Expected:**

- ✅ All 3 windows receive SSE updates
- ✅ All 3 windows display new messages

## Regression Tests

### Test 13: Text-Only Mode Still Works

**Goal:** Verify text mode wasn't broken by voice changes.

**Steps:**

1. Set `input_mode = "text"`
2. Restart daemon
3. Type message and send via web UI

**Expected:**

- ✅ Message sends successfully
- ✅ Response appears
- ✅ No voice icon on text messages

## Build & Test Summary

```bash
# Run all unit tests
cd src/agent
go test ./...

# Build daemon
go build ./cmd/daemon

# Run integration tests (manual)
# Follow test scenarios above
```

## Sign-Off Checklist

- [ ] All 13 test scenarios executed
- [ ] No console errors in browser
- [ ] No server errors in daemon logs
- [ ] Audio files saved/cleaned correctly
- [ ] SSE connection stable
- [ ] Security tests passed (path traversal blocked)
- [ ] Mixed text/voice history works
- [ ] UI displays voice indicators correctly
- [ ] Audio playback works
- [ ] Performance acceptable (SSE <100ms latency)

## Known Limitations

1. **Audio archive cleanup runs after every save** — may cause slight delay on fast consecutive utterances
2. **SSE connection drops if server restarts** — browser must refresh page
3. **No audio file expiration UI** — broken links show error on playback attempt
4. **UUID collision possible** — 8-char truncation gives ~0.003% collision risk at 500 files

## Troubleshooting

**Issue:** Web UI not loading in audio mode

- Check: `grep "🌐 Web UI" daemon.log`
- Fix: Verify HTTP server starts in background

**Issue:** Voice messages missing in history

- Check: `ls /tmp/aiden-test/memory/chat_history/`
- Fix: Ensure ChatHistoryStore callback wired

**Issue:** SSE not connecting

- Check browser console for errors
- Fix: Verify `/api/events` route registered

**Issue:** Audio playback fails

- Check: `ls /tmp/aiden-test/audio/*.wav`
- Fix: Enable audio_archive in config

**Issue:** Path traversal attack succeeds

- Check: `grep EvalSymlinks internal/agent/server.go`
- Fix: Ensure using filepath.EvalSymlinks, not filepath.Abs
