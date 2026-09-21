# PR #715: random ten-target baseline, before the fix

**Settled-viewport success: 6/10 (60%).** 4 trials passed the target and failed. Only the before revision was tested in this protocol; the after run awaits the user.

This measures whether scrolling brings a target into the settled viewport. It does not require recognizing it or opening its detail page. Once a target is visible, the trial succeeds immediately. If the first visible item is beyond the target, the trial fails immediately. A transient glimpse during motion does not count. No reverse swipe is executed, and no mutation is allowed after either terminal outcome.

## Frozen sample and setup

- Sample order: 45, 89, 24, 58, 56, 87, 61, 76, 30, 11.
- Uniform sampling without replacement: `random.Random(11570657923826534781).sample(range(1,101),10)`. The seed and sample were recorded before the original random batch and reused unchanged after clarification.
- Before source: `b4281646d4a02acc6a169760096fd44448e26a23` (PR #715 parent).
- MobileGym: `d0549195beccbe08f963bf7758bef3a65fe3ac08` (MobileGym #3, pinned by firmware #703).
- Agent: board configuration from `ssh luckfox`, `deepseek-flash`, response-token limit 1396, no temperature override.
- One fresh Agent process/conversation/memory and simulator reset per target. One attempt per target. Existing sweep task prompts retained; the primary judge is settled visibility.
- Viewport: 360 × 800 CSS pixels, DPR 3. Any positive intersection with the target row counts as visible; exact overlap is saved.
- Same sample, model configuration and judging rule are reserved for the after run. No after binary was started for this phase.

## Every trial

| Target | Result | Settled visible items | Target visible px | Swipes | Seconds | Evidence |
| ---: | --- | --- | ---: | ---: | ---: | --- |
| 45 | Fail (inertia) | 48–55 | 0.0 | 4 | 32.1 | [screen](evidence/before-045-01-0007.jpg), [gesture](evidence/before-045-01-0007.json) |
| 89 | Pass | 86–92 | 98.1 | 8 | 77.7 | [screen](evidence/before-089-01-0015.jpg), [gesture](evidence/before-089-01-0015.json) |
| 24 | Pass | 21–28 | 84.0 | 2 | 26.1 | [screen](evidence/before-024-01-0017.jpg), [gesture](evidence/before-024-01-0017.json) |
| 58 | Pass | 55–62 | 98.1 | 4 | 34.8 | [screen](evidence/before-058-01-0021.jpg), [gesture](evidence/before-058-01-0021.json) |
| 56 | Fail (inertia) | 60–67 | 0.0 | 5 | 40.2 | [screen](evidence/before-056-01-0026.jpg), [gesture](evidence/before-056-01-0026.json) |
| 87 | Pass | 84–91 | 98.1 | 7 | 56.2 | [screen](evidence/before-087-01-0033.jpg), [gesture](evidence/before-087-01-0033.json) |
| 61 | Fail (inertia) | 64–71 | 0.0 | 6 | 44.2 | [screen](evidence/before-061-01-0039.jpg), [gesture](evidence/before-061-01-0039.json) |
| 76 | Pass | 75–82 | 98.1 | 7 | 55.9 | [screen](evidence/before-076-01-0046.jpg), [gesture](evidence/before-076-01-0046.json) |
| 30 | Fail (inertia) | 32–39 | 0.0 | 3 | 24.5 | [screen](evidence/before-030-01-0049.jpg), [gesture](evidence/before-030-01-0049.json) |
| 11 | Pass | 11–17 | 90.9 | 1 | 13.3 | [screen](evidence/before-011-01-0050.jpg), [gesture](evidence/before-011-01-0050.json) |

All ten outcomes are included. There are no selective retries. The 95% Wilson interval is 31.3%–83.2%; ten trials do not establish a precise population success rate or any before/after improvement.

## Inertia evidence

For target 45, the final swipe released at scroll offset 4000 CSS px, before the target bottom at 4399.44. Inertia added 624 px, settling at 4624 with items 48–55 visible. The target was lost after release; the trial failed and its request was canceled. Target 89 settled at items 86–92 and passed without a detail-page click.

![Target 45: failed, settled on items 48–55](evidence/before-045-01-0007.jpg)

The three unscored fixed-path probes moved 480 CSS px during contact. Requested durations 120/300/600 ms produced 1761/422/124 CSS px of post-release coast, confirming inertia was enabled.

## What this experiment does and does not test

The normal MobileGym HTTP provider bypasses the HID implementation changed by #715. This harness captures the unchanged old production HID reports and replays them through a shared final-100-ms velocity estimator and MobileGym’s original Android fling functions. These are simulated HID-report replay results using Mac timing, not unmodified MobileGym HTTP results or measured real-phone success rates. The velocity estimator approximates Android release behavior; it is not a full Android VelocityTracker or an iOS calibration.

**Raw runner scores are secondary diagnostics.** The original suite asks to open a detail page. We cancel as soon as visibility succeeds or overshoot fails, so its final-response/detail-page checks can fail on a visibility success. `comparison.json` and this table contain the requested primary outcome; the raw runner records are retained unmodified for audit.

## Validation and artifact provenance

- Integration probes verified overshoot rejection, immediate visibility success without a click, and blocking rollback/click/reset/direct-tool mutations after termination.
- 48 focused Python tests passed, including specific-request cancellation and normal completion.
- Source check: all 615 tracked Agent files match the before commit; binaries match the earlier frozen build hashes.
- Audit checked all 10 trials, 47 swipes, 1,316 HID reports and 10 terminal cancellations, including geometry and no later swipe or rollback after termination. The 64 shell calls only read saved tool results. Credential scan passed; no board service, runtime binary or configuration was changed.
- The previous 40 fixed-target trials are historical, not pooled here. An intervening ten-target batch was stopped after four completed trials and one interrupted trial when the user clarified that settled visibility alone suffices. It is preserved as superseded; this entire ten-target batch was run afresh under the clarified rule.

Full raw data: `benchmark/runs/pr715-visible10-before-20260921/`. The sibling `.tar.gz` archive and its checksum are local artifacts. Tracked evidence includes all terminal screens, final gestures, cancellations and states.

Reproduction instructions: [harness README](../../../../benchmark/scripts/pr715/README.md).
