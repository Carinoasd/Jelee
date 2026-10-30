"""Validate bounded container-memory evidence without logging input or secrets.

Callers collect actual cgroup v2 files and one complete Docker inspect object.
These pure functions check that evidence; they do not run Docker, read a cgroup,
or infer the main process's Go settings from a separate probe process.
"""

HARD_MEMORY_BYTES = 768 * 1024 * 1024
GO_MEMORY_LIMIT_BYTES = 512 * 1024 * 1024
OOM_MEMORY_BYTES = 64 * 1024 * 1024
INVALID_EVIDENCE = "container_memory_evidence_invalid"

_MAX_UINT64 = (1 << 64) - 1
_REQUIRED_EVENTS = frozenset(("low", "high", "max", "oom", "oom_kill"))
# Newer kernels add counters to memory.events (the Go observer already accepts
# them). Other well-formed counter names are kept and must stay monotonic; the
# OOM decision uses only the required counters and oom_group_kill.
_MAX_EVENTS = 32
_NO_NEW_PRIVILEGES = frozenset(("no-new-privileges", "no-new-privileges:true", "no-new-privileges=true"))


class Rejected(ValueError):
    """A fixed error code, never an input value or an underlying exception.

    ``check`` names the failed check with a fixed identifier so a report can
    say which contract was not met without retaining any inspected value.
    """

    def __init__(self, code, check="evidence"):
        super().__init__(code)
        self.check = check


def _need(condition, check="evidence"):
    if not condition:
        raise Rejected(INVALID_EVIDENCE, check)


def _object(value, check="shape"):
    _need(type(value) is dict, check)
    return value


def _integer(value, expected=None, check="integer"):
    # bool subclasses int in Python, but is not a numeric measurement.
    _need(type(value) is int and 0 <= value <= _MAX_UINT64, check)
    if expected is not None:
        _need(value == expected, check)
    return value


def _event_name(name):
    return (type(name) is str and 0 < len(name) <= 32 and name[0].isascii() and name[0].islower()
            and all(c == "_" or c.isascii() and (c.islower() or c.isdigit()) for c in name))


def _events(value):
    value = _object(value, "cgroup_events")
    _need(_REQUIRED_EVENTS <= value.keys() and len(value) <= _MAX_EVENTS, "cgroup_events")
    _need(all(_event_name(key) for key in value), "cgroup_events")
    return {key: _integer(value[key], check="cgroup_events") for key in sorted(value)}


def _cgroup(value):
    value = _object(value, "cgroup")
    version = _integer(value.get("cgroupVersion"), 2, "cgroup_version")
    current = _integer(value.get("currentBytes"), check="cgroup_current")
    peak = _integer(value.get("peakBytes"), check="cgroup_peak")
    maximum = _integer(value.get("maxBytes"), HARD_MEMORY_BYTES, "cgroup_max")
    swap = _integer(value.get("swapMaxBytes"), 0, "cgroup_swap_max")
    _need(peak >= current, "cgroup_peak")
    return {"cgroupVersion": version, "currentBytes": current, "peakBytes": peak,
            "maxBytes": maximum, "swapMaxBytes": swap, "events": _events(value.get("events"))}


def _container(inspect, *, oom):
    inspect = _object(inspect, "inspect_shape")
    config = _object(inspect.get("Config"), "inspect_shape")
    host = _object(inspect.get("HostConfig"), "inspect_shape")
    state = _object(inspect.get("State"), "inspect_shape")
    hard, cpus, pids = (OOM_MEMORY_BYTES, 1_000_000_000, 32) if oom else (HARD_MEMORY_BYTES, 2_000_000_000, 128)
    _need(config.get("User") == "65532:65532", "config_user")
    _need(host.get("ReadonlyRootfs") is True, "host_readonly_rootfs")
    _need(host.get("Privileged") is False, "host_privileged")
    _need("CapAdd" in host, "host_cap_add")
    added = host.get("CapAdd")
    _need(added is None or type(added) is list and not added, "host_cap_add")
    dropped = host.get("CapDrop")
    _need(type(dropped) is list and len(dropped) == 1 and dropped[0] in ("ALL", "all"), "host_cap_drop")
    security = host.get("SecurityOpt")
    _need(type(security) is list and len(security) == 1 and type(security[0]) is str and security[0] in _NO_NEW_PRIVILEGES,
          "host_security_opt")
    _integer(host.get("Memory"), hard, "host_memory")
    # Docker's MemorySwap is RAM + swap. Equality with Memory disables swap:
    # https://docs.docker.com/engine/containers/resource_constraints/
    _integer(host.get("MemorySwap"), hard, "host_memory_swap")
    _integer(host.get("NanoCpus"), cpus, "host_nano_cpus")
    _integer(host.get("PidsLimit"), pids, "host_pids_limit")
    _integer(inspect.get("RestartCount"), 0, "restart_count")
    _need(state.get("Status") == "exited" and state.get("Running") is False
          and state.get("Restarting") is False and state.get("Dead") is False, "state_status")
    _need(state.get("Error") == "", "state_error")
    # The exit code is checked before the OOM flag so that a "state_oom_killed"
    # rejection always means every other field, including the exit, matched.
    exit_code = _integer(state.get("ExitCode"), 137 if oom else 0, "state_exit_code")
    _need(state.get("OOMKilled") is oom, "state_oom_killed")
    # Never return Config.Env, bind paths, labels, error text or other inspect
    # fields. Even accepted evidence can contain unrelated private values.
    return {"user": "65532:65532", "readonlyRootfs": True, "privileged": False,
            "capabilitiesDropped": "ALL", "noNewPrivileges": True,
            "memoryBytes": hard, "memorySwapBytes": hard, "nanoCPUs": cpus,
            "pidsLimit": pids, "restartCount": 0, "status": "exited",
            "exitCode": exit_code, "oomKilled": oom}


def validate_container_interval(before, after, inspect):
    """Validate a real cgroup interval and a successfully stopped main container.

    This says nothing about that main process's GC settings or KDF workload.
    memory.peak covers the cgroup and its children, not just the Go heap. The
    kernel may temporarily exceed memory.max during reclaim, so this contract
    checks its configured hard limit and OOM events, not peak <= max.
    https://docs.kernel.org/admin-guide/cgroup-v2.html#memory-interface-files
    """
    before, after = _cgroup(before), _cgroup(after)
    _need(after["peakBytes"] >= before["peakBytes"], "interval_peak")
    _need(before["events"].keys() == after["events"].keys(), "interval_events")
    delta = {}
    for key, initial in before["events"].items():
        final = after["events"][key]
        _need(final >= initial, "interval_events")
        delta[key] = final - initial
    _need(delta["oom"] == 0 and delta["oom_kill"] == 0 and delta.get("oom_group_kill", 0) == 0, "interval_oom")
    return {"validated": True, "before": before, "after": after,
            "eventDelta": delta, "container": _container(inspect, oom=False)}


def _runtime_counters(value):
    value = _object(value, "runtime_counters")
    return {key: _integer(value.get(key), check="runtime_counters") for key in ("totalAllocBytes", "numGC", "pauseTotalNs")}


def validate_memory_profile(profile, inspect, expected_gogc=100):
    """Validate the fixed mixed-worker + KDF profile, returning safe evidence.

    The reported runtime values must come from the measured Go process. An
    environment variable alone cannot prove the effective runtime settings.
    Only the explicitly selected GOGC 50/100 comparison profiles are accepted.
    """
    _need(type(expected_gogc) is int and expected_gogc in (50, 100), "expected_gogc")
    profile = _object(profile, "profile_shape")
    _integer(profile.get("version"), 1, "profile_version")
    elapsed = _integer(profile.get("elapsedMillis"), check="profile_elapsed")
    _need(elapsed > 0, "profile_elapsed")
    runtime = _object(profile.get("runtime"), "runtime_settings")
    gogc = _integer(runtime.get("gogcPercent"), expected_gogc, "runtime_gogc")
    soft = _integer(runtime.get("goMemoryLimitBytes"), GO_MEMORY_LIMIT_BYTES, "runtime_memory_limit")
    interval = validate_container_interval(profile.get("before"), profile.get("after"), inspect)
    runtime_before = _runtime_counters(profile.get("runtimeBefore"))
    runtime_after = _runtime_counters(profile.get("runtimeAfter"))
    _need(all(runtime_after[key] >= value for key, value in runtime_before.items()), "runtime_counters")
    kdf = _object(profile.get("kdf"), "kdf")
    fixed_kdf = {"memoryKiB": 65536, "iterations": 3, "parallelism": 2, "concurrency": 2,
                 "peakAdmitted": 2, "hashCompleted": 2, "verifyCompleted": 2, "completed": 4}
    for key, value in fixed_kdf.items():
        _integer(kdf.get(key), value, "kdf")
    _need(kdf.get("workerActiveDuringKDF") is True, "kdf")
    return {**interval, "version": 1, "elapsedMillis": elapsed,
            "runtime": {"gogcPercent": gogc, "goMemoryLimitBytes": soft},
            "runtimeBefore": runtime_before, "runtimeAfter": runtime_after,
            "kdf": {**fixed_kdf, "workerActiveDuringKDF": True}}


def validate_oom_negative(inspect):
    """Accept only the dedicated 64 MiB OOM probe's independently verified exit.

    This is not a successful service profile. A killed probe cannot be required
    to emit a complete final memory report; Docker must prove both the bounded
    configuration and the OOM-killed terminal state instead.
    """
    return {"validated": True, "result": "expected_oom", "container": _container(inspect, oom=True)}
