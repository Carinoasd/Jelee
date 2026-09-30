# Linux media helper isolation

## Scope and current status

`internal/platform/sandbox` implements a dedicated Linux amd64 helper that applies kernel restrictions before executing an approved ffprobe. `process.NewIsolatedFFprobe` supplies the dedicated bounded runner registration. These packages add no HTTP endpoint, database access, import, cache, or transcoding. Windows and other architectures return `media_sandbox_unsupported`; callers must leave media probing disabled there.

Successful `New` means the supplied identity policy and ELF dependency closure were verified. It does not enable a capability. Production must use the embedded executable/runtime manifests, `RequireProtectedFiles=true`, early helper dispatch, and a read-only runtime image. The application registrar owns the final capability check. A development policy with protection disabled is not eligible for production registration.

## Registration and helper contract

```go
type PinnedFile struct { Path, SHA256 string }
type Policy struct {
    FFprobeSHA256 string
    Libraries []PinnedFile
    RequireProtectedFiles bool
}
type Profile struct { FFprobePath string }

New(context.Context, Profile, Policy) (*Launcher, error)
(*Launcher).Executable() string
(*Launcher).HelperArguments() []string
(*Launcher).ProtectedFilesRequired() bool
RunHelper(argv []string, policy Policy) int

process.NewIsolatedFFprobe(process.Config, *Launcher) (*process.IsolatedRunner, error)
```

`Launcher.Executable()` is the current Jelee executable. `HelperArguments()` returns a fresh argument slice containing `--internal-probe-helper` and one descriptor. The isolated factory accepts this sealed launcher and registers exactly `Tool: "ffprobe"`, `Operation: "metadata"`; it accepts no caller-supplied program or arguments. `Run` requires an already opened read-only regular file as stdin and uses bounded output pipes. Ordinary `process.New` retains its original ffprobe-name and argument bounds.

The main program must recognize the internal helper command **before** starting the application, opening databases, or creating application workers. It must call `os.Exit(sandbox.RunHelper(args, independentPolicy))` immediately. `RunHelper` is never suitable for an application goroutine: restrictions are irreversible and a successful invocation replaces the process image.

The descriptor is canonical JSON, encoded with strict raw URL base64, at most 32 KiB decoded. Only version `1`, mode `metadata`, and an absolute canonical ffprobe pathname are accepted. Unknown fields, duplicates, alternate encodings, additional arguments, digests, and library lists are rejected. The helper reconstructs the fixed metadata arguments internally. Its trust policy never comes from the descriptor, media, environment, or an HTTP request.

`tools.FFprobeSpec("linux-amd64").SHA256` supplies the independently embedded executable identity. `tools.RuntimeSpec("linux-amd64").Libraries` supplies the fixed paths and digests of the eight packaged runtime ELFs. An empty library list supports only an ELF without an interpreter or `DT_NEEDED` dependencies. Hashing arbitrary host libraries and immediately trusting the result is a development experiment, not production identity verification.

Errors are fixed sentinels: `media_sandbox_invalid`, `media_sandbox_unavailable`, and `media_sandbox_unsupported`. Helper exit codes are 64 for invalid input and 78 for unavailable isolation. Diagnostic lines never contain source paths, library paths, raw OS errors, media metadata, or descriptor contents. The runner must continue to discard/redact raw child stderr and discard stdout on failure.

The isolated runner maps 64 to `ErrSandboxInvalid`, 78 to `ErrSandboxUnavailable`, and only ffprobe's normal rejection code 1 to `ErrExit`. Other nonzero codes, including signals, 2, and 127, map to `ErrUnexpectedExit`. They indicate a tool failure, not rejected media. Cancellation, deadlines, output limits, and cleanup errors keep their existing classifications. All error results contain no raw output; the ordinary runner's exit classification is unchanged.

## Identity and dependency checks

Both registration and the child verify the tool again. Each tool/library file must be a canonical, non-symlink, regular Linux amd64 ELF, no larger than 512 MiB. Reads use a 32 KiB hashing buffer; size and modification time are checked again. The executable needs an execute permission bit. The policy allows at most 32 distinct library files.

The parser follows the tool's `PT_INTERP` and transitive `DT_NEEDED`/`DT_SONAME` relationships. Every required name must resolve to an approved file; duplicate sonames and extra unrelated files are rejected. The exact opened file objects supply Landlock rules. The verified executable is copied to FD 255 and executed through `execveat(AT_EMPTY_PATH)`, so execution does not reopen its pathname.

Dependency rules do not grant a directory recursively. The child receives `LD_LIBRARY_PATH` containing only the parents of the approved files; Landlock still permits reads only from the individually approved file objects. Unlisted loader caches, optional `dlopen` libraries, configuration, and locale files remain denied. A format needing an additional runtime dependency fails until the shipped policy explicitly and correctly accounts for it.

With `RequireProtectedFiles=true`, each pinned file and every ancestor from `/` must be root-owned without group/other write bits. The caller must have nonzero real/effective UID, and `faccessat2(AT_EMPTY_PATH | AT_EACCESS)` must affirmatively deny writing to the exact opened inode. Ancestors are opened individually without following symlinks; the leaf's device/inode must match the file already hashed. Unsupported permission checks fail closed. The same checks protect the current Jelee executable at registration, and its reopened inode must equal `/proc/self/exe` so another protected program cannot substitute for the running helper.

An opened inode prevents a pathname replacement from selecting a different executable. It does not freeze bytes against a privileged external writer modifying that inode in place. Production must mount the trusted runtime filesystem read-only and avoid in-place deployment updates during operation. Permission checks protect against changes by the unprivileged service identity; they do not constrain a hostile privileged host. Development files owned by the same user are refused when protection is required.

## Applied kernel policy

The child locks its OS thread, refuses real/effective UID 0, clears the environment and capabilities, and requires `PR_SET_NO_NEW_PRIVS`. Stdin must be a read-only, seekable regular file. Stdout and stderr must be write-only pipes without `O_ASYNC`; regular output files and sockets are rejected.

Landlock ABI 3 or newer is mandatory. The ruleset handles all known filesystem rights through ABI 3, including truncation and cross-directory references, and device ioctl rights when ABI 5 is available. It permits only:

- reading/executing the approved ffprobe inode;
- reading approved dependency inodes;
- executing the approved ELF interpreter.

No directory enumeration, creation, removal, rename, or content write permission is granted. The already opened stdin remains readable: Landlock intentionally does not remove permissions from preopened descriptors. `close_range(3, UINT_MAX, UNSHARE | CLOEXEC)` gives the locked thread a private FD table and marks all non-stdio descriptors to close at exec. This includes accidental non-CLOEXEC descriptors inherited from the service.

The architecture-checked seccomp filter rejects x32 and non-amd64 syscall entry. A syscall allowlist denies all socket families and socket operations, io_uring, ptrace/process-memory access, pidfd access, namespaces, process creation, session/process-group escape, and privilege changes. It permits only thread-form `clone`; `clone3` returns `ENOSYS` so libc can fall back to the inspectable `clone` arguments. `kill`/`tgkill` can target only the helper's own thread group.

`fcntl` is limited to `F_DUPFD`, `F_DUPFD_CLOEXEC`, `F_GETFD`, `F_GETFL`, and `F_SETFD(FD_CLOEXEC)`. It denies `F_SETFL`, owner/signal selection, leases, and notification. This matters because asynchronous pipe notifications can otherwise signal another same-UID process without calling `kill`.

Only the initial `execveat` through FD 255 is allowed. That FD closes on exec; the hard FD limit is 128, so the target cannot recreate FD 255 and execute another image. Arguments are fixed, and the environment is reduced to `LANG=C`, `LC_ALL=C`, `TZ=UTC`, plus the approved dependency search paths when needed.

### Resource limits

| Kernel limit | Soft / hard |
| --- | --- |
| Address space | 2 GiB / 2 GiB |
| CPU time | 30 seconds / 31 seconds |
| FD number ceiling | 128 / 128 |
| Thread/process counter | 128 / 128 |
| Regular-file output size | 0 / 0 |
| Core files | 0 / 0 |

`RLIMIT_NPROC` checks the calling process's real-UID thread count on Linux. The child has a nonzero real UID and no privilege exemption, and seccomp prevents raising limits. Two probes under the same UID share this counter. This is a limit on creation by the restricted probe; setting it does not retroactively cap or reconfigure other processes under that UID. A busy developer UID may have no remaining thread budget. Deploy under a dedicated service UID, and also use a container/cgroup PID limit for the service as a whole.

The process runner remains responsible for concurrency, wall-clock deadlines, bounded stdout/stderr, process-group termination, and reaping. The helper's CPU limit does not replace a wall-clock timeout. Tool verification checks context between reads; a kernel filesystem operation stuck on a network filesystem is not guaranteed to return at a Go deadline. Production runtime dependencies should be local immutable files.

## Boundary and remaining assumptions

This policy protects file contents and changes. It does not hide all filesystem or system metadata: allowed `stat`, `readlink`, `uname`, and `sysinfo` calls can reveal metadata. The target can read its single authorized source and the approved executable/dependencies, and it can emit untrusted output through its pipes. A bounded parser must validate output before storing or exposing it.

The policy assumes the trusted helper/runtime has no untrusted goroutines before exec. Seccomp and Landlock are applied to its locked thread and inherited by the executed program; successful exec removes the old helper threads. A helper error must terminate the child immediately. Failure of any required setup step leaves the capability unavailable, with no fallback to an unrestricted media invocation.

The source FD is read-only and the child cannot reopen neighboring files for content access. An unrelated authorized writer could still change source bytes during the read; the helper does not create a snapshot. Tests compare source hashes before/after to establish that the tested operations themselves do not mutate media. Kernel vulnerabilities and a hostile privileged host administrator are outside this process boundary.

## Verification

After bootstrapping and verifying the manifest tools/runtime and running `make fixtures` on Linux amd64, run:

```sh
python3 -B scripts/test_sandbox_native.py
```

The script selects the most recently generated Linux fixture record, verifies every recorded size/SHA256 and the pinned generator identity, and retains a transcript at `.testdata/sandbox-native.txt`. Use `--fixtures .testfixtures/media-linux-amd64-<id>` to select an existing generated set explicitly. It does not change the original fixtures. Each invocation owns a private temporary build directory and UUID-named test image/container, then removes those resources. Shared caches and earlier test images are retained. Missing requirements, any skipped selected test, or merged sandbox coverage below 85% fail this required verification.

The native Linux container suite uses only pinned Go 1.27.1 with `CGO_ENABLED=0`. The test binaries, helpers, and SDK coverage tool are copied root-owned with mode 0555 into a local test image based on the manifest-pinned Go image. No host directories are mounted. Writable fixtures live in `/project/.testdata` on native tmpfs with `nosuid,nodev,noexec`. The container runs as UID/GID 65532 with a read-only root, no capabilities, no-new-privileges, no network, 256 PIDs, 768 MiB memory, and two CPUs. Its outer seccomp policy is disabled so the tested restrictions come from the helper itself. The helper must successfully install its own Landlock and seccomp policy.

Selected native tests complete without skips and verify 25 actual file/network/FD/environment/syscall boundaries, the asynchronous-signal regression against a disposable sibling, FIFO/nonregular rejection, wrong digest/architecture, oversized sparse executable rejection, read-write stdin rejection, regular stdout rejection, tool replacement after registration, descriptor framing, and syscall argument/architecture edge cases.

A separate pure Go/amd64 assembly fixture creates bounded-stack raw threads until the kernel returns `EAGAIN`. Child threads execute only assembly futex/exit operations, never enter the Go runtime, inherit blocked signals, and are joined through kernel `CHILD_CLEARTID`. The accepted run reached 116 live fixture threads before the real-UID limit rejected another clone; all were reclaimed. The available count varies with other threads under that UID. No C compiler or global installation is used for this proof. A prior exploratory C fixture was removed and is not accepted as compliant evidence.

The real pinned Linux ffprobe `n9.0.2-17-g2a571b6068-20260930` was tested with all eight runtime ELFs from the embedded manifest, protected root-owned files, and `RequireProtectedFiles=true`. MKV and MP4 returned metadata from `fd:`. HLS and local/nested concat references were rejected; a loopback HTTP control received no requests, and fixture hashes were unchanged. Tests also reject a missing interpreter, missing transitive dependency, duplicate SONAME, service-owned file, writable ancestor, changed leaf inode, and a different executable substituted for the running helper. This proves the tested packaged tool/profile combination; it does not establish universal container-format support.

The full process-runner suite passes with 89.6% statement coverage. It includes the sealed registration, invalid requests, read-only stdin, cancellation, timeout, bounded output, quota release, descendant termination, and exit classification for 1, 2, 64, 78, 127, 255, and signals. Error results never expose raw child output. Windows focused tests and Windows/Linux `go vet` also pass; Windows media isolation remains unsupported.

### Coverage of the dedicated child

The ordinary parent test reports 64.2% sandbox statement coverage. Security setup runs in a separately compiled helper, and successful exec replaces the Go process before it can flush counters. A test-only wrapper therefore runs two deliberately invalid ELF fixtures: a minimal ELF with no loadable segment, and a copy of the pinned ffprobe with only its `PT_LOAD` entries disabled. The latter retains the real interpreter and dependency metadata. Each reaches the actual kernel exec failure after the normal Landlock, limits, FD, and seccomp setup; there is no production hook or security bypass.

After this genuine failure, the wrapper writes original `runtime/coverage.WriteMeta` and `WriteCounters` bytes through its inherited stdout pipe. The parent saves those bytes without changing counters. Both builds use `-covermode=atomic`; the helper's main package is included so the Go runtime initializes coverage. The child snapshot covers 62.1% of sandbox statements. Standard SDK `covdata merge` combines these snapshots with the parent counters, giving **86.5% sandbox coverage**. The helper fixture itself is a separate test package and is not counted as production sandbox code. Successful exec is established by the independent behavior tests, not inferred from the late-failure snapshots.

The coverage commands are:

```sh
CGO_ENABLED=0 .bin/go build -cover -covermode=atomic \
  -coverpkg=github.com/MoYuanCN/Jelee/internal/platform/sandbox,github.com/MoYuanCN/Jelee/internal/platform/sandbox/testdata/helper \
  -o .testdata/sandbox-coverage-helper ./internal/platform/sandbox/testdata/helper
CGO_ENABLED=0 .bin/go test -cover -covermode=atomic -c \
  -o .testdata/sandbox-native.test ./internal/platform/sandbox
CGO_ENABLED=0 .bin/go build -o .testdata/sandbox-covdata cmd/covdata
# Inside the protected test image, with the fixture environment configured:
/project/sandbox.test -test.v -test.timeout=3m \
  '-test.run=Test(Native|Real|Pinned|Protected|New|Descriptor|Launcher|Policy|Syscall)' \
  -test.gocoverdir=/project/.testdata/parent
/project/covdata percent -i=/project/.testdata/parent
/project/covdata percent -i=/project/.testdata/child
/project/covdata merge -i=/project/.testdata/parent,/project/.testdata/child \
  -o=/project/.testdata/merged
/project/covdata percent -i=/project/.testdata/merged
```

The SDK coverage tool is compiled before entering the container because its tmpfs is `noexec`. Raw and merged results, the image digest, binary hashes, and source hashes are retained in the native test evidence. Shared-UID WSL runs may explicitly skip synthetic thread tests when the available UID budget is insufficient; the required isolated run sets `JELEE_REQUIRE_SANDBOX_TEST=true` so missing kernel support or thread budget is a failure. The accepted native run has no skipped selected tests, including FIFO checks on native tmpfs. This run uses `CGO_ENABLED=0` and does not claim race-detector coverage.

## Primary references

- [Linux Landlock userspace API](https://docs.kernel.org/userspace-api/landlock.html): rights, inheritance, preopened FD behavior, and ABI differences.
- [Linux seccomp filter API](https://docs.kernel.org/userspace-api/seccomp_filter.html): architecture checks, no-new-privileges, inheritance, and syscall filtering limits.
- [Linux `execveat(2)`](https://man7.org/linux/man-pages/man2/execveat.2.html): execution through an FD with `AT_EMPTY_PATH`.
- [Linux `close_range(2)`](https://man7.org/linux/man-pages/man2/close_range.2.html): `UNSHARE` and `CLOEXEC` behavior.
- [Linux `getrlimit(2)`](https://man7.org/linux/man-pages/man2/getrlimit.2.html): FD, address-space, CPU, and real-UID thread limits and privilege exemptions.
- [Linux `access(2)` / `faccessat2`](https://man7.org/linux/man-pages/man2/access.2.html): effective-identity permission checks on an already opened inode using `AT_EMPTY_PATH`.
- [Go integration-test coverage](https://go.dev/doc/build-cover): instrumented binaries, atomic coverage data, and merging separate process counters.
- [FFmpeg fd protocol](https://ffmpeg.org/ffmpeg-protocols.html#fd) and [selected-release source](https://github.com/FFmpeg/FFmpeg/blob/n9.0.2/libavformat/file.c): inherited FD input and seek behavior.
