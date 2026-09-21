# PR #715 Scroll Comparison

## Random ten-target protocol (current)

The user's corrected protocol samples ten distinct targets uniformly from all
100 Scroll Lab items, once before either arm runs. Preserve the seed and ordered
sample in `manifest.json`; never choose a replacement sample after seeing results.
Run each selected task once, with its existing prompt and 240-second timeout.
The primary outcome is visibility after a settled swipe; the original runner's
detail-page assertions are retained as secondary diagnostics only.
Only the **before** arm is currently authorized; wait for the user before the
after experiment. The frozen sample is 45, 89, 24, 58, 56, 87, 61, 76, 30, 11
(seed `11570657923826534781`).

```bash
.venv/bin/python scripts/pr715/run.py \
  --mobilegym-root /path/to/pinned-mobilegym \
  --out /path/to/new-results-directory \
  --arms before --random-count 10 --seed 11570657923826534781 \
  --repeats 1 --visibility-goal
```

After the full contact and inertial motion has settled, the visibility mode
checks the rendered list viewport. Any positive intersection with the target
row is success; a first visible ordinal beyond the target is failure. A target
that flashed past during motion but is gone at rest fails. Both outcomes are
terminal: all subsequent mutations (including rollback, clicks and same-trial
reset) return HTTP 409. State/screenshot reads remain available for evidence.
Reverse swipe attempts before a terminal outcome are also blocked and fail.
An experiment-local runner hook observes the terminal file and cancels the
specific Agent request, preserving its trace and the frozen final state. The
Agent need not open the detail page. Cancellation therefore makes the original
runner's final-response/detail-page checks fail even on primary visibility
success; use the derived visibility result, not that raw score.
Internal target/crossing data is saved only as judge evidence, never returned
to the Agent. The model receives the unchanged task prompt.

Before formal trials, verify termination with `--arms before
--visibility-goal --probe-only`: a 120 ms old-version swipe passes target 5 during
the fling and fails after settling, then rollback, click, direct tool and reset all return 409
without changing the route or Scroll Lab state. These probes are excluded from
the ten-trial denominator. A 300 ms swipe leaves item 12 visible and immediately
passes without a detail click. Unit tests also verify request-specific cancellation
and normal completion. No after-version probes run in this baseline-only phase.

All sampled targets stay in the denominator, including overshoots, blocked
rollback attempts, timeouts and other failures. The earlier fixed-target run
below remains historical evidence and is not pooled with this protocol.
An intervening random-target batch was stopped when the user clarified that
visibility alone suffices. Its four completed trials and interrupted fifth
trial are preserved as superseded, excluded from this baseline. The entire
same ten-target sample is run afresh; no failures are selectively rerun.

## Original fixed-target protocol (historical)

The original experiment compares the two tasks from
`benchmark/suites/mobilegym_scroll_regression.json` using the active DeepSeek
account/model configuration read from `ssh luckfox`. Credentials are kept in
temporary directories with mode 0700 and configuration files with mode 0600;
the manifest contains no API key. The board's running services are not changed.

## Frozen Inputs

- Before: `b4281646d4a02acc6a169760096fd44448e26a23`, the parent of PR #715.
- After: `3e9eac9ecd425b4f5e2f1279e0f527d0ec6a85d2`, PR #715 head.
- MobileGym: `d0549195beccbe08f963bf7758bef3a65fe3ac08`, the merged MobileGym #3
  commit pinned by firmware #703.
- Targets: item 24 and item 83. Task prompts, routes, target IDs, the 40-call
  limit, the 240-second limit, and environment assertions remain unchanged.
- Ten repetitions per target and revision, 40 formal trials in total.
- Each before/after pair runs concurrently. Target order alternates between
  repetitions. Each trial receives a fresh Agent process, conversation, memory,
  skill state, and simulator reset. The revision's own bundled skills are used.
- No LLM judge: the existing deterministic route, selected-item, high-water,
  elapsed-time, and tool-count assertions determine success.

The task does **not** physically prohibit reverse gestures. Its monotonic
`maxFirstVisibleOrdinal` assertion makes passing the target irreversible for
scoring. Opening the right item after scrolling back is still a failure.

## Why HID Replay Is Necessary

The default MobileGym bridge replaces the Go HID provider with an HTTP provider.
PR #715 changes the HID implementation and tool/skill guidance, but not the HTTP
provider. Running two ordinary daemon versions against the standard bridge would
therefore test guidance alone and omit the implementation being evaluated.

`benchmark-hid-capture` calls the production `HIDProvider.SwipeWithOptions` from
each revision with a recording `Device`. It saves every emitted touchscreen HID
report and its measured timestamp, including the first contact release.
`bridge.py` intercepts only MNK swipes and replays those reports; screenshots,
taps, resets, state collection, the Agent, and scoring use the repository's
existing implementations. The Agent receives screenshots and normal tool
results, never the benchmark's internal target/scroll state.

MobileGym #3 estimates release speed as total travel / total gesture duration,
which cannot represent a changing speed profile. `replay.js` instead estimates
release speed over the final 100 ms of the captured reports, interpolating the
window boundary. Both revisions use exactly the same estimator and the original
MobileGym `androidFlingDistance`, `androidFlingDurationMs`, and
`androidFlingProgress` functions. Inertia is enabled in both arms. Scroll
coordinates accumulate from the initial position to avoid per-event rounding
loss. No profile name or revision flag enters the physics calculation.

These are **simulated HID-report replay results**, not an unmodified HTTP
MobileGym benchmark or real-device success rates. Timing is measured on the Mac,
not through board USB polling. The finite-window estimator approximates release
velocity; it is not Android's full VelocityTracker or an iOS calibration.

## Reproduction

Use isolated worktrees for both firmware revisions and the pinned MobileGym
commit. The after tree can also be made by applying the two PR commits to the
before tree; verify that its tracked tree equals the PR head. Do not edit the
production HID implementation for this experiment.

Copy the identical `src/agent/cmd/benchmark-hid-capture/main.go` helper into each
firmware worktree. From each worktree's `src/agent` directory, build:

```bash
# Before worktree
go build -o /tmp/pr715-before-agent ./cmd/daemon
go build -o /tmp/pr715-before-hid ./cmd/benchmark-hid-capture

# After worktree
go build -o /tmp/pr715-after-agent ./cmd/daemon
go build -o /tmp/pr715-after-hid ./cmd/benchmark-hid-capture
```

Prepare MobileGym with `npm ci --ignore-scripts --no-audit --no-fund`. From the
experiment worktree's `benchmark` directory:

```bash
uv sync
uv pip install tomli-w
.venv/bin/python scripts/pr715/run.py \
  --mobilegym-root /path/to/pinned-mobilegym \
  --fixed-root /path/to/after-firmware \
  --out /path/to/new-results-directory \
  --repeats 10
.venv/bin/python scripts/pr715/summarize.py /path/to/new-results-directory
```

The output directory must not already exist. The script starts its own local
Vite and bridge servers on unused ports and closes all owned processes on exit.
The board must be SSH-accessible and its active provider must be DeepSeek.

Before any model trial, each arm performs the same 480 CSS-pixel swipe at
requested durations of 120, 300, and 600 ms. These mechanical sanity probes are
saved in `probes.json` and are not part of the success-rate denominator. The
four successful pilot trials on 2026-09-21 are also excluded from the formal run.

The only runtime configuration changes are removal of the obsolete
`input_mode=text`, disabling voice side effects and raw HTTP logging, and using
isolated local storage. Model name, provider endpoint, response token limit,
and other model settings are copied from the board. No temperature is added.

## Artifacts

- `manifest.json`: exact input revisions, model settings, suite digest, binary
  and harness digests, sample size, limitations, and start time.
- `runs/<arm>-<target>-<repeat>/`: the original runner's result, history, tool
  trace, pre/post screenshots, final environment state, and HTML report.
- `<arm>/hid/<trial>-*.json`: each swipe request, raw reports/timestamps,
  release velocity, coast distance, and post-swipe state.
- `<arm>/hid/<trial>-*.jpg`: post-swipe screenshots.
- `comparison.csv`, `comparison.json`, `comparison.md`: derived trial-level
  data and grouped success rates, overshoots, latency, and tool counts.

Wilson intervals describe binomial sampling uncertainty. Fisher's two-sided
test is reported descriptively for the fixed task mixture; repeated trials of
two fixed targets do not measure generalization to arbitrary apps or lists.
All formal outcomes, including timeouts and recovered overshoots, remain in
the planned denominator. Do not rerun only failed trials or pool pilot results
into the formal comparison.

## Supplemental Information Coverage

The original suite only checks overshooting the requested target. It can pass
even if a fling skipped intermediate rows. `coverage.py` measures this separate,
post-hoc diagnostic using the pinned Scroll Lab's rendered row geometry and the
saved initial/post-swipe viewport positions. A row counts as available if even
one pixel intersects any settled list viewport. A wholly unseen row has no
intersection with any such viewport. This is a conservative measure of missing
visual information, not proof that the model read the visible rows. Frames that
passed transiently during motion are not screenshots observed by the Agent.

This metric was added after the first 12 formal trials. It does not change the
suite, prompts, trial scheduling, sample size, or original success denominator.
It must be labeled exploratory rather than presented as a predeclared primary
endpoint. For the same rendering dimensions used by the trial bridge, collect
the geometry once from an independent browser page while Vite is running:

```bash
.venv/bin/python scripts/pr715/coverage.py /path/to/results \
  --mobilegym-root /path/to/pinned-mobilegym \
  --env-url http://127.0.0.1:<vite-port>
```

Subsequent invocations use the saved `row-geometry.json` without a browser.
`coverage.json` lists every unseen row ordinal for every trial. Review any listed
unsupported tool calls before interpreting a trial's coverage.

After all trials finish, audit the frozen binaries/harness/suite, raw HID report
decoding, per-trial resets, monotonic high-water marks, successful final states,
full planned trial coverage, and absence of the board's credential values:

```bash
.venv/bin/python scripts/pr715/audit.py /path/to/results --check-board-credentials
```

To export the figure after generating `comparison.json` and `coverage.json`:

```bash
uv pip install matplotlib
.venv/bin/python scripts/pr715/plot.py /path/to/results
```

The PNG and SVG distinguish the original pass rate from exploratory row coverage
and include all trial durations, including timeouts.
