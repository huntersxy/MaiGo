#!/usr/bin/env python3
"""Python Playwright HTML render sidecar for MaiGo.

This process intentionally imports the original MaiBot
``src.services.html_render_service`` implementation instead of reimplementing
Chrome DevTools Protocol in Go. It exposes a tiny HTTP endpoint that the Go
``htmlrender`` service calls through ``MAIBOT_HTMLRENDER_URL``.
"""

from __future__ import annotations

import asyncio
import json
import os
import signal
import sys
import threading
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path
from typing import Any, Dict

# Must be set before importing src.config.config, which initializes config on import.
os.environ.setdefault("MAIBOT_INITIALIZE_ONLY", "1")
os.environ.setdefault("MAIBOT_INSTANCE_DIR", "/MaiMBot")

from src.config.official_configs import PluginRuntimeRenderConfig  # noqa: E402
from src.services.html_render_service import HtmlRenderRequest, HTMLRenderService  # noqa: E402

PORT = int(os.environ.get("MAIBOT_HTMLRENDER_SIDECAR_PORT", "8766") or "8766")
TOKEN = os.environ.get("MAIBOT_HTMLRENDER_SIDECAR_TOKEN", "").strip()
INSTANCE_DIR = Path(os.environ.get("MAIBOT_INSTANCE_DIR", "/MaiMBot"))
BROWSER_ROOT = os.environ.get(
    "MAIBOT_HTMLRENDER_BROWSER_INSTALL_ROOT",
    str(INSTANCE_DIR / "data" / "playwright-browsers"),
)
STATE_PATH = Path(
    os.environ.get(
        "MAIBOT_HTMLRENDER_STATE_PATH",
        str(INSTANCE_DIR / "data" / "plugin_runtime" / "html_render_browser_state.json"),
    )
)
REQUEST_TIMEOUT = float(os.environ.get("MAIBOT_HTMLRENDER_SIDECAR_TIMEOUT", "90") or "90")
MAX_BODY = 32 << 20


def _text(value: Any) -> str:
    return "" if value is None else str(value).strip()


def _int(value: Any, default: int) -> int:
    try:
        return int(value)
    except (TypeError, ValueError):
        return default


def _float(value: Any, default: float) -> float:
    try:
        return float(value)
    except (TypeError, ValueError):
        return default


def _bool(value: Any, default: bool = False) -> bool:
    if value is None:
        return default
    if isinstance(value, str):
        normalized = value.strip().lower()
        if normalized in {"0", "false", "no", "off", ""}:
            return False
        if normalized in {"1", "true", "yes", "on"}:
            return True
    return bool(value)


class SidecarRenderService(HTMLRenderService):
    """Use a sidecar-local render config instead of the global bot config."""

    def __init__(self, config: PluginRuntimeRenderConfig) -> None:
        self._sidecar_config = config
        super().__init__()

    def _get_render_config(self) -> PluginRuntimeRenderConfig:
        return self._sidecar_config

    def _get_browser_state_path(self) -> Path:
        return STATE_PATH


def _build_render_config() -> PluginRuntimeRenderConfig:
    return PluginRuntimeRenderConfig(
        enabled=True,
        browser_ws_endpoint=os.environ.get("MAIBOT_HTMLRENDER_BROWSER_WS", "").strip(),
        executable_path=os.environ.get("MAIBOT_HTMLRENDER_BROWSER_PATH", "").strip(),
        browser_install_root=BROWSER_ROOT,
        headless=_bool(os.environ.get("MAIBOT_HTMLRENDER_HEADLESS"), True),
        launch_args=[
            "--disable-gpu",
            "--disable-dev-shm-usage",
            "--disable-setuid-sandbox",
            "--no-sandbox",
            "--no-zygote",
        ],
        concurrency_limit=max(1, _int(os.environ.get("MAIBOT_HTMLRENDER_CONCURRENCY"), 2)),
        startup_timeout_sec=20.0,
        render_timeout_sec=15.0,
        auto_download_chromium=_bool(os.environ.get("MAIBOT_HTMLRENDER_AUTO_DOWNLOAD"), True),
        download_connection_timeout_sec=120.0,
        restart_after_render_count=200,
    )


def _build_request(payload: Dict[str, Any]) -> HtmlRenderRequest:
    return HtmlRenderRequest(
        html=_text(payload.get("html")),
        selector=_text(payload.get("selector")) or "body",
        viewport_width=max(1, _int(payload.get("viewport_width"), 900)),
        viewport_height=max(1, _int(payload.get("viewport_height"), 500)),
        device_scale_factor=max(1.0, _float(payload.get("device_scale_factor"), 2.0)),
        full_page=_bool(payload.get("full_page")),
        omit_background=_bool(payload.get("omit_background")),
        wait_until=_text(payload.get("wait_until")) or "load",
        wait_for_selector=_text(payload.get("wait_for_selector")),
        wait_for_timeout_ms=max(0, _int(payload.get("wait_for_timeout_ms"), 0)),
        timeout_ms=max(0, _int(payload.get("timeout_ms"), 0)),
        allow_network=_bool(payload.get("allow_network")),
    )


_loop = asyncio.new_event_loop()
_service_ready = threading.Event()
_service_holder: Dict[str, SidecarRenderService] = {}


def _run_render_loop() -> None:
    asyncio.set_event_loop(_loop)

    async def initialize() -> None:
        service = SidecarRenderService(_build_render_config())
        _service_holder["service"] = service
        # 启动期预热浏览器：首次安装/下载 Chromium 不应落到第一个真实渲染请求上。
        try:
            await service._ensure_browser(service._get_render_config())
            print("[html-render] browser warm-up complete", file=sys.stderr, flush=True)
        except Exception as exc:  # noqa: BLE001 - first render can retry
            print(f"[html-render] browser warm-up failed: {exc}", file=sys.stderr, flush=True)

    _loop.run_until_complete(initialize())
    _service_ready.set()
    _loop.run_forever()


async def _render(payload: Dict[str, Any]) -> Dict[str, Any]:
    service = _service_holder["service"]
    result = await service.render_html_to_png(_build_request(payload))
    return result.to_payload()


class RenderHandler(BaseHTTPRequestHandler):
    server_version = "MaiGoRenderSidecar/1.0"

    def log_message(self, fmt: str, *args: Any) -> None:
        print("[html-render] " + fmt % args, file=sys.stderr, flush=True)

    def _send_json(self, status: int, payload: Dict[str, Any]) -> None:
        body = json.dumps(payload, ensure_ascii=False).encode("utf-8")
        self.send_response(status)
        self.send_header("Content-Type", "application/json; charset=utf-8")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def _authorized(self) -> bool:
        if not TOKEN:
            return True
        if self.headers.get("Authorization", "") == "Bearer " + TOKEN:
            return True
        self._send_json(401, {"detail": "unauthorized"})
        return False

    def do_GET(self) -> None:  # noqa: N802 - BaseHTTPRequestHandler API
        path = self.path.split("?", 1)[0].rstrip("/") or "/"
        if path not in {"/", "/health", "/status"}:
            self._send_json(404, {"detail": "not found"})
            return
        if not self._authorized():
            return
        self._send_json(
            200,
            {
                "status": "ok",
                "mode": "python_playwright",
                "browser_install_root": BROWSER_ROOT,
            },
        )

    def do_POST(self) -> None:  # noqa: N802 - BaseHTTPRequestHandler API
        path = self.path.split("?", 1)[0].rstrip("/") or "/"
        if path != "/render":
            self._send_json(404, {"detail": "not found"})
            return
        if not self._authorized():
            return
        try:
            length = int(self.headers.get("Content-Length", "0") or "0")
            if length <= 0 or length > MAX_BODY:
                self._send_json(400, {"detail": "invalid body length"})
                return
            payload = json.loads(self.rfile.read(length).decode("utf-8"))
            if not isinstance(payload, dict):
                self._send_json(400, {"detail": "body must be a JSON object"})
                return
            future = asyncio.run_coroutine_threadsafe(_render(payload), _loop)
            result = future.result(timeout=REQUEST_TIMEOUT)
            self._send_json(200, result)
        except Exception as exc:  # noqa: BLE001 - sidecar reports render errors as HTTP 500
            self._send_json(500, {"detail": str(exc)})


def main() -> int:
    threading.Thread(target=_run_render_loop, daemon=True).start()
    startup_timeout = float(os.environ.get("MAIBOT_HTMLRENDER_STARTUP_TIMEOUT", "180") or "180")
    if not _service_ready.wait(timeout=startup_timeout):
        print("[html-render] render service initialization timed out", file=sys.stderr, flush=True)
        return 1

    httpd = ThreadingHTTPServer(("0.0.0.0", PORT), RenderHandler)

    def shutdown(_signum: int, _frame: Any) -> None:
        threading.Thread(target=httpd.shutdown, daemon=True).start()

    signal.signal(signal.SIGTERM, shutdown)
    signal.signal(signal.SIGINT, shutdown)
    print(f"[html-render] listening on 0.0.0.0:{PORT}", file=sys.stderr, flush=True)
    try:
        httpd.serve_forever()
    finally:
        _loop.call_soon_threadsafe(_loop.stop)
        httpd.server_close()
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
