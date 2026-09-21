"""Experiment-local cancellation hook; leave the normal benchmark runner intact."""
from __future__ import annotations

import json
import os
from pathlib import Path
import sys
import threading
import time

sys.path.insert(0, str(Path(__file__).resolve().parents[2]))
from runner.agent_client import AgentClient
from runner.main import cli

original_wait = AgentClient._wait_for_chat_result


def wait_with_terminal(self, request_id, timeout_sec=None):
    terminal_path = Path(os.environ['PR715_TERMINAL_FILE'])
    stopped = threading.Event()

    def watch():
        while not stopped.wait(0.05):
            if terminal_path.exists():
                began = time.monotonic()
                try:
                    status = self.cancel_chat(request_id)
                    result = {'request_id':request_id, 'status':status,
                              'cancel_latency_ms':(time.monotonic()-began)*1000}
                except Exception as error:
                    result = {'request_id':request_id, 'error':str(error)}
                terminal_path.with_suffix('.cancel.json').write_text(json.dumps(result, indent=2))
                return

    watcher = threading.Thread(target=watch, daemon=True)
    watcher.start()
    try:
        return original_wait(self, request_id, timeout_sec)
    finally:
        stopped.set()
        watcher.join(timeout=20)


AgentClient._wait_for_chat_result = wait_with_terminal
if __name__ == '__main__':
    cli()
