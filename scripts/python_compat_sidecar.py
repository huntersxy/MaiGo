#!/usr/bin/env python3
"""Python 原版能力侧车（MaiGo）。

把「复刻 Python 字节级行为」的 Go 代码交还给 Python 原版执行。当前提供两类能力：

- ``/typo``        中文错别字注入，直接调用 ``src/chat/utils/typo_generator.py``
- ``/kmeans``      表达向量 cosine k-means，直接调用
                   ``src/chat/replyer/expression_vector_index.py``

两者都只依赖 numpy/jieba/pypinyin，不碰 MaiBot 的配置与数据库；这里用模块级
stub 屏蔽 ``src.common.logger``、``src.config.config`` 等重依赖，避免为了算一个
k-means 去初始化整个机器人。

约定：
- 默认只监听 127.0.0.1，不对外暴露；token 为空表示不校验。
- 请求/响应均为 JSON；错误统一 HTTP 500 + ``{"detail": "..."}``。
- 进程由 Go 按需拉起；空闲超时按 in-flight 计数自行退出。
"""

from __future__ import annotations

import importlib.util
import json
import os
import random
import signal
import sys
import threading
import time
import types
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path
from typing import Any, Callable, Dict, List, Optional

INSTANCE_DIR = Path(os.environ.get("MAIBOT_INSTANCE_DIR", "/MaiMBot"))
SOURCE_ROOT = Path(os.environ.get("MAIBOT_SOURCE_ROOT", str(INSTANCE_DIR)))
HOST = os.environ.get("MAIBOT_COMPAT_SIDECAR_HOST", "127.0.0.1").strip() or "127.0.0.1"
PORT = int(os.environ.get("MAIBOT_COMPAT_SIDECAR_PORT", "8767") or "8767")
TOKEN = os.environ.get("MAIBOT_COMPAT_SIDECAR_TOKEN", "").strip()
IDLE_SECONDS = float(os.environ.get("MAIBOT_COMPAT_SIDECAR_IDLE", "300") or "300")
MAX_BODY = 64 << 20

_IN_FLIGHT = 0
_IN_FLIGHT_LOCK = threading.Lock()
_LAST_ACTIVE = time.monotonic()
_IDLE_STOP = threading.Event()



# --------------------------------------------------------------------------- #
# MaiBot 模块加载：只装真正需要的模块，其余用占位对象挡住初始化副作用。
# --------------------------------------------------------------------------- #


class _NullLogger:
    """吞掉所有日志调用；侧车自己的输出走 stderr。"""

    def __getattr__(self, _name: str) -> Callable[..., None]:
        return lambda *args, **kwargs: None


def _install_module_stubs() -> None:
    logger_module = types.ModuleType("src.common.logger")
    logger_module.get_logger = lambda name=None: _NullLogger()  # noqa: ARG005
    sys.modules["src.common.logger"] = logger_module

    async_work = types.ModuleType("src.common.async_work")

    async def _run_sync_work(function, *args, **kwargs):  # noqa: ANN001, ANN202
        return function(*args, **kwargs)

    async_work.run_sync_work = _run_sync_work
    sys.modules["src.common.async_work"] = async_work

    paths = types.ModuleType("src.common.paths")
    paths.get_instance_root = lambda: INSTANCE_DIR
    sys.modules["src.common.paths"] = paths

    config_module = types.ModuleType("src.config.config")
    config_module.config_manager = types.SimpleNamespace()
    sys.modules["src.config.config"] = config_module


def _load_module(name: str, relative_path: str):
    """按文件路径加载模块，绕过 ``src/__init__`` 与包内重依赖。"""
    path = SOURCE_ROOT / relative_path
    if not path.is_file():
        raise FileNotFoundError(f"找不到 Python 原版模块: {path}")
    spec = importlib.util.spec_from_file_location(name, path)
    if spec is None or spec.loader is None:
        raise ImportError(f"无法加载 Python 原版模块: {path}")
    module = importlib.util.module_from_spec(spec)
    # dataclass 在 exec 期间需要模块已注册，否则取不到 __dict__。
    sys.modules[name] = module
    spec.loader.exec_module(module)
    return module


_install_module_stubs()

_TYPO_MODULE: Optional[Any] = None
_EXPRESSION_MODULE: Optional[Any] = None
_LOAD_LOCK = threading.Lock()


def _typo_module():
    global _TYPO_MODULE
    with _LOAD_LOCK:
        if _TYPO_MODULE is None:
            _TYPO_MODULE = _load_module("maigo_typo_generator", "src/chat/utils/typo_generator.py")
        return _TYPO_MODULE


def _expression_module():
    global _EXPRESSION_MODULE
    with _LOAD_LOCK:
        if _EXPRESSION_MODULE is None:
            _EXPRESSION_MODULE = _load_module(
                "maigo_expression_vector_index",
                "src/chat/replyer/expression_vector_index.py",
            )
        return _EXPRESSION_MODULE


# --------------------------------------------------------------------------- #
# 能力实现
# --------------------------------------------------------------------------- #

# Python 原版 ChineseTypoGenerator 用模块级 random，Go 侧是每请求一个 *rand.Rand。
# 请求可以带 seed 让调用方复现，默认按随机种子重置，避免跨请求互相影响。
_TYPO_STATE_LOCK = threading.Lock()


def _typo(payload: Dict[str, Any]) -> Dict[str, Any]:
    module = _typo_module()
    config = payload.get("config") or {}
    generator = module.ChineseTypoGenerator(
        error_rate=float(config.get("error_rate", 0.3)),
        min_freq=int(config.get("min_freq", 5)),
        tone_error_rate=float(config.get("tone_error_rate", 0.2)),
        word_replace_rate=float(config.get("word_replace_rate", 0.3)),
    )
    sentences = payload.get("sentences") or []
    if not isinstance(sentences, list):
        raise ValueError("sentences 必须是字符串数组")
    seed = payload.get("seed")
    with _TYPO_STATE_LOCK:
        if seed is not None:
            random.seed(int(seed))
        else:
            random.seed()
        results: List[Dict[str, Any]] = []
        for sentence in sentences:
            typoed, correction = generator.create_typo_sentence(str(sentence))
            results.append({"text": typoed, "correction": correction})
    return {"sentences": results}


def _kmeans(payload: Dict[str, Any]) -> Dict[str, Any]:
    module = _expression_module()
    np = module.np
    index = module.ExpressionVectorIndex
    rows, cols = int(payload["rows"]), int(payload["cols"])
    raw: List[float] = payload["vectors"]
    if rows < 0 or cols < 0:
        raise ValueError("rows/cols 不能为负")
    if rows * cols != len(raw):
        raise ValueError(f"向量长度不匹配: rows={rows} cols={cols} len={len(raw)}")
    cluster_count = int(payload["cluster_count"])
    if rows == 0:
        return {"labels": [], "centers": [], "centers_rows": 0, "centers_cols": 0}

    vectors = np.asarray(raw, dtype=np.float32).reshape(rows, cols)
    # 对齐 Go 侧：先按行 L2 归一化，再做 cosine k-means 与中心计算。
    normalized = module.l2_normalize(vectors)

    # initial_centers 对应 Go 侧复用上一轮中心的增量路径。
    initial_centers = payload.get("initial_centers")
    initial_array = None
    if initial_centers:
        initial_rows = int(payload.get("initial_rows") or 0)
        initial_cols = int(payload.get("initial_cols") or 0)
        if initial_rows <= 0 or initial_cols <= 0 or initial_rows * initial_cols != len(initial_centers):
            raise ValueError("initial_centers 维度不合法")
        initial_array = np.asarray(initial_centers, dtype=np.float32).reshape(initial_rows, initial_cols)

    max_iter = int(payload.get("max_iter") or 0)
    if max_iter <= 0:
        max_iter = 100
    labels = index._run_kmeans(
        normalized,
        cluster_count=cluster_count,
        initial_centers=initial_array,
        seed=int(payload.get("seed") or 20260621),
        max_iter=max_iter,
    )
    centers = index._build_cluster_centers_from_labels(normalized, labels, cluster_count)
    return {
        "labels": [int(value) for value in labels.tolist()],
        "centers": [float(value) for value in centers.reshape(-1).tolist()],
        "centers_rows": int(centers.shape[0]),
        "centers_cols": int(centers.shape[1]),
    }


_ROUTES = {
    "/typo": _typo,
    "/kmeans": _kmeans,
}


# --------------------------------------------------------------------------- #
# HTTP
# --------------------------------------------------------------------------- #


class Handler(BaseHTTPRequestHandler):
    server_version = "MaiGoCompatSidecar/1.0"

    def log_message(self, fmt: str, *args: Any) -> None:
        print("[compat] " + fmt % args, file=sys.stderr, flush=True)

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

    def _mark_active(self) -> None:
        global _LAST_ACTIVE
        with _IN_FLIGHT_LOCK:
            _LAST_ACTIVE = time.monotonic()

    def _begin_flight(self) -> None:
        global _IN_FLIGHT, _LAST_ACTIVE
        with _IN_FLIGHT_LOCK:
            _IN_FLIGHT += 1
            _LAST_ACTIVE = time.monotonic()

    def _end_flight(self) -> None:
        global _IN_FLIGHT, _LAST_ACTIVE
        with _IN_FLIGHT_LOCK:
            _IN_FLIGHT = max(0, _IN_FLIGHT - 1)
            _LAST_ACTIVE = time.monotonic()

    def do_GET(self) -> None:  # noqa: N802 - BaseHTTPRequestHandler API
        path = self.path.split("?", 1)[0].rstrip("/") or "/"
        if path not in {"/", "/health", "/status"}:
            self._send_json(404, {"detail": "not found"})
            return
        if not self._authorized():
            return
        self._mark_active()
        self._send_json(
            200,
            {
                "status": "ok",
                "routes": sorted(_ROUTES),
                "source_root": str(SOURCE_ROOT),
            },
        )

    def do_POST(self) -> None:  # noqa: N802 - BaseHTTPRequestHandler API
        path = self.path.split("?", 1)[0].rstrip("/") or "/"
        handler = _ROUTES.get(path)
        if handler is None:
            self._send_json(404, {"detail": "not found"})
            return
        if not self._authorized():
            return
        self._begin_flight()
        try:
            length = int(self.headers.get("Content-Length", "0") or "0")
            if length <= 0 or length > MAX_BODY:
                self._send_json(400, {"detail": "invalid body length"})
                return
            payload = json.loads(self.rfile.read(length).decode("utf-8"))
            if not isinstance(payload, dict):
                self._send_json(400, {"detail": "body must be a JSON object"})
                return
            self._send_json(200, handler(payload))
        except Exception as exc:  # noqa: BLE001 - 侧车把失败统一报给调用方
            self._send_json(500, {"detail": str(exc)})
        finally:
            self._end_flight()


def _force_exit(code: int = 0, delay: float = 1.0) -> None:
    """ThreadingHTTPServer.shutdown 可能卡住 serve_forever；超时后强退释放 RSS。"""

    def _exit_later() -> None:
        time.sleep(delay)
        print(f"[compat] force exit after {delay:.1f}s", file=sys.stderr, flush=True)
        os._exit(code)

    threading.Thread(target=_exit_later, daemon=True).start()


def _idle_watch(httpd: ThreadingHTTPServer) -> None:
    if IDLE_SECONDS <= 0:
        return
    while not _IDLE_STOP.wait(1.0):
        with _IN_FLIGHT_LOCK:
            idle_for = time.monotonic() - _LAST_ACTIVE
            busy = _IN_FLIGHT > 0
        if busy or idle_for < IDLE_SECONDS:
            continue
        print(
            f"[compat] idle {idle_for:.1f}s >= {IDLE_SECONDS}s, shutting down",
            file=sys.stderr,
            flush=True,
        )
        _IDLE_STOP.set()
        threading.Thread(target=httpd.shutdown, daemon=True).start()
        # shutdown 汇合失败时进程会僵死占内存；1s 后 os._exit 兜底
        _force_exit(0, 1.0)
        return


def main() -> int:
    httpd = ThreadingHTTPServer((HOST, PORT), Handler)

    def shutdown(_signum: int, _frame: Any) -> None:
        _IDLE_STOP.set()
        threading.Thread(target=httpd.shutdown, daemon=True).start()
        _force_exit(0, 2.0)

    signal.signal(signal.SIGTERM, shutdown)
    signal.signal(signal.SIGINT, shutdown)
    print(f"[compat] listening on {HOST}:{PORT} (source_root={SOURCE_ROOT})", file=sys.stderr, flush=True)
    idle_thread = threading.Thread(target=_idle_watch, args=(httpd,), daemon=True)
    idle_thread.start()
    try:
        httpd.serve_forever()
    finally:
        _IDLE_STOP.set()
        httpd.server_close()
    return 0}Report the result once you have determined it. Use a tool if needed to gather more information before reporting. Use multiple tools in parallel if needed. Do what you can to continue making progress if you can. Then summarize your findings.


if __name__ == "__main__":
    raise SystemExit(main())
