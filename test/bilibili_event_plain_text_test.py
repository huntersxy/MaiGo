"""bilibili ON_MESSAGE 正文字段优先级局部测试。"""

from __future__ import annotations

import sys
import unittest
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]
sys.path.insert(0, str(ROOT / "plugins" / "bilibili"))

from core.message_text import event_message_plain_text  # noqa: E402


class EventMessagePlainTextTests(unittest.TestCase):
    def test_prefers_processed_plain_text(self):
        got = event_message_plain_text(
            {
                "processed_plain_text": "https://www.bilibili.com/video/BV1ktyszKEZg",
                "plain_text": "",
                "content": "ignore",
            }
        )
        self.assertIn("BV1ktyszKEZg", got)

    def test_falls_back_to_plain_text_then_content(self):
        self.assertEqual(event_message_plain_text({"plain_text": "BV1aaa", "content": "BV1bbb"}), "BV1aaa")
        self.assertEqual(event_message_plain_text({"content": "BV1ccc"}), "BV1ccc")

    def test_non_dict_message(self):
        self.assertEqual(event_message_plain_text("BV1ddd"), "BV1ddd")


if __name__ == "__main__":
    unittest.main()
