#!/usr/bin/env python3
"""Tests for scripts/traceability_stats.py."""

import os
import sys
import tempfile
import unittest

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
import traceability_stats as stats  # noqa: E402

SAMPLE = """# Matrix

| 状态 | 子项数 | 占比 |
| --- | --- | --- |
| 已完成 | 9 | 9% |
| 部分完成 | 9 | 9% |
| 阻塞 | 9 | 9% |
| 未开始 | 9 | 9% |
| 合计 | 9 | 100% |

| G 群组 | 子项 | 已完成 | 部分完成 | 阻塞 | 未开始 | 已完成比例 | 有实现比例（已完成＋部分完成） |
| --- | --- | --- | --- | --- | --- | --- | --- |
| G01 | 9 | 9 | 9 | 9 | 9 | 9% | 9% |
| G02 | 9 | 9 | 9 | 9 | 9 | 9% | 9% |

| 阶段 | 群组 | 子项 | 已完成 | 部分完成 | 阻塞 | 未开始 |
| --- | --- | --- | --- | --- | --- | --- |
| 全部 | G01–G02 | 9 | 9 | 9 | 9 | 9 |

| **G01.1** a | b | c | d | 已完成：x | e |
| **G01.2** a \\| pipe | b | c | d | 部分完成：y | e |
| **G01.3** a | b | c | d | 未開始：z | e |
| **G02.1** a | b | c | d | 阻塞：w | e |
| **G02.1b** a | b | c | d | 部分完成：v | e |
| **G02.2** a | b | c | d | 部分完成：stray | pipe | e |
| **G02.3** a | b | c | d | 未开始：u | e |
| **G02.4** a | b | c | d | 部分完成：t | e |
"""


class TraceabilityStatsTest(unittest.TestCase):
    def test_rewrites_every_table_from_the_rows(self):
        text, total, rows = stats.rewrite(SAMPLE)
        self.assertEqual(rows, 8)
        self.assertEqual(total, {"已完成": 1, "部分完成": 4, "阻塞": 1, "未开始": 2})
        self.assertIn("| 已完成 | 1 | 12.5% |", text)
        self.assertIn("| 部分完成 | 4 | 50.0% |", text)
        self.assertIn("| 合计 | 8 | 100% |", text)
        # Half to even: one of eight is 12%, like the hand-made tables.
        self.assertIn("| G01 | 3 | 1 | 1 | 0 | 1 | 33% | 67% |", text)
        self.assertIn("| G02 | 5 | 0 | 3 | 1 | 1 | 0% | 60% |", text)
        self.assertIn("| 全部 | G01–G02 | 8 | 1 | 4 | 1 | 2 |", text)
        again, _, _ = stats.rewrite(text)
        self.assertEqual(again, text)

    def test_check_mode_reports_stale_tables(self):
        with tempfile.TemporaryDirectory() as directory:
            path = os.path.join(directory, "matrix.md")
            with open(path, "w", encoding="utf-8") as f:
                f.write(SAMPLE)
            self.assertEqual(stats.main(["--check", path]), 1)
            self.assertEqual(stats.main([path]), 0)
            self.assertEqual(stats.main(["--check", path]), 0)

    def test_rows_without_status_are_errors(self):
        with self.assertRaises(ValueError):
            stats.rewrite("| **G01.1** a | b | c | d | 进行中 | e |\n")


if __name__ == "__main__":
    unittest.main()
