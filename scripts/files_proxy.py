#!/usr/bin/env python3
"""
Lightweight HTTP server exposing /benchmark and /user_files on 0.0.0.0.

Serves files directly from /userdata/agent/ without depending on config_web.
- /user_files       -> /userdata/agent/files_report.html
- /user_files/regenerate (POST) -> regenerate the report
- /benchmark        -> latest benchmark report or list
- /benchmark/<run>  -> specific benchmark run report
"""
import html
import http.server
import socketserver
import os
import subprocess
import sys
import time
from pathlib import Path
from urllib.parse import quote

PROXY_PORT = 8090
AGENT_DIR = Path('/userdata/agent')
FILES_REPORT = AGENT_DIR / 'files_report.html'
BENCHMARK_DIR = AGENT_DIR / 'benchmark'
TOOLS_DIR = Path('/userdata/agent_tools')
REPORT_MAX_AGE_SECONDS = 300  # 5 minutes


def _ensure_report_exists():
    """Generate report if missing or stale. Returns True if report is ready."""
    if FILES_REPORT.exists():
        age = time.time() - FILES_REPORT.stat().st_mtime
        if age < REPORT_MAX_AGE_SECONDS:
            return True

    # Report missing or stale - regenerate synchronously
    script = TOOLS_DIR / 'generate_agent_files_report.py'
    template = TOOLS_DIR / 'agent_files_template.html'

    if not script.exists() or not template.exists():
        return False

    try:
        subprocess.run(
            [
                'python3', str(script),
                '--memory-dir', '/userdata/agent/memory',
                '--skills-dir', '/userdata/agent/skills',
                '--skill-state-dir', '/userdata/agent/skill-state',
                '--skillopt-dir', '/userdata/agent/benchmark/runs/skillopt',
                '--output', str(FILES_REPORT)
            ],
            cwd=str(TOOLS_DIR),
            timeout=30,
            capture_output=True,
            check=True
        )
        return True
    except (subprocess.TimeoutExpired, subprocess.CalledProcessError, OSError):
        return False


def _serve_html(handler, path: Path):
    if not path.exists():
        handler.send_error(404, f'Not Found: {path.name}')
        return
    try:
        data = path.read_bytes()
    except OSError as e:
        handler.send_error(500, f'Read error: {e}')
        return
    handler.send_response(200)
    handler.send_header('Content-Type', 'text/html; charset=utf-8')
    handler.send_header('Content-Length', str(len(data)))
    handler.send_header('Cache-Control', 'no-cache, must-revalidate')
    handler.end_headers()
    handler.wfile.write(data)


def _serve_benchmark_index(handler):
    runs_dir = BENCHMARK_DIR / 'runs'
    if not runs_dir.exists():
        handler.send_error(404, 'No benchmark runs found')
        return

    try:
        runs = sorted([d.name for d in runs_dir.iterdir() if d.is_dir()], reverse=True)
    except OSError as e:
        handler.send_error(500, f'List error: {e}')
        return

    rows = []
    for run_id in runs:
        report = runs_dir / run_id / 'report.html'
        safe_text = html.escape(run_id)
        safe_href = quote(run_id, safe='')
        link = f'<a href="/benchmark/{safe_href}">{safe_text}</a>' if report.exists() else safe_text
        rows.append(f'<tr><td>{link}</td></tr>')

    html_content = f'''<!DOCTYPE html>
<html><head><meta charset="utf-8"><title>Benchmark Runs</title>
<style>body{{font-family:system-ui;max-width:900px;margin:40px auto;padding:0 20px}}
table{{width:100%;border-collapse:collapse}}td{{padding:10px;border-bottom:1px solid #eee}}
a{{color:#2563eb;text-decoration:none}}a:hover{{text-decoration:underline}}</style>
</head><body><h1>Benchmark Runs ({len(runs)})</h1>
<table>{''.join(rows)}</table></body></html>'''
    data = html_content.encode('utf-8')
    handler.send_response(200)
    handler.send_header('Content-Type', 'text/html; charset=utf-8')
    handler.send_header('Content-Length', str(len(data)))
    handler.end_headers()
    handler.wfile.write(data)


class FilesHandler(http.server.BaseHTTPRequestHandler):
    def do_GET(self):
        path = self.path.split('?', 1)[0]

        if path == '/user_files':
            # Auto-generate report if missing or stale
            if not _ensure_report_exists():
                self.send_error(500, 'Failed to generate report')
                return
            _serve_html(self, FILES_REPORT)
            return

        if path == '/benchmark' or path == '/benchmark/':
            _serve_benchmark_index(self)
            return

        if path.startswith('/benchmark/'):
            run_id = path[len('/benchmark/'):].strip('/')
            safe_id = ''.join(c for c in run_id if c.isalnum() or c in '-_')
            report = BENCHMARK_DIR / 'runs' / safe_id / 'report.html'
            _serve_html(self, report)
            return

        self.send_error(404, 'Not Found')

    def log_message(self, format, *args):
        sys.stderr.write(f"[files-proxy] {self.address_string()} - {format % args}\n")


class ThreadedHTTPServer(socketserver.ThreadingMixIn, http.server.HTTPServer):
    daemon_threads = True
    allow_reuse_address = True


def main():
    server = ThreadedHTTPServer(('0.0.0.0', PROXY_PORT), FilesHandler)
    print(f"Listening on 0.0.0.0:{PROXY_PORT}", flush=True)
    print(f"  GET  /user_files          -> {FILES_REPORT} (auto-generated if missing/stale)", flush=True)
    print(f"  GET  /benchmark           -> list of runs", flush=True)
    print(f"  GET  /benchmark/<run_id>  -> {BENCHMARK_DIR}/runs/<run_id>/report.html", flush=True)
    print(f"  Report max age: {REPORT_MAX_AGE_SECONDS}s", flush=True)
    try:
        server.serve_forever()
    except KeyboardInterrupt:
        print("Shutting down")
        server.shutdown()


if __name__ == '__main__':
    main()
