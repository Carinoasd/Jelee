"""Controller contracts: bounded local fixtures and mocked Docker, never a workload."""
import contextlib
import copy
import hashlib
import io
import json
import os
from pathlib import Path, PurePosixPath
import subprocess
import sys
import tempfile
import types
import unittest
from unittest.mock import patch

import test_image_memory as controller


def fixture_manifest(count=1000):
    bodies = {kind: (kind + " synthetic bytes").encode() for kind in ("jpeg", "png", "png16", "media")}
    dimensions = {"jpeg": (640, 960, "jpeg"), "png": (256, 384, "png"), "png16": (4096, 2560, "png"), "media": (0, 0, "anchor")}
    templates = {kind: {"sha256": hashlib.sha256(body).hexdigest(), "bytes": len(body),
                        "width": dimensions[kind][0], "height": dimensions[kind][1], "format": dimensions[kind][2]}
                 for kind, body in bodies.items()}
    boundary = max(100, count // 20)
    counts = {"jpeg": count - boundary, "png": boundary - 64, "png16": 64}
    samples, files = [], {}
    for index, media in ((0, True), (0, False), (64, False), (boundary, False), (count - 1, False)):
        kind = "media" if media else controller.fixture_kind(index, count)
        relative = "dir-%05d/clip-%06d" % (index // 10, index)
        relative += ".mkv" if media else "-poster." + ("jpg" if kind == "jpeg" else "png")
        samples.append({"relativePath": relative, "bytes": len(bodies[kind]), "sha256": templates[kind]["sha256"]})
        files[relative] = bodies[kind]
    negatives = []
    for kind, extension in controller.NEGATIVE_CASES.items():
        for leaf, body in (("clip.mkv", bodies["media"]),
                           ("clip-poster." + extension, b"x" * (controller.MAX_SOURCE_BYTES + 1) if kind == "oversized-source" else kind.encode())):
            relative = "negative/" + kind + "/" + leaf
            negatives.append({"relativePath": relative, "bytes": len(body), "sha256": hashlib.sha256(body).hexdigest()})
            files[relative] = body
    return {"version": 1, "count": count, "directories": count // 10, "fileCount": 2 * count,
            "totalBytes": count * len(bodies["media"]) + sum(counts[k] * len(bodies[k]) for k in counts),
            "counts": counts, "templates": templates, "samples": samples, "negativeFiles": 8,
            "negativeBytes": sum(v["bytes"] for v in negatives), "negativeSamples": negatives}, files


class ControllerContracts(unittest.TestCase):
    def setUp(self):
        # Permission assertions need native Linux mode bits. A WSL checkout
        # may be on NTFS, where chmod(0444) does not remove write permissions.
        parent = Path("/tmp") if sys.platform == "linux" else controller.ROOT / ".testdata"
        parent.mkdir(exist_ok=True)
        self.temporary = tempfile.TemporaryDirectory(prefix="image-controller-", dir=parent)
        self.addCleanup(self.temporary.cleanup)
        self.root = Path(self.temporary.name).resolve()
        self.addCleanup(self.restore_modes)

    def restore_modes(self):
        # Only this test's owned tree; permit Windows cleanup of read-only files.
        for directory, _children, files in os.walk(self.root):
            Path(directory).chmod(0o700)
            for name in files:
                path = Path(directory) / name
                if not path.is_symlink():
                    path.chmod(0o600)

    def test_manifest_requires_exact_distribution_safe_paths_and_all_negative_sources(self):
        with patch.object(controller, "MAX_SOURCE_BYTES", 1024):
            for count in (1000, 100000):
                value, _files = fixture_manifest(count)
                self.assertEqual(controller.validate_manifest(value, count), value)
                self.assertEqual(value["counts"], {"jpeg": 900 if count == 1000 else 95000,
                                                  "png": 36 if count == 1000 else 4936, "png16": 64})
            base, _files = fixture_manifest()
            mutations = (
                lambda v: v.update(version=True),
                lambda v: v.update(fileCount=2008),
                lambda v: v.update(directories=101),
                lambda v: v.update(totalBytes=v["totalBytes"] + 1),
                lambda v: v["counts"].update(jpeg=950),
                lambda v: v.update(hostPath="PRIVATE"),
                lambda v: v["templates"]["png16"].update(format="png16"),
                lambda v: v["samples"][0].update(relativePath="../escape"),
                lambda v: v["samples"][0].update(relativePath="dir-00001/clip-000000.mkv"),
                lambda v: v["samples"][0].update(bytes=0),
                lambda v: v["samples"].append(dict(v["samples"][0])),
                lambda v: v.update(samples=v["samples"] * 129),
                lambda v: v["negativeSamples"].pop(),
                lambda v: v["negativeSamples"][0].update(relativePath="negative/../escape"),
                lambda v: v["negativeSamples"][0].update(sha256="0" * 64),
                lambda v: v.update(negativeBytes=v["negativeBytes"] - 1),
            )
            for mutate in mutations:
                value = copy.deepcopy(base)
                mutate(value)
                with self.assertRaises(controller.ImageFailure):
                    controller.validate_manifest(value, 1000)

    def test_real_sample_hash_and_metadata_detect_changed_source(self):
        with patch.object(controller, "MAX_SOURCE_BYTES", 1024):
            manifest, files = fixture_manifest()
            controller.validate_manifest(manifest, 1000)
            directory = self.root / "media"
            for relative, body in files.items():
                path = directory / relative
                path.parent.mkdir(parents=True, exist_ok=True)
                path.write_bytes(body)
                path.chmod(0o444)
            for parent, _children, _files in os.walk(directory, topdown=False):
                Path(parent).chmod(0o555)
            before = controller.fixture_sample(directory, manifest)
            self.assertEqual(before, controller.fixture_sample(directory, manifest))
            self.assertEqual(len(before), len(manifest["samples"]) + 8)
            target = directory / manifest["samples"][1]["relativePath"]
            old = target.stat()
            target.chmod(0o600)
            target.write_bytes(b"y" * old.st_size)
            target.chmod(0o444)
            os.utime(target, ns=(old.st_atime_ns, old.st_mtime_ns))
            with self.assertRaisesRegex(controller.ImageFailure, "^image_memory_fixture_changed$"):
                controller.fixture_sample(directory, manifest)

    def test_fixture_capacity_counts_two_sources_directories_and_negative_reserve(self):
        with patch.object(Path, "open", return_value=io.BytesIO(b"1 0 8:1 / / rw - ext4 /dev/root rw\n")), \
                patch.object(controller, "capture", return_value=subprocess.CompletedProcess([], 0, b"5242880 4096 300000")), \
                patch.object(controller, "storage_record", return_value={}) as storage:
            controller.check_fixture_storage(PurePosixPath("/var/tmp"), 100000)
        self.assertEqual(storage.call_args.args, ("ext4", b"5242880 4096 300000", 220014))

    def test_event_order_strict_json_and_image_id_do_not_accept_ambiguous_evidence(self):
        ready = b'{"imagesMemoryReadyForSIGTERM":true}\n'
        final = b'{"imagesMemoryAcceptance":{"result":"passed"}}\n'
        self.assertEqual(controller.parse_events(ready + final), (True, {"result": "passed"}))
        for body in (ready + ready, final + final, final + ready,
                     b'{"imagesMemoryReadyForSIGTERM":1}\n', b'{"imagesMemoryReadyForSIGTERM":true,"x":0}\n',
                     b'{"imagesMemoryAcceptance":{},"imagesMemoryAcceptance":{}}\n',
                     b'{"imagesMemoryAcceptance":{"value":NaN}}\n', b'{"imagesMemoryAcceptance":'):
            with self.subTest(body=body), self.assertRaises((controller.ImageFailure, controller.ScanFailure)):
                controller.parse_events(body)
        self.assertEqual(controller.image_identity(b"sha256:" + b"a" * 64 + b"\n"), "sha256:" + "a" * 64)
        for body in (b"PRIVATE inspect", b"sha256:" + b"a" * 64 + b"\nPRIVATE", b"sha256:" + b"a" * 63):
            with self.assertRaises(controller.ImageFailure):
                controller.image_identity(body)

    def exercise_case(self, mode, *, smoke=True):
        count = 1000 if smoke else 100000
        native = self.root / "native-parent" / "jelee-image-memory-owned"
        native.parent.mkdir(exist_ok=True)
        native.mkdir()
        (self.root / "tools").mkdir(exist_ok=True)
        (self.root / "tools/manifest.json").write_text('{"fixture":"pinned SDK manifest"}')
        dsn = "postgres://user:PRIVATE_SECRET@127.0.0.1:55437/jelee_test"
        ready = b'{"imagesMemoryReadyForSIGTERM":true}\n'
        final = {"version": 1, "result": "passed", "fixtureItems": count}
        complete = ready + json.dumps({"imagesMemoryAcceptance": final}).encode() + b"\nPASS\n"
        logs = [ready, complete]
        if mode == "missing-handshake":
            logs = [json.dumps({"imagesMemoryAcceptance": final}).encode() + b"\nPASS\n"]
        if mode == "duplicate-event":
            logs = [ready + ready]
        if mode == "bad-json":
            logs = [b'{"imagesMemoryAcceptance":']
        if mode == "disappearing-event":
            logs = [ready, b"ordinary log\n"]
        manifest, _files = fixture_manifest(count)
        calls, validations, removed = [], [], set()
        state = {"reads": 0}

        def capture(argv, **kwargs):
            calls.append(list(argv))
            self.assertNotIn("PRIVATE", " ".join(argv))
            if "-o" in argv:
                Path(argv[argv.index("-o") + 1]).write_bytes(b"owned binary")
                self.assertNotIn("JELEE_TEST_DATABASE_URL", kwargs["env"])
            if argv[-1] == "version":
                return subprocess.CompletedProcess(argv, 0, b"go version go1.27.1 linux/amd64\n")
            if argv[:2] == ["docker", "build"]:
                self.assertFalse((native / "database.env").exists())
                self.assertFalse((native / "media").exists())
            if "--root" in argv:
                self.assertEqual(argv[-1], str(count))
                self.assertTrue(Path(argv[argv.index("--root") + 1]).is_dir())
                return subprocess.CompletedProcess(argv, 1 if mode == "generator-failed" else 0, json.dumps(manifest).encode())
            if argv[:2] == ["docker", "logs"]:
                index = min(state["reads"], len(logs) - 1)
                state["reads"] += 1
                if mode == "interrupted" and state["reads"] == 1:
                    raise KeyboardInterrupt
                if mode == "logs-timeout":
                    raise controller.ScanFailure("scan_memory_command_limit")
                return subprocess.CompletedProcess(argv, 1 if mode == "logs-failed" else 0, logs[index])
            if argv[:3] == ["docker", "image", "inspect"]:
                return subprocess.CompletedProcess(argv, 0, b"sha256:" + b"a" * 64 + b"\n")
            if argv[:3] in (["docker", "container", "ls"], ["docker", "image", "ls"]):
                query = argv[argv.index("--filter") + 1]
                name = query.removeprefix("name=^/").removesuffix("$") if query.startswith("name=") else query.removeprefix("reference=")
                return subprocess.CompletedProcess(argv, 1 if mode == "cleanup-unverified" else 0,
                                                   b"" if name in removed else name.encode() + b"\n")
            if argv[:3] in (["docker", "container", "rm"], ["docker", "image", "rm"]):
                removed.add(argv[-1])
            if "--cleanup-probe-worker" in argv:
                secret = (native / "database.env").read_text()
                self.assertIn("JELEE_IMAGES_MEMORY_ITEMS=" + str(count), secret)
                self.assertIn("JELEE_IMAGES_MEMORY_ACCEPTANCE=true", secret)
                return subprocess.CompletedProcess(argv, 1 if mode == "cleanup-failed" else 0, b"fixed cleanup status\n")
            return subprocess.CompletedProcess(argv, 0, b"owned\n")

        def inspected(_name):
            running = state["reads"] == 0 and mode != "missing-handshake"
            return {"State": {"Running": running, "Status": "running" if running else "exited",
                              "ExitCode": 137 if mode == "oom" else 0, "OOMKilled": mode == "oom"}}

        def validate(value, inspected_value, budget, expected_items):
            validations.append(expected_items)
            self.assertEqual(value, final)
            if mode == "validator-failed":
                raise ValueError("PRIVATE validator detail")
            return {"result": "passed", "fixtureItems": expected_items, "finalAcceptance": expected_items == 100000}

        fake_validator = types.SimpleNamespace(validate_image_memory=validate)
        real_resolve = Path.resolve

        def resolve(path, *args, **kwargs):
            if path == Path("/var/tmp"):
                return native.parent
            return real_resolve(path, *args, **kwargs)

        with contextlib.ExitStack() as stack:
            stack.enter_context(patch.dict(sys.modules, {"image_memory_acceptance": fake_validator}))
            stack.enter_context(patch.object(controller, "ROOT", self.root))
            stack.enter_context(patch.object(controller.sys, "platform", "linux"))
            stack.enter_context(patch.object(controller, "capture", side_effect=capture))
            stack.enter_context(patch.object(controller, "inspect_owned", side_effect=inspected))
            stack.enter_context(patch.object(controller, "container_state", return_value={"status": "exited", "exitCode": 0, "oomKilled": False, "running": False}))
            stack.enter_context(patch.object(controller, "source_digest", side_effect=["b" * 64, ("d" if mode == "source-changed" else "b") * 64]))
            stack.enter_context(patch.object(controller, "load_budget", return_value=({}, "c" * 64)))
            stack.enter_context(patch.object(controller, "check_fixture_storage", return_value={"nativeDisk": True}))
            stack.enter_context(patch.object(controller, "check_postgres_storage", return_value={"nativeDisk": True}))
            stack.enter_context(patch.object(controller, "fixture_sample", side_effect=[[("fixed", 1)], [("fixed", 2 if mode == "fixture-changed" else 1)]]))
            stack.enter_context(patch.object(controller.tempfile, "mkdtemp", return_value=str(native)))
            stack.enter_context(patch.object(Path, "resolve", resolve))
            stack.enter_context(patch.object(controller.time, "sleep"))
            if mode == "workload-timeout":
                stack.enter_context(patch.object(controller.time, "monotonic", side_effect=[0, 4000]))
            stack.enter_context(patch.dict(os.environ, {"JELEE_TEST_DATABASE_URL": dsn, "JELEE_IMAGE_MEMORY_PG_CONTAINER": "existing-db"}))
            result = controller.run_case(smoke=smoke)
        self.assertFalse(native.exists())
        self.assertNotIn("PRIVATE", json.dumps(result))
        summary = next((self.root / ".testdata").glob("image-memory-*/summary.json"))
        self.assertEqual(json.loads(summary.read_text(encoding="utf-8")), result)
        self.assertEqual(any("--cleanup-probe-worker" in call for call in calls), mode != "generator-failed")
        self.assertFalse(any(call[:3] == ["docker", "container", "rm"] and "existing-db" in call for call in calls))
        transcript = summary.parent / "acceptance.txt"
        self.assertNotIn("PRIVATE", transcript.read_text(encoding="utf-8"))
        if mode == "validator-failed":
            raw = summary.parent / "acceptance-report.private.json"
            self.assertEqual(json.loads(raw.read_text()), final)
            self.assertEqual(controller.digest(raw), result["rawAcceptanceSha256"])
        return result, calls, validations

    def test_smoke_preserves_fixed_security_and_cannot_claim_final_acceptance(self):
        with patch.object(controller, "MAX_SOURCE_BYTES", 1024):
            result, calls, validations = self.exercise_case("success")
        self.assertEqual(result["result"], "passed")
        self.assertEqual(result["scope"], "smoke-1000")
        self.assertFalse(result["finalAcceptance"])
        self.assertTrue(result["testArtifactsCleaned"])
        self.assertEqual(validations, [1000])
        command = next(call for call in calls if call[:3] == ["docker", "run", "-d"])
        for option, value in (("--user", "65532:65532"), ("--memory", "768m"), ("--memory-swap", "768m"),
                              ("--cpus", "2"), ("--pids-limit", "128"), ("--cap-drop", "ALL"),
                              ("--security-opt", "no-new-privileges")):
            self.assertEqual(command[command.index(option) + 1], value)
        for value in ("--read-only", "GOGC=100", "GOMEMLIMIT=512MiB", "GOMAXPROCS=2", controller.SCRATCH):
            self.assertIn(value, command)
        self.assertTrue(command[command.index("--volume") + 1].endswith(":/media:ro"))
        self.assertEqual(sum(call[:4] == ["docker", "kill", "--signal", "SIGTERM"] for call in calls), 1)

    def test_full_mode_requires_full_count_through_generator_and_validator(self):
        with patch.object(controller, "MAX_SOURCE_BYTES", 1024):
            result, _calls, validations = self.exercise_case("success", smoke=False)
        self.assertEqual(result["result"], "passed")
        self.assertTrue(result["finalAcceptance"])
        self.assertEqual(result["fixtureItems"], 100000)
        self.assertEqual(validations, [100000])

    def test_all_failures_clear_final_claim_and_attempt_owned_cleanup(self):
        for mode in ("missing-handshake", "duplicate-event", "bad-json", "disappearing-event", "logs-failed", "logs-timeout",
                     "validator-failed", "generator-failed", "fixture-changed", "source-changed", "oom", "interrupted", "workload-timeout",
                     "cleanup-failed", "cleanup-unverified"):
            with self.subTest(mode=mode), patch.object(controller, "MAX_SOURCE_BYTES", 1024):
                previous = self.root
                self.root = previous / mode
                self.root.mkdir()
                try:
                    result, calls, validations = self.exercise_case(mode)
                    self.assertEqual(result["result"], "failed")
                    self.assertFalse(result["finalAcceptance"])
                    self.assertEqual(result["testArtifactsCleaned"], mode not in ("cleanup-failed", "cleanup-unverified"))
                    if mode in ("missing-handshake", "duplicate-event", "bad-json", "disappearing-event", "logs-failed", "logs-timeout", "oom", "interrupted", "workload-timeout", "generator-failed"):
                        self.assertEqual(validations, [])
                    if mode != "cleanup-unverified":
                        self.assertTrue(any(call[:3] == ["docker", "image", "rm"] for call in calls))
                finally:
                    self.root = previous

    def test_cleanup_refuses_unowned_path_and_unverifiable_docker_listing(self):
        directory = self.root / "do-not-remove"
        directory.mkdir()
        with self.assertRaises(controller.ImageFailure):
            controller.remove_native(directory)
        self.assertTrue(directory.exists())
        for code, body in ((1, b""), (0, b"unexpected-other-container\n")):
            with patch.object(controller, "capture", return_value=subprocess.CompletedProcess([], code, body)) as capture:
                with self.assertRaises(controller.ImageFailure):
                    controller.remove_owned("container", "owned-container")
                self.assertEqual(capture.call_count, 1)

    def test_cli_has_no_arbitrary_count_override(self):
        with patch.object(controller, "run_case", return_value={"scope": "smoke-1000", "result": "failed", "fixtureItems": 1000,
                                                                "finalAcceptance": False, "testArtifactsCleaned": True}) as run, \
                patch.object(controller.signal, "signal"), contextlib.redirect_stdout(io.StringIO()):
            self.assertEqual(controller.main(["--smoke"]), 1)
            run.assert_called_once_with(smoke=True)
        with contextlib.redirect_stderr(io.StringIO()), self.assertRaises(SystemExit):
            controller.main(["--items", "1"])


if __name__ == "__main__":
    unittest.main()
