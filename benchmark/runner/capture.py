from __future__ import annotations
import base64
import json
from pathlib import Path
from runner.agent_client import AgentClient

class CaptureError(RuntimeError):
    pass

def take_screenshot(client: AgentClient, out_path: Path) -> tuple[int, int]:
    """Invoke the screenshot tool and write the JPEG bytes to out_path. Returns (width, height)."""
    result = client.invoke_tool("screenshot", {})
    if result.is_error:
        raise CaptureError(f"screenshot failed: {result.output}")
    try:
        payload = json.loads(result.output)
    except json.JSONDecodeError as e:
        raise CaptureError(f"screenshot returned non-JSON: {result.output[:120]}") from e
    data = payload.get("data")
    if not data:
        raise CaptureError("screenshot returned no data field")
    out_path.parent.mkdir(parents=True, exist_ok=True)
    out_path.write_bytes(base64.b64decode(data))
    return int(payload.get("width", 0)), int(payload.get("height", 0))

def write_step_screenshot(out_path: Path, base64_data: str) -> None:
    out_path.parent.mkdir(parents=True, exist_ok=True)
    out_path.write_bytes(base64.b64decode(base64_data))
