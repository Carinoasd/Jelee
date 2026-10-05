#!/usr/bin/env python3
"""Recompute the progress tables of docs/requirements-traceability.md.

The matrix rows are the source of truth: every row starting with
``| **Gnn.x**`` carries its status at the start of the fifth cell (已完成,
部分完成, 阻塞 or 未开始/未開始). This script counts them and rewrites the
overall table, the per-group table and the per-phase table of the "进度统计"
section, so the totals never drift from the rows. The same counts are
written into the owner summary (docs/progress-summary.md): its overall
table, its phase table and its two ratio sentences.

Usage: python3 scripts/traceability_stats.py [--check] [matrix [summary]]
  --check  exit 1 (and print a diff summary) when the tables are stale,
           instead of rewriting the files.
"""

import re
import sys

STATUSES = ("已完成", "部分完成", "阻塞", "未开始")
ROW = re.compile(r"^\| \*\*G(\d\d)\.([0-9a-z]+)\*\*")
CELL = re.compile(r"(?<!\\)\|")
STATUS = re.compile(r"^(已完成|部分完成|阻塞|未开始|未開始)")


def classify(line):
    """Return (group, status) for a matrix row, or None for other lines."""
    match = ROW.match(line)
    if not match:
        return None
    cells = [c.strip() for c in CELL.split(line)[1:-1]]
    if len(cells) < 5:
        raise ValueError("matrix row has fewer than five cells: " + line[:80])
    status = STATUS.match(cells[4])
    if not status:
        raise ValueError("matrix row has no status: " + line[:80])
    value = status.group(1)
    return "G" + match.group(1), "未开始" if value == "未開始" else value


def percent(part, whole, digits=0):
    """Round half to even like the existing tables (1/8 is 12%)."""
    value = round(100 * part / whole, digits) if whole else 0
    return ("%." + str(digits) + "f%%") % value if digits else "%d%%" % value


def count(lines):
    groups = {}
    for line in lines:
        row = classify(line)
        if row is None:
            continue
        group, status = row
        groups.setdefault(group, dict.fromkeys(STATUSES, 0))[status] += 1
    return groups


TRADITIONAL = {"未開始": "未开始"}


def rewrite(text, groups=None):
    lines = text.split("\n")
    if groups is None:
        groups = count(lines)
    total = {s: sum(g[s] for g in groups.values()) for s in STATUSES}
    rows = sum(total.values())
    out = []
    for line in lines:
        cells = [c.strip() for c in CELL.split(line)[1:-1]] if line.startswith("| ") else []
        # Overall table: | 状态 | 子项数 | 占比 |
        if len(cells) == 3 and TRADITIONAL.get(cells[0], cells[0]) in STATUSES + ("合计",):
            n = rows if cells[0] == "合计" else total[TRADITIONAL.get(cells[0], cells[0])]
            share = "100%" if cells[0] == "合计" else percent(n, rows, 1)
            line = "| %s | %d | %s |" % (cells[0], n, share)
        # Per-group table: | Gnn | rows | done | partial | blocked | not started | done% | implemented% |
        elif len(cells) == 8 and re.fullmatch(r"G\d\d", cells[0]) and cells[0] in groups:
            g = groups[cells[0]]
            n = sum(g.values())
            line = "| %s | %d | %d | %d | %d | %d | %s | %s |" % (
                cells[0], n, g["已完成"], g["部分完成"], g["阻塞"], g["未开始"],
                percent(g["已完成"], n), percent(g["已完成"] + g["部分完成"], n))
        # Per-phase table: | phase | Gaa–Gbb | rows | done | partial | blocked | not started | [note] |
        elif len(cells) in (7, 8) and re.fullmatch(r"G\d\d–G\d\d", cells[1]):
            low, high = (int(x[1:]) for x in cells[1].split("–"))
            members = [g for name, g in groups.items() if low <= int(name[1:]) <= high]
            sums = {s: sum(g[s] for g in members) for s in STATUSES}
            line = "| %s | %s | %d | %d | %d | %d | %d |" % (
                cells[0], cells[1], sum(sums.values()), sums["已完成"], sums["部分完成"], sums["阻塞"], sums["未开始"])
            if len(cells) == 8:
                line += " %s |" % cells[7]
        else:
            done, implemented = total["已完成"], total["已完成"] + total["部分完成"]
            line = re.sub(r"驗收完成度是 [0-9.]+%（\d+／\d+）", "驗收完成度是 %s（%d／%d）" % (percent(done, rows, 1), done, rows), line)
            line = re.sub(r"有實作的比例是 [0-9.]+%（\d+／\d+）", "有實作的比例是 %s（%d／%d）" % (percent(implemented, rows, 1), implemented, rows), line)
        out.append(line)
    return "\n".join(out), total, rows


def main(argv):
    check = "--check" in argv
    paths = [a for a in argv if a != "--check"]
    matrix = paths[0] if paths else "docs/requirements-traceability.md"
    summary_path = paths[1] if len(paths) > 1 else ("docs/progress-summary.md" if not paths else None)
    with open(matrix, encoding="utf-8") as f:
        text = f.read()
    groups = count(text.split("\n"))
    status = 0
    for path in [matrix] + ([summary_path] if summary_path else []):
        with open(path, encoding="utf-8") as f:
            original = f.read()
        updated, total, rows = rewrite(original, groups)
        summary = ", ".join("%s %d" % (s, total[s]) for s in STATUSES)
        if check:
            if updated != original:
                changed = sum(1 for a, b in zip(original.split("\n"), updated.split("\n")) if a != b)
                print("%s: %d table lines are stale (%d rows: %s)" % (path, changed, rows, summary))
                status = 1
            else:
                print("%s: tables match %d rows (%s)" % (path, rows, summary))
            continue
        if updated != original:
            with open(path, "w", encoding="utf-8") as f:
                f.write(updated)
        print("%s: %d rows (%s)" % (path, rows, summary))
    return status


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
