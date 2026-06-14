#!/usr/bin/env python3
from __future__ import annotations

import argparse
import json
import os
import subprocess
import sys
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path
from typing import Any, Callable
from urllib.parse import urlparse


HOST = "127.0.0.1"
PORT = 4175
LAUNCHER_LABEL = "com.aiden.mobilegym-local-launcher"
LAUNCHER_PLIST = Path.home() / "Library" / "LaunchAgents" / f"{LAUNCHER_LABEL}.plist"


class HelperError(RuntimeError):
    pass


def start_local_launcher() -> dict[str, Any]:
    target = f"gui/{os.getuid()}/{LAUNCHER_LABEL}"
    domain = f"gui/{os.getuid()}"
    try:
        result = run_launchctl(["launchctl", "kickstart", "-k", target])
    except subprocess.CalledProcessError as exc:
        if "Could not find service" in "\n".join(part for part in (exc.stdout, exc.stderr) if part):
            run_launchctl(["launchctl", "bootstrap", domain, str(LAUNCHER_PLIST)])
            result = run_launchctl(["launchctl", "kickstart", "-k", target])
        else:
            output = "\n".join(part for part in (exc.stdout, exc.stderr) if part).strip()
            raise HelperError(output or f"launchctl exited with {exc.returncode}") from exc
    output = "\n".join(part for part in (result.stdout, result.stderr) if part).strip()
    return {"ok": True, "status": "started", "output": output}


def run_launchctl(argv: list[str]) -> subprocess.CompletedProcess[str]:
    return subprocess.run(
        argv,
        check=True,
        capture_output=True,
        text=True,
    )


def make_handler(starter: Callable[[], dict[str, Any]] = start_local_launcher) -> type[BaseHTTPRequestHandler]:
    class LocalLauncherHelperHandler(BaseHTTPRequestHandler):
        def do_OPTIONS(self) -> None:
            self.send_response(204)
            self.send_common_headers("text/plain; charset=utf-8")
            self.end_headers()

        def do_POST(self) -> None:
            path = urlparse(self.path).path
            if path != "/start":
                self.send_error(404, "not found")
                return
            try:
                self.send_json(starter())
            except HelperError as exc:
                self.send_json({"error": str(exc)}, status=500)
            except Exception as exc:
                self.send_json({"error": str(exc)}, status=500)

        def send_json(self, payload: Any, status: int = 200) -> None:
            body = json.dumps(payload, ensure_ascii=False).encode("utf-8")
            self.send_response(status)
            self.send_common_headers("application/json; charset=utf-8")
            self.send_header("Content-Length", str(len(body)))
            self.end_headers()
            self.wfile.write(body)

        def send_common_headers(self, content_type: str) -> None:
            self.send_header("Content-Type", content_type)
            self.send_header("Access-Control-Allow-Origin", "*")
            self.send_header("Access-Control-Allow-Methods", "POST,OPTIONS")
            self.send_header("Access-Control-Allow-Headers", "Content-Type")
            self.send_header("Access-Control-Allow-Private-Network", "true")

        def log_message(self, fmt: str, *args: Any) -> None:
            print(f"{self.address_string()} - {fmt % args}", file=sys.stderr)

    return LocalLauncherHelperHandler


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(description="Mac-local helper that starts the MobileGym launcher.")
    parser.add_argument("--host", default=HOST)
    parser.add_argument("--port", type=int, default=PORT)
    args = parser.parse_args(argv)

    server = ThreadingHTTPServer((args.host, args.port), make_handler())
    print(f"MobileGym launcher helper listening on http://{args.host}:{args.port}")
    try:
        server.serve_forever()
    except KeyboardInterrupt:
        print("\nStopping MobileGym launcher helper")
        return 130
    finally:
        server.server_close()
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
