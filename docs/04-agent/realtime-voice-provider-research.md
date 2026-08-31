---
sidebar_position: 17
---

# Realtime Voice Provider Research

> Verified against first-party documentation and the current public SDK source on 2026-08-29. Provider contracts,
> model availability, and regional access change frequently; re-check the linked
> source before implementation.

This note evaluates providers for Aiden's `input_mode = "realtime"` path. The
hard requirements are:

1. **Full duplex**: input audio and output audio can be streamed in the same
   live session, with server turn detection and interruption/barge-in support.
2. **Tool calling**: the model can emit a structured function/tool call and the
   client can send the result back without leaving that session.
3. **Server integration**: Go can own the connection, audio pump, tool executor,
   cancellation, and provider credentials. Browser-only media APIs do not meet
   this requirement by themselves.

## Executive answer

The providers that currently meet both functional requirements are:

- **OpenAI Realtime**: best first adapter for Aiden. JSON over WebSocket, native
  audio streaming, VAD/interruption, and function calling are all documented.
- **Google Gemini Live**: good second adapter. It is also bidirectional over
  WebSocket and supports VAD/interruption and `toolCall`/`toolResponse`, but its
  setup and event graph are different from Qwen and OpenAI.
- **Azure OpenAI Realtime**: viable when the deployment must stay in Azure. The
  current GA contract uses `/openai/v1`; it is not the old `api-version` preview
  URL described in older examples.
- **Amazon Nova 2 Sonic / Nova Sonic v1**: both functionally qualify, but use
  Bedrock bidirectional event streaming rather than WebSocket. Nova 2 adds
  asynchronous tool handling and connection renewal; neither version currently
  documents Chinese voice support. They are not Chinese-first choices.
- **ElevenLabs Agents**: qualifies as a managed conversational agent with a
  WebSocket and tools. It is a separate product shape: ElevenLabs owns more of
  the agent loop, so it is not a raw model-session adapter.
- **Speko Realtime S2S**: is a provider-direct control and billing path, not a
  media proxy. `POST /v1/sessions` mints a scoped, short-lived credential and
  allowlisted provider endpoint; the board then connects directly to Gemini
  Live, OpenAI Realtime, or xAI Grok Voice. Use a dedicated mint-and-delegate
  adapter and keep the native provider wire implementation underneath it.

**Recommended order for Aiden:** OpenAI Realtime and Gemini Live remain the
native spikes. Speko S2S can be added as a credential broker around those native
adapters when Speko entitlement/billing is required; it does not add a second
media hop. Add Azure OpenAI when deployment policy requires it, and Nova 2 Sonic
only for an AWS/non-Chinese deployment. Do not mark Volcengine/Doubao as
production-ready until an account-visible realtime voice API and tool-call
contract have been verified.

## Native-provider count and the Speko route

There are two different counts, and they must not be represented by the same
field:

1. **Top-level Aiden adapters:** `qwen`, `speko`, `openai`, `gemini`, and `xai`
   are five provider values with four native adapters plus the Speko mint-and-delegate adapter. Each
   value owns its own endpoint, authentication, event mapping, audio format,
   interruption semantics, and tool-result protocol.
2. **Speko upstream routes used by Aiden:** `google` and `xai` are routing values in a
	Speko S2S session. They use the single Speko session-mint contract and then connect
	directly to the selected native provider. They are not additional Aiden adapters;
	each native transport and credential mode remains provider-specific. Speko's
	OpenAI/WebRTC route is intentionally outside the Aiden adapter.

The resulting configuration should therefore look like this. Native OpenAI, Gemini, and xAI records are available now:

```toml
[voice_model_providers.qwen]
type = "qwen"

[voice_model_providers.speko]
type = "speko"
upstream_provider = "google"

[voice_model_providers.openai]
type = "openai"

[voice_model_providers.gemini]
type = "gemini"

[voice_model_providers.xai]
type = "xai"

[voice_model]
provider = "openai"
```

The UI should only offer provider types whose adapters and credential validation
are present. Selecting Speko and then changing `upstream_provider` must keep the
native OpenAI/Gemini/xAI records and their keys untouched.

### Native OpenAI Realtime: approved first adapter

OpenAI's Realtime contract is a server-owned JSON WebSocket session. The client
sends `input_audio_buffer.append` events and receives streamed audio deltas; the
documented PCM16 format is 24 kHz, mono, little-endian. Server VAD and semantic VAD
emit speech start/stop events; cancellation and conversation-item truncation are
the mechanisms needed to stop already-buffered assistant audio on barge-in.
[Realtime guide](https://platform.openai.com/docs/guides/realtime),
[client events](https://platform.openai.com/docs/api-reference/realtime-client-events),
[VAD guide](https://platform.openai.com/docs/guides/realtime-vad),
[official SDK session schema](https://github.com/openai/openai-python/blob/main/src/openai/types/beta/realtime/session_create_params.py)

The daemon can authenticate with a standard OpenAI API key in the WebSocket
`Authorization: Bearer` header. Ephemeral client secrets are intended for
untrusted client environments and are unnecessary when the Go agent owns the
socket; the official SDK exposes the `/realtime/sessions` mint operation and
labels those tokens as short-lived client credentials.
[Realtime WebSocket authentication](https://platform.openai.com/docs/guides/realtime#connect-to-the-realtime-api),
[official session-create implementation](https://github.com/openai/openai-python/blob/main/src/openai/resources/beta/realtime/sessions.py)

Function tools are part of the same session: declare JSON-Schema functions in the
session, collect `response.function_call_arguments.delta`/`done`, then send a
`conversation.item.create` function output and `response.create` to continue the
turn. This maps cleanly to Aiden's normalized tool lifecycle.
[Realtime function calling](https://platform.openai.com/docs/guides/realtime#function-calling),
[client event reference](https://platform.openai.com/docs/api-reference/realtime-client-events)

The official SDK currently lists `gpt-realtime` alongside dated realtime preview
IDs and exposes named audio voices. Model and voice availability is account and
region dependent, so the adapter should accept a model/voice string and validate
it at session creation rather than hard-code one preview ID.
[official SDK model/voice fields](https://github.com/openai/openai-python/blob/main/src/openai/types/beta/realtime/session_create_params.py),
[Realtime models](https://platform.openai.com/docs/models#realtime-models)

### Native Gemini Live: approved second adapter

Gemini Live is a separate bidirectional WebSocket protocol. A connection begins
with a `setup` message, after which the client sends `realtimeInput` audio blobs
and receives `serverContent` model turns. The Live API documents automatic VAD,
server interruption notifications, and PCM audio (the standard examples use
16 kHz input and 24 kHz model output). It is not wire-compatible with either
Qwen or OpenAI even though the normalized semantics are similar.
[Live API guide](https://ai.google.dev/gemini-api/docs/live),
[Live API reference](https://ai.google.dev/api/live),
[official Google Gen AI Live client](https://github.com/googleapis/python-genai/blob/main/google/genai/live.py)

Gemini API sessions use a Google AI API key; Vertex AI uses a regional endpoint
and an OAuth access token (which may be minted from a service account). The
adapter keeps those credential modes distinct: Vertex uses a Bearer header and
a fully qualified project/location model resource, while the Gemini API uses
the `key` query parameter.
[Gemini API authentication](https://ai.google.dev/gemini-api/docs/api-key),
[Vertex AI authentication](https://cloud.google.com/vertex-ai/generative-ai/docs/start/api-keys)

The Live protocol has `toolCall` and `toolResponse` messages. Aiden can execute
the calls locally and send the JSON result back over the same socket while the
session remains active. The official client marks Live support as preview, and
model IDs/voices change more frequently than the stable text API; discover or
configure them per account instead of assuming that a Speko upstream model name
is accepted by the native endpoint.
[Live function calling](https://ai.google.dev/gemini-api/docs/live#function-calling),
[Live API reference (`toolCall`/`toolResponse`)](https://ai.google.dev/api/live),
[Google Gen AI SDK preview marker](https://github.com/googleapis/python-genai/blob/main/google/genai/live.py)

### Native xAI Voice Agent: approved adapter

xAI now publishes a complete first-party Speech to Speech WebSocket contract. The native adapter uses `wss://api.x.ai/v1/realtime?model=grok-voice-latest` with a server-side Bearer API key, JSON base64 PCM audio, server VAD, cancellation, and function-call events. xAI is OpenAI-Realtime compatible with documented differences: assistant audio uses `response.output_audio.delta`, and cumulative user transcription uses `conversation.item.input_audio_transcription.updated` when `grok-transcribe` is configured.
[xAI Speech to Speech](https://docs.x.ai/developers/model-capabilities/audio/speech-to-speech), [xAI realtime API reference](https://docs.x.ai/developers/rest-api-reference/inference/voice#realtime)

The native adapter maps the common function-tool and PCM JSON path; xAI binary transport, resumption, and xAI-hosted tools remain optional extensions. Speko `upstream_provider = "xai"` remains a provider-direct credential-broker route.

## Aiden's current constraints

The current `src/agent/internal/agent/realtimevoice/qwen.go` adapter uses the
shared JSON-over-WebSocket realtime session module while retaining Qwen's
provider-specific wire contract:

- It builds `wss://.../api-ws/v1/realtime` and adds `model` as a query parameter.
- It sends `Authorization: Bearer ...` and optionally
  `X-DashScope-WorkSpace`.
- It sends Qwen event names such as `session.update`,
  `input_audio_buffer.append`, `response.create`, and
  `conversation.item.create`.
- The daemon currently records 16 kHz PCM16 mono and expects Qwen's 24 kHz PCM16
  mono output before playback resampling.

The implementation now exposes a provider-neutral realtime session interface
with Qwen, Speko, OpenAI, Gemini, and xAI adapters. `[voice_model].provider` references a named
`[voice_model_providers.<name>]` record, matching the existing LLM provider
configuration pattern. Credentials, model, voice, and provider routing remain
stored per record, so switching Qwen/Speko does not overwrite the inactive
configuration. An endpoint override alone is still only safe for a service that
implements the selected adapter's wire contract.

## Hard-requirement matrix

| Provider | Full duplex + interruption | Tool call in same session | Transport/auth | Aiden fit |
| --- | --- | --- | --- | --- |
| **OpenAI Realtime** | Yes. Bidirectional audio over JSON WebSocket with server/semantic VAD, cancellation, and truncation events. | Yes. Function tools and streamed function-call arguments are part of the Realtime event model. | WebSocket; server API key for the daemon. Current GA examples use `audio/pcm` at 24 kHz; pin the exact model's audio contract. [Realtime guide](https://platform.openai.com/docs/guides/realtime), [client events](https://platform.openai.com/docs/api-reference/realtime-client-events), [function calling](https://platform.openai.com/docs/guides/realtime#function-calling) | **Best first adapter.** Reuse Gorilla WebSocket and the normalized audio/tool lifecycle, but map every event and session field. |
| **Google Gemini Live** | Yes. Bidirectional Live WebSocket with automatic VAD and server interruption notifications. Standard Live examples use 16 kHz PCM input and 24 kHz model audio output. | Yes. The Live API has `toolCall` and `toolResponse` messages. | Gemini API key or Vertex AI regional endpoint/credentials; setup/content messages differ from Qwen. [Live guide](https://ai.google.dev/gemini-api/docs/live), [Live reference](https://ai.google.dev/api/live), [function calling](https://ai.google.dev/gemini-api/docs/live#function-calling) | **Good second adapter.** Add a dedicated event translator and validate output PCM against the board playback path. |
| **Azure OpenAI Realtime** | Yes. Same realtime speech-in/speech-out shape and interruption controls as the current OpenAI family. | Yes. The Azure realtime reference documents function calling. | Current GA uses the resource URL plus `/openai/v1`; deployment is selected as `model`. API key and Microsoft Entra ID are supported. Do not add `api-version` to the GA URL. [Realtime overview](https://learn.microsoft.com/en-us/azure/foundry/openai/how-to/realtime-audio), [WebSocket quickstart](https://learn.microsoft.com/en-us/azure/foundry/openai/how-to/realtime-audio-websockets), [reference](https://learn.microsoft.com/en-us/azure/foundry/openai/realtime-audio-reference) | **Good when Azure is required.** Share the normalized OpenAI event adapter, but add endpoint, deployment, and Entra credential configuration. |
| **Amazon Nova 2 Sonic** | Yes. Bidirectional speech, graceful interruption, and continued conversation while an async tool runs are documented. | Yes. Tools are declared in `promptStart`; `toolUse` and `toolResult` stay in the stream. | `InvokeModelWithBidirectionalStream` / AWS event stream, not WebSocket; use AWS SDK for Go v2 and IAM. Current Nova 2 docs list English variants, French, Italian, German, Spanish, Portuguese, and Hindi, but not Chinese; connections have an 8-minute limit and renewal flow. [Nova 2 speech](https://docs.aws.amazon.com/nova/latest/nova2-userguide/using-conversational-speech.html), [language support](https://docs.aws.amazon.com/nova/latest/nova2-userguide/sonic-language-support.html), [tool configuration](https://docs.aws.amazon.com/nova/latest/nova2-userguide/sonic-tool-configuration.html) | **Technically viable, largest adapter.** Reject for Chinese-first rollout. |
| **Amazon Nova Sonic v1** | Yes. Bidirectional streaming and graceful interruption are documented. | Yes. The v1 stream exposes `toolUse` and accepts `toolResult`. | Same native Bedrock event stream; v1 currently lists only English, French, Italian, German, and Spanish. [Nova v1 speech](https://docs.aws.amazon.com/nova/latest/userguide/speech.html), [bidirectional API](https://docs.aws.amazon.com/nova/latest/userguide/speech-bidirection.html), [speech tools](https://docs.aws.amazon.com/nova/latest/userguide/speech-tools.html) | **Technically viable, but legacy/narrower.** Do not choose for Chinese-first rollout. |
| **ElevenLabs Agents** | Yes. Managed agent WebSocket supports live input/output, turn-taking, and interruption. | Yes. Client/server tools are supported by the agent protocol. | Managed conversational-agent WebSocket; the vendor owns orchestration, agent prompt, and more of the tool lifecycle. [Agents overview](https://elevenlabs.io/docs/eleven-agents/overview), [WebSocket API](https://elevenlabs.io/docs/eleven-agents/api-reference/eleven-agents/websocket) | **Separate product path.** Integrate only if vendor-managed agent behavior is acceptable; do not model it as a raw LLM adapter. |
| **Speko Realtime S2S** | **Yes, provider-direct.** Aiden supports the Gemini Live and xAI direct provider WebSockets. Speko does not receive PCM media. | **Yes.** Tool definitions are included in the mint request and tool results travel over the selected native provider session. | Backend `POST /v1/sessions` with `mode: "s2s"`, `Authorization: Bearer <SPEKO_API_KEY>`, and an `Idempotency-Key` mints a scoped credential plus endpoint, transport, reservation, telemetry, and rate metadata. Gemini puts the delegated token in the provider URL and xAI uses a WebSocket subprotocol. Speko also documents an OpenAI WebRTC path, but Aiden intentionally does not expose it. [Sessions API](https://docs.speko.ai/api-reference/sessions), [S2S SDK](https://docs.speko.ai/sdk/realtime), [browser helper](https://docs.speko.ai/client/realtime-voice-conversation) | **Mint-and-delegate adapter.** The board validates the provider-direct response and runs the Google or xAI native adapter; OpenAI routes are rejected. |
| **Volcengine / Doubao** | **Unconfirmed for the repository account.** Public Ark text and existing Volcengine TTS endpoints do not prove a full-duplex audio model. | **Unconfirmed.** Require an account-visible realtime tool-call reference. | Existing repo integrations cover Ark text and WebSocket TTS, not a documented full-duplex voice session. [Ark](https://www.volcengine.com/product/ark), [Ark API docs](https://www.volcengine.com/docs/82379) | **Do not approve yet.** Run a provider spike only after the account exposes the required realtime API. |
| **xAI Voice Agent** | Yes. Native Speech to Speech uses bidirectional WebSocket audio with server VAD and response cancellation. | Yes. Function call events and function_call_output continuation are documented. | WebSocket `wss://api.x.ai/v1/realtime?model=grok-voice-latest`; Bearer API key; JSON base64 PCM path implemented. [Speech to Speech](https://docs.x.ai/developers/model-capabilities/audio/speech-to-speech), [realtime reference](https://docs.x.ai/developers/rest-api-reference/inference/voice#realtime) | **Implemented.** xAI-specific transcript and output-audio event aliases are normalized. |


## Speko Realtime S2S evidence and limits

Speko exposes two different real-time voice shapes. The regular
`VoiceConversation` path is a LiveKit/WebRTC cascade (STT -> LLM -> TTS); the
`RealtimeVoiceConversation` path is provider-direct speech-to-speech. The S2S
path is the one relevant to Aiden's full-duplex adapter: `POST /v1/sessions`
reserves an entitlement and returns a short-lived credential, then the device
connects directly to the selected provider. Speko does not receive PCM media on
this path.

### Full-duplex audio and provider transports

The S2S SDK documents `openai`, `google`, and `xai`. Aiden requires callers to
pin Gemini Live or xAI so an automatic route cannot select OpenAI. OpenAI needs
the WebRTC path that this client does not implement. Audio is sent as
PCM16 chunks and response audio is returned by the selected provider. Aiden
therefore reuses the native Gemini or xAI adapter after the Speko mint step,
which keeps provider-specific framing and VAD semantics in one implementation.
[Realtime SDK](https://docs.speko.ai/sdk/realtime),
[client overview](https://docs.speko.ai/client/realtime-voice-conversation)

### Session mint contract

The backend calls `POST https://api.speko.dev/v1/sessions` with
`Authorization: Bearer <SPEKO_API_KEY>`, `Content-Type: application/json`, and a
stable `Idempotency-Key` (required for S2S). The JSON body sets `mode: "s2s"`
and includes an optional `s2s` object with `provider` (`openai`, `google`, or `xai`) and `model`. Although Speko permits omitting both for automatic routing, Aiden requires both and accepts only `google` or `xai`, preventing selection of an unsupported OpenAI/WebRTC route. The object also accepts optional `voice`, `systemPrompt`, `temperature`, input/output sample
rates, and `tools`. `agentId`, `webhookTags`, `metadata`, and `ttlSeconds` are
optional top-level fields. Reuse the same idempotency key only when retrying an
ambiguous bootstrap timeout. [Sessions API](https://docs.speko.ai/api-reference/sessions), [S2S SDK](https://docs.speko.ai/sdk/realtime)

A Gemini request can be represented as:

```json
{
  "mode": "s2s",
  "s2s": {
    "provider": "google",
    "model": "gemini-3.1-flash-live-preview",
    "voice": "Puck",
    "systemPrompt": "You are a concise voice assistant.",
    "inputSampleRate": 16000,
    "outputSampleRate": 24000,
    "tools": []
  },
  "ttlSeconds": 900
}
```

The provider-direct response contains `mode: "s2s"`,
`transport: "provider_direct"`, `sessionId`, `planId`, `attemptId`, selected
`provider`/`model`, an adapter identifier, `providerTransport`, an allowlisted
`endpoint`, and the negotiated `inputSampleRate`/`outputSampleRate`. The
`credential` object is `{kind: "bearer", value, expiresAt}`. It also carries
`telemetry: {endpoint, token, flushIntervalMs}` and a `reservation` containing
`id`, `authorizedDurationSeconds`, `leaseExpiresAt`, and billing state
(`mode: "direct_entitlement"`, `state`, `maximumAmountMicros`, `currency`, and
optional `renewalUrl`/`renewableUntil`). `session` echoes provider session
options; `sidebandUrl` is optional and is used by OpenAI. `expiresAt` is the
session-level expiry. [Current SDK response type](https://github.com/SpekoAI/typescript-sdk/blob/47e1c0b760f80c53aefd5e8b227f637208eba946/src/lib/resources/realtime.ts)

Require `transport == "provider_direct"` and validate `adapter`,
`providerTransport`, `endpoint`, `credential`, and negotiated rates before
opening the board transport. Older SDK revisions and cascade sessions use
`wsUrl`/`wsToken` or LiveKit `transportToken`/`transportUrl`; those are different
contracts and must not receive board PCM on this path. Do not log or persist the
short-lived `credential.value` or `telemetry.token`.

For the Gemini board connection, validate the returned endpoint and append
`access_token=<credential.value>` as a query parameter. Open a WebSocket to the
allowlisted path
`/ws/google.ai.generativelanguage.v1beta.GenerativeService.BidiGenerateContentConstrained`
and send a Live `setup` frame selecting `models/<model>`,
`generationConfig.responseModalities: ["AUDIO"]`, and optional voice,
temperature, system instruction, transcription, session resumption, and
function declarations. Wait for `setupComplete` before sending media. Send
microphone audio as JSON `realtimeInput.audio` with base64 PCM16 and
`audio/pcm;rate=16000`; receive model audio from
`serverContent.modelTurn.parts[].inlineData.data` (base64 PCM16 at 24 kHz),
transcripts from `inputTranscription`/`outputTranscription`, and function calls
from `toolCall`. `sessionResumptionUpdate.newHandle` plus
`reservation.billing.renewalUrl` supports rotating Gemini entitlement slices;
resume with the handle instead of starting a new conversation. [Current SDK Gemini setup and URL code](https://github.com/SpekoAI/typescript-sdk/blob/47e1c0b760f80c53aefd5e8b227f637208eba946/src/lib/resources/realtime.ts)

OpenAI is a separate transport: the response's `providerTransport` is
`webrtc`, `endpoint` accepts an SDP offer with `Authorization: Bearer
<credential.value>`, and `sidebandUrl` must bind the provider call using the
Speko telemetry token before media is enabled. xAI uses a provider WebSocket at
the returned endpoint (normally `wss://api.x.ai/v1/realtime?model=...`) and the
`xai-client-secret.<credential.value>` subprotocol. [Current SDK transport code](https://github.com/SpekoAI/typescript-sdk/blob/47e1c0b760f80c53aefd5e8b227f637208eba946/src/lib/resources/realtime.ts)

### Tools and lifecycle

Tool declarations are sent during session mint. The provider-direct connection
emits normalized tool calls and accepts tool results over the native provider
session. Commit, interruption, transcripts, usage, and close/error events retain
the semantics of the selected native adapter; Speko's entitlement and telemetry
remain control-plane concerns. Speko's browser helper also documents delegated
credential expiry and provider-specific renewal, so a long-running board process
must reconnect before the earliest of the credential expiry, session expiry, and
`reservation.leaseExpiresAt`. Aiden deliberately chooses a fresh mint plus local
context replay rather than implementing the provider-specific
`sessionResumptionUpdate.newHandle`/`renewalUrl` flow.

### Hardware implications

Browser-only AEC constraints do not apply to the board. The raw PCM16 path still
needs device/driver echo control when microphone capture and speaker playback
run concurrently. Provider-direct S2S removes a Speko media hop, but it does not
remove the need to validate the board's capture format, output rate, and local
barge-in behavior with a real credential.

### Vendor confirmation

On 2026-08-29, Speko founder Bek confirmed the account-level S2S contract: S2S is
enabled for this organization; `POST /v1/sessions` returns a short-lived provider
credential; and the device connects to Gemini Live directly instead of streaming
PCM16 through a Speko socket. Speko remains in the control plane for session
authorization, entitlement, and billing. The alternative server-side recording or
transcript path is a different media-routing product and must not be substituted
for Aiden's provider-direct S2S path.

This confirmation is consistent with the published provider-direct response shape
and is the basis for the Go adapter below. A production board rollout still needs
an account key and a real capture/playback test; mock protocol tests cannot verify
provider authorization, acoustic echo behavior, or lease renewal.

## Speko open-source scope

Speko's public repositories cover the customer-side runtime and client
integration layers, not the entire hosted product.
[`SpekoAI/gateway`](https://github.com/SpekoAI/gateway/tree/1770f56635ddde8fa964afd098f0a1730330d571)
is MIT-licensed Go source whose README describes a local HTTP/WebSocket service,
protocol/schema, plan verification, BYOK injection, provider adapters, telemetry,
tests, and build files. Its provider adapters include local STT/TTS integrations
for vendors such as OpenAI, Google, xAI, Deepgram, ElevenLabs, Cartesia, and
Speechmatics ([example adapter](https://github.com/SpekoAI/gateway/blob/1770f56635ddde8fa964afd098f0a1730330d571/providers/openai/stt.go)).
The same repository publishes the hosted relay wire contract in
[`relayapi/s2s.go`](https://github.com/SpekoAI/gateway/blob/1770f56635ddde8fa964afd098f0a1730330d571/relayapi/s2s.go),
but it does not contain a dedicated upstream S2S provider implementation.

The public [TypeScript SDK](https://github.com/SpekoAI/typescript-sdk/tree/b9c44d94d131f6a83eb70eeca86792a21e0e0b40)
and [Python SDK](https://github.com/SpekoAI/python-sdk/tree/3253369c2631b870adbbaae5fb1632878e48eaf6)
are MIT-licensed. Selected framework adapters are also public, including the
MIT-licensed [LiveKit adapter](https://github.com/SpekoAI/typescript-adapter-livekit/tree/1eb19f9d639f169555e2b4c5b14dcbab8e8806d8)
and BSD-2-Clause [Pipecat package](https://github.com/SpekoAI/pipecat-speko/tree/1e1e6a1beec204421c391492889c026a9c0db499).
This is enough to implement a Go client-side S2S adapter against the documented session mint and provider-direct credential handoff, but it is not evidence that Speko's hosted S2S
connector/control plane is self-hostable.

The Gateway README explicitly excludes Speko's hosted control plane, credential
broker, billing systems, databases, and infrastructure. Treat those as external
runtime dependencies for the S2S route. Also check licensing per repository:
the public [`client`](https://github.com/SpekoAI/client/tree/e5e5991938fbf2607ddf5aced0b962905d3c1f28)
source currently has no repository `LICENSE`, so source visibility alone does not
grant unrestricted reuse.

## Adapter boundary

The provider-neutral interface should own only semantics that Aiden needs:

1. connection, authentication, model/deployment selection, and close/error;
2. append input audio and receive output audio deltas;
3. turn started/stopped, response cancellation, and interruption/truncation;
4. input/output transcript deltas and final messages;
5. normalized tool calls plus tool-result submission;
6. usage and provider request identifiers.

Audio codec, sample rate, endpoint shape, session JSON, and credential headers
stay inside each adapter. The existing Qwen event structs should remain the Qwen
implementation, not become the shared protocol.

The implemented Go seam follows that rule with one small core interface:
`Provider.Open(SessionConfig) (Session, error)` creates a session whose core surface
only owns negotiated session information, normalized events, audio upload, and
close/error streams. Text injection, context replay, client-side turn commit,
response interruption, and tool-result submission are optional capability interfaces;
a provider does not receive a fake method that only returns "unsupported".

`SessionInfo` reports both legacy sample-rate fields and complete `AudioFormat`
values (`encoding`, sample rate, channels, and bit depth). The daemon uses the
negotiated format for device setup and only falls back to the legacy rate fields
for adapters that have not yet been upgraded. `ProviderRegistry` owns provider
construction settings such as endpoints and routing hints; API keys and per-session
model options remain in `SessionConfig`. This keeps daemon dispatch independent of
provider-specific constructors while leaving experimental adapters registerable in
tests or downstream builds.

## Practical integration plan

### 1. OpenAI Realtime spike

Implement a mockable provider interface and an OpenAI adapter first. The spike is
complete only when it proves, on the board audio path:

- continuous microphone upload and speaker playback in one session;
- user speech interrupts queued model audio;
- a real Aiden tool call completes and the response continues in the same
  session;
- 16 kHz input and provider output are converted without audible underruns;
- reconnect, cancellation, and tool timeout leave no active playback/recording
  session behind.

### 2. Gemini Live spike

Reuse the interface, not the OpenAI wire structs. Test `toolCall` followed by
`toolResponse`, server interruption, and the 16 kHz/24 kHz PCM path separately.
For Vertex AI, add regional endpoint and service-account/token configuration.

### 3. Azure deployment option

Share the OpenAI semantic adapter. Add `provider`, deployment name, resource
endpoint, and an explicit API-key versus Microsoft Entra credential path. Keep
Azure endpoint/version handling separate from public OpenAI so a future GA or
preview change cannot silently affect both.

### 4. Nova or managed-agent branch

Treat Nova 2 as a native AWS event-stream adapter and ElevenLabs as a managed-agent
integration. Include Nova's connection-renewal/session-continuation path. Neither
should be implemented as a Qwen endpoint override.

## Chinese-first decision

Keep Qwen as the baseline for mainland-China deployment. OpenAI, Gemini, and
Azure require account, region, network, and legal-access checks; multilingual
model claims are not evidence of Chinese voice quality. Nova 2 and Nova Sonic v1
currently documented language lists exclude Chinese. Measure Chinese recognition,
pronunciation, turn latency, barge-in, and tool-call reliability on the actual
board before changing the default provider.

## Primary-source index

- [OpenAI Realtime guide](https://platform.openai.com/docs/guides/realtime)
- [OpenAI Realtime client events](https://platform.openai.com/docs/api-reference/realtime-client-events)
- [OpenAI Realtime VAD](https://platform.openai.com/docs/guides/realtime-vad)
- [Google Gemini Live](https://ai.google.dev/gemini-api/docs/live)
- [Google Live API reference](https://ai.google.dev/api/live)
- [Google Gemini available regions](https://ai.google.dev/gemini-api/docs/available-regions)
- [Azure Realtime overview](https://learn.microsoft.com/en-us/azure/foundry/openai/how-to/realtime-audio)
- [Azure Realtime WebSockets](https://learn.microsoft.com/en-us/azure/foundry/openai/how-to/realtime-audio-websockets)
- [Azure Realtime reference](https://learn.microsoft.com/en-us/azure/foundry/openai/realtime-audio-reference)
- [Amazon Nova model overview](https://docs.aws.amazon.com/nova/latest/userguide/what-is-nova.html)
- [Amazon Nova 2 conversational speech](https://docs.aws.amazon.com/nova/latest/nova2-userguide/using-conversational-speech.html)
- [Amazon Nova 2 language support](https://docs.aws.amazon.com/nova/latest/nova2-userguide/sonic-language-support.html)
- [Amazon Nova 2 tool configuration](https://docs.aws.amazon.com/nova/latest/nova2-userguide/sonic-tool-configuration.html)
- [Amazon Nova Sonic v1 bidirectional speech](https://docs.aws.amazon.com/nova/latest/userguide/speech-bidirection.html)
- [Amazon Nova Sonic v1 speech tools](https://docs.aws.amazon.com/nova/latest/userguide/speech-tools.html)
- [ElevenLabs Agents overview](https://elevenlabs.io/docs/eleven-agents/overview)
- [ElevenLabs Agents WebSocket API](https://elevenlabs.io/docs/eleven-agents/api-reference/eleven-agents/websocket)
- [Speko Realtime S2S SDK](https://docs.speko.ai/sdk/realtime)
- [Speko Python Realtime SDK](https://docs.speko.ai/sdk-python/realtime)
- [Speko RealtimeVoiceConversation client](https://docs.speko.ai/client/realtime-voice-conversation)
- [Speko realtime client source](https://github.com/SpekoAI/client/tree/e5e5991938fbf2607ddf5aced0b962905d3c1f28)
- [Speko TypeScript SDK source](https://github.com/SpekoAI/typescript-sdk/tree/47e1c0b760f80c53aefd5e8b227f637208eba946)
- [Speko published OpenAPI](https://docs.speko.ai/openapi.json)
- [Volcengine Ark](https://www.volcengine.com/product/ark)
- [Volcengine Ark API docs](https://www.volcengine.com/docs/82379)
- [xAI Voice Agent guide](https://docs.x.ai/docs/guides/voice/voice-agent)
