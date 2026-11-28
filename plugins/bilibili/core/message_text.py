"""ON_MESSAGE 事件正文字段读取。"""

from __future__ import annotations

from typing import Any


def event_message_plain_text(message: Any) -> str:
    """读取事件正文；MaiBot 事件优先提供 processed_plain_text。"""
    if isinstance(message, dict):
        for key in ("processed_plain_text", "plain_text", "content"):
            value = message.get(key)
            if value is None:
                continue
            text = str(value).strip()
            if text:
                return text
        return ""
    return str(message)
