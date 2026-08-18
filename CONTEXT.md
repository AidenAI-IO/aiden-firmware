# Aiden Firmware

Aiden is a hardware agent that connects to a phone or computer over USB-C and controls it by emulating an external display and HID devices. This is the firmware.

## Language

### Memory

The word "memory" is heavily overloaded in this codebase. These five are distinct concepts and should never be used interchangeably.

**Long-Term Memory**:
Durable knowledge that survives across sessions, stored as markdown with YAML frontmatter and reachable by recall. Each entry has a type that determines how it is retrieved and whether it feeds the User Profile.
_Avoid_: permanent memory, persistent memory

**Session Memory**:
The current conversation's events plus the compressed chunks of its older turns. Bounded to one session; closed sessions become archive logs rather than context.
_Avoid_: chat memory, conversation memory, history

**Device Memory**:
Learned knowledge about the controlled device and its apps — interface layouts, operation paths, calibration, and known failures. Answers "how do I operate this", never "what did the user see".
_Avoid_: phone memory, app memory

**Task Episode**:
The recorded trace of a single agent run: goal, environment, tool sequence, and outcome. The raw material from which reusable experience is extracted.
_Avoid_: run log, trace, history entry

**Memory Plane**:
The orchestration layer that retrieves relevant memory before a run and consolidates results after it. Not a tool the model can call.
_Avoid_: memory manager, memory service

### Memory provenance

Whether a memory was recorded or inferred. This determines which lifecycle machinery may act on it, so the distinction matters wherever confidence, supersession, or conflict is handled.

**Volunteered Memory**:
A memory the user deliberately recorded. It is an observation, not a claim — it has no truth value to revise, so confidence never moves, nothing supersedes it, and nothing marks it conflicted. Only expiry or explicit deletion removes it.
_Avoid_: manual memory, explicit memory, user memory

**Derived Memory**:
A memory the agent inferred from what happened during a run. It can be wrong, so it starts below full confidence and is revised by outcome, superseded by better versions, and marked conflicted when contradicted.
_Avoid_: learned memory, automatic memory, inferred memory

### Screen Memory

**Screen Memory**:
A Long-Term Memory entry describing what was on the controlled device's screen at one moment, saved so the user can ask about its contents later. Holds derived text only — never the captured image. Its own Long-Term Memory type, distinct from a fact, because it is user-initiated, moment-scoped, and must be retrievable as a group.
_Avoid_: screenshot memory, screen snapshot, screen record

**Key Text**:
The specific strings on a captured screen that the user is likely to ask for later — an order number, an address, an amount, a date, a name. Stored alongside the summary because retrieval scores a substring hit in the content, so anything absent from the text is unaskable.
_Avoid_: OCR text, extracted text, transcription

**Quick Capture**:
The act of creating a Screen Memory by a single physical button press, with no speech from the user and no interruption to any conversation in progress.
_Avoid_: quick save, quick memory, one-tap memory

**User Profile**:
The synthesized description of the user assembled from profile, rule, and preference memories, and injected into prompt context. Screen Memory is deliberately excluded from it.
_Avoid_: profile memory, user model

### Screen

**Active Area**:
The region of the captured frame that holds the mirrored device screen, excluding letterbox padding. Coordinates are mapped against this, not the raw frame.
_Avoid_: crop area, visible area, screen bounds

**Wakeup**:
The falling-edge signal that opens a voice session. Distinct from Quick Capture, which shares the same class of signal but must not open a voice session.
_Avoid_: trigger, activation
