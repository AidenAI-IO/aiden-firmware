# MobileGym Docker Image

This directory contains the Dockerfile used by the active benchmark WebUI and
CLI service paths.

The supported entry points are:

```bash
cd benchmark
uv run python -m runner webui
uv run python -m runner start-mobilegym-env
```

Both paths build `benchmark/mobilegym/docker/Dockerfile` with the
`mobilegym-base` target when the MobileGym simulator image is missing. The
runner and daemon orchestration lives in `benchmark/runner/webui.py`,
`benchmark/runner/services.py`, and `benchmark/docker/`.

Standalone MobileGym compose/test-runner files were removed because they used
the old `run_aiden.py` flow and were no longer part of the WebUI or benchmark
CLI execution path.
