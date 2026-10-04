#!/usr/bin/env python3
"""Regression for the deterministic Go test sharding used by CI."""
import argparse
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


if __name__ == "__main__":
    unittest.main()
