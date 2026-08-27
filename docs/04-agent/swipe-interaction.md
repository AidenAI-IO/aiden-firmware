---
sidebar_position: 15
---

# Swipe Interaction

Aiden controls phone scrolling and picker wheels through USB HID while observing the result through HDMI screenshots. Because it does not receive native accessibility or scroll events, every gesture must be verified from the visible screen.

## Constraints

| Capability | Aiden behavior |
| --- | --- |
| Touch confirmation | Infer success from the screenshot returned after the action. |
| Widget value reading | Read visible values from the screen; no native picker value API is available. |
| Scroll boundary detection | Compare before/after screenshots and inspect the resulting page. |
| Inertia control | Use bounded gestures and re-observe instead of assuming an exact displacement. |

JPEG noise, animation frames, and repeated content make exact pixel displacement unreliable. Treat scrolling as an iterative observe-act-verify loop.

## Tools

### `touch_gesture`

`touch_gesture` accepts an atomic action program. Actions execute in order in one HID session, so a contact remains down across waits and moves:

```json
{
  "actions": [
    {"action": "touch_down", "point": {"x": 500, "y": 700}},
    {"action": "wait", "ms": 80},
    {"action": "move_to", "point": {"x": 500, "y": 300}, "speed": 2500},
    {"action": "touch_up"}
  ]
}
```

The action vocabulary is deliberately small:

- `touch_down`: starts a contact and requires `point`.
- `move_to`: moves to `point`, preserving the current contact state. Optional `speed` uses normalized coordinate units per second and derives movement time from the preceding point. `duration_ms` overrides `speed`; omitting both keeps the existing immediate move.
- `wait`: waits for `ms` milliseconds without changing contact state.
- `touch_up`: releases the current contact; `point` is optional.

Coordinates use the normalized `0..1000` range. A program must contain at least one action and must end with `touch_up`; waits are bounded to 30 seconds and programs to 128 actions. The legacy one-object `type` form remains accepted for existing scripts, but new integrations should use the atomic form.

On Android ADB backends, the provider discovers the physical touchscreen and its absolute coordinate range with `getevent -lp`, then emits one `sendevent` program that preserves the contact across `wait` and `move_to`. This requires the Android shell user to have write access to the selected `/dev/input/event*` device. When device permissions or SELinux prohibit raw injection, the provider falls back to Android's `input touchscreen motionevent DOWN|MOVE|UP` primitive; if that is also unavailable, atomic actions return `module_unavailable` and HID remains the fallback.

Use the atomic form for ordinary lists, carousels, maps, and other free-scrolling surfaces:

```json
{
  "actions": [
    {"action": "touch_down", "point": {"x": 500, "y": 650}},
    {"action": "move_to", "point": {"x": 500, "y": 350}},
    {"action": "touch_up"}
  ]
}
```

`speed` is optional and defaults to `2500` normalized coordinate units per second. A swipe accepts either `start` + `end`, or `start` + `direction` (`up`, `down`, `left`, `right`). `duration_ms` is optional: with an explicit `end` it overrides the calculated timing; with a `direction` it determines travel as `speed * duration_ms / 1000`. Without a duration, a directional swipe travels to the corresponding screen edge. `hold_before_ms` and `hold_after_ms` optionally add dwell after press and before release (default 0), while `steps` optionally controls HID interpolation (default 24). For example, `{"type":"swipe","start":{"x":500,"y":800},"direction":"up","speed":2500,"duration_ms":300}` ends at `{"x":500,"y":50}`.

Normalized coordinates use a `0..1000` range on each axis. HID action tools return a post-action screenshot after the screen settles.

Do not use `touch_gesture`, mouse clicks, or keyboard input to change an active picker wheel. Use `wheel_nudge` for the entire picker interaction.

### `image_diff`

`image_diff` compares two JPEG screenshots. Agent calls use screenshot attachment IDs from the current context; direct HTTP calls may pass Base64 JPEG data.

```json
{
  "before": "<before-screenshot-attachment-id>",
  "after": "<after-screenshot-attachment-id>",
  "region": {"x": 100, "y": 150, "w": 800, "h": 700}
}
```

Output fields:

| Field | Meaning |
| --- | --- |
| `changed` | `true` when more than 1% of compared pixels changed significantly. |
| `diff_ratio` | Fraction of compared pixels that changed, from `0` to `1`. |
| `primary_axis` | Dominant change axis: `horizontal`, `vertical`, or `none`. |

Use a region to exclude static navigation bars or overlays. A `diff_ratio` below about `0.03` is weak evidence of movement and should be followed by another visual check.

`image_diff` deliberately does not report exact scroll distance. The screenshot artifacts that are acceptable for change detection are not reliable enough for precise displacement measurement.

### `wheel_nudge`

`wheel_nudge` performs one bounded interaction inside a visible numeric picker column. It requires a fresh screenshot from the current Agent run.

```json
{
  "picker_id": "alarm-create",
  "column_x": 393,
  "current_value": 10,
  "target_value": 16,
  "cycle_size": 24,
  "cycle_start": 0,
  "row_spacing": 39,
  "value_step": 1,
  "center_y": 253
}
```

All geometry uses normalized `0..1000` coordinates:

- Normalize `column_x` using screenshot width.
- Normalize `center_y`, `row_spacing`, and `visible_target_y` using screenshot height.
- `cycle_size` is the numeric modulus, not the number of visible rows. A `00..59` minute wheel stepping by five still uses `cycle_size: 60` and `value_step: 5`.
- `value_step` is the signed numeric change represented by one visible row downward.
- Use `visible_target_y` only when the target is visibly one adjacent row above or below the selected row.

The runtime derives the shortest reachable row gap and gesture direction. It also measures repeated row geometry from the latest screenshot and may replace an inaccurate `row_spacing` estimate when confidence is sufficient.

## Picker Workflow

1. Capture a screenshot and identify the picker, active column, selected value, target value, and selected-row center.
2. Read the visible row order to determine `value_step`. If the order is genuinely unknown, omit `value_step` for one probe.
3. Call `wheel_nudge` with the latest values and geometry.
4. Read the returned screenshot and update `current_value` from what is visibly centered.
5. Continue from the new observation until the target is centered or the safety policy stops the run.

The run-scoped safety policy:

- requires fresh screenshot evidence;
- limits per-column and total wheel actions;
- checks that observed movement agrees with the declared `value_step`;
- blocks generic taps or drags on a picker column once `wheel_nudge` owns it;
- stops repeated attempts that make no progress.

## Ordinary Scrolling

### Lists

1. Prefer a visible search field over blind scrolling.
2. Use a moderate swipe for exploration.
3. Inspect the returned screenshot or use `image_diff` on the scrollable region.
4. Switch to a shorter swipe when the target approaches the viewport.
5. Stop when the screen no longer changes or a visible boundary is reached.

### Horizontal carousels

Swipe within the carousel rather than across global navigation or system gesture areas. Confirm the page or selected item changed before continuing.

### Maps and canvases

Use short pans, re-observe the viewport, and adjust direction iteratively. Screenshot interpretation is more useful than global image-diff ratios for these surfaces.

## Reusing Stable Parameters

When a widget has been operated successfully across repeated observations, the Agent can save a procedure memory containing the app, page, picker location, row spacing, and effective interaction pattern. Recalled values are hints only: current screenshot geometry always takes precedence.

## Operational Boundaries

- Visual reading can misidentify similar picker values; verify the centered result.
- Exact scroll distance is not available.
- Layouts can change across device size, language, OS version, and app version.
- A successful gesture does not prove the task succeeded; verify the resulting page or value separately.
