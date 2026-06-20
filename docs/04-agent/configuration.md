# Agent Configuration Reference

The Agent expects `-config` to point to a directory, not a single config file. This page covers both the on-device **Config Web configuration page** (the most common way to edit these fields) and a complete reference for each `agent.toml` field.

## Directory layout

```text
/userdata/agent/
├── agent.toml       # required
├── skills/          # optional, auto-discovers **/SKILL.md
├── log/             # runtime log directory
└── memory/          # conversation memory persistence directory
```

TOML is the currently supported config format; JSON config is deprecated.

## Config Web: the device config page

`config_web` is a lightweight C++ web service for maintaining the device Agent configuration, system environment variables, and Wi-Fi configuration. It is the primary way to edit the fields documented on this page without manually editing `agent.toml`.

On a device, open the config page in a browser at the USB-network gateway address:

```text
http://192.168.42.1
```

The firmware starts `config_web` on port 80.

### What the page can configure

The page fields cover the following config sections (all detailed later on this page):

- `agent`: `input_mode`, `trigger_mode`, VAD params, `max_iterations`, `custom_instruction`, `additional_prompt`
- `model`: provider, token_env, model, api_key, base_url, temperature, max_response_tokens, context_window, model_max_output_tokens. `context_window = 0` means auto-discover from OpenRouter/Ollama metadata when available.
- `stt`: provider, api_key, model, base_url, Tencent ASR fields
- `tts`: provider, api_key, model, voice_id, emotion, speed
- `audio`: socket, sample_rate, channels, bit_width
- `hid`: keyboard_device, mouse_device, frame_socket
- `env`: shell-style environment text written to `/userdata/system/env`, including optional proxy variables such as `http_proxy`, `HTTPS_PROXY`, and `NO_PROXY`
- Wi-Fi: SSID / PSK etc. (written to `/userdata/wpa_supplicant.conf`)

## Minimal Web UI config

```toml
custom_instruction = ""
max_iterations = -1
screenshot_keep_n = 3
screenshot_prune_interval = 2
input_mode = "text"

[model]
provider = "openrouter"
model = "bytedance-seed/seed-2.0-lite"
token_env = "OPENROUTER_API_KEY"
temperature = 0.2
max_response_tokens = 1000
# Optional model metadata overrides. Leave unset or 0 for provider metadata auto-discovery when available.
# context_window = 128000
# model_max_output_tokens = 8192

[audio]
socket = "/run/audio_service/audio_service.sock"
sample_rate = 16000
channels = 1
bit_width = 16

[hid]
keyboard_device = "/dev/hidg0"
mouse_device = "/dev/hidg1"
frame_socket = "/run/frame_service/frame_service.sock"
```

> `token_env` means the key is read from an environment variable. Overlay example configs may also write the `api_key` field directly; for production, prefer environment variables or a device-side secure injection method.

## Minimal STT voice-mode config

```toml
custom_instruction = ""
input_mode = "stt"
trigger_mode = "manual"
vad_backend = "rknn"
vad_model_path = "/oem/usr/model/silero_vad_6_2_encoder_rv1106_w8a8_v1.rknn"
vad_helper_path = "/oem/usr/bin/rknn_vad"
vad_speech_threshold = 0.5
silence_ms = 650
min_speech_ms = 300
voice_followup_enabled = false
voice_followup_timeout_ms = 6000
voice_first_turn_timeout_ms = 10000
voice_max_turns = 0
voice_interrupt_on_wakeup = true
voice_streaming_tts_enabled = true
voice_tool_call_speech = true
voice_progress_speech_enabled = true
voice_max_response_tokens = 400

[model]
provider = "openrouter"
model = "bytedance-seed/seed-2.0-lite"
token_env = "OPENROUTER_API_KEY"

[stt]
provider = "openrouter"
api_key = "OPENROUTER_API_KEY"
model = "qwen/qwen3-asr-flash-2026-02-10"

[tts]
provider = "minimax-ws"
model = "speech-2.8-hd"
voice_id = "male-qn-qingse"
emotion = "happy"
speed = 1.0

[audio]
socket = "/run/audio_service/audio_service.sock"
sample_rate = 16000
channels = 1
bit_width = 16

[hid]
keyboard_device = "/dev/hidg0"
mouse_device = "/dev/hidg1"
frame_socket = "/run/frame_service/frame_service.sock"
```

## Top-level fields

| Field | Default / allowed values | Description |
| --- | --- | --- |
| `custom_instruction` | - | Optional deployment/persona override for the built-in runtime instruction. Leave empty to use the agent binary default; set only for internal testing or deployment-specific behavior. |
| `additional_prompt` | - | Additional prompt field; appended after the base instruction at runtime |
| `max_iterations` | `-1` | Maximum number of tool-call loops per run; `-1` means unlimited |
| `screenshot_keep_n` | `3` | Number of most recent screenshots to keep when pruning screenshots from the LLM context; unset or `0` uses the default |
| `screenshot_prune_interval` | `2` | Once screenshots exceed `screenshot_keep_n + screenshot_prune_interval`, replace old screenshots with placeholders in batches; unset or `0` uses the default |
| `input_mode` | `text` / `stt` / `audio` | Input mode |
| `trigger_mode` | `manual` / `wakeup` | Voice-mode trigger method |
| `vad_backend` | `rknn` | VAD backend: `rknn` uses NPU encoder + CPU LSTM/decoder, `cpu` uses a pure-CPU helper |
| `vad_model_path` | `/oem/usr/model/silero_vad_6_2_encoder_rv1106_w8a8_v1.rknn` | Silero VAD RKNN encoder model path; not used when `vad_backend="cpu"` |
| `vad_helper_path` | `/oem/usr/bin/rknn_vad` | VAD helper executable path; the CPU backend defaults to `/oem/usr/bin/cpu_vad` |
| `vad_speech_threshold` | `0.5` | Silero VAD speech probability threshold |
| `silence_ms` | `650` | How many milliseconds of silence before an utterance is considered finished |
| `min_speech_ms` | `300` | Minimum valid speech duration |
| `voice_followup_enabled` | `false` | Enable continuous follow-up after a single wakeup in wakeup mode; defaults to one wakeup per turn |
| `voice_followup_timeout_ms` | `6000` | Window to wait for a user follow-up after the Agent replies |
| `voice_first_turn_timeout_ms` | `10000` | Window to wait for the first utterance after wakeup |
| `voice_max_turns` | `0` | Maximum turns per wakeup session; `0` means unlimited |
| `voice_interrupt_on_wakeup` | `true` | When a wakeup is received again within a session, cancel thinking/TTS and listen again; repeated wakeups during the listening or recording phase are merged or ignored |
| `voice_streaming_tts_enabled` | `true` | Feed the LLM streaming output into TTS sentence by sentence, reducing the wait before the first sentence plays |
| `voice_tool_call_speech` | `true` | Whether to asynchronously read the `content` of a tool-call event; this content comes only from the assistant content in the same LLM tool-call response, and stays silent when absent |
| `voice_progress_speech_enabled` | `true` | Whether to announce a short progress message when a todo item enters `in_progress`; todo state is still sent to the UI/trace |
| `voice_max_response_tokens` | `400` | Per-turn output token limit for voice replies (must be `>= 0`) |
| `todo_reminder_tool_calls` | `3` | In single-agent/default mode, after how many consecutive tool calls to remind the model to update the todo; set to `0` to use the default |

The model pointed to by `vad_model_path` must first be converted from the Silero ONNX to RV1106 RKNN on a PC using `silero-vad/convert_silero_vad_to_rknn.py`, then placed at the corresponding path on the device. The CPU backend requires `silero_vad_6_2_lstm_decoder_weights.bin` to include the Conv1d encoder extension, which can be generated from the TorchScript file shipped with the repo using `silero-vad/export_silero_vad_v6_2_weights.py`.
When `vad_helper_path` is still the built-in default, switching `vad_backend` automatically switches the helper; only when set to a custom path does it run that custom path.

## `[model]`

| Field | Description |
| --- | --- |
| `provider` | `openai`, `openrouter`, `ollama`, `fake` |
| `model` | Model name; usually required except for `fake` |
| `base_url` | Custom OpenAI-compatible endpoint |
| `api_key` | API key written directly |
| `token_env` | Read the API key from the specified environment variable; only supported by `[model]` |
| `temperature` | Sampling temperature |
| `max_response_tokens` | Maximum output tokens passed to the model on request |
| `context_window` | Optional total context window override in tokens. Unset or `0` uses provider metadata for OpenRouter/Ollama when available, then the built-in registry, then memory fallback. |
| `model_max_output_tokens` | Optional advertised max output override in tokens. Unset or `0` uses provider metadata when fetched, then the built-in registry. |

## `memory/extraction.yaml`

Optional. Place `memory/extraction.yaml` under the config directory to control session-memory compaction and chunk extraction. Missing files and invalid fields fall back to defaults. See [session-memory.md](./session-memory.md) for the full flow.

| Field | Default | Description |
| --- | --- | --- |
| `reserve_tokens` | `8192` | Token headroom reserved below the active model context window. Compaction triggers when `prompt_tokens >= context_window - reserve_tokens`. The value is clamped to at most half of the window so small-window models remain usable. |
| `keep_recent_tokens` | `20000` | Approximate token budget for the hot window retained by token-based cut-point selection. It is clamped together with `reserve_tokens` to fit the active window. |
| `hot_window_events` | `30` | Target number of recent events retained by the count fallback. Used only when prompt-token data is unavailable. |
| `count_compress_after_events` | `hot_window_events * 2` | Event-count trigger used only when prompt-token data is unavailable. If omitted, it is derived from the normalized `hot_window_events`; explicit values must be greater than `hot_window_events`. |
| `context_window` | `32000` | Fallback context window for compaction when the active model is not present in `model_specs`. Runtime normally derives this from `ModelResolver.Spec()`; this value is only used for unknown models. |
| `compress_at_percent` | `50` | Percentage trigger: compaction starts when `prompt_tokens / context_window >= compress_at_percent%`. |
| `summary_max_chunks` | `10` | Number of chunk summaries kept in the Recent Chunks section of `summary.md`. Older entries move to the archive and are folded into the Rolling Summary. |
| `session_boundary_enabled` | `true` | Classify each new user turn as continuing the current session or starting a new one. A `new` boundary archives the current `memory/session/` directory and recreates an empty active session. |
| `session_boundary_short_gap_seconds` | `300` | Gap below which a turn is treated as continuation regardless of lexical signals. |
| `session_boundary_long_gap_seconds` | `1800` | Gap above which a turn is treated as a fresh session regardless of lexical signals. |
| `tag_candidates` | see defaults | Candidate keywords matched when tagging chunk summaries. |
| `entity_suffixes` | `["App","app","APP"]` | Suffixes recognized during entity extraction. |

## System Environment Variables

The Agent no longer reads `[proxy]` from `agent.toml`. Outbound HTTP/WebSocket requests, shell tool subprocesses, OTA commands launched through `aiden-env-run`, and SSH login shells all use environment variables from `/userdata/system/env`. The file is loaded with shell syntax, for example:

```sh
HTTP_PROXY=http://127.0.0.1:7890
HTTPS_PROXY=http://127.0.0.1:7890
NO_PROXY=localhost,127.0.0.1,::1,10.0.0.0/8,172.16.0.0/12,192.168.0.0/16
OPENROUTER_API_KEY=...
```

| Variable | Description |
| --- | --- |
| `HTTP_PROXY` / `http_proxy` | HTTP proxy URL, for example `http://127.0.0.1:7890` |
| `HTTPS_PROXY` / `https_proxy` | HTTPS proxy URL, usually the same HTTP proxy endpoint |
| `ALL_PROXY` / `all_proxy` | Generic proxy used by HTTP clients and some WebSocket adapters |
| `NO_PROXY` / `no_proxy` | Comma-separated bypass rules; when a proxy URL is set and no bypass value is present, the launcher injects the default private-network bypass list |

## `[audio]`

| Field | Default | Description |
| --- | --- | --- |
| `socket` | `/run/audio_service/audio_service.sock` | Audio Service socket |
| `sample_rate` | `16000` | Sample rate |
| `channels` | `1` | Number of channels |
| `bit_width` | `16` | Bit width |

## `[hid]`

| Field | Default | Description |
| --- | --- | --- |
| `keyboard_device` | `/dev/hidg0` | Keyboard HID device |
| `mouse_device` | `/dev/hidg1` | Mouse/touch HID device |
| `frame_socket` | `/run/frame_service/frame_service.sock` | Frame Service socket used by the screenshot tool |

## `[live_activity]`

For the iOS companion app's Live Activity / Dynamic Island task status. The agent-side status snapshot is enabled by default; the APNs-related fields only apply to remote updates when the app is backgrounded, on the lock screen, or not open. Foreground local updates do not need and will not use APNs. See the full flow in [Live Activity / Dynamic Island](./live-activity.md).

| Field | Default | Description |
| --- | --- | --- |
| `enabled` | `true` | Whether to enable the agent-side status snapshot and API |
| `bundle_id` | - | iOS app bundle id; required only when configuring background APNs and `topic` is not explicitly set |
| `topic` | `<bundle_id>.push-type.liveactivity` | APNs topic; usually does not need to be set manually |
| `environment` | `sandbox` | `sandbox` or `production` |
| `team_id` | - | Apple Developer Team ID; used only by background APNs |
| `key_id` | - | APNs Auth Key ID; used only by background APNs |
| `private_key_path` | - | APNs `.p8` private key path; used only by background APNs |
| `private_key_pem` | - | Inline APNs `.p8` PEM directly; for development/debugging only, do not place in open-source config or on user boards in production |
| `timeout_sec` | `10` | Background APNs request timeout |

## `[stt]` and `[tts]`

`[stt]` is required when `input_mode = "stt"`; `[tts]` is required when `input_mode = "stt"` or `"audio"`.

STT:

- `provider = "openai-whisper"`: currently available;
- `provider = "openrouter"`: currently available, default endpoint is `https://openrouter.ai/api/v1/audio/transcriptions`, request body uses base64 WAV;
- `provider = "tencent-asr"`: Tencent Cloud Sentence Recognition (SentenceRecognition), uses `secret_id` / `secret_key`, no `base_url` needed; the legacy values `tencent` / `tencent_asr` are retained only as compatibility aliases.

TTS:

- `provider = "minimax-ws"`: Minimax WebSocket;
- `provider = "fish-audio"`: Fish Audio WebSocket;
- `provider = "alicloud"`: Alibaba Cloud Qwen-TTS Realtime;
- `provider = "volcengine"`: Volcengine WebSocket bidirectional streaming V3. Currently only the new console's `X-Api-Key` authentication is supported: `api_key` maps to `X-Api-Key`, `model` maps to `X-Api-Resource-Id` (default `seed-tts-2.0`), and `voice_id` maps to the speaker.

`[tts]` common fields:

| Field | Description |
| --- | --- |
| `provider` | Required. One of `minimax-ws`, `fish-audio`, `alicloud`, `volcengine` |
| `api_key` | Required. The authentication key for each provider; the examples below omit this field to avoid writing keys into the docs |
| `model` | Optional. Minimax model name, Fish Audio model header, Alibaba Cloud Realtime model name, Volcengine `X-Api-Resource-Id` |
| `voice_id` | Optional. Minimax voice id, Alibaba Cloud voice, Volcengine speaker; Fish Audio can use it as a reference id |
| `reference_id` | Optional. Fish Audio reference id; takes priority over `voice_id` when set |
| `emotion` | Optional. Minimax emotion; Volcengine passes it through as `audio_params.emotion`, requires voice support |
| `speed` | Optional. Speech rate, default `1.0`; the supported range varies by provider, refer to the official docs |

The config examples below only show non-key fields relevant to adapter behavior; at actual runtime you still need to provide the corresponding `api_key` in the device config via `[tts]` or `[tts.credentials.<provider>]`.

Common TTS adapter configs:

| Provider | `model` example | Voice/reference field | Description |
| --- | --- | --- | --- |
| `minimax-ws` | `speech-2.8-hd` | `voice_id = "male-qn-qingse"` | Minimax WebSocket; `emotion` is passed through to Minimax |
| `fish-audio` | `s2-pro` | `reference_id = "98655a12fa944e26b274c535e5e03842"` | WebSocket live TTS; `model` is sent via the handshake header, `reference_id` takes priority over `voice_id` |
| `alicloud` | `qwen3-tts-flash-realtime` | `voice_id = "Cherry"` | DashScope Realtime; the adapter outputs 24 kHz PCM, automatically resampling when the sample rate differs |
| `volcengine` | `seed-tts-2.0` | `voice_id = "zh_female_vv_uranus_bigtts"` | `model` maps to `X-Api-Resource-Id`, `voice_id` maps to the speaker, and the two must match |

Minimax WebSocket:

```toml
[tts]
provider = "minimax-ws"
model = "speech-2.8-hd"
voice_id = "male-qn-qingse"
emotion = "happy"
speed = 1.0
```

Fish Audio WebSocket:

```toml
[tts]
provider = "fish-audio"
model = "s2-pro"
reference_id = "98655a12fa944e26b274c535e5e03842"
speed = 1.0
```

Fish Audio `model` defaults to `s2-pro` and is sent as a WebSocket handshake header. `voice_id` is also accepted as the reference id. If both `reference_id` and `voice_id` are set, `reference_id` wins. In some networks, the public Fish Audio endpoint may require `ALL_PROXY` or `HTTPS_PROXY` in `/userdata/system/env`.

Alibaba Cloud Qwen-TTS Realtime:

```toml
[tts]
provider = "alicloud"
model = "qwen3-tts-flash-realtime"
voice_id = "Cherry"
speed = 1.0
```

The Alibaba Cloud adapter uses the DashScope WebSocket Realtime endpoint and outputs a fixed 24 kHz PCM; when the device playback sample rate differs, it automatically resamples.

Volcengine WebSocket bidirectional streaming V3:

```toml
[tts]
provider = "volcengine"
model = "seed-tts-2.0"
voice_id = "zh_female_vv_uranus_bigtts"
speed = 1.0
```

For Volcengine, `api_key` is the new console's `X-Api-Key`, `model` is the `X-Api-Resource-Id`, and `voice_id` is the speaker. `voice_id` must match the resource corresponding to `model`; when they do not match, the server returns `resource ID is mismatched with speaker related resource`. A verified working voice example for `seed-tts-2.0` is `zh_female_vv_uranus_bigtts`.

Switching providers at runtime:

```bash
curl -X POST http://<device-ip>:8080/api/settings/tts \
  -H 'Content-Type: application/json' \
  -d '{"provider":"volcengine","voice":"zh_female_vv_uranus_bigtts"}'
```

If you need to store the keys of multiple providers in the same config, use per-provider credentials. When switching providers via runtime POST, the corresponding credentials are read first, then overridden by the request body.

```toml
[tts]
provider = "minimax-ws"
model = "speech-2.8-hd"
voice_id = "male-qn-qingse"

[tts.credentials.fish-audio]
model = "s2-pro"
reference_id = "98655a12fa944e26b274c535e5e03842"

[tts.credentials.alicloud]
model = "qwen3-tts-flash-realtime"
voice_id = "Cherry"

[tts.credentials.volcengine]
model = "seed-tts-2.0"
voice_id = "zh_female_vv_uranus_bigtts"
```

## Episode telemetry (Langfuse)

Optional. After a task ends, asynchronously report the full episode to Langfuse; see [telemetry-langfuse.md](./telemetry-langfuse.md) for details.

```toml
[telemetry]
enabled = false
provider = "langfuse"
base_url = "http://langfuse.example.com:3000"
public_key = "pk-lf-..."
secret_key = "sk-lf-..."
upload_screenshots = true
upload_timeout_sec = 30
max_retry = 2
environment = "default"
tags = ["aiden-hardware"]
```

## Known limitations

- Web UI mode and on-device voice mode are mutually exclusive;
- Tencent ASR is still not fully implemented;
- `preferred_model`, `allowed_children`, and `model_text` are currently parsed but not fully wired into execution;
- The Agent loop has been split into three RoleProfiles: `planner`, `executor`, and `verifier`; skill instructions go into each role profile, function tools are exposed only to the executor, and the tool catalog is given to other roles as planning/review reference; the `verifier` validates against the original task and completion criteria;
- Example skills may reference old tools and should be checked before production use.
