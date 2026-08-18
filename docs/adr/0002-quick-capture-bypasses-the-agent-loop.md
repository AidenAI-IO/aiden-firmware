# Quick Capture bypasses the agent loop

Quick Capture runs as a standalone pipeline — grab a frame, make one vision model call, write the memory — instead of injecting a synthetic input into `Runtime.Run` and letting the agent call the `screenshot` and `save_memory` tools itself. Reusing the agent loop looks cheaper, but `Runtime.Run` begins by preempting the active run, and the audio mode's preempt hook stops playback and recording. A button press during a conversation would therefore kill that conversation. Quick Capture is also deterministic: there is nothing for the model to decide, so paying for tool-loop latency and risking the model simply not calling `save_memory` buys nothing.

## Consequences

- Quick Capture does not appear in conversation history or as a Task Episode. It is not a turn.
- Duplicate detection via `DecideAction` is skipped; each press writes a new entry. Two presses on the same screen produce two memories.
- The pipeline holds a `model.Model` and calls it directly, following `LLMSkillMergeModel` as precedent.
- A capture and a conversation can now hit the vision model concurrently, which was not previously possible.
