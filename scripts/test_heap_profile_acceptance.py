"""Bounded profile contracts; synthetic fixtures do not prove SDK parsing."""
import copy
import gzip
import hashlib
import json
import os
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest
from unittest import mock

import heap_profile_acceptance as h


def metadata_fixture(stage="before", body=b"x"):
    return {"version": 1, "stage": stage, "bytes": len(body), "sha256": hashlib.sha256(body).hexdigest(),
            "memProfileRate": 524288, "numGC": 1 if stage == "before" else 2,
            "elapsedNanos": 1000 if stage == "before" else 2000}


def raw_fixture(value=8192):
    return ("PeriodType: space bytes\nPeriod: 524288\nTime: 2026-10-03 00:00:00 +0000 UTC\n"
            "Samples:\nalloc_objects/count alloc_space/bytes inuse_objects/count inuse_space/bytes\n"
            f"         2      16384          1       {value}: 1 2\n"
            "                bytes:[8192]\nLocations\n"
            "     1: 0x1234 M=1 runtime.mallocgc runtime/malloc.go:1:0 s=1\n"
            "     2: 0x5678 M=1 example.org/project.work example.org/project/work.go:1:0 s=1\n"
            "Mappings\n1: 0x1000/0xffff/0x0 /worker.test buildid [FN][FL][LN][IN]\n").encode()


def top_fixture(value=8192, name="runtime.mallocgc", percentage="100%"):
    token = str(value) + "B" if value else "0"
    total = str(abs(value)) + "B" if value else "0"
    return ("File: worker.test\nType: inuse_space\n"
            f"Showing nodes accounting for {token}, {percentage} of {total} total\n"
            "      flat  flat%   sum%        cum   cum%\n"
            f"{token} {percentage} {percentage} {token} {percentage}  {name}\n").encode()


# Shortened from real `go tool pprof -top -inuse_space -unit=bytes
# -nodecount=20 -symbolize=none -base before after` output (Go 1.27.1 SDK)
# where the heap shrank: the shown sum is negative and labels are added.
SHRINKING_DIFF = (
    "File: worker.test\nBuild ID: 54229f6bb1c0faf3e4101c5d826753496a627c63\nType: inuse_space\n"
    "Time: 2026-10-04 18:17:00 CST\n"
    "Showing nodes accounting for -1096162B, 98.12% of 1117209B total\n"
    "Dropped 19 nodes (cum <= 5586B)\nShowing top 20 nodes out of 22\n"
    "      flat  flat%   sum%        cum   cum%\n"
    " -1018548B 91.17% 91.17%  -1104354B 98.85%  main.allocB\n"
    "   -85806B  7.68% 98.85%    -85806B  7.68%  internal/strconv.FormatInt\n"
    "     8192B  0.73% 98.12%      8192B  0.73%  hash/crc32.slicingMakeTable\n"
    "         0     0% 98.12%    -85806B  7.68%  strconv.Itoa (inline)\n"
    "         0     0% 98.12%  -1092069B 97.75%  runtime.main\n").encode()

# Real diff row for a generic function instantiated with a tagged struct:
# Go prints the struct tags Go-quoted inside the shape name, so `json:"-"`
# appears as json:\"-\" (a letter, a colon and a backslash).
GENERIC_DIFF = (
    "File: worker.test\nBuild ID: fdbcd0419346f405fbba0cb5242c9d13e88be695\nType: inuse_space\n"
    "Time: 2026-10-04 18:18:47 CST\n"
    "Showing nodes accounting for 826405B, 33.07% of 2499007B total\n"
    "Dropped 17 nodes (cum <= 12495B)\nShowing top 20 nodes out of 32\n"
    "      flat  flat%   sum%        cum   cum%\n"
    "  1511116B 60.47% 60.47%   1629165B 65.19%  main.allocB\n"
    "  -816402B 32.67% 27.80%   -816402B 32.67%  main.collect[go.shape.struct { RootID string "
    "\"json:\\\"-\\\"\"; Directory string \"json:\\\"dir\\\"\"; X int \"x:\\\"y\\\"\" }]\n"
    "         0     0% 33.07%    823889B 32.97%  runtime.main\n").encode()
GENERIC_NAME = 'main.collect[go.shape.struct { RootID string "json:\\"-\\""; Directory string "json:\\"dir\\""; X int "x:\\"y\\"" }]'


def diff_fixture(rows, shown="0", percentage="0%", total="0"):
    return ("File: worker.test\nType: inuse_space\n"
            f"Showing nodes accounting for {shown}, {percentage} of {total} total\n"
            "      flat  flat%   sum%        cum   cum%\n" + "".join(row + "\n" for row in rows)).encode()


class HeapProfileTests(unittest.TestCase):
    def rejected(self, call, *args, **kwargs):
        with self.assertRaises(h.Rejected) as raised:
            call(*args, **kwargs)
        self.assertEqual(str(raised.exception), "heap_profile_evidence_invalid")
        self.assertIsNone(raised.exception.__cause__)

    def test_metadata_is_safe_owned_and_pair_is_ordered_with_unchanged_rate(self):
        records = [metadata_fixture(), metadata_fixture("after")]
        records[0]["private"] = "secret/private"
        original = copy.deepcopy(records)
        result = h.validate_heap_profiles(records)
        self.assertNotIn("secret", json.dumps(result))
        result[0]["bytes"] = 2
        self.assertEqual(records, original)
        for invalid in ([], records[:1], records * 2, list(reversed(records)), None, {}, "secret/private"):
            self.rejected(h.validate_heap_profiles, invalid)
        for key, value in (("memProfileRate", 1), ("numGC", 0), ("elapsedNanos", 1000)):
            changed = copy.deepcopy(records)
            changed[1][key] = value
            self.rejected(h.validate_heap_profiles, changed)
        for key in ("version", "bytes", "memProfileRate", "numGC", "elapsedNanos"):
            for invalid in (True, False, -1, 1.0, float("nan"), "1", None, 1 << 64):
                changed = metadata_fixture()
                changed[key] = invalid
                self.rejected(h.validate_heap_metadata, changed)
        for key, value in (("bytes", 0), ("bytes", 8388609), ("memProfileRate", 0),
                           ("stage", "secret/private"), ("sha256", "F" * 64), ("sha256", "a" * 63)):
            changed = metadata_fixture()
            changed[key] = value
            self.rejected(h.validate_heap_metadata, changed)
        for key in metadata_fixture():
            changed = metadata_fixture()
            del changed[key]
            self.rejected(h.validate_heap_metadata, changed)
        self.rejected(h.validate_heap_metadata, metadata_fixture(), "after")

    def test_file_requires_regular_complete_gzip_with_exact_size_and_hash(self):
        with tempfile.TemporaryDirectory() as folder:
            path = Path(folder) / "before.heap.pb.gz"
            body = gzip.compress(b"synthetic protobuf placeholder")
            path.write_bytes(body)
            record = metadata_fixture(body=body)
            result = h.validate_heap_file(path, record)
            self.assertTrue(result["fileValidated"])
            self.assertEqual(result["expandedBytes"], 30)
            self.assertNotIn(folder, json.dumps(result))
            self.rejected(h.validate_heap_file, Path(folder), record)
            self.rejected(h.validate_heap_file, Path(folder) / "missing", record)
            for key, value in (("bytes", len(body) + 1), ("sha256", "0" * 64)):
                changed = dict(record, **{key: value})
                self.rejected(h.validate_heap_file, path, changed)
            for malformed in (b"not-gzip", body[:-1], body[:-8], body[:-8] + b"\0" * 8, gzip.compress(b"")):
                path.write_bytes(malformed)
                self.rejected(h.validate_heap_file, path, metadata_fixture(body=malformed))

    def test_compressed_and_expanded_caps_and_symlinks_cannot_be_bypassed(self):
        with tempfile.TemporaryDirectory() as folder:
            path = Path(folder) / "before.heap.pb.gz"
            body = gzip.compress(b"x" * 1024)
            path.write_bytes(body)
            with mock.patch.object(h, "MAX_EXPANDED_BYTES", 1024):
                self.assertEqual(h.validate_heap_file(path, metadata_fixture(body=body))["expandedBytes"], 1024)
            with mock.patch.object(h, "MAX_EXPANDED_BYTES", 1023):
                self.rejected(h.validate_heap_file, path, metadata_fixture(body=body))
            with mock.patch.object(h, "MAX_PROFILE_BYTES", len(body) - 1):
                self.rejected(h.validate_heap_file, path, metadata_fixture(body=body))
            link = Path(folder) / "link"
            try:
                link.symlink_to(path)
            except (OSError, NotImplementedError):
                # Windows may prohibit creating symlinks without privilege;
                # exercise the same lstat rejection without claiming OS support.
                info = path.stat()
                synthetic = os.stat_result((0o120777, info.st_ino, info.st_dev, 1, 0, 0, info.st_size, 0, 0, 0))
                with mock.patch.object(Path, "lstat", return_value=synthetic):
                    self.rejected(h.validate_heap_file, path, metadata_fixture(body=body))
            else:
                self.rejected(h.validate_heap_file, link, metadata_fixture(body=body))

    def test_raw_privacy_checks_all_sections_and_requires_actual_heap_sample_contract(self):
        result = h.validate_heap_raw(raw_fixture(), metadata_fixture())
        self.assertEqual(result["sampledInuseBytes"], 8192)
        self.assertEqual(result["sampleCount"], 1)
        for leaked in (b"C:\\Users\\someone\\work.go", b"/home/someone/work.go", b"postgresql://private:password@host/db"):
            self.rejected(h.validate_heap_raw, raw_fixture() + leaked, metadata_fixture())
        self.rejected(h.validate_heap_raw, raw_fixture() + b"/opt/OWNED/secret", metadata_fixture(), ["/opt/owned"], [])
        self.rejected(h.validate_heap_raw, raw_fixture() + b"PRIVATE_FIXTURE", metadata_fixture(), [], ["PRIVATE_FIXTURE"])
        for malformed in (raw_fixture().replace(b"inuse_space/bytes", b"inuse_space/count"),
                          raw_fixture().replace(b"Period: 524288", b"Period: 1"),
                          raw_fixture().replace(b"Mappings\n", b""), raw_fixture(value=-1),
                          raw_fixture() + b"\xff", raw_fixture() + b"\x00"):
            self.rejected(h.validate_heap_raw, malformed, metadata_fixture())
        with mock.patch.object(h, "MAX_RAW_BYTES", len(raw_fixture()) - 1):
            self.rejected(h.validate_heap_raw, raw_fixture(), metadata_fixture())

    def test_top_preserves_signed_bytes_and_labels_magnitude_separately(self):
        result = h.summarize_heap_top(top_fixture(-4096, "runtime.mallocgc (inline)", "-100%"))
        self.assertEqual(result["totalMagnitudeBytes"], 4096)
        self.assertEqual(result["functions"], [{"function": "runtime.mallocgc", "flatBytes": -4096,
                                               "cumulativeBytes": -4096, "inline": True}])
        self.assertNotIn("netDeltaBytes", result)
        self.assertEqual(h.summarize_heap_top(top_fixture(0))["functions"][0]["flatBytes"], 0)
        for malformed in (top_fixture().replace(b"Type: inuse_space", b"Type: alloc_space"),
                          top_fixture().replace(b"8192B", b"8kB"), top_fixture(percentage="NaN%"),
                          top_fixture(percentage="Infinity%"), top_fixture(name="/private/work.go"),
                          top_fixture(name="C:\\temp\\work.go"), top_fixture(name="https://private.example")):
            self.rejected(h.summarize_heap_top, malformed)
        self.rejected(h.summarize_heap_top, top_fixture(name="private.marker"), [], ["private.marker"])
        lines = top_fixture().splitlines(keepends=True)
        self.assertEqual(len(h.summarize_heap_top(b"".join(lines[:-1] + lines[-1:] * 20))["functions"]), 20)
        self.rejected(h.summarize_heap_top, b"".join(lines[:-1] + lines[-1:] * 21))
        with mock.patch.object(h, "MAX_TOP_BYTES", len(top_fixture()) - 1):
            self.rejected(h.summarize_heap_top, top_fixture())

    def test_diff_with_negative_shown_total_and_dropped_labels_is_valid(self):
        result = h.summarize_heap_top(SHRINKING_DIFF)
        self.assertEqual(result["shownBytes"], -1096162)
        self.assertEqual(result["totalMagnitudeBytes"], 1117209)
        self.assertEqual(result["functions"][0], {"function": "main.allocB", "flatBytes": -1018548,
                                                  "cumulativeBytes": -1104354, "inline": False})
        self.assertEqual(result["functions"][3]["function"], "strconv.Itoa")
        self.assertTrue(result["functions"][3]["inline"])

    def test_diff_with_generic_struct_tag_shape_name_is_valid(self):
        result = h.summarize_heap_top(GENERIC_DIFF)
        self.assertEqual(result["functions"][1]["function"], GENERIC_NAME)
        self.assertEqual(result["functions"][1]["flatBytes"], -816402)
        for tag in ('"x:\\"y\\""', '"json:\\"/media/x\\""', '"a:\\"\\"" "b:\\"c\\""'):
            name = "pkg.F[go.shape.struct { V string " + tag + " }]"
            self.assertEqual(h.summarize_heap_top(top_fixture(name=name))["functions"][0]["function"], name)

    def test_diff_with_zero_or_out_of_range_percentages_is_valid(self):
        zero = h.summarize_heap_top(diff_fixture(["         0     0%     0%          0     0%  runtime.main"]))
        self.assertEqual((zero["shownBytes"], zero["totalMagnitudeBytes"]), (0, 0))
        self.assertEqual(h.summarize_heap_top(diff_fixture([]))["functions"], [])
        rows = ["  -20480B   250%   250%    -20480B   250%  main.grow",
                "       1B 4.1e-05% 250.00%        1B 4.1e-05%  main.tiny",
                "    4096B 50.00% 200.00%     4096B 50.00%  main.part (partial-inline)"]
        result = h.summarize_heap_top(diff_fixture(rows, "-16383B", "200.00%", "8192B"))
        self.assertEqual(result["shownBytes"], -16383)
        self.assertEqual([f["flatBytes"] for f in result["functions"]], [-20480, 1, 4096])

    def test_drive_paths_and_private_text_are_still_rejected_in_diff_names(self):
        for name in ("C:\\temp\\work.go", "pkg.F C:/temp/work.go", 'pkg.F[go.shape.struct { V string "p:\\"C:\\\\Users\\\\x\\"" }]',
                     'pkg.F[go.shape.struct { V string "json:\\"/home/someone\\"" }]', "pkg.F d:\\x", "/private/work.go",
                     "https://private.example", "pkg.F[\u00e9]"):
            with self.subTest(name=name):
                self.rejected(h.summarize_heap_top, top_fixture(name=name))
        self.rejected(h.summarize_heap_top, GENERIC_DIFF, [], ["RootID"])
        self.rejected(h.summarize_heap_top, SHRINKING_DIFF, [], ["allocB"])
        self.rejected(h.summarize_heap_top, SHRINKING_DIFF.replace(b"-1018548B", b"-1018548kB"))
        self.rejected(h.summarize_heap_top, SHRINKING_DIFF.replace(b"98.85%  main", b"98.85%"))

    def test_rejection_reason_is_a_fixed_token(self):
        with self.assertRaises(h.Rejected) as raised:
            h.summarize_heap_top(top_fixture(name="D:\\work\\secret.go"))
        self.assertEqual(raised.exception.reason, "top_function_name")
        with self.assertRaises(h.Rejected) as raised:
            h.summarize_heap_top(top_fixture(name="private.marker"), [], ["private.marker"])
        self.assertEqual(raised.exception.reason, "text_private_marker")

    def test_excerpt_redacts_private_tokens_bounds_size_and_drops_partial_line(self):
        body = (b"File: worker.test\nType: inuse_space\nmain.f /home/runner/work/x.go\n"
                b"pg postgres://jelee:ci-only@host/db and PrivateMarker and C:\\Users\\a\\b\nsecret-tail-no-newline")
        excerpt = h._redacted_excerpt(body, ["/opt/owned"], ["privatemarker", "ci-only"])
        text = excerpt["outputExcerpt"]
        self.assertFalse(excerpt["outputExcerptTruncated"])
        self.assertIn("main.f [redacted]", text)
        self.assertNotIn("\\a\\b", text)
        self.assertEqual(h._redacted_excerpt(b"x /opt/tool y E:/data z\n", [], [])["outputExcerpt"], "x [redacted] y [redacted] z\n")
        for leaked in ("/home/", "runner", "ci-only", "postgres", "PrivateMarker", "Users", "secret-tail"):
            self.assertNotIn(leaked, text)
        large = b"".join(b"%06d 0 0%% 0%%  main.f\n" % i for i in range(3000))
        excerpt = h._redacted_excerpt(large[:h.MAX_TOP_BYTES], [], [])
        self.assertTrue(excerpt["outputExcerptTruncated"])
        self.assertLessEqual(len(excerpt["outputExcerpt"]), h.MAX_EXCERPT_CHARS)
        self.assertTrue(excerpt["outputExcerpt"].endswith("main.f\n"))
        self.assertEqual(h._redacted_excerpt(b"\x00\xff ok\n", [], [])["outputExcerpt"], "?\ufffd ok\n")
        for unusable in (b"", b"no newline", None):
            self.assertIsNone(h._redacted_excerpt(unusable, [], []))
        self.assertIsNone(h._redacted_excerpt(b"ok\n", "not-a-list", []))

    def test_child_drains_both_pipes_and_keeps_all_output_private(self):
        with tempfile.TemporaryDirectory() as folder:
            output = Path(folder) / "child.private.txt"
            result = h._run_bounded([sys.executable, "-c", "import os; os.write(1,b'profile'); os.write(2,b'PRIVATE_ERROR')"],
                                    folder, os.environ.copy(), output, 1024, timeout=10)
            self.assertEqual(result, {"stdoutBytes": 7, "stderrBytes": 13, "exitCode": 0})
            self.assertEqual(output.read_bytes(), b"profile")
            self.assertNotIn("PRIVATE", json.dumps(result))
            self.rejected(h._run_bounded, [sys.executable, "-c", "pass"], folder, os.environ.copy(), output, 1024, timeout=10)
            self.assertEqual(output.read_bytes(), b"profile")

    def test_child_output_limits_timeout_and_failure_kill_wait_and_join(self):
        helpers = [("stdout", "import os; os.write(1,b'x'*2048)", 10),
                   ("stderr", "import os; os.write(2,b'x'*65537)", 10),
                   ("exit", "import sys; sys.stderr.write('PRIVATE_ERROR'); sys.exit(3)", 10),
                   ("timeout", "import time; time.sleep(10)", 0.1)]
        real_popen = subprocess.Popen
        for name, code, timeout in helpers:
            with self.subTest(name=name), tempfile.TemporaryDirectory() as folder:
                children = []

                def start(*args, **kwargs):
                    child = real_popen(*args, **kwargs)
                    children.append(child)
                    return child

                output = Path(folder) / "child.private.txt"
                with mock.patch.object(h.subprocess, "Popen", side_effect=start):
                    self.rejected(h._run_bounded, [sys.executable, "-c", code], folder, os.environ.copy(), output, 1024, timeout=timeout)
                self.assertEqual(len(children), 1)
                self.assertIsNotNone(children[0].poll())
                self.assertTrue(children[0].stdout.closed)
                self.assertTrue(children[0].stderr.closed)
                self.assertLessEqual(output.stat().st_size, 1024)

    def test_analysis_uses_fixed_local_leaf_commands_and_reports_negative_net_change(self):
        with tempfile.TemporaryDirectory() as folder:
            directory = Path(folder).resolve()
            tool = directory / "pprof"
            tool.write_bytes(b"fixture leaf; never executed")
            records = []
            for stage in ("before", "after"):
                body = gzip.compress(b"synthetic protobuf placeholder " + stage.encode())
                (directory / (stage + ".heap.pb.gz")).write_bytes(body)
                records.append(metadata_fixture(stage, body))
            calls = []

            def run(argv, cwd, env, output, limit, timeout=30):
                calls.append(argv)
                self.assertEqual(cwd, directory)
                self.assertEqual(env.get("HOME"), os.environ.get("HOME"))
                self.assertEqual(env["PPROF_TMPDIR"], str(directory / "pprof-work"))
                self.assertEqual(env["PPROF_BINARY_PATH"], str(directory / "pprof-work"))
                self.assertEqual(argv[0], str(tool))
                self.assertIn("-symbolize=none", argv)
                self.assertTrue(all("://" not in value for value in argv))
                if "-raw" in argv:
                    body = raw_fixture(8192 if argv[-1].startswith("before") else 4096)
                else:
                    self.assertIn("-unit=bytes", argv)
                    self.assertIn("-inuse_space", argv)
                    self.assertIn("-nodecount=20", argv)
                    value = -4096 if "-base" in argv else (8192 if argv[-1].startswith("before") else 4096)
                    body = top_fixture(value)
                self.assertLessEqual(len(body), limit)
                output.write_bytes(body)
                return {"stdoutBytes": len(body), "stderrBytes": 0, "exitCode": 0}

            with mock.patch.object(h, "_run_bounded", side_effect=run):
                result = h.analyze_heap_profiles(tool, directory / "before.heap.pb.gz", directory / "after.heap.pb.gz", records, directory)
            self.assertEqual(len(calls), 5)
            self.assertEqual(result["before"]["sampledInuseBytes"], 8192)
            self.assertEqual(result["after"]["sampledInuseBytes"], 4096)
            self.assertEqual(result["diff"]["netDeltaBytes"], -4096)
            self.assertEqual(result["diff"]["netDirection"], "decreased")
            self.assertEqual(result["diff"]["shownBytes"], -4096)
            self.assertEqual(result["diff"]["functions"][0]["flatBytes"], -4096)
            self.assertNotIn(folder, json.dumps(result))
            state = json.loads((directory / "heap-analysis-state.json").read_text())
            self.assertEqual(state["result"], "passed")
            self.assertTrue(all(step["status"] == "passed" for step in state["steps"]))
            self.assertTrue(all(set(step) == {"name", "status", "stdoutBytes", "stderrBytes", "exitCode"} for step in state["steps"]))
            self.rejected(h.analyze_heap_profiles, tool, directory / "before.heap.pb.gz", directory / "after.heap.pb.gz", records, directory)
            self.assertEqual(json.loads((directory / "heap-analysis-state.json").read_text()), state)

    def analysis_fixture(self, directory, before=8192, after=4096, diff=None):
        tool = directory / "pprof"
        tool.write_bytes(b"fixture leaf; never executed")
        records = []
        for stage in ("before", "after"):
            body = gzip.compress(b"synthetic protobuf placeholder " + stage.encode())
            (directory / (stage + ".heap.pb.gz")).write_bytes(body)
            records.append(metadata_fixture(stage, body))

        def run(argv, cwd, env, output, limit, timeout=30):
            stage_before = argv[-1].startswith("before")
            if "-raw" in argv:
                body = raw_fixture(before if stage_before else after)
            elif "-base" in argv:
                body = diff if diff is not None else top_fixture(after - before)
            else:
                body = top_fixture(before if stage_before else after)
            output.write_bytes(body)
            return {"stdoutBytes": len(body), "stderrBytes": 0, "exitCode": 0}

        return tool, records, run

    def test_analysis_records_net_direction_for_growth_shrink_and_no_change(self):
        for before, after, direction in ((4096, 8192, "increased"), (8192, 4096, "decreased"), (8192, 8192, "unchanged")):
            with self.subTest(direction=direction), tempfile.TemporaryDirectory() as folder:
                directory = Path(folder).resolve()
                tool, records, run = self.analysis_fixture(directory, before, after)
                with mock.patch.object(h, "_run_bounded", side_effect=run):
                    result = h.analyze_heap_profiles(tool, directory / "before.heap.pb.gz", directory / "after.heap.pb.gz", records, directory)
                self.assertEqual(result["result"], "passed")
                self.assertEqual(result["diff"]["netDirection"], direction)
                self.assertEqual(result["diff"]["netDeltaBytes"], after - before)

    def test_real_shrinking_and_generic_diff_outputs_pass_full_analysis(self):
        for diff in (SHRINKING_DIFF, GENERIC_DIFF):
            with tempfile.TemporaryDirectory() as folder:
                directory = Path(folder).resolve()
                tool, records, run = self.analysis_fixture(directory, diff=diff)
                with mock.patch.object(h, "_run_bounded", side_effect=run):
                    result = h.analyze_heap_profiles(tool, directory / "before.heap.pb.gz", directory / "after.heap.pb.gz", records, directory,
                                                     forbidden_prefixes=[folder], secret_markers=["ci-only"])
                self.assertEqual(result["result"], "passed")
                self.assertNotIn("outputExcerpt", (directory / "heap-analysis-state.json").read_text())

    def test_invalid_diff_keeps_redacted_bounded_excerpt_and_reason_in_state(self):
        with tempfile.TemporaryDirectory() as folder:
            directory = Path(folder).resolve()
            invalid = GENERIC_DIFF.replace(b"main.allocB", b"main.allocB D:\\src\\leak.go ci-only " + folder.encode())
            tool, records, run = self.analysis_fixture(directory, diff=invalid)
            with mock.patch.object(h, "_run_bounded", side_effect=run):
                self.rejected(h.analyze_heap_profiles, tool, directory / "before.heap.pb.gz", directory / "after.heap.pb.gz", records, directory,
                              forbidden_prefixes=[folder], secret_markers=["ci-only"])
            state = json.loads((directory / "heap-analysis-state.json").read_text())
            self.assertEqual(state["stage"], "diff-top")
            step = state["steps"][-1]
            self.assertEqual((step["status"], step["phase"], step["reason"], step["exitCode"]),
                             ("failed", "validation", "text_private_marker", 0))
            self.assertFalse(step["outputExcerptTruncated"])
            self.assertIn("Showing nodes accounting for 826405B", step["outputExcerpt"])
            self.assertIn("main.allocB [redacted] [redacted] [redacted]", step["outputExcerpt"])
            self.assertIn(GENERIC_NAME, step["outputExcerpt"])
            serialized = json.dumps(state)
            self.assertNotIn("ci-only", serialized)
            self.assertNotIn(folder, serialized)
            self.assertNotIn(folder.replace("\\", "\\\\"), serialized)

    def test_raw_failures_never_publish_an_excerpt_and_child_failures_name_the_cause(self):
        with tempfile.TemporaryDirectory() as folder:
            directory = Path(folder).resolve()
            tool, records, run = self.analysis_fixture(directory)

            def bad_raw(argv, cwd, env, output, limit, timeout=30):
                output.write_bytes(raw_fixture(-1))
                return {"stdoutBytes": 1, "stderrBytes": 0, "exitCode": 0}

            with mock.patch.object(h, "_run_bounded", side_effect=bad_raw):
                self.rejected(h.analyze_heap_profiles, tool, directory / "before.heap.pb.gz", directory / "after.heap.pb.gz", records, directory)
            step = json.loads((directory / "heap-analysis-state.json").read_text())["steps"][-1]
            self.assertEqual((step["name"], step["phase"]), ("before-raw", "validation"))
            self.assertFalse(any(key.startswith("outputExcerpt") for key in step))
        with tempfile.TemporaryDirectory() as folder:
            directory = Path(folder).resolve()
            tool, records, run = self.analysis_fixture(directory)
            real_run = h._run_bounded

            def failing_diff(argv, cwd, env, output, limit, timeout=30):
                if "-base" not in argv:
                    return run(argv, cwd, env, output, limit, timeout)
                return real_run([sys.executable, "-c", "import os,sys; os.write(1,b'partial top\\nsecond'); sys.exit(2)"],
                                      cwd, env, output, limit, timeout=10)

            with mock.patch.object(h, "_run_bounded", side_effect=failing_diff):
                self.rejected(h.analyze_heap_profiles, tool, directory / "before.heap.pb.gz", directory / "after.heap.pb.gz", records, directory)
            step = json.loads((directory / "heap-analysis-state.json").read_text())["steps"][-1]
            self.assertEqual((step["name"], step["status"], step["reason"]), ("diff-top", "failed", "child_exit"))
            self.assertNotIn("phase", step)
            self.assertEqual(step["outputExcerpt"], "partial top\n")

    def test_analysis_failure_keeps_before_file_and_safe_state_without_raw_error(self):
        with tempfile.TemporaryDirectory() as folder:
            directory = Path(folder).resolve()
            tool = directory / "pprof"
            tool.write_bytes(b"fixture leaf; never executed")
            body = gzip.compress(b"fixture")
            records = [metadata_fixture("before", body), metadata_fixture("after", body)]
            for stage in ("before", "after"):
                (directory / (stage + ".heap.pb.gz")).write_bytes(body)

            def fail(argv, cwd, env, output, limit, timeout=30):
                output.write_bytes(b"PRIVATE_ERROR /home/private")
                raise OSError("PRIVATE_ERROR " + folder)

            with mock.patch.object(h, "_run_bounded", side_effect=fail):
                self.rejected(h.analyze_heap_profiles, tool, directory / "before.heap.pb.gz", directory / "after.heap.pb.gz", records, directory)
            self.assertEqual((directory / "before.heap.pb.gz").read_bytes(), body)
            self.assertEqual((directory / "before-raw.private.txt").read_bytes(), b"PRIVATE_ERROR /home/private")
            state = json.loads((directory / "heap-analysis-state.json").read_text())
            self.assertEqual(state["result"], "failed")
            self.assertEqual(state["stage"], "before-raw")
            self.assertEqual(state["steps"][-1]["status"], "failed")
            self.assertNotIn("PRIVATE", json.dumps(state))
            self.assertNotIn(folder, json.dumps(state))

    def test_final_state_write_failure_cannot_preserve_a_success_result(self):
        with tempfile.TemporaryDirectory() as folder:
            directory = Path(folder).resolve()
            tool = directory / "pprof"
            tool.write_bytes(b"fixture leaf; never executed")
            body = gzip.compress(b"fixture")
            records = [metadata_fixture("before", body), metadata_fixture("after", body)]
            for stage in ("before", "after"):
                (directory / (stage + ".heap.pb.gz")).write_bytes(body)

            def run(argv, cwd, env, output, limit, timeout=30):
                content = raw_fixture() if "-raw" in argv else top_fixture()
                output.write_bytes(content)
                return {"stdoutBytes": len(content), "stderrBytes": 0, "exitCode": 0}

            real_write, failures = h._write_state, []

            def write(directory, state):
                if state["result"] == "passed" and not failures:
                    failures.append(True)
                    raise OSError("PRIVATE final status write failure")
                real_write(directory, state)

            with mock.patch.object(h, "_run_bounded", side_effect=run), mock.patch.object(h, "_write_state", side_effect=write):
                self.rejected(h.analyze_heap_profiles, tool, directory / "before.heap.pb.gz", directory / "after.heap.pb.gz", records, directory)
            self.assertEqual(len(failures), 1)
            state = json.loads((directory / "heap-analysis-state.json").read_text())
            self.assertEqual(state["result"], "failed")
            self.assertEqual(state["failureCode"], "heap_profile_evidence_invalid")
            self.assertTrue(all(step["status"] == "passed" for step in state["steps"]))
            self.assertNotIn("PRIVATE", json.dumps(state))
            self.assertEqual((directory / "before.heap.pb.gz").read_bytes(), body)


if __name__ == "__main__":
    unittest.main()
