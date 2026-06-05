#!/usr/bin/env python3
"""
Lightweight HTTP server exposing /benchmark and /user_files on 0.0.0.0.

Serves files directly from /userdata/agent/ without depending on config_web.
- /user_files       -> /userdata/agent/files_report.html
- /user_files/regenerate (POST) -> regenerate the report
- /benchmark        -> latest benchmark report or list
- /benchmark/<run>  -> specific benchmark run report
"""
import http.server
import socketserver
import os
import subprocess
import sys
from pathlib import Path

PROXY_PORT = 8090
AGENT_DIR = Path('/userdata/agent')
FILES_REPORT = AGENT_DIR / 'files_report.html'
BENCHMARK_DIR = AGENT_DIR / 'benchmark'
TOOLS_DIR = Path('/userdata/agent_tools')


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

    runs = sorted([d.name for d in runs_dir.iterdir() if d.is_dir()], reverse=True)
    rows = []
    for run_id in runs:
        report = runs_dir / run_id / 'report.html'
        link = f'<a href="/benchmark/{run_id}">{run_id}</a>' if report.exists() else run_id
        rows.append(f'<tr><td>{link}</td></tr>')

    html = f'''<!DOCTYPE html>
<html><head><meta charset="utf-8"><title>Benchmark Runs</title>
<style>body{{font-family:system-ui;max-width:900px;margin:40px auto;padding:0 20px}}
table{{width:100%;border-collapse:collapse}}td{{padding:10px;border-bottom:1px solid #eee}}
a{{color:#2563eb;text-decoration:none}}a:hover{{text-decoration:underline}}</style>
</head><body><h1>Benchmark Runs ({len(runs)})</h1>
<table>{''.join(rows)}</table></body></html>'''
    data = html.encode('utf-8')
    handler.send_response(200)
    handler.send_header('Content-Type', 'text/html; charset=utf-8')
    handler.send_header('Content-Length', str(len(data)))
    handler.end_headers()
    handler.wfile.write(data)


class FilesHandler(http.server.BaseHTTPRequestHandler):
    def do_GET(self):
        path = self.path.split('?', 1)[0]

        if path == '/user_files':
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

    def do_POST(self):
        if self.path == '/user_files/regenerate':
            script = TOOLS_DIR / 'view_agent_files.sh'
            if not script.exists():
                self.send_error(500, f'Script not found: {script}')
                return
            try:
                subprocess.Popen(
                    ['/bin/sh', str(script)],
                    cwd=str(TOOLS_DIR),
                    stdout=subprocess.DEVNULL,
                    stderr=subprocess.DEVNULL,
                    start_new_session=True,
                )
                body = b'{"status":"ok","message":"Regeneration started"}'
                self.send_response(200)
                self.send_header('Content-Type', 'application/json')
                self.send_header('Content-Length', str(len(body)))
                self.end_headers()
                self.wfile.write(body)
            except OSError as e:
                self.send_error(500, f'Spawn failed: {e}')
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
    print(f"  GET  /user_files            -> {FILES_REPORT}", flush=True)
    print(f"  POST /user_files/regenerate -> {TOOLS_DIR}/view_agent_files.sh", flush=True)
    print(f"  GET  /benchmark             -> list of runs", flush=True)
    print(f"  GET  /benchmark/<run_id>    -> {BENCHMARK_DIR}/runs/<run_id>/report.html", flush=True)
    try:
        server.serve_forever()
    except KeyboardInterrupt:
        print("Shutting down")
        server.shutdown()


if __name__ == '__main__':
    main()
