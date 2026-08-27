---
sidebar_position: 17
---

# Realtime Voice Provider Research

> Verified against first-party documentation on 2026-08-27. Provider contracts,
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
- **Speko Realtime S2S**: qualifies as a managed speech-to-speech proxy for
  full-duplex PCM streaming, explicit interruption, and same-session tool
  calls. It is not a model provider by itself: the documented route bridges
  OpenAI Realtime, Gemini Live, xAI Grok Voice, or Inworld. Use a dedicated
  Speko adapter; do not treat its S2S URL as a Qwen endpoint override. The
  public S2S SDK contract and the published `/v1/sessions` OpenAPI schema
  currently disagree, so verify the account-visible endpoint before committing
  an implementation.

**Recommended order for Aiden:** OpenAI Realtime, Gemini Live, Azure OpenAI when
required by deployment policy, then Nova 2 Sonic only for an AWS/non-Chinese
deployment. Consider ElevenLabs only when managed agent orchestration is an
explicit product decision. Consider Speko after its account-visible S2S session
contract has been verified; it adds a proxy/control-plane dependency rather than
direct provider access. Do not mark Volcengine/Doubao or xAI as approved providers
until an account-visible realtime voice API and tool-call contract have been
verified.

## Aiden's current constraints

The current `src/agent/internal/agent/rtclient` is a Qwen-specific JSON-over-
WebSocket client:

- It builds `wss://.../api-ws/v1/realtime` and adds `model` as a query parameter.
- It sends `Authorization: Bearer ...` and optionally
  `X-DashScope-WorkSpace`.
- It sends Qwen event names such as `session.update`,
  `input_audio_buffer.append`, `response.create`, and
  `conversation.item.create`.
- The daemon currently records 16 kHz PCM16 mono and expects Qwen's 24 kHz PCM16
  mono output before playback resampling.

The implementation now exposes a provider-neutral realtime session interface
with Qwen and Speko adapters. `[voice_model].provider` references a named
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
| **Speko Realtime S2S** | **Yes at the transport/proxy layer.** One WebSocket carries ongoing mic PCM uploads and streamed response PCM downloads; the wire includes `interrupt` and `interruption` frames. | **Yes.** `tool_call` frames carry `callId`, name, and JSON arguments; `sendToolResult` returns the result on the same socket. | Backend `POST /v1/sessions` with `mode: "s2s"` mints short-lived `wsUrl`/`wsToken`; token is sent as the first WebSocket subprotocol. Speko then proxies to a selected realtime provider. The SDK docs describe this S2S shape, while the current [published OpenAPI session schema](https://docs.speko.ai/openapi.json) still describes the cascade/WebRTC shape (`transportToken`/`transportUrl`) and omits `mode`/`wsUrl`/`wsToken`. [S2S SDK](https://docs.speko.ai/sdk/realtime), [browser helper](https://docs.speko.ai/client/realtime-voice-conversation) | **Possible, but separate adapter and account validation required.** No Go SDK appears in Speko's [public repositories](https://github.com/orgs/SpekoAI/repositories); implement the short-lived session mint plus binary-PCM WebSocket protocol in Go, or run the official async/Python/TypeScript SDK out of process. |
| **Volcengine / Doubao** | **Unconfirmed for the repository account.** Public Ark text and existing Volcengine TTS endpoints do not prove a full-duplex audio model. | **Unconfirmed.** Require an account-visible realtime tool-call reference. | Existing repo integrations cover Ark text and WebSocket TTS, not a documented full-duplex voice session. [Ark](https://www.volcengine.com/product/ark), [Ark API docs](https://www.volcengine.com/docs/82379) | **Do not approve yet.** Run a provider spike only after the account exposes the required realtime API. |
| **xAI Voice Agent** | Potentially yes, but the current public contract, codec, and interruption details must be pinned from the account's accessible docs. | Potentially yes, but do not assume the tool event schema. | Separate realtime voice API; no compatibility claim is made here without a versioned first-party reference. [Voice Agent guide](https://docs.x.ai/docs/guides/voice/voice-agent) | **Pending verification.** Keep out of the implementation order until access and event details are confirmed. |

## Speko Realtime S2S evidence and limits

Speko exposes two different real-time voice shapes. The regular
`VoiceConversation` path is a LiveKit/WebRTC cascade (STT → LLM → TTS); the
`RealtimeVoiceConversation` path is a direct speech-to-speech WebSocket. The
S2S path is the one relevant to a full-duplex provider adapter. [Client overview](https://docs.speko.ai/client/overview)
states that this path uses a browser ↔ Speko S2S WebSocket proxy, while the
[S2S SDK reference](https://docs.speko.ai/sdk/realtime) names the upstream
providers as OpenAI Realtime, Gemini Live, xAI Grok Voice, and Inworld.

### Full-duplex audio is supported

The first-party browser implementation opens one WebSocket, treats incoming
binary frames as PCM16 response audio, and continuously sends microphone PCM16
frames from an `AudioWorklet`/`ScriptProcessor`. The send path only checks
`micMuted` and socket state; it does not pause when response playback is in
progress. Therefore microphone upload and assistant playback can overlap on
the same live session — this is genuine concurrent bidirectional streaming,
not a half-duplex push-to-talk loop. The public transport notes specify 20 ms
PCM16 binary frames up and PCM16 binary response frames down at negotiated
16 kHz or 24 kHz rates. [Browser source, WebSocket receive and dispatch](https://github.com/SpekoAI/client/blob/e5e5991938fbf2607ddf5aced0b962905d3c1f28/src/realtime-voice-conversation.ts#L122-L150), [browser source, capture/send loop](https://github.com/SpekoAI/client/blob/e5e5991938fbf2607ddf5aced0b962905d3c1f28/src/realtime-voice-conversation.ts#L235-L315), [transport notes](https://docs.speko.ai/client/realtime-voice-conversation#transport-notes)

### Interruption / barge-in exists, but threshold control is limited

The lower-level `@spekoai/sdk` handle exposes `interrupt()`, which sends
`{"t":"interrupt"}` to cancel the assistant response, and emits an
`interruption` frame with `at: "user" | "assistant"`. The browser helper
handles the server's `t: "interruption"` frame by stopping all queued and
currently playing audio immediately. Its public API does not expose an
explicit `interrupt()` method, so a hardware adapter should use the lower-level
S2S handle (or send the documented control frame) when it needs a local
barge-in trigger. [TypeScript SDK source](https://github.com/SpekoAI/typescript-sdk/blob/a737ec0a14d33e1f6db16c12392774d1b8df08f8/src/lib/resources/realtime.ts#L104-L198), [realtime frame types](https://github.com/SpekoAI/typescript-sdk/blob/a737ec0a14d33e1f6db16c12392774d1b8df08f8/src/lib/types/index.ts#L459-L507), [browser interruption handling](https://github.com/SpekoAI/client/blob/e5e5991938fbf2607ddf5aced0b962905d3c1f28/src/realtime-voice-conversation.ts#L173-L197)

The S2S SDK also exposes `commit()` to mark the end of a user turn. The
published S2S options do not expose endpointing delays, VAD thresholds, or VAD
score streaming. Speko's separate cascade session API has a richer
`turnHandling` object (`endpointing`, `interruption.mode`, and
`turnDetection`), but that contract is documented for the WebRTC cascade and
must not be assumed to apply to S2S. Treat S2S automatic turn detection as
provider/proxy behavior and validate it per selected upstream model. [S2S SDK methods](https://docs.speko.ai/sdk/realtime#methods), [cascade `turnHandling` schema](https://docs.speko.ai/api-reference/sessions#post-v1-sessions), [client roadmap (VAD score streaming deferred)](https://docs.speko.ai/client/overview#what-the-sdk-doesnt-do)

### AEC is a browser input constraint, not a Speko server guarantee

The browser helper requests `echoCancellation: true` and
`noiseSuppression: true` by default (with optional AGC) through
`getUserMedia`. Those are browser/OS media-track constraints. The S2S wire
contract is raw PCM16 and does not document server-side acoustic echo
cancellation. Aiden's hardware path cannot rely on these browser flags: it
needs device/driver AEC or another playback-reference echo-control strategy
when the speaker and microphone are live simultaneously. [Browser source, `getUserMedia` constraints](https://github.com/SpekoAI/client/blob/e5e5991938fbf2607ddf5aced0b962905d3c1f28/src/realtime-voice-conversation.ts#L200-L229), [audio constraint options](https://docs.speko.ai/client/realtime-voice-conversation#realtimeconversationoptions)

### Transport, authentication, and tools

The backend calls `POST /v1/sessions` with `mode: "s2s"` and an `s2s` block
containing provider/model/voice/system prompt (and, in the SDK, optional
tools). The response supplies `sessionId`, short-lived `wsUrl`/`wsToken`, and
negotiated input/output sample rates. The WebSocket token is passed as the
first subprotocol because browser WebSockets cannot set an Authorization
header. The SDK's `tool_call` frame carries a call id, name, and JSON argument
string; `sendToolResult(callId, output)` sends the result back without leaving
the session. [S2S SDK connect and params](https://docs.speko.ai/sdk/realtime),
[S2S SDK source](https://github.com/SpekoAI/typescript-sdk/blob/a737ec0a14d33e1f6db16c12392774d1b8df08f8/src/lib/resources/realtime.ts#L1-L123), [browser session mint example](https://docs.speko.ai/client/realtime-voice-conversation#mint-the-session-on-your-server)

There is an important documentation/API mismatch to resolve in an integration
spike. The S2S docs and SDK source post `mode: "s2s"` and expect `wsUrl` plus
`wsToken`, but the currently published OpenAPI `/v1/sessions` schema models a
cascade request and returns LiveKit `transportToken` plus `transportUrl` only.
Pin the account's deployed API version and run a real session-creation probe;
do not infer that the public OpenAPI document and S2S SDK are interchangeable.
[Published OpenAPI](https://docs.speko.ai/openapi.json), [S2S SDK reference](https://docs.speko.ai/sdk/realtime), [cascade session reference](https://docs.speko.ai/api-reference/sessions)

For Aiden, this means Speko is technically integrable but requires a new
provider adapter and configuration fields. The existing Qwen client sends
Qwen-specific JSON events with base64 audio and a bearer header; Speko S2S
uses binary PCM16 frames, `t`-tagged JSON controls, and a short-lived
subprotocol token. An endpoint URL swap is unsafe. The adapter should normalize
`sendAudio`/audio frames, `commit`, `interrupt`/`interruption`, transcripts,
`tool_call`/`sendToolResult`, usage, and close/error events while keeping Speko
credentials and sample-rate conversion inside the adapter.

## Adapter boundary

The provider-neutral interface should own only semantics that Aiden needs:

1. connection, authentication, model/deployment selection, and close/error;
2. append input audio and receive output audio deltas;
3. turn started/stopped, response cancellation, and interruption/truncation;
4. input/output transcript deltas and final messages;
5. tool-call start/delta/end plus tool-result submission;
6. usage and provider request identifiers.

Audio codec, sample rate, endpoint shape, session JSON, and credential headers
stay inside each adapter. The existing Qwen event structs should remain the Qwen
implementation, not become the shared protocol.

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
- [Speko TypeScript SDK source](https://github.com/SpekoAI/typescript-sdk/tree/a737ec0a14d33e1f6db16c12392774d1b8df08f8)
- [Speko published OpenAPI](https://docs.speko.ai/openapi.json)
- [Volcengine Ark](https://www.volcengine.com/product/ark)
- [Volcengine Ark API docs](https://www.volcengine.com/docs/82379)
- [xAI Voice Agent guide](https://docs.x.ai/docs/guides/voice/voice-agent)
