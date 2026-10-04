#!/usr/bin/env python3
"""Run one deterministic shard of a Go package's top-level tests.

The PostgreSQL repository package no longer fits one 45-minute race run on a
CI runner, so CI splits it across jobs. Names come from `go test -list`, are
sorted and dealt round-robin; every test lands in exactly one shard, and an
empty shard is an error rather than a silent pass.

Usage: go_test_shard.py --go GO --package PKG --shard I/N [-- go test flags]
"""
import argparse
import re
import subprocess
import sys

NAME = re.compile(r"^(Test|Fuzz|Example)[A-Za-z0-9_]*$")


def parse_shard(value):
    match = re.fullmatch(r"([1-9][0-9]*)/([1-9][0-9]*)", value)
    if not match or int(match.group(1)) > int(match.group(2)):
        raise argparse.ArgumentTypeError("shard must be I/N with 1 <= I <= N")
    return int(match.group(1)), int(match.group(2))


def select(names, index, count):
    """Returns the sorted names of shard index (1-based) out of count."""
    return [name for position, name in enumerate(sorted(set(names))) if position % count == index - 1]


def main(argv):
    parser = argparse.ArgumentParser()
    parser.add_argument("--go", required=True)
    parser.add_argument("--package", required=True)
    parser.add_argument("--shard", required=True, type=parse_shard)
    parser.add_argument("flags", nargs=argparse.REMAINDER)
    args = parser.parse_args(argv)
    flags = args.flags[1:] if args.flags[:1] == ["--"] else args.flags
    listing = subprocess.run([args.go, "test", "-list", ".*", args.package], check=True, capture_output=True, text=True).stdout
    names = [line.strip() for line in listing.splitlines() if NAME.match(line.strip())]
    chosen = select([name for name in names if name.startswith("Test")], *args.shard)
    if not chosen:
        print(f"shard {args.shard[0]}/{args.shard[1]} of {args.package} selects no tests", file=sys.stderr)
        return 1
    print(f"shard {args.shard[0]}/{args.shard[1]}: {len(chosen)} tests of {args.package}", flush=True)
    pattern = "^(" + "|".join(chosen) + ")$"
    return subprocess.run([args.go, "test", *flags, "-run", pattern, args.package]).returncode


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
