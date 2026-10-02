"""Fixed baseline loading and mandatory controller gate; no native workload."""
import contextlib
import copy
import hashlib
import io
import json
import os
from pathlib import Path
import subprocess
import tempfile
import unittest
from unittest.mock import patch

import check_memory_compose
import resident_memory as resident
import test_nfo_worker as controller
from test_resident_memory import budget_fixture, profile_fixture


def documents(root):
    budget = budget_fixture()
    cases = []
    for selected in budget["profiles"]:
        profile = profile_fixture()
        profile["runtime"]["gogcPercent"] = selected["gogcPercent"]
        selected["baselinePeakBytes"] = resident.validate_resident_samples(profile)["peakRssBytes"]
        cases.append({"gogcPercent": selected["gogcPercent"],
                      "sourceDigest": selected["baselineSourceDigest"], "memoryProfile": profile})
    baseline_bytes = (json.dumps({"version": 1, "cases": cases}) + "\n").encode()
    for selected in budget["profiles"]:
        selected["baselineEvidenceSha256"] = hashlib.sha256(baseline_bytes).hexdigest()
    for name, data in ((controller.RESIDENT_BASELINE, baseline_bytes),
                       (controller.RESIDENT_BUDGET, json.dumps(budget).encode())):
        target = root / name
        target.parent.mkdir(parents=True, exist_ok=True)
        target.write_bytes(data)
    return budget, cases


def successful_cases(budget, baseline):
    cases = copy.deepcopy(baseline)
    for case in cases:
        case.update(result="passed", testArtifactsCleaned=True, sourceUnchanged=True)
        case["residentValidation"] = resident.validate_resident_budget(case["memoryProfile"], budget, case["gogcPercent"])
    return cases


class ResidentControllerTests(unittest.TestCase):
    def test_fixed_loader_hashes_the_actual_bytes(self):
        with tempfile.TemporaryDirectory(prefix="resident-contract-", dir=Path.cwd()) as folder, patch.object(controller, "ROOT", Path(folder)):
            expected, _ = documents(Path(folder))
            budget, identity = controller.load_resident_budget()
            self.assertEqual(budget, expected)
            self.assertEqual(identity["budgetSha256"], hashlib.sha256((Path(folder) / controller.RESIDENT_BUDGET).read_bytes()).hexdigest())
            self.assertEqual(identity["baselineEvidenceSha256"], expected["profiles"][0]["baselineEvidenceSha256"])

    def test_missing_invalid_and_oversized_files_fail_without_content(self):
        for name in (controller.RESIDENT_BUDGET, controller.RESIDENT_BASELINE):
            for replacement in (None, b"PRIVATE_INPUT", b"x" * (4 * 1024 * 1024 + 1),
                                b'{"version":1,"version":1}', b'{"value":NaN}', b'{"value":Infinity}'):
                with self.subTest(name=name, size=None if replacement is None else len(replacement)), \
                        tempfile.TemporaryDirectory(prefix="resident-contract-", dir=Path.cwd()) as folder, patch.object(controller, "ROOT", Path(folder)):
                    documents(Path(folder))
                    path = Path(folder) / name
                    if replacement is None:
                        path.unlink()
                    else:
                        path.write_bytes(replacement)
                    with self.assertRaisesRegex(RuntimeError, "^resident budget baseline files unavailable or invalid$"):
                        controller.load_resident_budget()

    def test_baseline_bytes_changed_even_by_whitespace_are_rejected(self):
        with tempfile.TemporaryDirectory(prefix="resident-contract-", dir=Path.cwd()) as folder, patch.object(controller, "ROOT", Path(folder)):
            documents(Path(folder))
            path = Path(folder) / controller.RESIDENT_BASELINE
            path.write_bytes(path.read_bytes() + b"\n")
            with self.assertRaises(resident.Rejected):
                controller.load_resident_budget()

    def test_missing_budget_retains_failed_summary_before_starting_work(self):
        with tempfile.TemporaryDirectory(prefix="resident-contract-", dir=Path.cwd()) as folder, patch.object(controller, "ROOT", Path(folder)), \
                patch.object(controller, "WITH_MEMORY", True), patch.object(controller, "WITH_SUSTAINED", True), \
                patch.object(controller, "source_digest", return_value="a" * 64), \
                patch.object(controller, "run_case") as run_case, \
                patch.object(check_memory_compose, "run_compose_check") as compose:
            with self.assertRaises(RuntimeError):
                controller.main()
            report = json.loads((Path(folder) / (".testdata/" + controller.EVIDENCE_PREFIX + "-summary.json")).read_text())
            self.assertEqual(report["result"], "failed")
            self.assertEqual(report["residentBudget"]["result"], "failed")
            self.assertEqual(report["cases"], [])
            run_case.assert_not_called()
            compose.assert_not_called()

    def test_main_requires_both_fresh_budget_results_and_keeps_failed_summary(self):
        for invalid in ("missing", "duplicate", "over_budget", "none"):
            with self.subTest(invalid=invalid), tempfile.TemporaryDirectory(prefix="resident-contract-", dir=Path.cwd()) as folder, \
                    patch.object(controller, "ROOT", Path(folder)), patch.object(controller, "WITH_MEMORY", True), \
                    patch.object(controller, "WITH_SUSTAINED", True), \
                    patch.object(controller, "source_digest", return_value="a" * 64), \
                    patch.object(check_memory_compose, "run_compose_check", return_value={"result": "passed"}), \
                    contextlib.redirect_stdout(io.StringIO()):
                budget, baseline = documents(Path(folder))
                cases = successful_cases(budget, baseline)
                if invalid == "missing":
                    del cases[1]["residentValidation"]
                elif invalid == "duplicate":
                    cases[1] = copy.deepcopy(cases[0])
                elif invalid == "over_budget":
                    cases[1]["memoryProfile"]["resident"]["samples"][10]["rssBytes"] = budget["profiles"][1]["processRssBudgetBytes"] + 1
                with patch.object(controller, "run_case", side_effect=cases) as run_case:
                    if invalid == "none":
                        controller.main()
                    else:
                        with self.assertRaises(resident.Rejected):
                            controller.main()
                    self.assertEqual([call.kwargs["gogc"] for call in run_case.call_args_list], [100, 50])
                    self.assertTrue(all(call.kwargs["resident_budget"] == budget for call in run_case.call_args_list))
                report = json.loads((Path(folder) / (".testdata/" + controller.EVIDENCE_PREFIX + "-summary.json")).read_text())
                self.assertEqual(report["result"], "passed" if invalid == "none" else "failed")
                self.assertEqual(report["residentBudget"]["result"], report["result"])

    def test_source_change_during_load_or_between_profiles_keeps_failure(self):
        for source_values in (["a", "b"], ["a", "a", "b"]):
            with self.subTest(source_values=source_values), tempfile.TemporaryDirectory(prefix="resident-contract-", dir=Path.cwd()) as folder, \
                    patch.object(controller, "ROOT", Path(folder)), patch.object(controller, "WITH_MEMORY", True), \
                    patch.object(controller, "WITH_SUSTAINED", True), \
                    patch.object(controller, "source_digest", side_effect=source_values), \
                    patch.object(check_memory_compose, "run_compose_check", return_value={"result": "passed"}):
                budget, baseline = documents(Path(folder))
                with patch.object(controller, "run_case", side_effect=successful_cases(budget, baseline)):
                    with self.assertRaisesRegex(RuntimeError, "memory acceptance source changed"):
                        controller.main()
                report = json.loads((Path(folder) / (".testdata/" + controller.EVIDENCE_PREFIX + "-summary.json")).read_text())
                self.assertEqual(report["result"], "failed")
                self.assertEqual(report["residentBudget"]["result"], "failed")

    def test_run_case_retains_over_budget_samples_and_cleans_owned_artifacts(self):
        # Docker/Go/PG boundaries are fakes; the controller, RSS rejection,
        # owned fixture creation, finally cleanup and JSON persistence are real.
        with tempfile.TemporaryDirectory(prefix="resident-contract-", dir=Path.cwd()) as folder:
            root = Path(folder)
            budget, _ = documents(root)
            profile = profile_fixture()
            profile["resident"]["samples"][10]["rssBytes"] = budget["profiles"][0]["processRssBudgetBytes"] + 1
            fixtures = root / "fixtures"
            fixtures.mkdir()
            (fixtures / "video-180p.mp4").write_bytes(b"synthetic test fixture")
            (root / "tools/manifest.json").write_text(json.dumps({"mediaTools": {"platforms": {"linux-amd64": {}}}}))
            log = "\n".join(json.dumps(value) for value in (
                {"memoryProfile": profile}, {"shutdown": "passed"}, {"cancellationRecovery": "passed"}))
            log += '\n{"readyForReplacement":true}\n{"readyForSIGTERM":true}\nPASS\n'
            calls = []
            def run(argv, **_kwargs):
                calls.append(argv)
                if argv[:2] == ["docker", "logs"]:
                    output = log.encode()
                elif argv[:2] == ["docker", "inspect"]:
                    output = b'{"Running":false,"Status":"exited","ExitCode":0,"OOMKilled":false}'
                else:
                    output = b"owned\n"
                return subprocess.CompletedProcess(argv, 0, output)
            with patch.object(controller, "ROOT", root), patch.object(controller.sys, "platform", "linux"), \
                    patch.object(controller, "WITH_MEMORY", True), patch.object(controller, "WITH_SUSTAINED", True), \
                    patch.object(controller, "WITH_FAMILY", True), patch.object(controller, "WITH_IGNORE", True), \
                    patch.object(controller, "source_digest", return_value="a" * 64), \
                    patch.object(controller, "select_fixtures", return_value=(fixtures, {})), \
                    patch.object(controller.subprocess, "run", side_effect=run), \
                    patch.object(controller, "check_production_entry"), patch.object(controller, "inspect_owned", return_value={}), \
                    patch.object(controller, "validate_memory_profile", return_value={"validated": True}), \
                    patch.object(controller, "container_state", return_value={"status": "exited"}), \
                    patch.dict(os.environ, {"JELEE_TEST_DATABASE_URL": "postgres://user:PRIVATE@localhost/jelee_test"}):
                identity = {"budgetSha256": "b" * 64, "baselineEvidenceSha256": "c" * 64}
                with self.assertRaises(resident.Rejected):
                    controller.run_case(10, gogc=100, resident_budget=budget, budget_identity=identity)
            report = json.loads((root / (".testdata/" + controller.EVIDENCE_PREFIX + "-case-10-gogc100.json")).read_text())
            self.assertEqual(report["result"], "failed")
            self.assertTrue(report["testArtifactsCleaned"])
            self.assertTrue(report["residentValidation"]["samplesValidated"])
            self.assertNotIn("budgetPassed", report["residentValidation"])
            self.assertEqual(report["residentBudget"]["result"], "failed")
            self.assertEqual(report["residentBudget"]["observedPeakRssBytes"], report["residentBudget"]["processRssBudgetBytes"] + 1)
            self.assertEqual(report["residentBudget"]["budgetSha256"], identity["budgetSha256"])
            self.assertTrue(any("--cleanup-probe-worker" in call for call in calls))
            self.assertTrue(any(call[:3] == ["docker", "container", "rm"] for call in calls))
            self.assertTrue(any(call[:3] == ["docker", "image", "rm"] for call in calls))
            self.assertNotIn("PRIVATE", json.dumps(report))


if __name__ == "__main__":
    unittest.main()
