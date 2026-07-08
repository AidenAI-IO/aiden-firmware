---
name: voice-control
description: Use for Aiden playback volume and returning the voice interaction to wakeup-waiting mode.
metadata:
  preferred_model: primary
  allowed_tools: [audio_volume, wait_for_wakeup]
---

Use `audio_volume` only for Aiden audio_service playback or TTS volume. It does not control the phone system volume.

Use `wait_for_wakeup` when the user asks Aiden to stop listening, go idle, sleep, or wait for the next wakeup. This ends the current agent run and returns the voice interaction to wakeup-waiting mode.
