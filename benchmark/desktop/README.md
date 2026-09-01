# Desktop Environment Bridge

The desktop bridge exposes the standard [Environment Bridge Protocol](../environment_bridge.md)
for the host macOS, Linux, or Windows desktop. It captures the primary screen
and translates normalized (0–1000) pointer coordinates and keyboard operations
to the active desktop session.

Start it from `benchmark/`:

```bash
uv run python -m desktop.scripts.start_bridge --bridge-port 8898
```

Install `pyautogui` in the benchmark environment before using input operations:

```bash
uv pip install pyautogui
```

The bridge can also be started through the benchmark service CLI, which writes
the endpoint and a stop command to stdout:

```bash
uv run python -m runner start-desktop-env --bridge-port 8898
```

On macOS, grant Screen Recording and Accessibility permissions to the terminal
or Python interpreter. Linux may require an X11/Wayland-compatible screenshot
utility (`gnome-screenshot`, `scrot`, or ImageMagick `import`) when pyautogui is
not installed. A desktop bridge is single-environment (`/api/concurrent` = 1)
and `/api/setup` claims a route but does not reset the host desktop.
