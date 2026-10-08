from __future__ import annotations

import os
import signal
import subprocess


def terminate_process_tree(
    proc: subprocess.Popen | None,
    timeout_sec: float = 3.0,
) -> None:
    """Stop a process group, escalating to SIGKILL after a short grace period."""
    if proc is None or proc.poll() is not None:
        return
    try:
        if os.name == "posix":
            os.killpg(proc.pid, signal.SIGTERM)
        else:
            proc.terminate()
    except ProcessLookupError:
        return
    except Exception:
        try:
            proc.terminate()
        except Exception:
            return
    try:
        proc.wait(timeout=timeout_sec)
        return
    except subprocess.TimeoutExpired:
        pass
    except Exception:
        return
    try:
        if os.name == "posix":
            os.killpg(proc.pid, signal.SIGKILL)
        else:
            proc.kill()
    except ProcessLookupError:
        return
    except Exception:
        try:
            proc.kill()
        except Exception:
            return
    try:
        proc.wait(timeout=1)
    except Exception:
        return
