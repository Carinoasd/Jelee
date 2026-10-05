#!/usr/bin/env python3
"""Check the real merged Compose model without starting any containers."""
import copy
import json
import os
from pathlib import Path
import shutil
import subprocess
import sys
import tempfile


ROOT = Path(__file__).resolve().parent.parent
FAILURE = "Compose memory check failed"


class MemoryComposeCheckError(RuntimeError):
    """A safe failure without command output, configuration, or credentials."""


def _require(condition):
    if not condition:
        raise MemoryComposeCheckError(FAILURE)


def _unique_object(pairs):
    result = {}
    for key, value in pairs:
        _require(key not in result)
        result[key] = value
    return result


def _reject_constant(_value):
    raise MemoryComposeCheckError(FAILURE)


def _configuration(docker, environment, env_file, with_memory):
    command = [docker, "compose", "--project-name", "jelee-memory-check",
               "--env-file", str(env_file), "-f", "deploy/docker-compose.yml"]
    if with_memory:
        command += ["-f", "deploy/docker-compose.memory.yml"]
    command += ["config", "--format", "json"]
    completed = subprocess.run(command, cwd=ROOT, env=environment,
                               stdout=subprocess.PIPE, stderr=subprocess.PIPE,
                               timeout=30, check=False)
    # Never return raw stdout/stderr, including on a missing Compose plugin.
    _require(completed.returncode == 0 and 0 < len(completed.stdout) <= 2 * 1024**2)
    model = json.loads(completed.stdout.decode("utf-8"),
                       object_pairs_hook=_unique_object, parse_constant=_reject_constant)
    _require(isinstance(model, dict) and isinstance(model.get("services"), dict))
    return model


def _check_base(model, media):
    services = model["services"]
    _require(set(services) == {"jelee", "postgres", "migrate"})
    service = services["jelee"]
    _require(isinstance(service, dict) and isinstance(service.get("environment"), dict))
    _require("GOGC" not in service["environment"] and "GOMEMLIMIT" not in service["environment"])
    _require("mem_limit" not in service and "memswap_limit" not in service)
    _require(service.get("read_only") is True and service.get("cap_drop") == ["ALL"])
    _require(service.get("security_opt") in (["no-new-privileges:true"], ["no-new-privileges"]))
    volumes = service.get("volumes")
    # Exactly two read-only binds: the media root and the built web client.
    _require(isinstance(volumes, list) and len(volumes) == 2)
    by_target = {volume.get("target"): volume for volume in volumes if isinstance(volume, dict)}
    _require(set(by_target) == {"/media", "/web"})
    volume = by_target["/media"]
    _require(volume.get("type") == "bind" and volume.get("read_only") is True and Path(volume["source"]).resolve() == media.resolve())
    web = by_target["/web"]
    _require(web.get("type") == "bind" and web.get("read_only") is True)
    _require(Path(web["source"]).resolve() == (Path(__file__).resolve().parent.parent / "web" / "dist").resolve())
    _require(service["environment"].get("JELEE_WEB_DIR") == "/web")
    _require(service.get("tmpfs") == ["/tmp:rw,noexec,nosuid,nodev,size=67108864,mode=1777"])
    ports = service.get("ports")
    _require(isinstance(ports, list) and len(ports) == 1)
    port = ports[0]
    _require(port.get("host_ip") == "127.0.0.1" and port.get("target") == 8097)
    _require(str(port.get("published")) == "8097" and port.get("protocol") == "tcp")
    _require("network_mode" not in service and service.get("networks") == {"default": None})


def _check_merged(base, merged, gogc, go_limit, memory_bytes):
    # Compose versions serialize byte quantities as either JSON numbers or
    # canonical decimal strings. Normalize only these two approved fields.
    merged = copy.deepcopy(merged)
    for name in ("mem_limit", "memswap_limit"):
        value = merged["services"]["jelee"][name]
        if type(value) is str:
            _require(value.isascii() and value.isdecimal() and str(int(value)) == value)
            value = int(value)
        _require(type(value) is int and value == memory_bytes)
        merged["services"]["jelee"][name] = value
    expected = copy.deepcopy(base)
    service = expected["services"]["jelee"]
    service["environment"].update(GOGC=gogc, GOMEMLIMIT=go_limit)
    service["mem_limit"] = memory_bytes
    service["memswap_limit"] = memory_bytes
    # Compare the complete model. This also protects other services, top-level
    # networks, mounts, ports, capabilities, tmpfs, and every unrelated field.
    _require(merged == expected)
    actual = merged["services"]["jelee"]
    return {
        "gogc": actual["environment"]["GOGC"],
        "goMemoryLimit": actual["environment"]["GOMEMLIMIT"],
        "memoryLimitBytes": actual["mem_limit"],
        "memorySwapLimitBytes": actual["memswap_limit"],
    }


def run_compose_check():
    """Return a safe report, or raise MemoryComposeCheckError with fixed text.

    This checks actual Compose parsing/merging only. It does not build images,
    start containers, inspect process environments, or verify runtime limits.
    """
    try:
        docker = shutil.which("docker")
        _require(docker is not None)
        # Keep OS/Docker executable discovery, but discard ambient interpolation
        # settings. A real empty env file also works on Windows, unlike /dev/null.
        environment = {
            key: value for key, value in os.environ.items()
            if not key.upper().startswith(("JELEE_", "COMPOSE_"))
            and key.upper() not in {"GOGC", "GOMEMLIMIT", "TMDB_API_KEY", "TMDB_API_KEY_FILE"}
        }
        with tempfile.TemporaryDirectory(prefix="jelee-memory-compose-") as folder:
            temporary = Path(folder)
            env_file = temporary / "empty.env"
            env_file.write_text("", encoding="utf-8")
            media = temporary / "sample-media"
            media.mkdir()
            environment.update(JELEE_POSTGRES_PASSWORD="compose-check-only",
                               JELEE_MEDIA_ROOT=media.as_posix(), COMPOSE_DISABLE_ENV_FILE="1")
            base = _configuration(docker, environment, env_file, False)
            _check_base(base, media)
            cases = [{"name": "base-only", "memoryOverrideApplied": False}]
            for name, overrides, gogc, go_limit, memory in (
                ("memory-default", {}, "100", "512MiB", 768 * 1024**2),
                ("memory-overridden", {"GOGC": "50", "GOMEMLIMIT": "384MiB",
                                       "JELEE_CONTAINER_MEMORY_LIMIT": "640m"},
                 "50", "384MiB", 640 * 1024**2),
            ):
                merged = _configuration(docker, {**environment, **overrides}, env_file, True)
                values = _check_merged(base, merged, gogc, go_limit, memory)
                cases.append({"name": name, "memoryOverrideApplied": True, **values})
        return {
            "result": "passed",
            "scope": "docker compose config parsing and merge only",
            "containersStarted": False,
            "runtimeLimitsValidated": False,
            "onlyExpectedJeleeFieldsChanged": True,
            "otherServicesUnchanged": ["migrate", "postgres"],
            "safetySettingsUnchanged": ["read_only", "volumes", "cap_drop", "security_opt",
                                        "tmpfs", "networks", "ports"],
            "cases": cases,
        }
    except Exception:
        # Suppress chained exceptions too: subprocess/JSON/filesystem errors may
        # embed environment values, configuration fragments, or absolute paths.
        raise MemoryComposeCheckError(FAILURE) from None


def main():
    if len(sys.argv) != 1:
        print(FAILURE, file=sys.stderr)
        return 1
    try:
        report = run_compose_check()
    except MemoryComposeCheckError:
        print(FAILURE, file=sys.stderr)
        return 1
    print(json.dumps(report, sort_keys=True))
    return 0


if __name__ == "__main__":
    sys.exit(main())
