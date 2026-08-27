from __future__ import annotations

import io
import os
import platform
import shutil
import shlex
import subprocess
import tempfile
import time
from typing import Any


class DesktopDeviceError(RuntimeError):
    pass


def _optional_pyautogui():
    try:
        import pyautogui  # type: ignore
    except Exception:
        return None
    pyautogui.PAUSE = 0
    return pyautogui


class DesktopDevice:
    """Screen and input adapter for the local macOS/Linux/Windows desktop.

    ``pyautogui`` is used when installed. Screenshot capture has command-line
    fallbacks so a minimal host can still expose the bridge without Python GUI
    bindings. Accessibility/screen-recording permissions are enforced by the
    operating system and surfaced as actionable errors.
    """

    def __init__(self, *, backend: str = "auto", screenshot_command: str = ""):
        self.system = platform.system().lower()
        self.backend_name = backend.strip().lower() or "auto"
        self.screenshot_command = screenshot_command.strip()
        self._pyautogui = _optional_pyautogui() if self.backend_name in {"auto", "pyautogui"} else None
        if self.backend_name == "pyautogui" and self._pyautogui is None:
            raise DesktopDeviceError("pyautogui backend requested but pyautogui is not installed")

    @property
    def platform(self) -> str:
        if self.system == "darwin":
            return "mac"
        if self.system == "windows":
            return "windows"
        return "linux"

    @property
    def backend(self) -> str:
        if self._pyautogui is not None:
            return "pyautogui"
        return "command" if self.screenshot_command else ("screencapture" if self.system == "darwin" else "desktop-command")

    @property
    def permission_hint(self) -> str:
        """Return the host permission guidance shown before first capture."""
        if self.system == "darwin":
            return (
                "截图需要 macOS Screen Recording 权限；鼠标/键盘控制还需要 Accessibility 权限。"
                "授权后请重启终端或 Python bridge 进程使权限生效。"
            )
        if self.system == "windows":
            return (
                "截图需要允许桌面应用访问屏幕；输入操作需要 pyautogui 和当前交互式桌面会话。"
                "如果刚修改权限，请重启终端或 Python bridge 进程。"
            )
        return (
            "截图需要当前图形桌面会话（X11/Wayland）和屏幕捕获权限；输入操作需要 pyautogui。"
            "如果刚修改权限，请重启终端或 Python bridge 进程。"
        )

    def check_device(self) -> dict[str, Any]:
        try:
            width, height = self.screen_size()
        except Exception as exc:
            raise DesktopDeviceError(f"desktop screen is unavailable: {exc}. {self.permission_hint}") from exc
        return {
            "state": "online",
            "width": width,
            "height": height,
            "backend": self.backend,
            "permission_hint": self.permission_hint,
        }

    def screen_size(self) -> tuple[int, int]:
        if self._pyautogui is not None:
            size = self._pyautogui.size()
            return int(size[0]), int(size[1])
        if self.system == "darwin":
            result = subprocess.run(["/usr/bin/system_profiler", "SPDisplaysDataType"], capture_output=True, text=True, timeout=5)
            import re
            match = re.search(r"Resolution:\s*(\d+) x (\d+)", result.stdout)
            if match:
                return int(match.group(1)), int(match.group(2))
        if self.system == "windows" and shutil.which("powershell"):
            result = subprocess.run(
                ["powershell", "-NoProfile", "-NonInteractive", "-Command", "Add-Type -AssemblyName System.Windows.Forms; $b=[Windows.Forms.Screen]::PrimaryScreen.Bounds; \"$($b.Width) $($b.Height)\""],
                capture_output=True,
                text=True,
                timeout=5,
            )
            parts = result.stdout.strip().split()
            if len(parts) == 2 and all(part.isdigit() for part in parts):
                return int(parts[0]), int(parts[1])
        if shutil.which("xrandr"):
            result = subprocess.run(["xrandr", "--current"], capture_output=True, text=True, timeout=5)
            import re
            match = re.search(r"^\s*(\d+)x(\d+)\s+[^\n]*\*", result.stdout, re.MULTILINE)
            if match is None:
                match = re.search(r"\b(\d+)x(\d+)\b", result.stdout)
            if match:
                return int(match.group(1)), int(match.group(2))
        raise DesktopDeviceError("cannot determine desktop size; install pyautogui or provide a GUI backend")

    def screenshot_jpeg(self, quality: int = 85) -> tuple[bytes, int, int]:
        quality = max(1, min(100, int(quality or 85)))
        if self._pyautogui is not None:
            try:
                image = self._pyautogui.screenshot()
            except Exception as exc:
                raise DesktopDeviceError(
                    f"desktop screenshot is unavailable: {exc}. {self.permission_hint}"
                ) from exc
            output = io.BytesIO()
            image.convert("RGB").save(output, format="JPEG", quality=quality, optimize=True)
            return output.getvalue(), int(image.width), int(image.height)

        command = self._screenshot_command()
        if self.system == "windows" and not self.screenshot_command:
            return self._screenshot_windows(quality)
        if not command:
            raise DesktopDeviceError("no screenshot backend available; install pyautogui or screencapture/scrot")
        with tempfile.NamedTemporaryFile(suffix=".jpg", delete=False) as handle:
            path = handle.name
        try:
            result = subprocess.run(command + [path], capture_output=True, timeout=30)
            if result.returncode != 0:
                detail = result.stderr.decode("utf-8", "replace").strip()
                raise DesktopDeviceError(
                    f"screenshot command failed: {detail or result.returncode}. {self.permission_hint}"
                )
            with open(path, "rb") as image_file:
                payload = image_file.read()
            width, height = self._image_size(payload)
            return payload, width, height
        finally:
            try:
                os.unlink(path)
            except OSError:
                pass

    def _screenshot_windows(self, quality: int) -> tuple[bytes, int, int]:
        # Keep the PowerShell script inline so this backend has no extra Python
        # dependency. `$args[0]` is the temporary output path supplied below.
        script = (
            "Add-Type -AssemblyName System.Drawing; Add-Type -AssemblyName System.Windows.Forms; "
            "$s=[Windows.Forms.Screen]::PrimaryScreen.Bounds; "
            "$b=New-Object Drawing.Bitmap($s.Width,$s.Height); "
            "$g=[Drawing.Graphics]::FromImage($b); "
            "$g.CopyFromScreen($s.Location,[Drawing.Point]::Empty,$s.Size); "
            "$b.Save($args[0],[Drawing.Imaging.ImageFormat]::Jpeg); "
            "$g.Dispose();$b.Dispose()"
        )
        with tempfile.NamedTemporaryFile(suffix=".jpg", delete=False) as handle:
            path = handle.name
        try:
            result = subprocess.run(["powershell", "-NoProfile", "-NonInteractive", "-Command", script, path], capture_output=True, timeout=30)
            if result.returncode != 0:
                detail = result.stderr.decode("utf-8", "replace").strip()
                raise DesktopDeviceError(f"PowerShell screenshot failed: {detail or result.returncode}")
            with open(path, "rb") as image_file:
                payload = image_file.read()
            width, height = self._image_size(payload)
            return payload, width, height
        finally:
            try:
                os.unlink(path)
            except OSError:
                pass

    def click(self, x: float, y: float, *, button: str = "left", clicks: int = 1, interval: float = 0.12) -> None:
        pyautogui = self._require_input()
        px, py = self._pixels(x, y)
        pyautogui.click(px, py, clicks=clicks, interval=max(0, interval), button=button)

    def long_press(self, x: float, y: float, hold_sec: float = 0.5, button: str = "left") -> None:
        pyautogui = self._require_input()
        px, py = self._pixels(x, y)
        pyautogui.moveTo(px, py, duration=0)
        pyautogui.mouseDown(button=button)
        try:
            time.sleep(max(0.0, hold_sec))
        finally:
            pyautogui.mouseUp(button=button)

    def move(self, x: float, y: float) -> None:
        pyautogui = self._require_input()
        px, py = self._pixels(x, y)
        pyautogui.moveTo(px, py, duration=0)

    def drag(self, start: tuple[float, float], end: tuple[float, float], duration: float = 0.5, button: str = "left") -> None:
        pyautogui = self._require_input()
        sx, sy = self._pixels(*start)
        ex, ey = self._pixels(*end)
        pyautogui.moveTo(sx, sy, duration=0)
        pyautogui.dragTo(ex, ey, duration=max(0, duration), button=button)

    def scroll(self, delta: int) -> None:
        pyautogui = self._require_input()
        pyautogui.scroll(int(delta))

    def write(self, text: str) -> None:
        pyautogui = self._require_input()
        if any(ord(char) > 127 for char in text):
            raise ValueError("desktop keyboard_text supports ASCII text only")
        pyautogui.write(text, interval=0)

    def press(self, keys: list[str]) -> None:
        pyautogui = self._require_input()
        normalized = [self._key_name(key) for key in keys]
        if len(normalized) == 1:
            pyautogui.press(normalized[0])
            return
        for key in normalized[:-1]:
            pyautogui.keyDown(key)
        try:
            pyautogui.press(normalized[-1])
        finally:
            for key in reversed(normalized[:-1]):
                pyautogui.keyUp(key)

    def quick_action(self, action: str) -> None:
        action = str(action or "").strip().lower().replace("-", "_")
        if action in {"back", "go_back"}:
            self.press(["alt", "left"])
        elif action in {"home", "show_desktop", "desktop"}:
            self.press(["winleft", "d"] if self.system in {"windows", "linux"} else ["command", "f11"])
        elif action in {"app_switch", "switch_app", "task_switcher"}:
            self.press(["alt", "tab"])
        elif action in {"close", "close_window"}:
            self.press(["command", "w"] if self.system == "darwin" else ["alt", "f4"])
        elif action in {"open_settings", "settings"}:
            if self.system == "darwin":
                subprocess.Popen(["open", "x-apple.systempreferences:"])
            elif self.system == "windows":
                subprocess.Popen(["cmd", "/c", "start", "ms-settings:"])
            elif shutil.which("gnome-control-center"):
                subprocess.Popen(["gnome-control-center"])
            elif shutil.which("xfce4-settings-manager"):
                subprocess.Popen(["xfce4-settings-manager"])
            else:
                raise DesktopDeviceError("no desktop settings launcher found")
        else:
            raise ValueError(f"unsupported desktop quick_action: {action!r}")

    def _require_input(self):
        if self._pyautogui is None:
            raise DesktopDeviceError("desktop input requires pyautogui; install it and grant accessibility permissions")
        return self._pyautogui

    def _pixels(self, x: float, y: float) -> tuple[int, int]:
        if not isinstance(x, (int, float)) or not isinstance(y, (int, float)):
            raise ValueError("x and y must be numbers")
        if not (0 <= float(x) <= 1000 and 0 <= float(y) <= 1000):
            raise ValueError("x and y must be in range [0, 1000]")
        width, height = self.screen_size()
        return round(float(x) * width / 1000), round(float(y) * height / 1000)

    def _screenshot_command(self) -> list[str]:
        if self.screenshot_command:
            return shlex.split(self.screenshot_command)
        if self.system == "darwin" and shutil.which("screencapture"):
            return ["screencapture", "-x", "-t", "jpg"]
        for command in (("gnome-screenshot", "-f"), ("scrot",), ("import", "-window", "root")):
            if shutil.which(command[0]):
                return list(command)
        return []

    @staticmethod
    def _image_size(payload: bytes) -> tuple[int, int]:
        try:
            from PIL import Image
            image = Image.open(io.BytesIO(payload))
            return int(image.width), int(image.height)
        except Exception as exc:
            dimensions = _encoded_image_size(payload)
            if dimensions is not None:
                return dimensions
            raise DesktopDeviceError(
                f"cannot decode screenshot: {exc}; install Pillow if it is missing. "
                "If the capture itself is unavailable, check desktop permissions. "
                "授权后请重启终端或 Python bridge 进程。"
            ) from exc

    @staticmethod
    def _key_name(key: str) -> str:
        key = str(key or "").strip().lower().replace(" ", "_")
        aliases = {"cmd": "command", "meta": "command", "ctrl": "ctrl", "control": "ctrl", "esc": "escape", "return": "enter", "del": "delete", "pgup": "pageup", "pgdn": "pagedown"}
        return aliases.get(key, key)


def _encoded_image_size(payload: bytes) -> tuple[int, int] | None:
    """Read dimensions from common screenshot formats without Pillow."""
    if payload.startswith(b"\x89PNG\r\n\x1a\n") and len(payload) >= 24:
        return int.from_bytes(payload[16:20], "big"), int.from_bytes(payload[20:24], "big")
    if not payload.startswith(b"\xff\xd8"):
        return None
    offset = 2
    frame_markers = {0xC0, 0xC1, 0xC2, 0xC3, 0xC5, 0xC6, 0xC7, 0xC9, 0xCA, 0xCB, 0xCD, 0xCE, 0xCF}
    while offset + 9 < len(payload):
        if payload[offset] != 0xFF:
            offset += 1
            continue
        marker = payload[offset + 1]
        offset += 2
        while marker == 0xFF and offset < len(payload):
            marker = payload[offset]
            offset += 1
        if marker in {0x01, *range(0xD0, 0xD9)}:
            continue
        if offset + 2 > len(payload):
            return None
        length = int.from_bytes(payload[offset:offset + 2], "big")
        if length < 2 or offset + length > len(payload):
            return None
        if marker in frame_markers and offset + 7 <= len(payload):
            height = int.from_bytes(payload[offset + 3:offset + 5], "big")
            width = int.from_bytes(payload[offset + 5:offset + 7], "big")
            return width, height
        offset += length
    return None
