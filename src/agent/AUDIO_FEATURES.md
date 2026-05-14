# Audio Features for Go Agent

This document describes the audio features added to the Go agent, ported from the C++ `agent_main.cpp` implementation.

## Overview

The Go agent now supports voice-based interaction with the following capabilities:

- **Speech-to-Text (STT)**: OpenAI Whisper and Tencent ASR
- **Text-to-Speech (TTS)**: Minimax TTS with streaming playback
- **Voice Activity Detection (VAD)**: Energy-based VAD for utterance detection
- **Audio Service Client**: Unix socket client for audio recording/playback

## Architecture

Audio features are integrated directly into the daemon. The daemon automatically switches between HTTP server mode and audio dialog mode based on the `input_mode` configuration.

```
┌─────────────┐
│   daemon    │
└──────┬──────┘
       │
       ├─ input_mode=text ──────► HTTP Server (Web UI)
       │
       └─ input_mode=audio/stt ─► Audio Dialog Loop
                                   │
                                   ├─ VAD
                                   ├─ STT (if stt mode)
                                   ├─ LLM
                                   └─ TTS
```

## Components

### 1. Audio Service Client (`audio_client.go`)

Provides a Go client for the `audio_service` Unix socket server:

- `StartRecording()` - Start audio recording session
- `ReadRecordChunk()` - Read PCM chunks (long-poll)
- `StopRecording()` - Stop recording session
- `StartPlayback()` - Start audio playback session
- `WritePlayChunk()` - Write PCM chunks for playback
- `StopPlayback()` - Stop playback session
- `Health()` - Check service health

### 2. STT Providers (`stt.go`)

Speech-to-text interface and implementations:

**OpenAI Whisper STT**
- Uses OpenAI Whisper API
- Supports custom base URL for compatible endpoints
- Configurable model (default: `whisper-1`)

**Tencent ASR STT**
- Placeholder for Tencent Cloud ASR
- Requires Tencent Cloud SDK integration (TODO)

### 3. TTS Providers (`tts.go`)

Text-to-speech interface and implementations:

**Minimax TTS**
- Streams MP3 audio from Minimax API
- Uses ffmpeg to convert MP3 → PCM s16le 16kHz mono
- Streams PCM to audio_service for playback
- Configurable voice, emotion, and speed

### 4. Voice Activity Detection (`vad.go`)

Energy-based VAD for detecting speech utterances:

- Configurable energy threshold
- Silence detection with configurable timeout
- Minimum speech duration filtering
- Manual mode (always buffer) for push-to-talk

### 5. Audio Dialog Manager (`audio_dialog.go`)

Orchestrates the audio conversation loop:

- Manages recording sessions
- Processes VAD frames
- Transcribes audio via STT (if in stt mode)
- Sends text/audio to LLM
- Speaks responses via TTS

## Configuration

Add these sections to your `agent.toml`:

```toml
# Input mode: "text" (default), "audio", or "stt"
input_mode = "stt"

# Trigger mode: "manual" (push-to-talk)
trigger_mode = "manual"

# VAD parameters
energy_threshold = 500
silence_ms = 1000
min_speech_ms = 300

[audio]
socket = "/run/audio_service/audio_service.sock"
sample_rate = 16000
channels = 1
bit_width = 16

[stt]
provider = "openai-whisper"
api_key = "sk-..."
model = "whisper-1"
# base_url = "https://api.openai.com/v1"  # optional

# For Tencent ASR:
# provider = "tencent"
# secret_id = "..."
# secret_key = "..."
# region = "ap-guangzhou"
# engine_model_type = "16k_zh"

[tts]
provider = "minimax"
api_key = "..."
voice_id = "male-qn-qingse"
emotion = "happy"
speed = 1.0
```

## Input Modes

### 1. `text` (Default)

HTTP server mode with Web UI. No audio processing.

```toml
input_mode = "text"
```

Run:
```bash
./bin/daemon -config /path/to/config -addr :8080
```

Access Web UI at http://localhost:8080

### 2. `stt` (Speech-to-Text)

Audio is recorded, transcribed to text via STT, then sent to LLM. Response is spoken via TTS.

```toml
input_mode = "stt"

[stt]
provider = "openai-whisper"
api_key = "sk-..."

[tts]
provider = "minimax"
api_key = "..."
```

Run:
```bash
./bin/daemon -config /path/to/config
```

### 3. `audio` (Direct Audio)

Audio is sent directly to LLM (for models that support audio input). Response is spoken via TTS.

```toml
input_mode = "audio"

[tts]
provider = "minimax"
api_key = "..."
```

**Note**: Direct audio mode is not yet implemented. Use `stt` mode instead.

## Trigger Modes

### `manual` (Push-to-Talk)

Press Enter to start recording, press Enter again to stop.

```toml
trigger_mode = "manual"
```

Flow:
1. Press Enter to start recording
2. Speak your command
3. Press Enter to stop (or wait for VAD to detect silence)
4. Audio is processed (STT if in stt mode)
5. Text is sent to LLM
6. Response is spoken via TTS
7. Repeat

### `wakeup` (GPIO Trigger)

**Not yet implemented**. Will support GPIO wakeup trigger (e.g., GPIO 33).

## Usage

### Build

```bash
cd /Volumes/dev/aiden-hardware-demo/src/agent
go build -o bin/daemon ./cmd/daemon
```

### Run in Text Mode (HTTP Server)

```bash
./bin/daemon -config /path/to/config -addr :8080
```

Access Web UI at http://localhost:8080

### Run in Audio Mode (STT)

```bash
./bin/daemon -config /path/to/config
```

Press Enter to start/stop recording.

## Implementation Notes

### Differences from C++ Implementation

1. **Integrated into daemon**: Audio features are part of the daemon, not a separate binary.

2. **Mode-based switching**: The daemon automatically switches between HTTP server and audio dialog based on `input_mode`.

3. **Simplified configuration**: Uses `input_mode` (text/audio/stt) instead of `asr_mode` (direct_audio/stt_then_text).

4. **Goroutines instead of pthreads**: Audio processing uses goroutines instead of POSIX threads.

5. **No wakeup mode yet**: GPIO wakeup mode can be added later.

### Dependencies

- `ffmpeg` - Required for TTS MP3 → PCM conversion
- `audio_service` - Must be running on the configured socket

### Future Enhancements

1. Implement Tencent ASR with proper signature v3 authentication
2. Add direct audio mode (send WAV to LLM)
3. Add GPIO wakeup mode support
4. Add audio chunk auto-splitting for long recordings
5. Add support for other TTS/STT providers
6. Add WebSocket support for audio streaming in Web UI

## Testing

To test the implementation:

1. Start `audio_service`:
   ```bash
   /path/to/audio_service
   ```

2. Configure `agent.toml`:
   ```toml
   input_mode = "stt"
   trigger_mode = "manual"
   
   [stt]
   provider = "openai-whisper"
   api_key = "sk-..."
   
   [tts]
   provider = "minimax"
   api_key = "..."
   ```

3. Run the daemon:
   ```bash
   ./bin/daemon -config /path/to/config
   ```

4. Press Enter, speak, press Enter again
5. Verify STT transcription and TTS playback

## Files Added

- `internal/agent/audio_client.go` - Audio service client
- `internal/agent/stt.go` - STT interface and providers
- `internal/agent/tts.go` - TTS interface and providers
- `internal/agent/vad.go` - Voice activity detection
- `internal/agent/audio_dialog.go` - Audio dialog manager

## Files Modified

- `internal/agent/config.go` - Added `input_mode` and `trigger_mode` fields
- `cmd/daemon/main.go` - Added audio mode support

## Configuration Reference

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `input_mode` | string | `"text"` | Input mode: `text`, `audio`, or `stt` |
| `trigger_mode` | string | `"manual"` | Trigger mode: `manual` or `wakeup` |
| `energy_threshold` | int | `500` | VAD energy threshold |
| `silence_ms` | int | `1000` | VAD silence timeout (ms) |
| `min_speech_ms` | int | `300` | VAD minimum speech duration (ms) |
| `audio.socket` | string | `/run/audio_service/audio_service.sock` | Audio service socket path |
| `audio.sample_rate` | int | `16000` | Audio sample rate (Hz) |
| `audio.channels` | int | `1` | Audio channels (1=mono) |
| `audio.bit_width` | int | `16` | Audio bit width |
| `stt.provider` | string | - | STT provider: `openai-whisper` or `tencent` |
| `stt.api_key` | string | - | STT API key (for OpenAI) |
| `stt.model` | string | `whisper-1` | STT model |
| `tts.provider` | string | - | TTS provider: `minimax` |
| `tts.api_key` | string | - | TTS API key |
| `tts.voice_id` | string | `male-qn-qingse` | TTS voice ID |
| `tts.emotion` | string | `happy` | TTS emotion |
| `tts.speed` | float | `1.0` | TTS speed |

