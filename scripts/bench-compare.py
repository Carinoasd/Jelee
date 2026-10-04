#!/usr/bin/env python3
"""Same-runner benchmark regression gate (G26.4, G38.1).

CI runners differ from the developer machine that produced
docs/evidence/bench-baseline.txt and from each other, so CI never compares
against that file. Instead this script checks the base commit out into a
temporary git worktree and runs the hot-path benchmarks of the base and the
head alternately on the same runner, then gates the medians with
tools/benchgate. Alternating rounds spread slow drift (thermal, noisy
neighbours) over both sides instead of penalising whichever ran second.

Only Python's standard library is required; Go comes from .bin/go.
"""
import argparse
from pathlib import Path
import re
import shutil
import subprocess
import sys

ROOT = Path(__file__).resolve().parent.parent
BENCH_FUNC = re.compile(r"^func Benchmark\w*\(b \*testing\.B\)", re.MULTILINE)


def run(args, cwd, stdout=None):
    print("+ " + " ".join(args), flush=True)
    subprocess.run(args, cwd=cwd, check=True, stdout=stdout)


def benchmark_packages(tree, packages):
    """Keep the packages that exist and declare benchmarks in this tree."""
    present = []
    for package in packages:
        directory = tree / package
        if directory.is_dir() and any(BENCH_FUNC.search(path.read_text(encoding="utf-8", errors="replace"))
                                      for path in sorted(directory.glob("*_test.go"))):
            present.append(package)
    return present


def bench_command(go, packages, args):
    return [go, "test", "-p", "1", "-run", "^$", "-bench", args.bench, "-skip", args.skip, "-benchmem",
            "-count=" + str(args.count), "-benchtime=" + args.benchtime] + packages


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--base-ref", required=True, help="commit to compare against (PR base or previous push)")
    parser.add_argument("--packages", required=True, help="space-separated ./package list (Makefile BENCH_PKGS)")
    parser.add_argument("--bench", default=".")
    parser.add_argument("--skip", default="ObservedHundredFiles")
    parser.add_argument("--rounds", type=int, default=3, help="alternating base/head rounds")
    parser.add_argument("--count", type=int, default=2, help="-count per side and round")
    parser.add_argument("--benchtime", default="500ms")
    parser.add_argument("--out", default=".testdata/bench-compare")
    parser.add_argument("--go", default=str(ROOT / ".bin" / "go"), help="pinned go wrapper")
    parser.add_argument("--gate", default="-ns 25 -allocs 10 -bytes 20",
                        help="tools/benchgate thresholds for runner-to-runner noise")
    args = parser.parse_args(argv)
    if not re.fullmatch(r"[0-9a-f]{7,40}", args.base_ref):
        parser.error("--base-ref must be a commit hash")
    if args.rounds < 1 or args.count < 1:
        parser.error("--rounds and --count must be positive")
    go = args.go
    out = ROOT / args.out
    base_tree = out / "base-tree"
    out.mkdir(parents=True, exist_ok=True)
    if base_tree.exists():
        run(["git", "worktree", "remove", "--force", str(base_tree)], ROOT)
    packages = args.packages.split()
    run(["git", "worktree", "add", "--detach", str(base_tree), args.base_ref], ROOT)
    try:
        sides = {"base": (base_tree, benchmark_packages(base_tree, packages)),
                 "head": (ROOT, benchmark_packages(ROOT, packages))}
        missing = sorted(set(sides["head"][1]) - set(sides["base"][1]))
        if missing:
            print("packages without benchmarks at the base (new, not gated): " + " ".join(missing))
        results = {name: out / (name + ".txt") for name in sides}
        for path in results.values():
            path.write_text("", encoding="utf-8")
        for round_number in range(args.rounds):
            order = ("base", "head") if round_number % 2 == 0 else ("head", "base")
            for name in order:
                tree, present = sides[name]
                if not present:
                    continue
                with results[name].open("a", encoding="utf-8") as stream:
                    run(bench_command(go, present, args), tree, stream)
        accept = ROOT / "tools" / "bench-accepted.json"
        accepted = ["-accept", str(accept)] if accept.exists() else []
        gate = subprocess.run([go, "run", "./tools/benchgate", "-base", str(results["base"]),
                               "-current", str(results["head"])] + accepted + args.gate.split(), cwd=ROOT)
        return gate.returncode
    finally:
        subprocess.run(["git", "worktree", "remove", "--force", str(base_tree)], cwd=ROOT, check=False)
        if base_tree.exists():
            shutil.rmtree(base_tree)


if __name__ == "__main__":
    try:
        sys.exit(main())
    except subprocess.CalledProcessError as error:
        print("bench-compare: " + str(error), file=sys.stderr)
        sys.exit(2)
