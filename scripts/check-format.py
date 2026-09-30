#!/usr/bin/env python3
import subprocess
import sys
sys.dont_write_bytecode = True
from toolchain import ROOT, selected, go_environment

_, spec, _ = selected()
files = sorted(str(p) for folder in ("cmd", "internal", "tools") for p in (ROOT / folder).rglob("*.go"))
result = subprocess.run([str(ROOT / ".tools" / spec["installPath"] / "go/bin/gofmt"), "-l"] + files,
                        check=True, text=True, capture_output=True, env=go_environment(spec))
if result.stdout:
    print("Run make fmt; unformatted files:\n" + result.stdout, file=sys.stderr)
    sys.exit(1)
