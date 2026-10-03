"""Evidence contract tests; fixtures are not claimed as container measurements."""
import copy
import json
import unittest

import container_memory as m


def inspect_fixture(oom=False):
    memory, cpus, pids = (64 * 1024 * 1024, 1_000_000_000, 32) if oom else (768 * 1024 * 1024, 2_000_000_000, 128)
    return {"Config": {"User": "65532:65532", "Env": ["PRIVATE_DSN=secret/private"],
                       "Labels": {"private": "secret/private"}},
            "HostConfig": {"Memory": memory, "MemorySwap": memory, "NanoCpus": cpus,
                           "PidsLimit": pids, "ReadonlyRootfs": True, "Privileged": False,
                           "CapAdd": None, "CapDrop": ["ALL"], "SecurityOpt": ["no-new-privileges"]},
            "State": {"Status": "exited", "Running": False, "Restarting": False, "Dead": False,
                      "Error": "", "ExitCode": 137 if oom else 0, "OOMKilled": oom},
            "RestartCount": 0, "Mounts": [{"Source": "secret/private", "Destination": "/media"}]}


def profile_fixture():
    before = {"cgroupVersion": 2, "currentBytes": 32 * 1024 * 1024, "peakBytes": 48 * 1024 * 1024,
              "maxBytes": 768 * 1024 * 1024, "swapMaxBytes": 0,
              "events": {"low": 0, "high": 0, "max": 0, "oom": 0, "oom_kill": 0, "oom_group_kill": 0}}
    after = copy.deepcopy(before)
    after.update(currentBytes=80 * 1024 * 1024, peakBytes=320 * 1024 * 1024)
    return {"version": 1, "elapsedMillis": 1800,
            "runtime": {"gogcPercent": 100, "goMemoryLimitBytes": 512 * 1024 * 1024},
            "before": before, "after": after,
            "runtimeBefore": {"totalAllocBytes": 1000, "numGC": 1, "pauseTotalNs": 5000},
            "runtimeAfter": {"totalAllocBytes": 300 * 1024 * 1024, "numGC": 8, "pauseTotalNs": 85000},
            "kdf": {"memoryKiB": 65536, "iterations": 3, "parallelism": 2, "concurrency": 2,
                    "peakAdmitted": 2, "hashCompleted": 2, "verifyCompleted": 2,
                    "completed": 4, "workerActiveDuringKDF": True}}


def set_path(value, path, replacement):
    for key in path[:-1]:
        value = value[key]
    value[path[-1]] = replacement


class ContainerMemoryTests(unittest.TestCase):
    def rejected(self, call, *args, **kwargs):
        with self.assertRaises(m.Rejected) as raised:
            call(*args, **kwargs)
        self.assertEqual(str(raised.exception), "container_memory_evidence_invalid")
        self.assertIsNone(raised.exception.__cause__)

    def test_success_is_a_safe_owned_projection(self):
        profile, inspect = profile_fixture(), inspect_fixture()
        profile["private"] = "secret/private"
        before = copy.deepcopy((profile, inspect))
        result = m.validate_memory_profile(profile, inspect)
        self.assertTrue(result["validated"])
        self.assertEqual(result["runtime"], {"gogcPercent": 100, "goMemoryLimitBytes": 536870912})
        self.assertEqual(result["container"]["memoryBytes"], 805306368)
        self.assertEqual(result["eventDelta"]["oom"], 0)
        self.assertEqual((profile, inspect), before)
        self.assertNotIn("secret", json.dumps(result))
        self.assertNotIn("Env", json.dumps(result))
        result["before"]["events"]["oom"] = 99
        result["runtimeBefore"]["numGC"] = 99
        result["kdf"]["completed"] = 99
        self.assertEqual((profile, inspect), before)

    def test_explicit_comparison_profile_and_soft_limit_is_not_rss(self):
        profile = profile_fixture()
        profile["runtime"]["gogcPercent"] = 50
        # Children and cgroup accounting are not part of Go's soft heap limit.
        profile["after"].update(currentBytes=600 * 1024 * 1024, peakBytes=700 * 1024 * 1024)
        self.rejected(m.validate_memory_profile, profile, inspect_fixture())
        self.assertTrue(m.validate_memory_profile(profile, inspect_fixture(), expected_gogc=50)["validated"])
        for unsupported in (True, "50", 0, 75, 200, None):
            with self.subTest(expected_gogc=unsupported):
                self.rejected(m.validate_memory_profile, profile, inspect_fixture(), expected_gogc=unsupported)

    def test_interval_does_not_infer_main_process_runtime_or_kdf(self):
        profile = profile_fixture()
        result = m.validate_container_interval(profile["before"], profile["after"], inspect_fixture())
        self.assertTrue(result["validated"])
        self.assertNotIn("runtime", result)
        self.assertNotIn("kdf", result)
        self.assertNotIn("elapsedMillis", result)
        self.rejected(m.validate_memory_profile, {"before": profile["before"], "after": profile["after"]}, inspect_fixture())

    def test_optional_kernel_event_is_not_fabricated(self):
        profile = profile_fixture()
        for point in ("before", "after"):
            del profile[point]["events"]["oom_group_kill"]
        result = m.validate_memory_profile(profile, inspect_fixture())
        self.assertNotIn("oom_group_kill", result["before"]["events"])
        self.assertNotIn("oom_group_kill", result["eventDelta"])
        profile["after"]["events"]["oom_group_kill"] = 0
        self.rejected(m.validate_memory_profile, profile, inspect_fixture())

    def test_required_cgroup_evidence_cannot_be_omitted_or_unbounded(self):
        for point in ("before", "after"):
            for field in ("cgroupVersion", "currentBytes", "peakBytes", "maxBytes", "swapMaxBytes", "events"):
                with self.subTest(point=point, field=field):
                    profile = profile_fixture()
                    del profile[point][field]
                    self.rejected(m.validate_memory_profile, profile, inspect_fixture())
            for event in ("low", "high", "max", "oom", "oom_kill"):
                with self.subTest(point=point, event=event):
                    profile = profile_fixture()
                    del profile[point]["events"][event]
                    self.rejected(m.validate_memory_profile, profile, inspect_fixture())
        for field in ("maxBytes", "swapMaxBytes"):
            for unbounded in ("max", None, -1, 0 if field == "maxBytes" else 1):
                with self.subTest(field=field, value=unbounded):
                    profile = profile_fixture()
                    profile["after"][field] = unbounded
                    self.rejected(m.validate_memory_profile, profile, inspect_fixture())

    def test_numeric_measurements_are_integers_not_booleans_or_nonfinite_values(self):
        paths = [("version",), ("elapsedMillis",), ("runtime", "gogcPercent"),
                 ("runtime", "goMemoryLimitBytes"), ("before", "cgroupVersion"),
                 ("before", "currentBytes"), ("before", "peakBytes"), ("before", "maxBytes"),
                 ("before", "swapMaxBytes"), ("after", "events", "oom"),
                 ("runtimeAfter", "totalAllocBytes"), ("runtimeAfter", "numGC"),
                 ("runtimeAfter", "pauseTotalNs"), ("kdf", "memoryKiB"),
                 ("kdf", "completed"), ("kdf", "peakAdmitted")]
        for path in paths:
            for invalid in (True, False, -1, 1.0, float("nan"), float("inf"), "0", None, 1 << 64):
                with self.subTest(path=path, invalid=invalid):
                    profile = profile_fixture()
                    set_path(profile, path, invalid)
                    self.rejected(m.validate_memory_profile, profile, inspect_fixture())

    def test_oom_growth_resets_and_inconsistent_peaks_are_rejected(self):
        for event in ("oom", "oom_kill", "oom_group_kill"):
            profile = profile_fixture()
            profile["after"]["events"][event] = 1
            with self.subTest(event=event):
                self.rejected(m.validate_memory_profile, profile, inspect_fixture())
        for path, value in ((('before', 'events', 'high'), 1),
                            (('before', 'events', 'oom'), 1),
                            (('after', 'peakBytes'), 1),
                            (('before', 'peakBytes'), 400 * 1024 * 1024),
                            (('runtimeAfter', 'totalAllocBytes'), 999),
                            (('runtimeAfter', 'numGC'), 0),
                            (('runtimeAfter', 'pauseTotalNs'), 4999)):
            with self.subTest(path=path):
                profile = profile_fixture()
                set_path(profile, path, value)
                self.rejected(m.validate_memory_profile, profile, inspect_fixture())
        # Counters are compared over the measured interval, not silently reset.
        profile = profile_fixture()
        profile["before"]["events"]["oom"] = profile["after"]["events"]["oom"] = 3
        profile["after"]["events"].update(high=2, max=4)
        result = m.validate_memory_profile(profile, inspect_fixture())
        self.assertEqual(result["eventDelta"]["oom"], 0)
        self.assertEqual(result["eventDelta"]["max"], 4)

    def test_effective_runtime_and_real_concurrent_kdf_are_required(self):
        changes = [(('runtime', 'gogcPercent'), 0), (('runtime', 'goMemoryLimitBytes'), 805306368),
                   (('elapsedMillis',), 0), (('kdf', 'memoryKiB'), 8192),
                   (('kdf', 'iterations'), 1), (('kdf', 'parallelism'), 1),
                   (('kdf', 'concurrency'), 1), (('kdf', 'peakAdmitted'), 1),
                   (('kdf', 'hashCompleted'), 0), (('kdf', 'verifyCompleted'), 0),
                   (('kdf', 'completed'), 3), (('kdf', 'workerActiveDuringKDF'), 1),
                   (('kdf', 'workerActiveDuringKDF'), False)]
        for path, value in changes:
            with self.subTest(path=path, value=value):
                profile = profile_fixture()
                set_path(profile, path, value)
                self.rejected(m.validate_memory_profile, profile, inspect_fixture())
        for field in ("runtime", "runtimeBefore", "runtimeAfter", "kdf"):
            profile = profile_fixture()
            del profile[field]
            self.rejected(m.validate_memory_profile, profile, inspect_fixture())

    def test_docker_configuration_and_successful_terminal_state_are_both_required(self):
        changes = [(('Config', 'User'), "root"), (('HostConfig', 'ReadonlyRootfs'), False),
                   (('HostConfig', 'ReadonlyRootfs'), 1), (('HostConfig', 'Privileged'), True),
                   (('HostConfig', 'CapAdd'), ["SYS_ADMIN"]), (('HostConfig', 'CapDrop'), []),
                   (('HostConfig', 'SecurityOpt'), []),
                   (('HostConfig', 'SecurityOpt'), ["no-new-privileges", "seccomp=unconfined"]),
                   (('HostConfig', 'Memory'), 0), (('HostConfig', 'Memory'), 536870912),
                   (('HostConfig', 'MemorySwap'), -1), (('HostConfig', 'MemorySwap'), 1610612736),
                   (('HostConfig', 'NanoCpus'), 1_000_000_000), (('HostConfig', 'PidsLimit'), 0),
                   (('RestartCount',), 1), (('State', 'Status'), "running"),
                   (('State', 'Running'), True), (('State', 'Restarting'), True),
                   (('State', 'Dead'), True), (('State', 'Error'), "secret/private"),
                   (('State', 'ExitCode'), 137), (('State', 'ExitCode'), False),
                   (('State', 'OOMKilled'), True), (('State', 'OOMKilled'), 0)]
        for path, value in changes:
            with self.subTest(path=path, value=value):
                inspect = inspect_fixture()
                set_path(inspect, path, value)
                self.rejected(m.validate_memory_profile, profile_fixture(), inspect)
        for path in (("Config", "User"), ("HostConfig", "CapAdd"), ("HostConfig", "MemorySwap"),
                     ("State", "OOMKilled"), ("RestartCount",)):
            inspect = inspect_fixture()
            parent = inspect
            for key in path[:-1]:
                parent = parent[key]
            del parent[path[-1]]
            self.rejected(m.validate_memory_profile, profile_fixture(), inspect)

    def test_oom_negative_requires_its_own_limits_and_kernel_kill_evidence(self):
        inspect = inspect_fixture(oom=True)
        result = m.validate_oom_negative(inspect)
        self.assertEqual(result["result"], "expected_oom")
        self.assertEqual(result["container"]["memoryBytes"], 67108864)
        self.assertNotIn("secret", json.dumps(result))
        self.assertNotIn("runtime", result)
        self.assertNotIn("kdf", result)
        self.rejected(m.validate_oom_negative, inspect_fixture())
        self.rejected(m.validate_memory_profile, profile_fixture(), inspect)
        for path, value in ((('State', 'OOMKilled'), False), (('State', 'ExitCode'), 0),
                            (('State', 'ExitCode'), 1), (('HostConfig', 'Memory'), 805306368),
                            (('HostConfig', 'MemorySwap'), 0), (('HostConfig', 'NanoCpus'), 2_000_000_000),
                            (('HostConfig', 'PidsLimit'), 128)):
            with self.subTest(path=path, value=value):
                invalid = copy.deepcopy(inspect)
                set_path(invalid, path, value)
                self.rejected(m.validate_oom_negative, invalid)
        for key in ("Memory", "MemorySwap", "NanoCpus", "PidsLimit"):
            invalid = copy.deepcopy(inspect)
            del invalid["HostConfig"][key]
            self.rejected(m.validate_oom_negative, invalid)

    def test_malformed_shapes_fail_with_only_the_fixed_error(self):
        for malformed in (None, [], "secret/private", True, 123):
            with self.subTest(shape=type(malformed).__name__):
                self.rejected(m.validate_memory_profile, malformed, inspect_fixture())
                self.rejected(m.validate_memory_profile, profile_fixture(), malformed)
                self.rejected(m.validate_oom_negative, malformed)
                for key in ("runtime", "before", "after", "runtimeBefore", "runtimeAfter", "kdf"):
                    profile = profile_fixture()
                    profile[key] = malformed
                    self.rejected(m.validate_memory_profile, profile, inspect_fixture())
        profile = profile_fixture()
        profile["after"]["events"]["secret/private"] = 0
        self.rejected(m.validate_memory_profile, profile, inspect_fixture())


if __name__ == "__main__":
    unittest.main()
