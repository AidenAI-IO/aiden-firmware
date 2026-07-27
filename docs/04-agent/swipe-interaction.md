# Universal Swipe Interaction Design

> Implementation update (2026-07): picker testing on physical devices showed
> that generic `touch_gesture` swipes could reverse direction, fling past the
> target, or activate editable fields below a picker. Picker interaction now
> uses the dedicated, run-scoped `wheel_nudge` policy described below. The
> earlier prompt-only strategy remains applicable to ordinary scrolling, not
> wheel controls.

## I. Challenges and Reality Boundaries

The current architecture simulates touch via USB HID absolute coordinate mouse and obtains visual feedback via HDMI screenshots. Compared to native test frameworks (iOS XCTest, Android UIAutomator), it lacks key feedback channels:

| Capability | Native Framework | Current Architecture |
|------|----------|----------|
| Touch event confirmation | ✅ AccessibilityEvent | ❌ No |
| Widget value reading | ✅ AXPickerWheel | ❌ Visual recognition only |
| Scroll boundary detection | ✅ Event callback | ❌ Image diff only |
| Inertia control | ✅ Precise MotionEvent | ⚠️ Parameter tuning, uncertain |

**Core constraints**:

- iOS/Android inertial scrolling is system-level behavior; parameters vary by device/version/widget implementation (native/Flutter/React Native), cannot be precisely controlled
- Image diff can determine "whether it moved", but **cannot reliably quantify "how much it moved"** (JPEG compression noise, animation intermediate frames, similar content interference)
- LLM visual value reading has errors and cannot be the sole basis, but is viable as final confirmation method

**Conclusion**: Ordinary scrolling avoids precise displacement claims and uses **small-step iteration + visual confirmation**. Picker wheels additionally use measured adjacent-row spacing only to bound each `wheel_nudge`; every step still requires a fresh visual observation.

---

## II. Existing Capabilities (No Changes Needed)

### touch_gesture Tool

Supports `type: "swipe"` with complete parameters:

```json
{
  "type": "swipe",
  "start": {"x": 500, "y": 600},
  "end":   {"x": 500, "y": 400},
  "duration_ms": 400,
  "steps": 16,
  "hold_before_ms": 80,
  "hold_after_ms": 100
}
```

Defaults: 700ms / 24 steps / hold_before 80ms / hold_after 0ms. Coordinate system prioritizes `normalized` (0-1000, center at `500,500`).

### screenshot Tool

Returns `previous_screenshot_id` + `screenshot_id` + base64 JPEG + width/height. Auto-screenshot after each HID operation (500ms delay), no manual call needed.

### save_memory / recall_memory

Persist cross-session memory for caching widget parameters (see Section V).

---

## III. New Tool: image_diff

**Only tool that needs to be added**, pure computation, no LLM involved, used to quickly determine if swipe was effective.

### Interface

```text
Input:
  before_id: integer — screenshot_id captured before the UI action
  after_id:  integer — screenshot_id returned by the UI action's post-action screenshot
  region:    object  — Optional, {x, y, w, h}, 0-1000 normalized coordinates, limits comparison area

The tool resolves the two IDs from the Agent's recent local screenshot state.
Screenshot Base64 is not passed back through the tool protocol. If the IDs no
longer match the retained pair, the tool returns an error instead of comparing
the wrong screenshots.

Output:
  changed:       bool    — diff_ratio > 0.01
  diff_ratio:    float   — Changed pixel ratio (0-1)
  primary_axis:  string  — "horizontal" | "vertical" | "none"
```

### Use Cases

- `changed: false`: Swipe had no effect, need to increase distance or check widget position
- `diff_ratio < 0.03`: Very small change, possibly inertia hasn't stopped, wait and retry
- `primary_axis`: Helps determine if swipe direction matches expectation

### Fields Not Output

`shift_y_normalized` (quantified displacement) not output. Reason: JPEG compression introduces block artifacts that pollute row brightness curves, making cross-correlation results unreliable; giving wrong displacement amounts is more dangerous than not giving any.

---

## IV. Swipe Strategies by Scenario

### General Principles

1. **Wait for screenshot confirmation after each swipe**, don't swipe blindly multiple times
2. **Prioritize small steps** (distance ≤ 50), increase after confirming effectiveness
3. **Use image_diff to determine "whether it moved"**, diff_ratio < 0.03 means no effect
4. **Iteration is more reliable than precision**, don't try to get it right in one swipe
5. **Maximum 10 retries for generic swipe calibration**, report failure after exceeding. `wheel_nudge` instead derives a per-column allowance from the initial remaining gap, with bounded per-action travel and a hard run-level ceiling.

### Picker / Wheel

Typical scenarios: time picker, date picker, city picker.

```text
Strategy:
1. Screenshot, recognize picker current value and target value
2. Read visible row ordering, selected value, target value, an estimated row spacing, and the column center
   - Convert all wheel geometry to normalized 0-1000 coordinates before calling `wheel_nudge`; use `max(screenshot width - 1, 1)` for `column_x`, but `max(screenshot height - 1, 1)` for `center_y`, `row_spacing`, and `visible_target_y`; `center_y` is required and must be measured from the selected row in the latest screenshot; the model-facing wheel contract does not expose a coordinate-space selector
   - For stepped cyclic wheels, `cycle_size` is the numeric modulus rather than visible row count (for example `00..59` by fives still uses `cycle_size:60`, `value_step:5`)
   - If row ordering is unknown, omit `value_step` for the single-row probe
3. Call `wheel_nudge` directly. Do not tap the selected row to expose keyboard/edit mode, and do not use `keyboard_text` or `keyboard_tap` to change picker values
4. `wheel_nudge` validates that the target is reachable by `value_step`, requires a screenshot captured during the current Agent run, measures repeated text-row geometry, overrides an inaccurate row-spacing estimate when confidence is sufficient, and derives the shortest direction and row gap internally. Confident image measurement enables at most 6/4/3/2/1 measured rows for gaps of 9+/5-8/3-4/2/1; low-confidence fallback remains at 5/3/2/1
5. When the target is an actually visible adjacent row, pass its exact `visible_target_y`; otherwise omit it and use the bounded drag path
6. Wait for the returned screenshot and read the new centered value
7. Continue from the new observation; if no movement occurred, retry one micro probe rather than repeating a large gesture
8. Stop when the target is centered or the run-scoped safety policy reports no progress
```

**Key parameters**: callers provide their best `row_spacing` estimate from the latest screenshot. Runtime uses a low-latency horizontal-gradient/autocorrelation measurement when confident and falls back to that estimate otherwise. Confident measurements unlock the faster bounded profile, and wheel drags briefly dwell at the final HID coordinate before release so the phone processes the complete intended travel. Usage is committed only after a successful tool result.

### List Scrolling

Typical scenarios: contacts list, settings list, message list.

```text
Strategy:
1. Screenshot, check for search box → prioritize search, avoid blind scrolling
2. When no search:
   - Coarse scroll: distance=350, duration_ms=500
   - Use image_diff to confirm scroll occurred (diff_ratio > 0.05)
   - Screenshot to determine if target is visible
3. Target visible but needs fine-tuning: distance=50
4. image_diff.changed=false → reached boundary, stop
```

### Horizontal Carousel / Tab Switching

Typical scenarios: image browsing, tab page switching, onboarding pages.

```text
Strategy:
- distance: 400 (don't use < 300, easily recognized as accidental touch and bounces back)
- duration_ms: 400
- Screenshot confirms page switch (diff_ratio usually > 0.3)
- If switch unsuccessful, check if in correct swipeable area
```

### Map / Canvas Panning

Typical scenarios: map navigation, image editing.

```text
Strategy:
- Estimate target direction, start with distance=200
- Screenshot to confirm if target entered viewport, iterate and adjust
- No need for image_diff, directly judge from screenshot
- Use distance=50 for fine positioning
```

---

## V. Widget Parameter Caching

Implemented via existing `save_memory`, LLM autonomously decides storage timing, no new tool needed.

### Storage Format

```json
{
  "type": "procedure",
  "title": "[AppName] [Widget name] swipe parameters",
  "content": "app: WeChat, screen: time picker, picker: hour column, effective distance: 35, hold_after_ms: 100, center position: x=500 y=380",
  "tags": ["swipe", "picker", "calibration"],
  "entities": ["WeChat"],
  "priority": 70
}
```

### Trigger Timing

- **Store**: After first successful operation of a swipe widget, actively save_memory
- **Retrieve**: When encountering similar widget, first recall_memory to check for cached parameters; if yes, use directly, skip exploration phase

---

## VI. Things Not Done

| Solution | Reason for Abandoning |
|------|----------|
| Image diff quantify displacement (shift_y_normalized) | JPEG noise + animation frames + similar content, unreliable results |
| Adaptive step calibration tool | LLM + save_memory already sufficient, no need for dedicated tool |
| anti_overshoot reverse micro-movement | Empirical parameters, large effect differences across devices/versions |
| Prompt-only picker control | Superseded after physical-device testing; dedicated `wheel_nudge` prevents generic touch fallbacks, bounds travel, and validates observed direction/progress |
| OCR tool (current phase) | Not needed when LLM visual value reading accuracy > 90%, can add as needed later |

---

## VII. Engineering Change List

| File | Change Type | Description |
|------|----------|------|
| `src/agent/internal/agent/tools_image_diff.go` | New | ImageDiffTool implementation |
| `src/agent/internal/agent/tools.go` | Modify | Register image_diff |
| `src/agent/internal/agent/tools_hid.go` | Modify | Add bounded `wheel_nudge` picker interaction |
| `src/agent/internal/agent/wheel_gesture_guard.go` | New | Add run-scoped wheel progress, budget, and generic-input policy |
| `src/agent/internal/agent/prompt.go` | Modify | Add swipe strategy guidance |
