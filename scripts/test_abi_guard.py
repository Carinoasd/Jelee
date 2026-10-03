#!/usr/bin/env python3
"""Lightweight ABI guard regressions; these do not build or mutate real assemblies."""

import importlib.util
import json
import os
from pathlib import Path
import shutil
import unittest
from unittest.mock import patch
import uuid


ROOT = Path(__file__).resolve().parents[1]
SPEC = importlib.util.spec_from_file_location("abi_guard", ROOT / "scripts/check-abi-report.py")
GUARD = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(GUARD)
FIXTURE = json.loads((ROOT / "scripts/fixtures/abi-legacy-report.json").read_text(encoding="utf-8"))
MANIFEST_TEXT = (ROOT / "tools/abi/expected-breaks.json").read_text(encoding="utf-8")


class AbiGuardTests(unittest.TestCase):
    def setUp(self):
        self.temp_parent = ROOT / ".testdata/abi-guard-fixtures"
        self.temp_parent.mkdir(parents=True, exist_ok=True)
        self.root = self.temp_parent / uuid.uuid4().hex
        self.root.mkdir()
        self.addCleanup(self.cleanup_fixture)
        summary_env = patch.dict(os.environ, {"GITHUB_STEP_SUMMARY": str(self.root / "fixture-summary.md")})
        summary_env.start()
        self.addCleanup(summary_env.stop)
        self.reports = self.root / "reports"
        self.reports.mkdir()
        self.manifest_path = self.root / "manifest.json"
        self.manifest_path.write_text(MANIFEST_TEXT, encoding="utf-8")
        self.manifest = GUARD.load_manifest(self.manifest_path)
        for report in FIXTURE["reports"]:
            (self.reports / (report["report"] + ".txt")).write_text(report["raw"], encoding="utf-8")
            (self.reports / (report["report"] + ".exit")).write_text(str(report["exit"]) + "\n", encoding="ascii")
        self.receipt = self.root / "baseline.sha"
        self.receipt.write_text(self.manifest["namingBaselineCommit"] + "\n", encoding="ascii")
        (self.reports / "tool-version.txt").write_text("10.0.401+e34a38d2ae1fc26406a317517196e55c68ff83ab\n", encoding="ascii")

    def cleanup_fixture(self):
        resolved = self.root.resolve()
        if resolved.parent != self.temp_parent.resolve() or len(resolved.name) != 32:
            raise ValueError("fixture cleanup escaped its dedicated temporary directory")
        shutil.rmtree(resolved)

    def validate(self):
        return GUARD.validate(self.manifest, self.reports, self.receipt, self.reports / "tool-version.txt")

    def assert_rejected(self):
        errors, _ = self.validate()
        self.assertTrue(errors)
        return errors

    def first_break(self):
        return next(item for item in self.manifest["comparisons"] if item["diagnostics"])

    def raw_path(self, comparison):
        return self.reports / (comparison["report"] + ".txt")

    def rewrite_manifest(self, edit):
        manifest = json.loads(MANIFEST_TEXT)
        edit(manifest)
        self.manifest_path.write_text(json.dumps(manifest), encoding="utf-8")
        with self.assertRaises(ValueError):
            GUARD.load_manifest(self.manifest_path)

    def test_saved_raw_reports_and_synthetic_naming_success(self):
        errors, results = self.validate()
        self.assertEqual(errors, [])
        self.assertEqual(len(results), 9)
        self.assertEqual(sum(item["approvedDiagnosticCount"] for item in results), 52)

    def test_new_unapproved_diagnostic(self):
        comparison = self.first_break()
        with self.raw_path(comparison).open("a", encoding="utf-8") as output:
            output.write("CP0001: Type 'Fixture.Unapproved' exists on left but not on right\n")
        self.assert_rejected()

    def test_stale_entry_even_if_tool_returns_zero(self):
        comparison = self.first_break()
        self.raw_path(comparison).write_text("APICompat ran successfully without finding any breaking changes.\n", encoding="utf-8")
        (self.reports / (comparison["report"] + ".exit")).write_text("0\n", encoding="ascii")
        errors = self.assert_rejected()
        self.assertTrue(any("unmatched approved output" in item for item in errors))

    def test_one_removed_diagnostic_with_other_failures_remaining(self):
        comparison = self.first_break()
        diagnostic = comparison["diagnostics"][0]
        line = diagnostic["diagnostic"] + ": " + diagnostic["message"] + "\n"
        path = self.raw_path(comparison)
        path.write_text(path.read_text(encoding="utf-8").replace(line, "", 1), encoding="utf-8")
        self.assert_rejected()

    def test_duplicate_observed_diagnostic(self):
        comparison = self.first_break()
        diagnostic = comparison["diagnostics"][0]
        with self.raw_path(comparison).open("a", encoding="utf-8") as output:
            output.write(diagnostic["diagnostic"] + ": " + diagnostic["message"] + "\n")
        self.assert_rejected()

    def test_exact_case_and_symbol_match(self):
        comparison = self.first_break()
        path = self.raw_path(comparison)
        symbol = comparison["diagnostics"][0]["symbol"]
        path.write_text(path.read_text(encoding="utf-8").replace(symbol, symbol.lower(), 1), encoding="utf-8")
        self.assert_rejected()

    def test_unknown_plain_error_with_zero_status(self):
        comparison = next(item for item in self.manifest["comparisons"] if not item["diagnostics"])
        with self.raw_path(comparison).open("a", encoding="utf-8") as output:
            output.write("Unable to load assembly: invalid metadata\n")
        self.assert_rejected()

    def test_tool_failure_127(self):
        comparison = self.first_break()
        (self.reports / (comparison["report"] + ".exit")).write_text("127\n", encoding="ascii")
        self.assert_rejected()

    def test_missing_raw_report(self):
        self.raw_path(self.first_break()).unlink()
        self.assert_rejected()

    def test_missing_status(self):
        (self.reports / (self.first_break()["report"] + ".exit")).unlink()
        self.assert_rejected()

    def test_invalid_status_and_empty_report(self):
        comparison = self.first_break()
        (self.reports / (comparison["report"] + ".exit")).write_text("1\n0\n", encoding="ascii")
        self.raw_path(comparison).write_text("", encoding="utf-8")
        self.assert_rejected()

    def test_extra_report(self):
        (self.reports / "unlisted.txt").write_text("unexpected", encoding="utf-8")
        self.assert_rejected()

    def test_wrong_or_missing_naming_baseline_receipt(self):
        self.receipt.write_text("0" * 40 + "\n", encoding="ascii")
        self.assert_rejected()
        self.receipt.unlink()
        self.assert_rejected()

    def test_wrong_tool_version(self):
        (self.reports / "tool-version.txt").write_text("10.0.402\n", encoding="ascii")
        self.assert_rejected()

    def test_new_naming_contract_diagnostic_is_not_covered_by_old_identity_breaks(self):
        comparison = next(item for item in self.manifest["comparisons"] if item["role"] == "current-contract")
        self.raw_path(comparison).write_text("CP0002: Member 'Fixture.VideoResolver.Resolve(string)' exists on left but not on right\n", encoding="utf-8")
        (self.reports / (comparison["report"] + ".exit")).write_text("1\n", encoding="ascii")
        errors = self.assert_rejected()
        self.assertTrue(any(comparison["id"] in item for item in errors))

    def test_duplicate_approved_diagnostic(self):
        def edit(manifest):
            item = next(item for item in manifest["comparisons"] if len(item["diagnostics"]) > 1)
            item["diagnostics"][1] = dict(item["diagnostics"][0])
        self.rewrite_manifest(edit)

    def test_missing_symbol_or_assembly_identity(self):
        def edit(manifest):
            next(item for item in manifest["comparisons"] if item["diagnostics"])["diagnostics"][0].pop("symbol")
        self.rewrite_manifest(edit)
        self.rewrite_manifest(lambda manifest: manifest["comparisons"][0].pop("left"))

    def test_duplicate_json_keys(self):
        self.manifest_path.write_text(MANIFEST_TEXT.replace('"schemaVersion": 1', '"schemaVersion": 1, "schemaVersion": 1', 1), encoding="utf-8")
        with self.assertRaises(ValueError):
            GUARD.load_manifest(self.manifest_path)

    def test_missing_naming_contract(self):
        self.rewrite_manifest(lambda manifest: manifest["comparisons"].pop())

    def test_raw_output_and_validation_are_retained(self):
        errors, results = self.validate()
        GUARD.write_reports(self.reports, errors, results)
        report = (self.reports / "report.md").read_text(encoding="utf-8")
        self.assertEqual((self.root / "fixture-summary.md").read_text(encoding="utf-8"), report)
        for item in FIXTURE["reports"]:
            self.assertIn(item["raw"], report)
        self.assertTrue(json.loads((self.reports / "validation.json").read_text(encoding="utf-8"))["passed"])
        self.assertEqual(self.validate()[0], [])


if __name__ == "__main__":
    unittest.main(verbosity=2)
