# Quick Capture is triggered by a long press, not a double press

Quick Capture needs its own trigger on hardware whose only two GPIO signals — one from the physical button, one from the ASRPro wake-word module — are both bound to Wakeup. A double press was considered and rejected: detecting one requires waiting 300-400ms after the first edge to see whether a second arrives, which delays _every_ Wakeup by that window. Voice interaction is the product's core scenario, so spending core-path latency on a secondary feature is the wrong trade. A long press decides at release instead — a short press dispatches Wakeup with zero added latency, and holding past the threshold dispatches Quick Capture.

## Consequences

- Only the button pin gets duration measurement and switches to both-edge listening. The wake-word pin keeps falling-edge Wakeup untouched: its signal is a pulse that cannot be held, so timing it is meaningless and a pulse wider than the threshold would fire Quick Capture from a spoken wake word. This asymmetry is the point, not an omission.
- The existing 500ms wakeup debounce sits on the falling edge and would collapse the press/release pair a gesture needs; it has to be reworked for the button pin regardless of which gesture wins.
- Gesture recognition is factored into its own layer (GPIO edge → gesture → action) so that switching to double press, or giving Quick Capture a dedicated pin, changes only that layer and leaves the Quick Capture pipeline untouched.
- Everything stays inside the agent binary: GPIO is driven through sysfs, so no kernel, device-tree, or partition change is involved and no full firmware reflash is needed.
- A long press needs feedback at the moment the threshold is crossed, otherwise the user cannot tell when to release.
