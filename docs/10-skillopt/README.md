# SkillOpt Workflow

## Positioning

SkillOpt is an independent developer tool for optimizing `SKILL.md` content. It is not a benchmark mode, and benchmark WebUI/runner discovery should not expose SkillOpt runs, suites, or reports.

The dependency direction is:

```text
SkillOpt -> benchmark runner APIs -> environment/agent
```

Benchmark code should stay generic: it runs suites and produces reports. SkillOpt may call those capabilities during its own optimization loop.

## Usage

Run SkillOpt manually from the repo root:

```bash
uv run --project skillopt python -m skillopt \
  --skill device-operator \
  --backend device \
  --train-suite skillopt/device-operator/device_operator_train \
  --validation-suite skillopt/device-operator/device_operator_verification \
  --budget 10 \
  --output /tmp/device-operator-optimized.md
```

Review the generated skill before applying it:

```bash
diff src/agent/config/skills/device-operator/SKILL.md /tmp/device-operator-optimized.md
```

SkillOpt-owned suites live under `skillopt/suites`. Benchmark suites can still be used by passing their benchmark suite label to `--suite`; that is SkillOpt calling benchmark runner functionality, not a benchmark feature.

## Boundary

- Benchmark WebUI lists benchmark suites only.
- Benchmark local launcher does not accept `mode=skillopt`.
- SkillOpt-owned suites and reports are managed by SkillOpt, even when their tasks are executed through benchmark runner functions.
