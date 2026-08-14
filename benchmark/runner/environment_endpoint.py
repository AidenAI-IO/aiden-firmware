from __future__ import annotations

from dataclasses import dataclass
import urllib.parse


@dataclass(frozen=True)
class EnvironmentEndpoint:
    """Environment bridge URLs derived from one explicit base URL."""

    base: str

    def __post_init__(self) -> None:
        raw = str(self.base or "").strip()
        parsed = urllib.parse.urlsplit(raw)
        if (
            parsed.scheme not in {"http", "https"}
            or not parsed.netloc
            or parsed.query
            or parsed.fragment
        ):
            raise ValueError(f"invalid environment base URL: {self.base!r}")
        path = parsed.path.rstrip("/")
        normalized = urllib.parse.urlunsplit(
            (parsed.scheme, parsed.netloc, path, "", "")
        )
        object.__setattr__(self, "base", normalized)

    def api(self, name: str) -> str:
        endpoint_name = str(name or "").strip().strip("/")
        if not endpoint_name or "/" in endpoint_name:
            raise ValueError(f"invalid environment API name: {name!r}")
        return f"{self.base}/api/{endpoint_name}"

    @property
    def health(self) -> str:
        return f"{self.base}/health"

    @property
    def setup(self) -> str:
        return self.api("setup")

    @property
    def release(self) -> str:
        return self.api("release")

    @property
    def screen(self) -> str:
        return self.api("screen")

    @property
    def concurrent(self) -> str:
        return self.api("concurrent")
