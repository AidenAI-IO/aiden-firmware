# Universal Swipe Interaction Design

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

**Conclusion**: Abandon precise quantification, shift to **small-step iteration + visual confirmation** closed-loop strategy.

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

Returns base64 JPEG + width/height. Auto-screenshot after each HID operation (500ms delay), no manual call needed.

### save_memory / recall_memory

Persist cross-session memory for caching widget parameters (see Section V).

---

## III. New Tool: image_diff

**Only tool that needs to be added**, pure computation, no LLM involved, used to quickly determine if swipe was effective.

### Interface

```text
Input:
  before:    string  — base64 JPEG (data field of pre-swipe screenshot)
  after:     string  — base64 JPEG (data field of post-swipe screenshot)
  region:    object  — Optional, {x, y, w, h}, 0-1000 normalized coordinates, limits comparison area

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
5. **Maximum 10 retries**, report failure after exceeding, don't loop infinitely

### Picker / Wheel

Typical scenarios: time picker, date picker, city picker.

```text
Strategy:
1. Screenshot, recognize picker current value and target value
2. Determine direction (target value > current value → swipe up; otherwise down)
3. Execute swipe:
   - distance: 30 (about 1 notch)
   - duration_ms: 400, steps: 16 (slow speed, reduce inertia)
   - hold_after_ms: 100 (pause before lifting, suppress iOS inertia)
4. Wait for screenshot, read new value
5. If image_diff.changed=false → increase distance to 50, retry
6. If reached target → end
7. If overshot → reverse swipe 1 notch, reconfirm
```

**Key parameters**: Slow speed (duration_ms=400) + hold_after_ms=100 is the most effective combination for suppressing iOS inertia, more reliable than anti_overshoot reverse micro-movement.

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
| Dedicated scroll_picker tool | touch_gesture swipe already sufficient, difference is in prompt strategy |
| OCR tool (current phase) | Not needed when LLM visual value reading accuracy > 90%, can add as needed later |

---

## VII. Engineering Change List

| File | Change Type | Description |
|------|----------|------|
| `src/agent/internal/agent/tools_image_diff.go` | New | ImageDiffTool implementation |
| `src/agent/internal/agent/tools.go` | Modify | Register image_diff |
| `src/agent/internal/agent/prompt.go` | Modify | Add swipe strategy guidance |
