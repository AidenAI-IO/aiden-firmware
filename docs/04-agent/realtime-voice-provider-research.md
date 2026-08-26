---
sidebar_position: 17
---

# Realtime Voice Provider Research

> Verified against first-party documentation on 2026-08-26. Provider contracts,
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

**Recommended order for Aiden:** OpenAI Realtime, Gemini Live, Azure OpenAI when
required by deployment policy, then Nova 2 Sonic only for an AWS/non-Chinese
deployment. Consider ElevenLabs only when managed agent orchestration is an
explicit product decision. Do not mark Volcengine/Doubao or xAI as approved
providers until an account-visible realtime voice API and tool-call contract have
been verified.

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

The current config also has no provider or authentication-mode field, validates
only Qwen's two region names, and always constructs `rtclient.Config` in
`realtime_wakeup.go`. The relevant implementation is in
`src/agent/internal/agent/config.go` and `src/agent/cmd/daemon/realtime_wakeup.go`.
Therefore an alternative provider needs an adapter plus configuration and
credential changes; an endpoint override alone is only safe for a service that
implements the Qwen wire contract.

Config Web exposes these existing Qwen settings under `[voice_model]` when
`agent.input_mode = "realtime"`: credential, model, region, and voice. Advanced
Qwen protocol settings keep their TOML values and runtime defaults. This makes
the normal Qwen path configurable without editing TOML, but it is not a provider
selector: OpenAI, Gemini, Azure, Nova, and ElevenLabs still require the adapters
described below.

## Hard-requirement matrix

| Provider | Full duplex + interruption | Tool call in same session | Transport/auth | Aiden fit |
| --- | --- | --- | --- | --- |
| **OpenAI Realtime** | Yes. Bidirectional audio over JSON WebSocket with server/semantic VAD, cancellation, and truncation events. | Yes. Function tools and streamed function-call arguments are part of the Realtime event model. | WebSocket; server API key for the daemon. Current GA examples use `audio/pcm` at 24 kHz; pin the exact model's audio contract. [Realtime guide](https://platform.openai.com/docs/guides/realtime), [client events](https://platform.openai.com/docs/api-reference/realtime-client-events), [function calling](https://platform.openai.com/docs/guides/realtime#function-calling) | **Best first adapter.** Reuse Gorilla WebSocket and the normalized audio/tool lifecycle, but map every event and session field. |
| **Google Gemini Live** | Yes. Bidirectional Live WebSocket with automatic VAD and server interruption notifications. Standard Live examples use 16 kHz PCM input and 24 kHz model audio output. | Yes. The Live API has `toolCall` and `toolResponse` messages. | Gemini API key or Vertex AI regional endpoint/credentials; setup/content messages differ from Qwen. [Live guide](https://ai.google.dev/gemini-api/docs/live), [Live reference](https://ai.google.dev/api/live), [function calling](https://ai.google.dev/gemini-api/docs/live#function-calling) | **Good second adapter.** Add a dedicated event translator and validate output PCM against the board playback path. |
| **Azure OpenAI Realtime** | Yes. Same realtime speech-in/speech-out shape and interruption controls as the current OpenAI family. | Yes. The Azure realtime reference documents function calling. | Current GA uses the resource URL plus `/openai/v1`; deployment is selected as `model`. API key and Microsoft Entra ID are supported. Do not add `api-version` to the GA URL. [Realtime overview](https://learn.microsoft.com/en-us/azure/foundry/openai/how-to/realtime-audio), [WebSocket quickstart](https://learn.microsoft.com/en-us/azure/foundry/openai/how-to/realtime-audio-websockets), [reference](https://learn.microsoft.com/en-us/azure/foundry/openai/realtime-audio-reference) | **Good when Azure is required.** Share the normalized OpenAI event adapter, but add endpoint, deployment, and Entra credential configuration. |
| **Amazon Nova 2 Sonic** | Yes. Bidirectional speech, graceful interruption, and continued conversation while an async tool runs are documented. | Yes. Tools are declared in `promptStart`; `toolUse` and `toolResult` stay in the stream. | `InvokeModelWithBidirectionalStream` / AWS event stream, not WebSocket; use AWS SDK for Go v2 and IAM. Current Nova 2 docs list English variants, French, Italian, German, Spanish, Portuguese, and Hindi, but not Chinese; connections have an 8-minute limit and renewal flow. [Nova 2 speech](https://docs.aws.amazon.com/nova/latest/nova2-userguide/using-conversational-speech.html), [language support](https://docs.aws.amazon.com/nova/latest/nova2-userguide/sonic-language-support.html), [tool configuration](https://docs.aws.amazon.com/nova/latest/nova2-userguide/sonic-tool-configuration.html) | **Technically viable, largest adapter.** Reject for Chinese-first rollout. |
| **Amazon Nova Sonic v1** | Yes. Bidirectional streaming and graceful interruption are documented. | Yes. The v1 stream exposes `toolUse` and accepts `toolResult`. | Same native Bedrock event stream; v1 currently lists only English, French, Italian, German, and Spanish. [Nova v1 speech](https://docs.aws.amazon.com/nova/latest/userguide/speech.html), [bidirectional API](https://docs.aws.amazon.com/nova/latest/userguide/speech-bidirection.html), [speech tools](https://docs.aws.amazon.com/nova/latest/userguide/speech-tools.html) | **Technically viable, but legacy/narrower.** Do not choose for Chinese-first rollout. |
| **ElevenLabs Agents** | Yes. Managed agent WebSocket supports live input/output, turn-taking, and interruption. | Yes. Client/server tools are supported by the agent protocol. | Managed conversational-agent WebSocket; the vendor owns orchestration, agent prompt, and more of the tool lifecycle. [Agents overview](https://elevenlabs.io/docs/eleven-agents/overview), [WebSocket API](https://elevenlabs.io/docs/eleven-agents/api-reference/eleven-agents/websocket) | **Separate product path.** Integrate only if vendor-managed agent behavior is acceptable; do not model it as a raw LLM adapter. |
| **Volcengine / Doubao** | **Unconfirmed for the repository account.** Public Ark text and existing Volcengine TTS endpoints do not prove a full-duplex audio model. | **Unconfirmed.** Require an account-visible realtime tool-call reference. | Existing repo integrations cover Ark text and WebSocket TTS, not a documented full-duplex voice session. [Ark](https://www.volcengine.com/product/ark), [Ark API docs](https://www.volcengine.com/docs/82379) | **Do not approve yet.** Run a provider spike only after the account exposes the required realtime API. |
| **xAI Voice Agent** | Potentially yes, but the current public contract, codec, and interruption details must be pinned from the account's accessible docs. | Potentially yes, but do not assume the tool event schema. | Separate realtime voice API; no compatibility claim is made here without a versioned first-party reference. [Voice Agent guide](https://docs.x.ai/docs/guides/voice/voice-agent) | **Pending verification.** Keep out of the implementation order until access and event details are confirmed. |

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
- [Volcengine Ark](https://www.volcengine.com/product/ark)
- [Volcengine Ark API docs](https://www.volcengine.com/docs/82379)
- [xAI Voice Agent guide](https://docs.x.ai/docs/guides/voice/voice-agent)
