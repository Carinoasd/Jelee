#!/usr/bin/env python3
import importlib.util
from pathlib import Path
import shutil
import sys
import types
import unittest
import uuid

sys.dont_write_bytecode = True
spec = importlib.util.spec_from_file_location("bench_compare", Path(__file__).with_name("bench-compare.py"))
bench_compare = importlib.util.module_from_spec(spec)
spec.loader.exec_module(bench_compare)


class BenchCompareTest(unittest.TestCase):
    def setUp(self):
        self.root = bench_compare.ROOT / ".testdata" / ("bench-compare-tests-" + uuid.uuid4().hex)
        self.root.mkdir(parents=True)

    def tearDown(self):
        shutil.rmtree(self.root)

    def write(self, name, text):
        path = self.root / name
        path.parent.mkdir(parents=True, exist_ok=True)
        path.write_text(text, encoding="utf-8")

    def test_only_packages_with_benchmarks(self):
        self.write("a/x_test.go", "package a\nfunc BenchmarkX(b *testing.B) {}\n")
        self.write("b/y_test.go", "package b\nfunc TestY(t *testing.T) {}\n")
        self.write("c/z.go", "package c\nfunc BenchmarkZ(b *testing.B) {}\n")
        got = bench_compare.benchmark_packages(self.root, ["a", "b", "c", "missing"])
        self.assertEqual(got, ["a"])

    def test_command_is_serial_and_pinned(self):
        args = types.SimpleNamespace(bench=".", skip="Slow", count=2, benchtime="100ms")
        command = bench_compare.bench_command("go", ["./a"], args)
        self.assertEqual(command[:4], ["go", "test", "-p", "1"])
        self.assertIn("-benchmem", command)
        self.assertEqual(command[-1], "./a")

    def test_rejects_non_commit_refs(self):
        for ref in ("HEAD; rm -rf /", "--upload-pack=x", "main"):
            with self.subTest(ref=ref), self.assertRaises(SystemExit):
                bench_compare.main(["--base-ref", ref, "--packages", "./a"])


if __name__ == "__main__":
    unittest.main(verbosity=2)
