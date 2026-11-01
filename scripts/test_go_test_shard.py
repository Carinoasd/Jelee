#!/usr/bin/env python3
"""Regression for the deterministic Go test sharding used by CI."""
import argparse
import contextlib
import io
import os
import stat
import tempfile
import unittest

import go_test_shard


class ShardTest(unittest.TestCase):
    def test_every_name_lands_in_exactly_one_shard(self):
        names = [f"Test{i:03d}" for i in range(23)] + ["TestA", "TestA"]
        shards = [go_test_shard.select(names, i, 4) for i in range(1, 5)]
        flat = [name for shard in shards for name in shard]
        self.assertEqual(sorted(flat), sorted(set(names)))
        self.assertEqual(len(flat), len(set(flat)))
        self.assertLessEqual(max(map(len, shards)) - min(map(len, shards)), 1)

    def test_selection_is_independent_of_listing_order(self):
        names = ["TestC", "TestA", "TestB", "TestD"]
        self.assertEqual(go_test_shard.select(names, 2, 2), go_test_shard.select(list(reversed(names)), 2, 2))

    def test_invalid_shards_are_rejected(self):
        for value in ("0/4", "5/4", "1/0", "a/b", "1/", "-1/2"):
            with self.assertRaises(argparse.ArgumentTypeError):
                go_test_shard.parse_shard(value)
        self.assertEqual(go_test_shard.parse_shard("4/4"), (4, 4))

    @unittest.skipIf(os.name == "nt", "the stand-in go is a shell script")
    def test_listing_failure_prints_its_output(self):
        with tempfile.TemporaryDirectory() as tmp:
            fake = os.path.join(tmp, "go")
            with open(fake, "w", encoding="utf-8") as f:
                f.write("#!/bin/sh\necho 'compile error: undefined: Foo' >&2\necho 'FAIL example/pkg [build failed]'\nexit 3\n")
            os.chmod(fake, os.stat(fake).st_mode | stat.S_IXUSR)
            stderr = io.StringIO()
            with contextlib.redirect_stderr(stderr):
                code = go_test_shard.main(["--go", fake, "--package", "./pkg", "--shard", "1/2"])
        self.assertEqual(code, 3)
        self.assertIn("compile error: undefined: Foo", stderr.getvalue())
        self.assertIn("FAIL example/pkg [build failed]", stderr.getvalue())
        self.assertIn("go test -list ./pkg failed with exit status 3", stderr.getvalue())


if __name__ == "__main__":
    unittest.main()
