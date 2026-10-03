#!/usr/bin/env python3
"""Check the four embedded UI catalogs; does not audit frontend text usage."""
import collections
import json
import pathlib
import re
import sys

LOCALES = {"zh-CN", "zh-TW", "ja-JP", "en-US"}
SLOT = re.compile(r"(?<!\{)\{(\d+)(?:,-?\d+)?(?::[^{}]+)?\}(?!\})")


def unique_object(pairs):
    result = {}
    for key, value in pairs:
        if key in result:
            raise ValueError(f"duplicate key: {key}")
        result[key] = value
    return result


def check(folder):
    paths = sorted(folder.glob("*.json"))
    if {p.stem for p in paths} != LOCALES:
        raise ValueError("UI catalogs must contain exactly zh-CN, zh-TW, ja-JP, en-US")
    catalogs = {}
    for path in paths:
        data = json.loads(path.read_text(encoding="utf-8"), object_pairs_hook=unique_object)
        if not isinstance(data, dict) or not data:
            raise ValueError(f"{path.name}: expected nonempty object")
        if any(not isinstance(v, str) or not v.strip() for v in data.values()):
            raise ValueError(f"{path.name}: expected nonempty string values")
        catalogs[path.stem] = data
    base = catalogs["en-US"]
    for locale, data in catalogs.items():
        missing = sorted(base.keys() - data.keys())
        extra = sorted(data.keys() - base.keys())
        if missing or extra:
            raise ValueError(f"{locale}: missing={missing}, extra={extra}")
        for key in base:
            if collections.Counter(SLOT.findall(base[key])) != collections.Counter(SLOT.findall(data[key])):
                raise ValueError(f"{locale}: placeholder mismatch for {key}")
    print(f"UI catalogs: {len(catalogs)} locales, {len(base)} keys; JSON, key and placeholder parity passed")


def main():
    roots = list(pathlib.Path(__file__).resolve().parents[1].glob("*/Localization/Core"))
    if len(sys.argv) == 2:
        folder = pathlib.Path(sys.argv[1])
    elif len(sys.argv) == 1 and len(roots) == 1:
        folder = roots[0]
    else:
        raise ValueError("expected one catalog directory")
    check(folder)


if __name__ == "__main__":
    try:
        main()
    except (ValueError, OSError, UnicodeError) as error:
        print(f"UI catalog check failed: {error}", file=sys.stderr)
        sys.exit(1)
