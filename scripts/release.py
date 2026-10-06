#!/usr/bin/env python3
"""Release packaging from a SemVer tag (G01.3).

Subcommands:

  check --tag vX.Y.Z      validate the tag, the CHANGELOG section and the
                          versions in web/package.json and buildinfo
  build --tag vX.Y.Z      check, then cross-compile jelee, jelee-cli and
                          jelee-migrate, archive them per platform, write
                          SHA256SUMS and release-notes.md
  dry-run [--version V]   the same build without a tag into
                          .testdata/release-dry-run; V defaults to
                          buildinfo.DefaultVersion and the notes fall back
                          to the "Unreleased" CHANGELOG section

The script never creates tags, pushes, or publishes anything:
.github/workflows/release.yml runs `build` for a pushed tag and uploads the
result as a draft release that a maintainer publishes by hand.
Standard library only.
"""
from __future__ import annotations

import argparse
import gzip
import hashlib
import io
import json
import os
import re
import shutil
import subprocess
import sys
import tarfile
import time
import zipfile
from pathlib import Path

ROOT = Path(__file__).resolve().parent.parent
MODULE = "github.com/MoYuanCN/Jelee"
COMMANDS = ("jelee", "jelee-cli", "jelee-migrate")
DEFAULT_TARGETS = ("linux/amd64", "linux/arm64", "windows/amd64")
# Shipped next to the binaries in every archive.
BUNDLED_FILES = ("LICENSE", "README.md", "CHANGELOG.md", "docs/LICENSE-COMPLIANCE.md")
# Same grammar as internal/platform/buildinfo (strict SemVer 2.0.0).
SEMVER = re.compile(
    r"^(0|[1-9][0-9]{0,8})\.(0|[1-9][0-9]{0,8})\.(0|[1-9][0-9]{0,8})"
    r"(?:-[0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*)?(?:\+[0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*)?$"
)


class ReleaseError(Exception):
    """A release precondition that is not met."""


def parse_tag(tag: str) -> str:
    """Return the version of a vX.Y.Z[-pre][+build] tag."""
    if not tag.startswith("v"):
        raise ReleaseError(f"tag {tag!r} must start with 'v' (vMAJOR.MINOR.PATCH)")
    version = tag[1:]
    if len(version) > 64 or not SEMVER.match(version):
        raise ReleaseError(f"tag {tag!r} is not semantic versioning (vMAJOR.MINOR.PATCH[-pre][+build])")
    return version


def changelog_section(text: str, version: str) -> str:
    """Return the body of the "## [version]" or "## version" section."""
    heading = re.compile(r"^## \[?v?" + re.escape(version) + r"\]?(?:\s|$)")
    lines = text.splitlines()
    for index, line in enumerate(lines):
        if heading.match(line):
            body = []
            for following in lines[index + 1:]:
                if following.startswith("## "):
                    break
                body.append(following)
            notes = "\n".join(body).strip()
            if not notes:
                raise ReleaseError(f"CHANGELOG.md section for {version} is empty")
            return notes
    raise ReleaseError(f"CHANGELOG.md has no '## [{version}]' section; move the Unreleased entries under it before tagging")


def default_version(root: Path) -> str:
    source = (root / "internal/platform/buildinfo/buildinfo.go").read_text(encoding="utf-8")
    match = re.search(r'const DefaultVersion = "([^"]+)"', source)
    if not match:
        raise ReleaseError("cannot find buildinfo.DefaultVersion")
    return match.group(1)


def check_versions(root: Path, version: str) -> None:
    """The web client and unstamped builds must report the released version."""
    web = json.loads((root / "web/package.json").read_text(encoding="utf-8"))["version"]
    problems = []
    if web != version:
        problems.append(f"web/package.json version is {web}")
    built = default_version(root)
    if built != version:
        problems.append(f"internal/platform/buildinfo DefaultVersion is {built}")
    if problems:
        raise ReleaseError(f"release {version}: " + "; ".join(problems) + " (bump them in the release commit)")


def source_epoch(root: Path) -> int:
    """Commit time of HEAD for reproducible archive timestamps."""
    if "SOURCE_DATE_EPOCH" in os.environ:
        return int(os.environ["SOURCE_DATE_EPOCH"])
    try:
        out = subprocess.run(["git", "-C", str(root), "log", "-1", "--format=%ct"], check=True, capture_output=True, text=True)
        return int(out.stdout.strip())
    except (OSError, subprocess.CalledProcessError, ValueError):
        return 315532800  # 1980-01-01, the earliest zip timestamp


def build_binaries(root: Path, go: str, version: str, target: str, stage: Path) -> list[Path]:
    goos, goarch = target.split("/")
    env = dict(os.environ, GOOS=goos, GOARCH=goarch, CGO_ENABLED="0")
    suffix = ".exe" if goos == "windows" else ""
    ldflags = f"-s -w -X {MODULE}/internal/platform/buildinfo.version={version}"
    built = []
    for command in COMMANDS:
        out = stage / (command + suffix)
        subprocess.run([go, "build", "-trimpath", "-ldflags", ldflags, "-o", str(out), f"./cmd/{command}"],
                       cwd=root, env=env, check=True)
        if not out.is_file():
            raise ReleaseError(f"go build did not produce {out.name}")
        built.append(out)
    return built


def write_archive(path: Path, prefix: str, files: list[tuple[str, Path]], epoch: int) -> None:
    """tar.gz or zip with sorted entries, fixed owner and timestamps."""
    entries = sorted(files)
    if path.suffix == ".zip":
        stamp = max(epoch, 315532800)
        date_time = time.gmtime(stamp)[:6]
        with zipfile.ZipFile(path, "w", compression=zipfile.ZIP_DEFLATED) as archive:
            for name, source in entries:
                info = zipfile.ZipInfo(f"{prefix}/{name}", date_time=date_time)
                info.external_attr = (0o755 if os.access(source, os.X_OK) else 0o644) << 16
                info.compress_type = zipfile.ZIP_DEFLATED
                archive.writestr(info, source.read_bytes())
        return
    raw = io.BytesIO()
    with tarfile.open(fileobj=raw, mode="w", format=tarfile.PAX_FORMAT) as archive:
        for name, source in entries:
            data = source.read_bytes()
            info = tarfile.TarInfo(f"{prefix}/{name}")
            info.size = len(data)
            info.mtime = epoch
            info.mode = 0o755 if os.access(source, os.X_OK) else 0o644
            info.uid = info.gid = 0
            info.uname = info.gname = ""
            archive.addfile(info, io.BytesIO(data))
    with open(path, "wb") as handle, gzip.GzipFile(filename="", mode="wb", fileobj=handle, mtime=epoch) as compressed:
        compressed.write(raw.getvalue())


def sha256sums(paths: list[Path]) -> str:
    lines = []
    for path in sorted(paths, key=lambda p: p.name):
        digest = hashlib.sha256(path.read_bytes()).hexdigest()
        lines.append(f"{digest}  {path.name}")
    return "\n".join(lines) + "\n"


def verify_sums(out: Path) -> None:
    for line in (out / "SHA256SUMS").read_text(encoding="utf-8").splitlines():
        digest, name = line.split("  ", 1)
        if hashlib.sha256((out / name).read_bytes()).hexdigest() != digest:
            raise ReleaseError(f"checksum mismatch for {name}")


def release_notes(version: str, notes: str, sums: str, tag: str | None) -> str:
    source = f"tag `{tag}`" if tag else "a dry run without a tag (not a release)"
    return (f"# Jelee {version}\n\n{notes}\n\n## Artifacts\n\n"
            f"Built from {source} by scripts/release.py with Go `-trimpath`, CGO disabled and "
            f"`buildinfo.version={version}`. Verify downloads with `sha256sum -c SHA256SUMS`.\n\n"
            f"```\n{sums}```\n")


def build(root: Path, go: str, version: str, notes: str, out: Path, targets: list[str], tag: str | None,
          web_dist: Path | None) -> list[Path]:
    out.mkdir(parents=True, exist_ok=True)
    for stale in out.iterdir():
        if stale.is_file():
            stale.unlink()
    epoch = source_epoch(root)
    stage_root = out / ".stage"
    archives = []
    for target in targets:
        goos, goarch = target.split("/")
        stage = stage_root / f"{goos}-{goarch}"
        stage.mkdir(parents=True, exist_ok=True)
        binaries = build_binaries(root, go, version, target, stage)
        files = [(b.name, b) for b in binaries] + [(Path(f).name, root / f) for f in BUNDLED_FILES]
        extension = ".zip" if goos == "windows" else ".tar.gz"
        name = f"jelee-{version}-{goos}-{goarch}"
        archive = out / (name + extension)
        write_archive(archive, name, files, epoch)
        archives.append(archive)
    if web_dist is not None:
        if not (web_dist / "index.html").is_file():
            raise ReleaseError(f"{web_dist} has no index.html; run make web-build first")
        files = [(p.relative_to(web_dist).as_posix(), p) for p in web_dist.rglob("*") if p.is_file()]
        name = f"jelee-web-{version}"
        archive = out / (name + ".tar.gz")
        write_archive(archive, name, files, epoch)
        archives.append(archive)
    shutil.rmtree(stage_root)
    sums = sha256sums(archives)
    (out / "SHA256SUMS").write_text(sums, encoding="utf-8")
    (out / "release-notes.md").write_text(release_notes(version, notes, sums, tag), encoding="utf-8")
    verify_sums(out)
    return archives


def default_go(root: Path) -> str:
    return os.environ.get("GO") or str(root / ".bin" / "go")


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    sub = parser.add_subparsers(dest="command", required=True)
    for name in ("check", "build", "dry-run"):
        p = sub.add_parser(name)
        p.add_argument("--root", type=Path, default=ROOT)
        if name == "dry-run":
            p.add_argument("--version", help="version to stamp (default: buildinfo.DefaultVersion)")
        else:
            p.add_argument("--tag", required=True, help="vMAJOR.MINOR.PATCH tag, e.g. $GITHUB_REF_NAME")
        if name != "check":
            p.add_argument("--go", help="go executable (default: $GO or .bin/go)")
            p.add_argument("--out", type=Path)
            p.add_argument("--targets", default=",".join(DEFAULT_TARGETS))
            p.add_argument("--web-dist", type=Path, help="also archive a built web client (web/dist)")
    args = parser.parse_args(argv)
    root = args.root.resolve()
    try:
        changelog = (root / "CHANGELOG.md").read_text(encoding="utf-8")
        tag = None
        if args.command == "dry-run":
            version = args.version or default_version(root)
            if not SEMVER.match(version):
                raise ReleaseError(f"{version!r} is not semantic versioning")
            try:
                notes = changelog_section(changelog, version)
            except ReleaseError:
                notes = changelog_section(changelog, "Unreleased")
            out = args.out or root / ".testdata" / "release-dry-run"
        else:
            tag = args.tag
            version = parse_tag(tag)
            notes = changelog_section(changelog, version)
            check_versions(root, version)
            out = getattr(args, "out", None) or root / "dist" / "release"
        if args.command == "check":
            print(f"release check: tag {tag} -> version {version}; CHANGELOG section {len(notes)} bytes; versions consistent")
            return 0
        targets = [t.strip() for t in args.targets.split(",") if t.strip()]
        for target in targets:
            if not re.match(r"^[a-z0-9]+/[a-z0-9]+$", target):
                raise ReleaseError(f"bad target {target!r}; want GOOS/GOARCH")
        archives = build(root, args.go or default_go(root), version, notes, out, targets, tag, args.web_dist)
        print(f"release {args.command}: version {version}, {len(archives)} archives in {out}")
        for archive in archives:
            print(f"  {archive.name} {archive.stat().st_size} bytes")
        return 0
    except ReleaseError as error:
        print(f"release: {error}", file=sys.stderr)
        return 1
    except subprocess.CalledProcessError as error:
        print(f"release: command failed with exit {error.returncode}: {error.cmd[:3]}", file=sys.stderr)
        return 1
    except OSError as error:
        print(f"release: {error}", file=sys.stderr)
        return 1


if __name__ == "__main__":
    sys.exit(main())
