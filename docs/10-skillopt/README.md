# SkillOpt Workflow

## Positioning

SkillOpt is an independent developer tool for optimizing `SKILL.md` content. It is not a benchmark suite discovery mode, and benchmark WebUI/runner discovery should not expose SkillOpt-owned suites as normal benchmark suites.

The dependency direction is:

```text
SkillOpt -> benchmark runner APIs -> environment bridge -> device/MobileGym
```

Benchmark code should stay generic: it runs suites and produces reports. SkillOpt calls those capabilities during its own optimization loop by launching child benchmark runs for each rollout phase.

## Usage

Run SkillOpt manually against an existing Aiden daemon from the repo root:

```bash
uv run --project skillopt python -m skillopt \
  --skill device-operator \
  --backend device \
  --train-suite skillopt/device-operator/device_operator_train \
  --validation-suite skillopt/device-operator/device_operator_verification \
  --budget 10 \
  --output /tmp/device-operator-optimized.md
```

Run SkillOpt through the current benchmark runner architecture by providing an environment bridge. This is the preferred path for MobileGym and bridge-backed physical devices because it reuses benchmark task setup, screenshots, isolated daemon workers, reports, and bridge concurrency:

```bash
uv run --project skillopt python -m skillopt \
  --skill device-operator \
  --backend mobilegym \
  --environment-url http://127.0.0.1:50196 \
  --no-build-daemon-image \
  --train-suite skillopt/device-operator/device_operator_train \
  --validation-suite skillopt/device-operator/device_operator_verification \
  --budget 10 \
  --output /tmp/device-operator-optimized.md
```

Start the standalone SkillOpt WebUI on its own port:

```bash
uv run --project skillopt python -m skillopt webui --host 127.0.0.1 --port 8766
```

The SkillOpt WebUI can start optimization jobs and optionally read running MobileGym environments from the benchmark WebUI at `http://127.0.0.1:8765`. It still executes rollouts through the same benchmark runner bridge path described above.

Review the generated skill before applying it:

```bash
diff src/agent/config/skills/device-operator/SKILL.md /tmp/device-operator-optimized.md
```

SkillOpt-owned suites live under `skillopt/suites`. Benchmark suites can still be used by passing their benchmark suite label to `--suite`; that is SkillOpt calling benchmark runner functionality, not a benchmark feature.

## Boundary

- Benchmark WebUI lists benchmark suites only.
- SkillOpt WebUI is a separate process, defaulting to port `8766`.
- SkillOpt bridge-backed runs use benchmark runner child runs, not the deprecated standalone MobileGym backend.
- SkillOpt-owned suites and reports are managed by SkillOpt, even when their tasks are executed through benchmark runner functions.
