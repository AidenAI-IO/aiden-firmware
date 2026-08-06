# Agent Configuration Reference

The Agent daemon takes `-dir`, the data directory it works out of. `agent.toml` is only one of the things that live there: skills, memory, cache and logs are all resolved relative to it (see [Directory layout](#directory-layout)). The `config`, `config-check` and `config-test` subcommands take `-config` with the path to a TOML config file. Every field below lives in `agent.toml`. Most fields can be edited through the on-device [Config Web page](#config-web-the-device-config-page); sections without dedicated controls are preserved by Config Web and can be edited by hand. TOML is the only supported config format; JSON config is deprecated.

## Contents

- [Directory layout](#directory-layout)
- [Config Web: the device config page](#config-web-the-device-config-page)
- [Minimal config examples](#minimal-config-examples)
- [Top-level fields](#top-level-fields)
- [`[device]`](#device)
- [`[model]`](#model)
- [`[log]`](#log)
- [`[audio]`](#audio)
- [`[voice_notifications]`](#voice_notifications)
- [`[hid]`](#hid)
- [`[stt]` and `[tts]`](#stt-and-tts)
- [`[live_activity]`](#live_activity)
- [Episode telemetry (Langfuse)](#episode-telemetry-langfuse)
- [System environment variables](#system-environment-variables)
- [`memory/extraction.yaml`](#memoryextractionyaml)
- [Known limitations](#known-limitations)

## Directory layout

Passed to the daemon as `-dir /userdata/agent`. Everything except `agent.toml`
is created on demand, so a directory holding only `agent.toml` is a valid start.

```text
/userdata/agent/
├── agent.toml               # required
├── quick_actions.json       # optional, falls back to the bundled defaults
├── skills/                  # optional, auto-discovers **/SKILL.md
├── skill-state/             # bundled skill sync manifest
├── memory/                  # conversation memory persistence directory
│   └── extraction.yaml      # optional memory extraction overrides
├── cache/                   # provider model metadata cache
├── log/                     # runtime log directory
└── board_id                 # generated on first run when live activity is on
```

## Config Web: the device config page

`config_web` is a lightweight C++ web service for maintaining the device Agent configuration, system environment variables, and Wi-Fi configuration. It is the primary way to edit the fields documented on this page without manually editing `agent.toml`.

On a device, open the config page in a browser at the USB-network gateway address:

```text
http://192.168.42.1
```

The firmware starts `config_web` on port 80.

### What the page can configure

The page fields cover the following config sections (all detailed later on this page). The language selector in the page header persists the device-level `locale`; switching it immediately updates the Config Web UI and restarts the Agent. If the locale changes the system prompt, startup creates a new context session instead of rewriting the previous session, so subsequent LLM responses use the selected language while old session history remains append-only.

- `agent`: `locale`, `input_mode`, `trigger_mode`, VAD params, `load_all_tools`, `max_iterations`, `custom_instruction`, `additional_prompt`
- `model`: provider, model, api_key, base_url, temperature, max_response_tokens, context_window, model_max_output_tokens. `context_window = 0` means auto-discover from OpenRouter/Ollama metadata when available.
- `stt`: provider, api_key, model, base_url, Tencent ASR fields
- `tts`: provider, api_key, model, voice_id, emotion, speed
- `audio`: socket, sample_rate, channels, bit_width, playback_backend
- `voice_notifications`: preserved by Config Web when other settings are saved; dedicated form controls are not currently rendered
- `log`: LLM HTTP log retention
- `device`: device_type
- `hid`: keyboard_device, keyboard_layout, mouse_device, android_keyboard_device, frame_socket, input_backend
- `env`: shell-style environment text written to `/userdata/system/env`, including optional proxy variables such as `http_proxy`, `HTTPS_PROXY`, and `NO_PROXY`
- Wi-Fi: SSID / PSK etc. (written to `/userdata/wpa_supplicant.conf`)

## Minimal config examples

### Web UI (text mode)

```toml
locale = "zh-CN"
custom_instruction = ""
max_iterations = -1
screenshot_keep_n = 3
screenshot_prune_interval = 2
input_mode = "text"

[providers.openrouter]
provider = "openrouter"
token_env = "OPENROUTER_API_KEY"

[device]
device_type = "iOS"

[model]
provider = "openrouter"
model = "bytedance-seed/seed-2.0-lite"
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
playback_backend = "auto"

[log]
llm_http_retention_days = 7

[hid]
keyboard_device = "/dev/hidg0"
keyboard_layout = "qwerty"
mouse_device = "/dev/hidg1"
android_keyboard_device = "/dev/hidg2"
frame_socket = "/run/frame_service/frame_service.sock"
```

> `token_env` lives on a named provider (`[providers.<name>]`), not on `[model]`, and means the key is read from that environment variable. In Config Web, type `$VAR_NAME` into the provider's API Key box to set it. Overlay example configs may also write the `api_key` field directly; for production, prefer environment variables or a device-side secure injection method.

### STT voice mode

```toml
locale = "zh-CN"
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

[providers.openrouter]
provider = "openrouter"
token_env = "OPENROUTER_API_KEY"

[device]
device_type = "iOS"

[model]
provider = "openrouter"
model = "bytedance-seed/seed-2.0-lite"

[stt]
provider = "openrouter"
api_key = "OPENROUTER_API_KEY"
model = "qwen/qwen3-asr-flash-2026-02-10"

[tts]
provider = "minimax"
model = "speech-2.8-hd"
voice_id = "male-qn-qingse"
emotion = "happy"
speed = 1.0

[audio]
socket = "/run/audio_service/audio_service.sock"
sample_rate = 16000
channels = 1
bit_width = 16
playback_backend = "auto"

[hid]
keyboard_device = "/dev/hidg0"
keyboard_layout = "qwerty"
mouse_device = "/dev/hidg1"
android_keyboard_device = "/dev/hidg2"
frame_socket = "/run/frame_service/frame_service.sock"
```

## Top-level fields

### General

| Field                       | Default / allowed values    | Description                                                                                                                                                                                               |
| --------------------------- | --------------------------- | --------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `locale`                    | `zh-CN` (default) / `en-US` | Device-level language for Config Web and user-facing Agent responses, including progress messages and `<tts>` content. This is independent from `[stt].language`, which only controls speech recognition. |
| `custom_instruction`        | -                           | Optional deployment/persona override for the built-in runtime instruction. Leave empty to use the agent binary default; set only for internal testing or deployment-specific behavior.                    |
| `additional_prompt`         | -                           | Additional prompt field; appended after the base instruction at runtime                                                                                                                                   |
| `load_all_tools`            | `false`                     | When `true`, also send `list_scripts`, `read_script`, and `write_script` to the conversational model. This does not expose HTTP-blocked maintenance tools.                                                |
| `max_iterations`            | `-1`                        | Maximum number of tool-call loops per run; `-1` means unlimited                                                                                                                                           |
| `screenshot_keep_n`         | `3`                         | Number of most recent screenshots to keep when pruning screenshots from the LLM context; unset or `0` uses the default                                                                                    |
| `screenshot_prune_interval` | `2`                         | Once screenshots exceed `screenshot_keep_n + screenshot_prune_interval`, replace old screenshots with placeholders in batches; unset or `0` uses the default                                              |
| `input_mode`                | `text` / `stt`              | Input mode                                                                                                                                                                                                |
| `todo_reminder_tool_calls`  | `3`                         | In single-agent/default mode, after how many consecutive tool calls to remind the model to update the todo; set to `0` to use the default                                                                 |

### Voice & VAD

These fields apply to the `stt` input mode.

| Field                           | Default                                                     | Description                                                                                                                                                                            |
| ------------------------------- | ----------------------------------------------------------- | -------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `trigger_mode`                  | `manual` / `wakeup`                                         | Voice-mode trigger method                                                                                                                                                              |
| `vad_backend`                   | `rknn`                                                      | VAD backend: `rknn` uses NPU encoder + CPU LSTM/decoder, `cpu` uses a pure-CPU helper                                                                                                  |
| `vad_model_path`                | `/oem/usr/model/silero_vad_6_2_encoder_rv1106_w8a8_v1.rknn` | Silero VAD RKNN encoder model path; not used when `vad_backend="cpu"`                                                                                                                  |
| `vad_helper_path`               | `/oem/usr/bin/rknn_vad`                                     | VAD helper executable path; the CPU backend defaults to `/oem/usr/bin/cpu_vad`                                                                                                         |
| `vad_speech_threshold`          | `0.5`                                                       | Silero VAD speech probability threshold                                                                                                                                                |
| `silence_ms`                    | `650`                                                       | How many milliseconds of silence before an utterance is considered finished                                                                                                            |
| `min_speech_ms`                 | `300`                                                       | Minimum valid speech duration                                                                                                                                                          |
| `voice_followup_enabled`        | `false`                                                     | Enable continuous follow-up after a single wakeup in wakeup mode; defaults to one wakeup per turn                                                                                      |
| `voice_followup_timeout_ms`     | `6000`                                                      | Window to wait for a user follow-up after the Agent replies                                                                                                                            |
| `voice_first_turn_timeout_ms`   | `10000`                                                     | Window to wait for the first utterance after wakeup                                                                                                                                    |
| `voice_max_turns`               | `0`                                                         | Maximum turns per wakeup session; `0` means unlimited                                                                                                                                  |
| `voice_interrupt_on_wakeup`     | `true`                                                      | When a wakeup is received again within a session, cancel thinking/TTS and listen again; repeated wakeups during the listening or recording phase are merged or ignored                 |
| `voice_streaming_tts_enabled`   | `true`                                                      | Feed the LLM streaming output into TTS sentence by sentence, reducing the wait before the first sentence plays                                                                         |
| `voice_tool_call_speech`        | `true`                                                      | Whether to asynchronously read the `content` of a tool-call event; this content comes only from the assistant content in the same LLM tool-call response, and stays silent when absent |
| `voice_progress_speech_enabled` | `true`                                                      | Whether to announce a short progress message when a todo item enters `in_progress`; todo state is still sent to the UI/trace                                                           |
| `voice_max_response_tokens`     | `400`                                                       | Per-turn output token limit for voice replies (must be `>= 0`)                                                                                                                         |

The model pointed to by `vad_model_path` must first be converted from the Silero ONNX to RV1106 RKNN on a PC using `silero-vad/convert_silero_vad_to_rknn.py`, then placed at the corresponding path on the device. The CPU backend requires `silero_vad_6_2_lstm_decoder_weights.bin` to include the Conv1d encoder extension, which can be generated from the TorchScript file shipped with the repo using `silero-vad/export_silero_vad_v6_2_weights.py`.
When `vad_helper_path` is still the built-in default, switching `vad_backend` automatically switches the helper; only when set to a custom path does it run that custom path.

## `[termination_policy]`

The termination policy prevents stalled runs from looping indefinitely. These
fields can be edited directly in `agent.toml`; omitted or zero-valued numeric
fields use the defaults below.

```toml
[termination_policy]
enabled = true
max_seconds = 0
repeat_action_limit = 3
same_result_limit = 3
screen_unchanged_limit = 5
soft_notice_stall_score = 2
restrict_tools_stall_score = 4
terminate_stall_score = 6
parse_failure_limit = 3
```

| Field                        | Default | Description                                                                                                 |
| ---------------------------- | ------- | ----------------------------------------------------------------------------------------------------------- |
| `enabled`                    | `true`  | Enable tiered loop detection and graceful termination                                                       |
| `max_seconds`                | `0`     | Wall-clock budget per instruction; `0` disables the time budget, and a consumed steer starts a fresh budget |
| `repeat_action_limit`        | `3`     | Stop after this many identical tool calls with identical results                                            |
| `same_result_limit`          | `3`     | Number of repeated identical results considered stalled                                                     |
| `screen_unchanged_limit`     | `5`     | Stop after this many UI actions without a screen change                                                     |
| `soft_notice_stall_score`    | `2`     | Stall score that injects a one-shot strategy-change notice                                                  |
| `restrict_tools_stall_score` | `4`     | Stall score that temporarily blocks repeated UI action tools                                                |
| `terminate_stall_score`      | `6`     | Stall score that ends the run gracefully                                                                    |
| `parse_failure_limit`        | `3`     | Stop after this many consecutive unparseable model outputs                                                  |

The three stall-score thresholds must satisfy
`soft_notice_stall_score < restrict_tools_stall_score < terminate_stall_score`.

## `[providers.<name>]`

Optional named provider configurations. Each section holds the credentials for
one endpoint, and `[model]`/`[model_text]` reference it by putting the name in
their own `provider` field. This lets several providers stay configured at once
so switching is a one-line change instead of a re-entry of keys.

| Field       | Description                                                                                        |
| ----------- | -------------------------------------------------------------------------------------------------- |
| `provider`  | Required provider type: `openai`, `openrouter`, `kimi`, `kimi-cn`, `volcengine`, `ollama`, `fake`   |
| `api_key`   | API key written directly                                                                           |
| `token_env` | Read the API key from the specified environment variable                                            |
| `base_url`  | Custom OpenAI-compatible endpoint; same `openai`/`ollama` restriction as `[model]`                  |

```toml
[providers.openai-work]
provider = "openai"
api_key = "sk-..."

[providers.ollama-local]
provider = "ollama"
base_url = "http://127.0.0.1:11434"

[model]
provider = "openai-work"   # references [providers.openai-work]
model = "gpt-5.5"
```

On load, a `provider` value that names a section under `[providers]` is replaced
by that section's provider type, and its `api_key`, `token_env` and `base_url`
fill in any field the model section leaves empty — values set directly on
`[model]` always win. `token_env` is the exception: it exists only on a named
provider, so the provider's value always applies. A `provider` that matches no section is treated as a
provider type, so existing configs keep working unchanged.

A `provider` that is neither a section name nor a known provider type is
rejected at load, so a typo or a reference left behind after deleting a section
fails with a clear error instead of surfacing later when the model client is
built. When a section is named exactly like a provider type, the section wins.
See `docs/04-agent/model-providers.md`.

## `[model]`

| Field                     | Description                                                                                                                                                                                                                                          |
| ------------------------- | ---------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `provider`                | A provider type, or the name of a `[providers.<name>]` section. Types: `openai`, `openrouter`, `kimi`, `kimi-cn`, `volcengine`, `ollama`, `fake`. `kimi` targets the Moonshot global site (`https://api.moonshot.ai/v1`) and `kimi-cn` targets the mainland China site (`https://api.moonshot.cn/v1`); `volcengine` targets Volcengine Ark (`https://ark.cn-beijing.volces.com/api/v3`). |
| `model`                   | Model name; usually required except for `fake`                                                                                                                                                                                                       |
| `base_url`                | Custom OpenAI-compatible endpoint. Only `openai` and `ollama` accept a `base_url` override; other providers use their built-in endpoints and a stored value is dropped on load. |
| `api_key`                 | API key written directly                                                                                                                                                                                                                             |
| `temperature`             | Sampling temperature. When unset, the default is model-dependent (some models such as Kimi K3 require a fixed temperature), falling back to `0.2`. An explicit value always takes precedence.                                                        |
| `reasoning_effort`        | Thinking budget. Unset is auto: the field is omitted and the provider decides, except that reasoning is disabled for no-tool requests. `low`/`medium`/`high` work everywhere; `minimal` is Volcengine Ark only; `none` is accepted by the others but not by Ark. Some models pin a lighter default (see the registry in `model_specs.go`); an explicit value always wins. |
| `max_response_tokens`     | Maximum output tokens passed to the model on request                                                                                                                                                                                                 |
| `context_window`          | Optional total context window override in tokens. Unset or `0` uses provider metadata for OpenRouter/Ollama when available, then the built-in registry, then memory fallback.                                                                        |
| `model_max_output_tokens` | Optional advertised max output override in tokens. Unset or `0` uses provider metadata when fetched, then the built-in registry.                                                                                                                     |

### Moonshot Kimi K3

Use the dedicated `kimi` (global) or `kimi-cn` (mainland China) provider. Each has a built-in Moonshot OpenAI-compatible endpoint, so only `model` and the API key are required. The `kimi-k3` context window and max output are in the built-in registry, so the metadata overrides can stay unset.

```toml
# Global site (https://api.moonshot.ai/v1)
[model]
provider = "kimi"
model = "kimi-k3"
api_key = "MOONSHOT_API_KEY"

# Mainland China site (https://api.moonshot.cn/v1)
# [model]
# provider = "kimi-cn"
# model = "kimi-k3"
# api_key = "MOONSHOT_API_KEY"
```

### Volcengine Ark (Doubao)

Use the `volcengine` provider. It targets Ark's OpenAI-compatible endpoint
(`https://ark.cn-beijing.volces.com/api/v3`), so only `model` and the API key are
required. `model` is the Ark model ID, and `api_key` is an Ark API key.

```toml
[model]
provider = "volcengine"
model = "doubao-seed-2-1-pro-260628"
api_key = "ARK_API_KEY"
```

To read the key from the environment instead of writing it here, put it on a
named provider and reference that:

```toml
[providers.ark]
provider = "volcengine"
token_env = "ARK_API_KEY"

[model]
provider = "ark"
model = "doubao-seed-2-1-pro-260628"
```

Ark also exposes an Anthropic-protocol endpoint at `/api/compatible`. This agent
always speaks the OpenAI-compatible protocol, so use the `/api/v3` path above.

`reasoning_effort` accepts `minimal` (no thinking), `low`, `medium`, and `high`.
Ark treats an omitted value as `high`, which delays the first streamed token by
several seconds; the built-in registry therefore pins `low` as the default for
`doubao-seed-2-1-pro-260628` so voice replies stay responsive. Set the field
explicitly to override. Note that `none` is not an Ark level — use `minimal`.

The context window and max output for `doubao-seed-2-1-pro-260628` are in the
built-in registry, so the metadata overrides can stay unset. Other dated releases
are not registered; for those, set `context_window` and
`model_max_output_tokens` explicitly, or add a registry entry keyed by the exact
Ark model ID.

The `[tts]` section has an unrelated provider that is also named `volcengine`. It
speaks a separate WebSocket protocol with its own host and credentials, so an Ark
API key and base URL do not carry over to it.

## `[log]`

| Field                     | Default | Description                                                                                                                                              |
| ------------------------- | ------- | -------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `llm_http_retention_days` | `7`     | Number of days to keep raw LLM HTTP logs under `<config_dir>/log` (`llm-http-*.log`). Cleanup runs when the agent starts; unset or `0` uses the default. |

## `[audio]`

| Field              | Default                                 | Description                                                                                                                                                                                                                   |
| ------------------ | --------------------------------------- | ----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `socket`           | `/run/audio_service/audio_service.sock` | Audio Service socket                                                                                                                                                                                                          |
| `sample_rate`      | `16000`                                 | Sample rate                                                                                                                                                                                                                   |
| `channels`         | `1`                                     | Number of channels                                                                                                                                                                                                            |
| `bit_width`        | `16`                                    | Bit width                                                                                                                                                                                                                     |
| `playback_backend` | `auto`                                  | TTS playback backend. `auto` uses `audio_service` on board and the local OS player when the Agent is running in desktop/PC mode through ADB input backend or environment bridge. Use `audio_service` or `local` to force one. |

## `[voice_notifications]`

Voice notifications attach system reminders to a normal spoken reply or replace a final failed LLM turn with a fixed error message. They never start an independent background announcement. See [Voice Notifications](voice-notifications.md) for the lifecycle and delivery contract.

```toml
[voice_notifications]
enabled = true
max_pending = 8

[voice_notifications.response_tail]
enabled = true
max_items = 1
max_text_chars = 40

[voice_notifications.expiration]
default_ttl_seconds = 0

[voice_notifications.expiration.code_ttl_seconds]
storage = 900
```

| Field                                | Default         | Description                                                               |
| ------------------------------------ | --------------- | ------------------------------------------------------------------------- |
| `enabled`                            | `true`          | Enable persistent tails and final-turn replacements                       |
| `max_pending`                        | `8`             | Maximum active condition records kept by the in-memory manager            |
| `response_tail.enabled`              | `true`          | Allow persistent reminders to be appended to normal replies               |
| `response_tail.max_items`            | `1`             | Maximum reminders per reply; the current implementation supports only `1` |
| `response_tail.max_text_chars`       | `40`            | Maximum reminder length in Unicode characters                             |
| `expiration.default_ttl_seconds`     | `0`             | Default active-condition lease; `0` disables automatic expiration         |
| `expiration.code_ttl_seconds.<code>` | `storage = 900` | Per-code lease override renewed by each active heartbeat                  |

Config Web preserves this section through GET/POST and TOML save operations. Edit it directly in `agent.toml` until dedicated controls are added to the page.

## `[device]`

| Field         | Default | Description |
| ------------- | ------- | ----------- |
| `device_type` | `iOS`   | Target host type for USB HID descriptors and Agent global device state. Accepted values: `iOS`, `Android`, `macOS`, `windows`, `linux`. `Android` derives HID `pointer_mode = "touchscreen"`; every other value derives `pointer_mode = "absolute"`. Changing it requires a reboot so USB descriptors are re-enumerated. |

## `[hid]`

| Field                     | Default                                 | Description                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                  |
| ------------------------- | --------------------------------------- | ---------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `keyboard_device`         | `/dev/hidg0`                            | Keyboard HID device                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                          |
| `keyboard_layout`         | `qwerty`                                | How the phone interprets the external USB HID keyboard: `qwerty`, `azerty`, or `qwertz`. The visible soft-keyboard layout may differ. Used by `keyboard_text` and standard text-like `keyboard_tap` keys; applying a new mapping requires only an Agent restart. iOS additionally locks the hardware layout at USB enumeration, so switch the phone's input language to match _before_ saving; Config Web then re-enumerates the gadget automatically, with no power-cycle needed. See [USB HID](../03-services/usb-hid.md). |
| `mouse_device`            | `/dev/hidg1`                            | Mouse/touch HID device                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                       |
| `android_keyboard_device` | `/dev/hidg2`                            | Consumer Control HID device (`hid.usb2`) used for Android extension keys when `[device].device_type = "Android"` and media/volume/brightness/screenshot keys for other device types                                                                                                                                                                                                                                                                                                                                          |
| `frame_socket`            | `/run/frame_service/frame_service.sock` | Frame Service socket used by the screenshot tool                                                                                                                                                                                                                                                                                                                                                                                                                                                                             |
| `input_backend`           | `hid`                                   | Low-level input backend for click/touch/keyboard tools. `hid` writes USB HID reports; `adb` uses the paired Android ADB connection and `adb shell input`/ADBKeyboard commands.                                                                                                                                                                                                                                                                                                                                               |

## `[tts_providers.<name>]` and `[stt_providers.<name>]`

Named voice provider configurations, the same shape `[providers.<name>]` gives
`[model]`. Each section holds the credentials and settings for one voice service,
and `[tts]` / `[stt]` reference one by putting the name in their own `provider`
field. Several providers stay configured at once, so switching is a one-line
change instead of a re-entry of keys.

Unlike `[providers.<name>]`, these are separate namespaces: the `[tts]`
`volcengine` provider speaks a different protocol with its own host and
credentials than the Ark LLM provider of the same name, so one map could not
serve both. Each namespace also validates its own provider types — a TTS type is
rejected for `[model]` and vice versa.

Several records may share one provider type, which is how two accounts of the
same service (different keys, different voices) stay configured together.

```toml
[tts_providers.minimax-main]
provider = "minimax"
api_key = "sk-aaa"
voice_id = "male-qn-qingse"

[tts_providers.minimax-alt]     # same type, second account
provider = "minimax"
api_key = "sk-bbb"
voice_id = "female-shaonv"

[tts_providers.fish]
provider = "fish-audio"
token_env = "FISH_API_KEY"
reference_id = "abc123"

[tts]
provider = "minimax-main"       # references [tts_providers.minimax-main]
speed = 1.0

[stt_providers.tencent]
provider = "tencent-asr"
app_id = "123"
secret_id = "AKID..."
secret_key = "..."
region = "ap-shanghai"

[stt]
provider = "tencent"
language = "zh"
```

### Field placement

A field lives on the record when it stops meaning anything once the provider
changes; it stays on `[tts]` / `[stt]` when it holds regardless of provider.

| | Record fields | Stays on the flat section |
| ---- | ---- | ---- |
| TTS | `provider` (type), `api_key`, `token_env`, `model`, `voice_id`, `emotion`, `reference_id` | `provider` (reference), `speed` |
| STT | `provider` (type), `api_key`, `token_env`, `model`, `base_url`, `app_id`, `secret_id`, `secret_key`, `region`, `engine_model_type` | `provider` (reference), `language` |

`speed` is a listening preference and `language` a transcription preference:
neither should change because the voice changed, so both stay global.

`token_env` reads the key from the named environment variable, and is used when
`api_key` is unset. Config Web folds both into one API Key box: a value starting
with `$` is stored as `token_env`.

### Backward compatibility

- A bare provider type in `[tts]` / `[stt]` keeps working. `provider = "minimax-cn"`
  with a flat `api_key` needs no migration to keep speaking.
- Flat credentials on `[tts]` / `[stt]` are upgraded to records on load, keyed
  by provider type. The upgrade is written back the next time the config is
  saved, and an existing record is never overwritten.
- An unresolvable reference does not stop the device from booting: voice is
  optional at runtime, so a stale name is reported and the agent starts without
  voice. Config Web rejects such a reference when saving instead, while the form
  is still on screen.

## `[stt]` and `[tts]`

`[stt]` is required when `input_mode = "stt"`; `[tts]` is required when `input_mode = "stt"`.

`provider` here is a reference to a `[tts_providers.<name>]` /
`[stt_providers.<name>]` record (a bare provider type still works — see above).
The provider-specific credentials listed below live on that record; Config Web
edits them in the provider dialog rather than on the `[tts]` / `[stt]` card.

STT:

- `provider = "openai-whisper"`: currently available;
- `provider = "openrouter"`: currently available, default endpoint is `https://openrouter.ai/api/v1/audio/transcriptions`, request body uses base64 WAV;
- `provider = "tencent-asr"`: Tencent Cloud Sentence Recognition (SentenceRecognition), uses `secret_id` / `secret_key`, no `base_url` needed; the legacy values `tencent` / `tencent_asr` are retained only as compatibility aliases.

TTS:

- `provider = "minimax"`: Minimax WebSocket, global endpoint `api.minimax.io`;
- `provider = "minimax-cn"`: Minimax WebSocket, mainland China endpoint `api.minimaxi.com`;
- `provider = "fish-audio"`: Fish Audio WebSocket;
- `provider = "alicloud"`: Alibaba Cloud Qwen-TTS Realtime;
- `provider = "volcengine"`: Volcengine WebSocket bidirectional streaming V3. Currently only the new console's `X-Api-Key` authentication is supported: `api_key` maps to `X-Api-Key`, `model` maps to `X-Api-Resource-Id` (default `seed-tts-2.0`), and `voice_id` maps to the speaker.

`[tts]` common fields:

| Field          | Description                                                                                                                |
| -------------- | -------------------------------------------------------------------------------------------------------------------------- |
| `provider`     | Required. One of `minimax`, `minimax-cn`, `fish-audio`, `alicloud`, `volcengine`                                           |
| `api_key`      | Required. The authentication key for each provider; the examples below omit this field to avoid writing keys into the docs |
| `model`        | Optional. Minimax model name, Fish Audio model header, Alibaba Cloud Realtime model name, Volcengine `X-Api-Resource-Id`   |
| `voice_id`     | Optional. Minimax voice id, Alibaba Cloud voice, Volcengine speaker. Not used by Fish Audio (see `reference_id`)           |
| `reference_id` | Optional Fish Audio reference id; defaults to the built-in demo voice shown by Config Web. Ignored by other providers      |
| `emotion`      | Optional. Minimax emotion; Volcengine passes it through as `audio_params.emotion`, requires voice support                  |
| `speed`        | Optional. Speech rate, default `1.0`; the supported range varies by provider, refer to the official docs                   |

The config examples below only show non-key fields relevant to adapter behavior; at actual runtime you still need to provide the corresponding `api_key` on the provider's `[tts_providers.<name>]` record.

Common TTS adapter configs:

| Provider     | `model` example            | Voice/reference field                               | Description                                                                                                         |
| ------------ | -------------------------- | --------------------------------------------------- | ------------------------------------------------------------------------------------------------------------------- |
| `minimax`    | `speech-2.8-hd`            | `voice_id = "male-qn-qingse"`                       | Minimax WebSocket via `api.minimax.io`; `emotion` is passed through to Minimax                                      |
| `minimax-cn` | `speech-2.8-hd`            | `voice_id = "male-qn-qingse"`                       | Minimax WebSocket via `api.minimaxi.com`; `emotion` is passed through to Minimax                                    |
| `fish-audio` | `s2-pro`                   | `reference_id = "98655a12fa944e26b274c535e5e03842"` | WebSocket live TTS; the shown reference is used by default, and `voice_id` is not used                              |
| `alicloud`   | `qwen3-tts-flash-realtime` | `voice_id = "Cherry"`                               | DashScope Realtime; the adapter outputs 24 kHz PCM, automatically resampling when the sample rate differs           |
| `volcengine` | `seed-tts-2.0`             | `voice_id = "zh_female_vv_uranus_bigtts"`           | `model` maps to `X-Api-Resource-Id`, `voice_id` maps to the speaker, and the two must match                         |

### Provider examples

Minimax WebSocket:

```toml
[tts]
provider = "minimax"
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

Fish Audio `model` defaults to `s2-pro` and is sent as a WebSocket handshake header. An empty `reference_id` uses the built-in demo voice shown in Config Web; configure `reference_id` on the selected `[tts_providers.<name>]` record to override it. `voice_id` is not used by Fish Audio and is ignored (this avoids inheriting a `voice_id` meant for another provider). In some networks, the public Fish Audio endpoint may require `ALL_PROXY` or `HTTPS_PROXY` in `/userdata/system/env`.

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

### Switching providers at runtime

```bash
curl -X POST http://<device-ip>:8080/api/settings/tts \
  -H 'Content-Type: application/json' \
  -d '{"provider":"volcengine","voice":"zh_female_vv_uranus_bigtts"}'
```

If you need to store the keys of multiple providers in the same config, use named provider records. When switching providers via runtime POST, the corresponding record is read first, then overridden by the request body.

```toml
[tts_providers.minimax-main]
provider = "minimax"
api_key = "..."
model = "speech-2.8-hd"
voice_id = "male-qn-qingse"

[tts_providers.fish-main]
provider = "fish-audio"
api_key = "..."
model = "s2-pro"
reference_id = "98655a12fa944e26b274c535e5e03842"

[tts_providers.alicloud-main]
provider = "alicloud"
api_key = "..."
model = "qwen3-tts-flash-realtime"
voice_id = "Cherry"

[tts_providers.volcengine-main]
provider = "volcengine"
api_key = "..."
model = "seed-tts-2.0"
voice_id = "zh_female_vv_uranus_bigtts"

[tts]
provider = "minimax-main"
speed = 1.0
```

## `[live_activity]`

For the iOS companion app's Live Activity / Dynamic Island task status. The agent-side status snapshot is enabled by default. See the full flow in [Live Activity / Dynamic Island](./live-activity.md).

**Relay-based updates** (legacy, used when APNs credentials are not configured):

| Field           | Default                                 | Description                                                                                       |
| --------------- | --------------------------------------- | ------------------------------------------------------------------------------------------------- |
| `relay_url`     | preconfigured in official firmware      | Aiden Live Activity relay URL; only advanced deployments need to override it                      |
| `relay_api_key` | preconfigured in official firmware      | Shared relay Bearer token; must match the app build config and relay server `AIDEN_RELAY_API_KEY` |
| `board_id`      | generated in `/userdata/agent/board_id` | Board ID in relay; generated on first run. Empty or `default` is not a valid relay identity       |

**APNs-based updates** (for remote updates when the app is backgrounded, on lock screen, or not open):

| Field              | Default                              | Description                                                                                                                        |
| ------------------ | ------------------------------------ | ---------------------------------------------------------------------------------------------------------------------------------- |
| `bundle_id`        | -                                    | iOS app bundle id; required only when configuring background APNs and `topic` is not explicitly set                                |
| `topic`            | `<bundle_id>.push-type.liveactivity` | APNs topic; usually does not need to be set manually                                                                               |
| `environment`      | `sandbox`                            | `sandbox` or `production`                                                                                                          |
| `team_id`          | -                                    | Apple Developer Team ID; used only by background APNs                                                                              |
| `key_id`           | -                                    | APNs Auth Key ID; used only by background APNs                                                                                     |
| `private_key_path` | -                                    | APNs `.p8` private key path; used only by background APNs                                                                          |
| `private_key_pem`  | -                                    | Inline APNs `.p8` PEM directly; for development/debugging only, do not place in open-source config or on user boards in production |
| `timeout_sec`      | `10`                                 | Background APNs request timeout                                                                                                    |

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

## System environment variables

The Agent no longer reads `[proxy]` from `agent.toml`. Outbound HTTP/WebSocket requests, shell tool subprocesses, OTA commands launched through `aiden-env-run`, and SSH login shells all use environment variables from `/userdata/system/env`. The file is loaded with shell syntax, for example:

```sh
HTTP_PROXY=http://127.0.0.1:7890
HTTPS_PROXY=http://127.0.0.1:7890
NO_PROXY=localhost,127.0.0.1,::1,10.0.0.0/8,172.16.0.0/12,192.168.0.0/16
OPENROUTER_API_KEY=...
```

| Variable                      | Description                                                                                                                                        |
| ----------------------------- | -------------------------------------------------------------------------------------------------------------------------------------------------- |
| `HTTP_PROXY` / `http_proxy`   | HTTP proxy URL, for example `http://127.0.0.1:7890`                                                                                                |
| `HTTPS_PROXY` / `https_proxy` | HTTPS proxy URL, usually the same HTTP proxy endpoint                                                                                              |
| `ALL_PROXY` / `all_proxy`     | Generic proxy used by HTTP clients and some WebSocket adapters                                                                                     |
| `NO_PROXY` / `no_proxy`       | Comma-separated bypass rules; when a proxy URL is set and no bypass value is present, the launcher injects the default private-network bypass list |

## `memory/extraction.yaml`

Optional. Place `memory/extraction.yaml` under the config directory to control session-memory compaction and chunk extraction. Missing files and invalid fields fall back to defaults. See [session-memory.md](./session-memory.md) for the full flow.

| Field                                | Default                 | Description                                                                                                                                                                                                                          |
| ------------------------------------ | ----------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------ |
| `reserve_tokens`                     | `8192`                  | Token headroom reserved below the active model context window. Compaction triggers when `prompt_tokens >= context_window - reserve_tokens`. The value is clamped to at most half of the window so small-window models remain usable. |
| `keep_recent_tokens`                 | `20000`                 | Approximate token budget for the hot window retained by token-based cut-point selection. It is clamped together with `reserve_tokens` to fit the active window.                                                                      |
| `hot_window_events`                  | `30`                    | Target number of recent events retained by the count fallback. Used only when prompt-token data is unavailable.                                                                                                                      |
| `count_compress_after_events`        | `hot_window_events * 2` | Event-count trigger used only when prompt-token data is unavailable. If omitted, it is derived from the normalized `hot_window_events`; explicit values must be greater than `hot_window_events`.                                    |
| `context_window`                     | `32000`                 | Fallback context window for compaction when the active model is not present in `model_specs`. Runtime normally derives this from `ModelResolver.Spec()`; this value is only used for unknown models.                                 |
| `compress_at_percent`                | `50`                    | Percentage trigger: compaction starts when `prompt_tokens / context_window >= compress_at_percent%`.                                                                                                                                 |
| `summary_max_chunks`                 | `10`                    | Number of chunk summaries kept in the Recent Chunks section of `summary.md`. Older entries move to the archive and are folded into the Rolling Summary.                                                                              |
| `session_boundary_enabled`           | `true`                  | Classify each new user turn as continuing the current session or starting a new one. A `new` boundary archives the current `memory/session/` directory and recreates an empty active session.                                        |
| `session_boundary_short_gap_seconds` | `300`                   | Gap below which a turn is treated as continuation regardless of lexical signals.                                                                                                                                                     |
| `session_boundary_long_gap_seconds`  | `1800`                  | Gap above which a turn is treated as a fresh session regardless of lexical signals.                                                                                                                                                  |
| `tag_candidates`                     | see defaults            | Candidate keywords matched when tagging chunk summaries.                                                                                                                                                                             |
| `entity_suffixes`                    | `["App","app","APP"]`   | Suffixes recognized during entity extraction.                                                                                                                                                                                        |

## Known limitations

- `preferred_model`, `allowed_children`, and `model_text` are currently parsed but not fully wired into execution;
- Example skills may reference old tools and should be checked before production use.
