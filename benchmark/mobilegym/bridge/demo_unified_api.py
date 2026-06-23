#!/usr/bin/env python3
"""Demo script: Using MobileGym Bridge Server unified /api/tools API."""

import json
import urllib.request


def call_tool(base_url: str, tool_name: str, tool_input: dict) -> dict:
    """Call a tool via the unified /api/tools endpoint."""
    request_body = json.dumps({"input": tool_input}).encode()
    req = urllib.request.Request(
        f"{base_url}/api/tools/{tool_name}",
        data=request_body,
        method="POST",
        headers={"Content-Type": "application/json"},
    )

    with urllib.request.urlopen(req, timeout=30) as resp:
        return json.loads(resp.read().decode())


def get_tool_catalog(base_url: str) -> dict:
    """Get available tools catalog."""
    req = urllib.request.Request(f"{base_url}/api/tools", method="GET")
    with urllib.request.urlopen(req, timeout=5) as resp:
        return json.loads(resp.read().decode())


def start_episode(base_url: str, episode_id: str) -> None:
    """Start an episode (required before using tools)."""
    request_body = json.dumps({"episode_id": episode_id}).encode()
    req = urllib.request.Request(
        f"{base_url}/api/setup",
        data=request_body,
        method="POST",
        headers={"Content-Type": "application/json"},
    )
    with urllib.request.urlopen(req, timeout=5) as resp:
        return json.loads(resp.read().decode())


def end_episode(base_url: str, episode_id: str) -> None:
    """End an episode."""
    request_body = b"{}"
    req = urllib.request.Request(
        f"{base_url}/api/release",
        data=request_body,
        method="POST",
        headers={"Content-Type": "application/json"},
    )
    with urllib.request.urlopen(req, timeout=5) as resp:
        return json.loads(resp.read().decode())


def main():
    """Demo: Use unified tool API."""
    # Configuration
    BASE_URL = "http://localhost:8888"
    EPISODE_ID = "demo-episode-001"

    print("=" * 60)
    print("MobileGym Bridge Server - Unified Tool API Demo")
    print("=" * 60)
    print()

    # 1. Get tool catalog
    print("1. Fetching tool catalog...")
    catalog = get_tool_catalog(BASE_URL)
    print(f"   Available tools: {', '.join(t['name'] for t in catalog['tools'])}")
    print()

    # 2. Start episode
    print(f"2. Starting episode: {EPISODE_ID}")
    start_episode(BASE_URL, EPISODE_ID)
    print("   ✓ Episode started")
    print()

    try:
        # 3. Take screenshot
        print("3. Taking screenshot...")
        result = call_tool(BASE_URL, "screenshot", {})
        if result["is_error"]:
            print(f"   ✗ Error: {result['output']}")
        else:
            output = json.loads(result["output"])
            print(f"   ✓ Screenshot captured: {output['width']}x{output['height']}")
            print(f"   Duration: {result['duration_ms']}ms")
        print()

        # 4. Perform tap gesture
        print("4. Performing tap gesture at (500, 800)...")
        result = call_tool(
            BASE_URL,
            "touch_gesture",
            {"type": "tap", "point": {"x": 500, "y": 800}},
        )
        if result["is_error"]:
            print(f"   ✗ Error: {result['output']}")
        else:
            output = json.loads(result["output"])
            print(f"   ✓ Tap executed: {output.get('action_output', 'ok')}")
            print(f"   Duration: {result['duration_ms']}ms")
        print()

        # 5. Type text
        print("5. Typing text: 'Hello MobileGym'...")
        result = call_tool(BASE_URL, "keyboard_text", {"text": "Hello MobileGym"})
        if result["is_error"]:
            print(f"   ✗ Error: {result['output']}")
        else:
            print("   ✓ Text typed")
            print(f"   Duration: {result['duration_ms']}ms")
        print()

        # 6. Perform swipe gesture
        print("6. Performing swipe gesture (500,1000) → (500,500)...")
        result = call_tool(
            BASE_URL,
            "touch_gesture",
            {
                "type": "swipe",
                "start": {"x": 500, "y": 1000},
                "end": {"x": 500, "y": 500},
                "duration_ms": 300,
            },
        )
        if result["is_error"]:
            print(f"   ✗ Error: {result['output']}")
        else:
            output = json.loads(result["output"])
            print(f"   ✓ Swipe executed: {output.get('action_output', 'ok')}")
            print(f"   Duration: {result['duration_ms']}ms")
        print()

        # 7. Press back button
        print("7. Pressing back button...")
        result = call_tool(BASE_URL, "touch_gesture", {"type": "back"})
        if result["is_error"]:
            print(f"   ✗ Error: {result['output']}")
        else:
            print("   ✓ Back pressed")
            print(f"   Duration: {result['duration_ms']}ms")
        print()

    finally:
        # 8. End episode
        print(f"8. Ending episode: {EPISODE_ID}")
        end_episode(BASE_URL, EPISODE_ID)
        print("   ✓ Episode ended")
        print()

    print("=" * 60)
    print("Demo completed successfully!")
    print("=" * 60)


if __name__ == "__main__":
    import sys

    if len(sys.argv) > 1 and sys.argv[1] == "--help":
        print("Usage: python demo_unified_api.py")
        print()
        print("Prerequisites:")
        print("1. Start MobileGym Bridge Server:")
        print("   python benchmark/mobilegym/scripts/start_simulator.py --bridge-port 8888")
        print()
        sys.exit(0)

    try:
        main()
    except Exception as e:
        print(f"\n✗ Error: {e}")
        print("\nMake sure:")
        print("1. Bridge Server is running on http://localhost:8888")
        sys.exit(1)
