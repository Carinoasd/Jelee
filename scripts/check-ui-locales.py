#!/usr/bin/env python3
"""Check the four UI catalogs under web/src/i18n; does not audit frontend text usage."""
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


def load(path):
    data = json.loads(path.read_text(encoding="utf-8"), object_pairs_hook=unique_object)
    if not isinstance(data, dict) or not data:
        raise ValueError(f"{path}: expected nonempty object")
    if any(not isinstance(v, str) or not v.strip() for v in data.values()):
        raise ValueError(f"{path}: expected nonempty string values")
    return data


def check(root):
    """Check web/src/i18n/<locale>/*.json: four locales, same files, same keys."""
    entries = sorted(p for p in root.iterdir())
    if any(not p.is_dir() for p in entries) or {p.name for p in entries} != LOCALES:
        raise ValueError("UI catalogs must contain exactly zh-CN, zh-TW, ja-JP, en-US directories")
    catalogs = {}
    for folder in entries:
        files = sorted(folder.iterdir())
        if not files or any(not p.is_file() or p.suffix != ".json" for p in files):
            raise ValueError(f"{folder.name}: expected only nonempty set of JSON files")
        catalogs[folder.name] = {p.name: load(p) for p in files}
    base = catalogs["en-US"]
    for locale, namespaces in catalogs.items():
        if namespaces.keys() != base.keys():
            raise ValueError(f"{locale}: files differ from en-US: {sorted(namespaces.keys() ^ base.keys())}")
        for name, reference in base.items():
            data = namespaces[name]
            missing = sorted(reference.keys() - data.keys())
            extra = sorted(data.keys() - reference.keys())
            if missing or extra:
                raise ValueError(f"{locale}/{name}: missing={missing}, extra={extra}")
            for key in reference:
                if collections.Counter(SLOT.findall(reference[key])) != collections.Counter(SLOT.findall(data[key])):
                    raise ValueError(f"{locale}/{name}: placeholder mismatch for {key}")
    keys = sum(len(v) for v in base.values())
    print(f"UI catalogs: {len(catalogs)} locales, {len(base)} files, {keys} keys; JSON, key and placeholder parity passed")


def main():
    if len(sys.argv) == 2:
        root = pathlib.Path(sys.argv[1])
    elif len(sys.argv) == 1:
        root = pathlib.Path(__file__).resolve().parents[1] / "web" / "src" / "i18n"
    else:
        raise ValueError("expected at most one catalog root directory")
    check(root)


if __name__ == "__main__":
    try:
        main()
    except (ValueError, OSError, UnicodeError) as error:
        print(f"UI catalog check failed: {error}", file=sys.stderr)
        sys.exit(1)
