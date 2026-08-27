from __future__ import annotations

import argparse
import signal
import sys
import time

from desktop.bridge.device import DesktopDevice
from desktop.bridge.server import DesktopBridgeServer


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(description="Start a host desktop environment bridge")
    parser.add_argument("--bridge-host", default="127.0.0.1")
    parser.add_argument("--bridge-port", type=int, default=8898)
    parser.add_argument("--backend", choices=["auto", "pyautogui"], default="auto")
    parser.add_argument("--screenshot-command", default="", help="Optional screenshot command prefix (the output path is appended)")
    args = parser.parse_args(argv)
    if args.bridge_port < 0 or args.bridge_port > 65535:
        parser.error("--bridge-port must be between 0 and 65535")
    try:
        device = DesktopDevice(backend=args.backend, screenshot_command=args.screenshot_command)
        print(f"permission_notice: {device.permission_hint}", file=sys.stderr, flush=True)
        bridge = DesktopBridgeServer(device=device, host=args.bridge_host, port=args.bridge_port)
        url = bridge.start()
    except Exception as exc:
        print(f"failed to start desktop bridge: {exc}", file=sys.stderr)
        return 1
    print(f"desktop environment bridge started: {url}", flush=True)
    print(f"platform: {device.platform}", flush=True)
    print(f"stop_command: kill -TERM {__import__('os').getpid()}", flush=True)
    stop = False
    def _stop(signum, frame):
        nonlocal stop
        stop = True
    signal.signal(signal.SIGTERM, _stop)
    signal.signal(signal.SIGINT, _stop)
    try:
        while not stop:
            time.sleep(0.5)
    finally:
        bridge.stop()
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
