# Claude visual coordinates: first implementation

Claude agent runs now prepare image attachments before the model request and
accept image pixel coordinates in visual tools. Device and HTTP tools retain
their normalized protocol. No separate normalization tool call is required.

## Provider selection

The runtime uses the actual model spec to select the Claude/Anthropic path.
Other providers keep their existing behavior. There is no user configuration
switch: this is a fixed compatibility adapter for Anthropic screenshot input.
The preparation limits are implementation constants, not inferred provider
resize dimensions. Images are never enlarged; model adherence to the displayed
coordinate contract still requires live evaluation.

## Data path

Only attachments tagged as device screenshot observations participate; ordinary
user uploads pass through unchanged. The outbound transform runs after screenshot
pruning. It reads the original
attachment, decodes its actual dimensions, downsamples it, and attaches a caption
with the prepared dimensions and an opaque frame ID. Prepared bytes and captions
exist only on outbound message clones; stored source attachments remain intact.
Replay regenerates the same frame within a run. Frames from another run are not
accepted. Prepared images are cached for attachments still present in context.

`touch_gesture`, `mouse_move`, `enter_text.focus`, and `wheel_nudge` geometry use
the pixel protocol in the conversational tool schema. Standard and atomic touch
points are converted; speed and timing retain their existing units. A pixel
center coordinate ranges from zero to dimension minus one. Conversion to the
existing normalized plane is `pixel / max(dimension - 1, 1) * 1000`.

```json
{"type":"tap","frame_id":"frame_...","point":{"x":300,"y":600}}
```

The frame adapter only resizes the captured image. Existing active-area cropping
and HID/ADB mapping continue to own the device transform, avoiding a second crop
offset. A frame ID identifies the dimensions used for conversion, not permission
to perform an action. The adapter does not consume frames, impose a latest-only
policy, or add device-scope, rotation or resume checks. Existing device tools keep
ownership of all screenshot freshness and operation lifecycle rules. Rebuilding
a run reconstructs coordinate metadata from stored screenshots; old run IDs are
not accepted, but there is no new requirement to capture a screenshot. Unknown
frame IDs and invalid coordinates are rejected before the underlying tool runs.

Each accepted action emits a `visual_coordinate_mapping` episode event with the
frame dimensions, original pixel input, and converted normalized input.

## Automated verification

From `src/agent`:

```sh
go test ./internal/agent -run TestVisualCoordinates -count=1
go test ./internal/agent/... -count=1
```

Tests decode the actual outgoing bytes and compare their dimensions with the
caption, exercise portrait/landscape/square/small images and replay, verify
cross-run ID rejection, repeat use and ordinary-upload exclusion, check nested gestures and wheel geometry,
and run the real agent loop with a scripted model and recording device tool.
The loop test also covers disabling the feature and leaving OpenAI unchanged.
These tests establish deterministic wiring, not Claude perception accuracy.

## Live evaluation still required

An opt-in real-provider harness is available for a controlled first-tap comparison.
It calls the actual Anthropic client with the production prompt, tool schema and
outbound image transform. Its receiving tool captures normalized coordinates,
so scoring is deterministic against the existing suite's target rectangles and
does not depend on the old trace judge. It does not drive a physical device or
exercise the daemon's complete tool catalog. Normal test runs skip this harness.

```sh
# Credentials are read from ANTHROPIC_BASE_URL and ANTHROPIC_AUTH_TOKEN.
AIDEN_VISUAL_LIVE=1 \
AIDEN_VISUAL_ABLATION=1 \
AIDEN_VISUAL_MODEL=claude-opus-5 \
AIDEN_VISUAL_REPEATS=3 \
AIDEN_VISUAL_RESULTS=/tmp/aiden-claude-coordinate-live \
go test ./internal/agent -run '^TestVisualCoordinatesLive$' -count=1 -v -parallel=3 -timeout=65m
```

`AIDEN_VISUAL_TASK=find_settings_iphone` restricts the run to the original failure
case. The ablation flag selects original/resized images crossed with normalized/pixel
coordinates; omit it for the production baseline/prepared comparison.
Each task/variant/repeat writes a JSON result with the model argument,
normalized receiving-tool argument, rubric bounds, latency, usage, errors and hit
flag. A passing Go test means results were collected successfully; use the JSON
`hit` and `error` fields to assess accuracy. Network failures remain in the result
set and are not silently retried. Use a new results directory for each run.

Run the perception suite with a fixed real model ID, provider, prompt and sampling
parameters, comparing the disabled baseline with the prepared-image protocol.
Include held-out resolutions and small edge controls. Report first-attempt target
box hits, normalized center error, protocol failures, latency, and API timeouts
separately. Follow with real HID/ADB tests for taps, swipes, focus and dragging.

The existing perception rubrics inspect model tool inputs as normalized values.
For the new protocol those inputs are pixels: evaluation must instead read the
`normalized_input` in `visual_coordinate_mapping`, or the actual bridge input.
Do not report the old judge result as a comparable accuracy score without that
adaptation. The existing suite has not been modified to hide this distinction.

## First-version limitations

- Frame IDs describe coordinate spaces only. They do not establish physical screen
  freshness; existing device checks continue to handle that. Local magnified crops
  remain unimplemented.
- The model-specific instructions are transient and override older normalized
  examples in skills. Live testing must verify that Claude follows that contract.
- Internal vision calls inside text-entry helpers retain their existing protocol;
  this change covers the main conversational agent loop.
- Prepared image artifacts are not separately persisted. Source attachments and
  mapping events support diagnosis; exact outbound captures use existing model
  request telemetry where enabled.
