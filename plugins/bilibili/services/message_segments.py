from __future__ import annotations

import base64
from dataclasses import dataclass
from pathlib import Path
from typing import Any


@dataclass
class Plain:
    text: str

    def to_send_segment(self) -> dict[str, Any]:
        return {"type": "text", "content": self.text}


@dataclass
class Image:
    content: str
    kind: str = "base64"

    @classmethod
    def fromURL(cls, url: str) -> "Image":
        return cls(content=url, kind="url")

    @classmethod
    def fromFileSystem(cls, path: str) -> "Image":
        data = Path(path).read_bytes()
        return cls(content=base64.b64encode(data).decode("ascii"), kind="base64")

    def to_send_segment(self) -> dict[str, Any]:
        if self.kind == "url":
            return {"type": "image", "content": self.content}
        return {"type": "image", "content": self.content}


@dataclass
class File:
    file: str
    name: str = ""

    def to_send_segment(self) -> dict[str, Any]:
        # MaiBot 发送能力没有通用文件段时，退化为图片/文本路径提示。
        path = Path(self.file)
        if path.is_file() and path.suffix.lower() in {".png", ".jpg", ".jpeg", ".webp", ".gif"}:
            return Image.fromFileSystem(str(path)).to_send_segment()
        return {"type": "text", "content": f"[文件] {self.name or path.name}"}


@dataclass
class At:
    qq: str

    def to_send_segment(self) -> dict[str, Any]:
        return {"type": "at", "content": str(self.qq)}


@dataclass
class AtAll:
    def to_send_segment(self) -> dict[str, Any]:
        return {"type": "at", "content": "all"}


def segments_to_hybrid(parts: list[Any]) -> list[dict[str, Any]]:
    out: list[dict[str, Any]] = []
    for part in parts:
        if hasattr(part, "to_send_segment"):
            out.append(part.to_send_segment())
        elif isinstance(part, dict):
            out.append(part)
        else:
            out.append({"type": "text", "content": str(part)})
    return out


def segments_to_forward(parts: list[Any], *, nickname: str = "Bilibili") -> list[dict[str, Any]]:
    return [
        {
            "user_id": "0",
            "nickname": nickname,
            "segments": segments_to_hybrid(parts),
        }
    ]