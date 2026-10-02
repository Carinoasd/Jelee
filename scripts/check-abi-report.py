#!/usr/bin/env python3
"""Validate exact reviewed ABI migration diagnostics and retain every raw report."""

import argparse
from collections import Counter
import json
import os
from pathlib import Path
import sys


SUCCESS = "APICompat ran successfully without finding any breaking changes."
BREAKING = (
    "API breaking changes found. If those are intentional, the APICompat suppression "
    "file can be updated by specifying the '--generate-suppression-file' parameter."
)
MAX_REPORT_BYTES = 4 * 1024 * 1024


def fail(message):
    raise ValueError(message)


def object_pairs(pairs):
    result = {}
    for key, value in pairs:
        if key in result:
            fail(f"duplicate JSON key: {key}")
        result[key] = value
    return result


def exact_keys(value, expected, context):
    if not isinstance(value, dict) or set(value) != set(expected):
        fail(f"{context}: missing or unknown schema fields")


def text_field(value, context):
    if not isinstance(value, str) or not value or any(c in value for c in "\r\n\x00"):
        fail(f"{context}: expected nonempty single-line text")
    return value


def full_sha(value, context):
    text_field(value, context)
    if len(value) != 40 or any(c not in "0123456789abcdef" for c in value):
        fail(f"{context}: expected full lowercase commit SHA")


def expected_lines(comparison):
    if not comparison["diagnostics"]:
        return [SUCCESS]
    return [
        *comparison["preamble"],
        "API compatibility errors between "
        f"'{comparison['diagnosticLeft']}' (left) and "
        f"'{comparison['diagnosticRight']}' (right):",
        *(f"{item['diagnostic']}: {item['message']}" for item in comparison["diagnostics"]),
        BREAKING,
    ]


def load_manifest(path):
    manifest = json.loads(path.read_text(encoding="utf-8"), object_pairs_hook=object_pairs)
    exact_keys(manifest, (
        "schemaVersion", "apiCompatVersion", "apiCompatInformationalVersion", "observedHead", "commonAncestor",
        "sourceJob", "reportSha256", "approvedHistoricalDiagnosticCount",
        "namingBaselineCommit", "comparisons",
    ), "manifest")
    if type(manifest["schemaVersion"]) is not int or manifest["schemaVersion"] != 1:
        fail("unsupported manifest schema")
    if manifest["apiCompatVersion"] != "10.0.401":
        fail("unsupported ApiCompat version; review the transcript contract before upgrading")
    if manifest["apiCompatInformationalVersion"] != "10.0.401+e34a38d2ae1fc26406a317517196e55c68ff83ab":
        fail("unsupported ApiCompat binary informational version")
    for field in ("observedHead", "commonAncestor", "namingBaselineCommit"):
        full_sha(manifest[field], field)
    if not isinstance(manifest["comparisons"], list) or len(manifest["comparisons"]) != 9:
        fail("exactly eight historical comparisons and one current contract are required")
    if (type(manifest["approvedHistoricalDiagnosticCount"]) is not int
            or manifest["approvedHistoricalDiagnosticCount"] != 52):
        fail("this reviewed migration contains exactly 52 historical diagnostics")
    ids, report_names, operands = set(), set(), set()
    roles = Counter()
    diagnostic_count = 0
    for comparison in manifest["comparisons"]:
        exact_keys(comparison, (
            "id", "role", "report", "left", "right", "diagnosticLeft",
            "diagnosticRight", "expectedExit", "preamble", "diagnostics",
        ), "comparison")
        for field in ("id", "role", "report", "left", "right", "diagnosticLeft", "diagnosticRight"):
            text_field(comparison[field], field)
        name = comparison["report"]
        if name in (".", "..") or any(c not in "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789.-_" for c in name):
            fail("report must be a plain portable file basename")
        pair = (comparison["left"], comparison["right"])
        if comparison["id"] in ids or name in report_names or pair in operands:
            fail("duplicate comparison, report, or assembly pair")
        ids.add(comparison["id"])
        report_names.add(name)
        operands.add(pair)
        for side in ("left", "right"):
            path = comparison[side]
            if not path.startswith("./abi-") or ".." in path.split("/") or "\\" in path:
                fail("assembly operands must stay in downloaded ABI artifact directories")
            if comparison["diagnostic" + side.title()] not in (path, side):
                fail("diagnostic operand must identify its exact input or missing counterpart")
        role = comparison["role"]
        roles[role] += 1
        if role not in ("historical", "current-contract"):
            fail("unknown comparison role")
        if type(comparison["expectedExit"]) is not int or comparison["expectedExit"] not in (0, 1):
            fail("expectedExit must be 0 or 1")
        if not isinstance(comparison["preamble"], list):
            fail("preamble must be an exact line list")
        for line in comparison["preamble"]:
            text_field(line, "preamble line")
        if len(comparison["preamble"]) != len(set(comparison["preamble"])):
            fail("duplicate expected preamble")
        diagnostics = comparison["diagnostics"]
        if not isinstance(diagnostics, list):
            fail("diagnostics must be a list")
        seen = set()
        for diagnostic in diagnostics:
            exact_keys(diagnostic, (
                "diagnostic", "symbol", "message", "requirements", "introducingCommit",
            ), "diagnostic")
            for field in ("diagnostic", "symbol", "message"):
                text_field(diagnostic[field], field)
            code = diagnostic["diagnostic"]
            if len(code) != 6 or not code.startswith("CP") or any(c not in "0123456789" for c in code[2:]):
                fail("diagnostic must be an exact CP diagnostic identifier")
            if f"'{diagnostic['symbol']}'" not in diagnostic["message"]:
                fail("exact symbol must occur in the diagnostic message")
            full_sha(diagnostic["introducingCommit"], "introducingCommit")
            requirements = diagnostic["requirements"]
            if (not isinstance(requirements, list) or not requirements
                    or any(r not in ("G00", "G05", "G05.1", "G11.5", "G28.1") for r in requirements)
                    or len(requirements) != len(set(requirements))):
                fail("diagnostic needs an exact reviewed requirement mapping")
            key = (code, diagnostic["symbol"])
            if key in seen:
                fail("duplicate approved diagnostic")
            seen.add(key)
        if bool(diagnostics) != bool(comparison["expectedExit"]):
            fail("approved diagnostic set and expected raw status disagree")
        if role == "current-contract" and (diagnostics or comparison["preamble"]):
            fail("the current Naming contract permits no accepted break or extra output")
        if not diagnostics and comparison["preamble"]:
            fail("compatible reports may contain only the exact success marker")
        diagnostic_count += len(diagnostics)
        lines = expected_lines(comparison)
        if len(lines) != len(set(lines)):
            fail("duplicate expected transcript line")
    if roles != {"historical": 8, "current-contract": 1} or diagnostic_count != 52:
        fail("missing comparison role or historical diagnostic")
    return manifest


def limited_read(path, encoding="utf-8"):
    if path.stat().st_size > MAX_REPORT_BYTES:
        fail(f"report exceeds review size limit: {path.name}")
    return path.read_text(encoding=encoding)


def validate(manifest, reports, receipt, tool_receipt):
    errors, results = [], []
    for path, expected, label in (
        (receipt, manifest["namingBaselineCommit"], "Naming baseline commit"),
        (tool_receipt, manifest["apiCompatInformationalVersion"], "actual ApiCompat binary version"),
    ):
        try:
            if limited_read(path) != expected + "\n":
                errors.append(f"{label} receipt does not match the reviewed manifest")
        except (OSError, UnicodeError, ValueError) as error:
            errors.append(f"{label} receipt: {error}")
    expected_files = {"report.md", "validation.json", "tool-version.txt"}
    for comparison in manifest["comparisons"]:
        expected_files.update((comparison["report"] + ".txt", comparison["report"] + ".exit"))
    for path in reports.iterdir():
        if path.name not in expected_files or not path.is_file():
            errors.append(f"unexpected report artifact: {path.name}")
    for comparison in manifest["comparisons"]:
        local_errors = []
        raw, status = "", None
        try:
            raw = limited_read(reports / (comparison["report"] + ".txt"))
        except (OSError, UnicodeError, ValueError) as error:
            local_errors.append(f"missing or unreadable raw report: {error}")
        try:
            status_text = limited_read(reports / (comparison["report"] + ".exit"), "ascii")
            digits = status_text[:-1] if status_text.endswith("\n") else ""
            if not digits or any(c not in "0123456789" for c in digits) or str(int(digits)) != digits:
                fail("status must be one canonical decimal integer followed by a newline")
            status = int(digits)
            if status != comparison["expectedExit"]:
                local_errors.append(f"raw tool exit {status}; expected {comparison['expectedExit']}")
        except (OSError, UnicodeError, ValueError) as error:
            local_errors.append(f"missing or invalid tool status: {error}")
        actual_lines = [line for line in raw.splitlines() if line != ""]
        observed, expected = Counter(actual_lines), Counter(expected_lines(comparison))
        for line, count in (observed - expected).items():
            local_errors.append(f"unexpected or duplicate output ({count}): {line}")
        for line, count in (expected - observed).items():
            local_errors.append(f"unmatched approved output ({count}): {line}")
        errors.extend(f"{comparison['id']}: {error}" for error in local_errors)
        results.append({
            "id": comparison["id"], "role": comparison["role"], "rawExit": status,
            "approvedDiagnosticCount": len(comparison["diagnostics"]),
            "passed": not local_errors, "errors": local_errors, "raw": raw,
        })
    return errors, results


def write_reports(directory, errors, results):
    outcome = "失敗：ABI 遷移契約未通過" if errors else "通過：符合已核准遷移契約；舊 ABI 仍有 52 條有意破壞"
    lines = ["# ABI 遷移契約報告", "", outcome, ""]
    for error in errors:
        lines.append("- " + error)
    for result in results:
        lines.extend((
            "", "## " + result["id"], "",
            f"原始結束碼：{result['rawExit']}；已核准診斷：{result['approvedDiagnosticCount']}；"
            f"門禁結果：{'通過' if result['passed'] else '失敗'}", "",
        ))
        fence = "```"
        while fence in result["raw"]:
            fence += "`"
        lines.extend((fence + "text", result["raw"], fence))
    report = "\n".join(lines) + "\n"
    (directory / "report.md").write_text(report, encoding="utf-8")
    machine = {"passed": not errors, "errors": errors, "comparisons": [
        {key: value for key, value in result.items() if key != "raw"} for result in results
    ]}
    (directory / "validation.json").write_text(json.dumps(machine, indent=2) + "\n", encoding="utf-8")
    if os.environ.get("GITHUB_STEP_SUMMARY"):
        with open(os.environ["GITHUB_STEP_SUMMARY"], "a", encoding="utf-8") as summary:
            summary.write(report)
    print(outcome)
    for error in errors:
        print(error, file=sys.stderr)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--manifest", type=Path, default=Path("tools/abi/expected-breaks.json"))
    parser.add_argument("--reports", type=Path, default=Path("abi-report"))
    parser.add_argument("--naming-baseline-receipt", type=Path, default=Path("abi-naming-base/baseline.sha"))
    args = parser.parse_args()
    args.reports.mkdir(parents=True, exist_ok=True)
    errors, results = [], []
    try:
        manifest = load_manifest(args.manifest)
        errors, results = validate(manifest, args.reports, args.naming_baseline_receipt, args.reports / "tool-version.txt")
    except (OSError, UnicodeError, ValueError, TypeError) as error:
        errors.append(f"ABI validation input error: {error}")
    write_reports(args.reports, errors, results)
    return 1 if errors else 0


if __name__ == "__main__":
    sys.exit(main())
