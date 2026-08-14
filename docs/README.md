---
slug: /
sidebar_position: 0
title: Aiden Hardware Documentation
---

# Aiden Hardware Documentation

Build, configure, operate, and extend Aiden hardware and its Agent runtime.

## Start Here

- Start the [Docker Sandbox](01-getting-started/docker-sandbox.md) for Config Web,
  Agent Web, and optional environment-bridge control without a development board.
- [Try Aiden on PC](01-getting-started/try-aiden-on-pc.md) without assembling hardware.
- Follow the [Newcomer Quickstart](01-getting-started/quickstart.md) for a physical device.
- Review [Hardware & Wiring](01-getting-started/hardware.md) before powering a self-assembled device.
- Set up the [Build Environment](01-getting-started/build.md), then [deploy to the device](01-getting-started/deployment.md).
- Use [Troubleshooting](07-operations/troubleshooting.md) when a service or peripheral is not working as expected.

## Core Reference

- [Architecture](02-architecture/overview.md): system boundaries, boot services, source layout, and runtime paths.
- [Device Services](03-services/agent.md): Agent, configuration, terminal, frame capture, audio, BLE, and USB HID services.
- [Agent](04-agent/overview.md): runtime configuration, tools, skills, memory, voice, and phone integration.
- [Persistent Python Packages](04-agent/python-packages.md): shared pip environment, storage layout, and cleanup policy.
- [SDK & Tools](05-sdk-and-tools/cpp-sdk.md): C++ SDK, examples, and image utilities.
- [Protocols](06-protocols/uds-protocol.md): Unix domain socket and service protocol contracts.
- [OTA](08-ota/README.md): signed releases, A/B updates, rollback, verification, and custom distribution.

## Developer Workflows

- [Benchmark](09-benchmark/README.md): run and evaluate Agent tasks against device or simulated environments.
- [SkillOpt](10-skillopt/README.md): improve Agent skills through rollout and held-out validation.
