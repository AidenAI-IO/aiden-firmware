---
sidebar_position: 1
title: Try Aiden on PC
description: Run the Aiden Agent against a simulated phone UI without assembling hardware.
---

# Try Aiden on PC

This guide is the shortest way to experience Aiden without a Luckfox board or a
physical phone. It runs the real Aiden Agent on your computer and connects it to
the MobileGym phone simulator through the Benchmark environment bridge.

The PC experience demonstrates how Aiden observes a screen, chooses tools, and
operates a mobile-style UI. It does not test HDMI capture, USB HID, audio, or
other physical-hardware behavior.

## What You Need

- A Docker-capable macOS or Linux computer. Start Docker Desktop or Docker Engine
  before continuing.
- Git and [uv](https://docs.astral.sh/uv/getting-started/installation/).
- An API key for the model that will run the Aiden Agent.
- An OpenRouter-compatible API key only if you also enable Benchmark judging.

The documented path currently targets macOS and Linux. Windows users should use
a Docker-capable WSL2 environment, but that path is not yet part of the regular
project validation matrix.

## 1. Clone and Install

```bash
git clone --recursive https://github.com/AidenAI-IO/aiden-hardware-demo.git
cd aiden-hardware-demo/benchmark
uv sync
```

If you already cloned the repository, update it before continuing:

```bash
git pull
git submodule update --init --recursive
```

## 2. Start the Benchmark WebUI

From the `benchmark/` directory:

```bash
uv run python -m runner webui
```

Open:

```text
http://127.0.0.1:8765
```

The first MobileGym run may take longer because the WebUI builds the simulator
and isolated Agent daemon images locally.

## 3. Configure the Agent Model

Open **Agent config** in the WebUI and configure the model provider used by the
isolated Agent workers. The default template contains:

```toml
[model_providers.benchmark]
type = "openrouter"
base_url = ""
api_key = "YOUR_MODEL_API_KEY"

[model]
provider = "benchmark"
model = "qwen3.6-35b"
```

Replace the key and, if necessary, the provider URL or model name. Save the
configuration before starting a job. Do not commit the generated local
`agent.toml` or API keys.

The **Enable judge** option is separate from the Agent model. For a first
experience, disable judging; this avoids requiring a second API key and still
keeps the Agent trace and simulator screenshots.

## 4. Start the Phone Simulator

1. Select a small MobileGym suite, such as `mobilegym_basic.json`.
2. Click **Run selected suites**.
3. Open the **MobileGym** tab in the environment dialog.
4. Enter a name and set **Envs** to `1` for the first run.
5. Click **Start MobileGym** and wait for the environment status to become
   running.
6. Select the running environment and confirm the run.

The WebUI starts an isolated Aiden Agent worker and connects its screenshot,
touch, keyboard, and text-entry tools to the simulator.

## 5. Watch Aiden Operate

Use **Task workers** to open the live simulator screen and worker log. A complete
run should show:

- the simulated phone UI changing as the Agent acts;
- tool calls such as screenshots, gestures, and text entry;
- a completed job in the Jobs table;
- pre/post screenshots and the full tool trace in the generated report.

This is the main PC capability experience. Benchmark scores and repeated suites
are developer features and are not required for the first run.

## Troubleshooting

### Docker is unavailable

Confirm Docker is running:

```bash
docker info
```

### The Agent worker cannot call the model

Reopen **Agent config** and verify the provider, model name, API key, and optional
base URL. The judge API key does not configure the Agent worker.

### The judge reports a missing API key

Disable **Enable judge**, or configure the OpenRouter-compatible judge key in the
WebUI.

### MobileGym stays in the starting state

Check the WebUI log and Docker status. The first image build can take several
minutes depending on the network and machine.

## Next Steps

- [Benchmark Overview](../09-benchmark/README.md)
- [Benchmark Detailed Guide](../09-benchmark/quickstart.md)
- [Build Aiden Hardware](quickstart.md)
- [Hardware and Wiring](hardware.md)
