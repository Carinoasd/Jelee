# Durable inventory workers

The phase 3A worker records a bounded, read-only directory inventory. PostgreSQL
owns scheduling, authorization, cancellation, checkpoints, retention and lease
fencing. The scanner owns filesystem handles. The worker does not probe codecs,
parse NFO files, alter media, derive catalog records or run external programs.

## Application boundary

`app.NewJobs(repository, policy)` provides `Submit`, `Retry`, `Cancel`, `Get`,
`List`, `Entries` and `Libraries`. It validates canonical UUIDs, actor IDs,
printable ASCII idempotency keys of 1–128 bytes, supported priorities/states and
page sizes of 1–100. The repository rechecks the live session and administrator
role for each public operation. A previously accepted job continues after the
submitting HTTP connection closes; cancellation requires the persisted job flag.

Policy limits accepted by the application:

| Setting | Range |
| --- | --- |
| Queued jobs | 1–1,000 |
| Globally retained terminal jobs | 1–100 |
| Entries per job | 100–500,000 |
| Directories per job | 1–100,000 |
| Attempts | 1–10 |
| Missing entry count threshold | 1–500,000 |
| Missing percentage threshold | 1–100 |

Missing counts are observations for review. Only a successfully completed scan
can be compared with its baseline. A failed, cancelled or incomplete scan must
not mark media as missing, and no inventory result deletes media.

## Construction and service lifetime

```go
opts := jobs.DefaultOptions()
runner, err := jobs.New(executionRepository, inventoryScanner, opts, logger)
// Handle err before registering lifecycle hooks.
```

`New` requires non-nil repository, scanner and logger. `Options` has these bounds:

| Option | Default | Range |
| --- | --- | --- |
| `Workers` | 2 | 1–8 |
| `PollInterval` | 250 ms | 100 ms–1 minute |
| `LeaseDuration` | 30 seconds | 10 seconds–5 minutes |
| `DBOperationTimeout` | 2 seconds | Positive and less than lease / 3 |
| `MaxJobRuntime` | 1 hour | 1 minute–24 hours per attempt |

`Owner` is an optional canonical UUID. When omitted, `New` creates a private
cryptographically random UUID for this runner. An injected value must be unique
among running processes; it is never accepted from an HTTP client. The optional
`Clock` controls timers for deterministic tests. Database time always determines
lease expiry, so a test clock cannot extend or bypass a database lease.

Call `Start(serviceContext)` once. Its context must remain live for the service
lifetime; fx's short-lived `OnStart` context and an HTTP request context are not
suitable. A lifecycle hook can call `Start(context.Background())` and use
`Stop(shutdownContext)` to own cancellation.

Open the store before starting workers. During shutdown, stop accepting requests,
call `Stop`, wait for all workers and their heartbeats, then close the store.
`Stop` is safe to repeat. If its context expires, it returns that context error;
joining is incomplete and the caller must not report a clean worker shutdown.
Once the scanner returns, a later `Stop` can finish joining. Each database
operation, including cleanup after cancellation, has its own short timeout.

## Scheduling, checkpoints and cancellation

Each worker claims one job at a time. Its first three successful claims prefer
manual work; its fourth prefers background work. The repository falls back to
the other priority when necessary. No available job (`ErrNotFound`) waits on a
poll timer. Claim failures also wait, so an unavailable database does not cause
a busy loop.

For each active job, one helper goroutine owns its heartbeat and maximum runtime
timers. Heartbeats run every lease / 3. There are no per-directory goroutines.
The execution loop obtains the next unfinished directory and invokes the scanner
with a synchronous save callback. A batch contains at most 128 entries and
subdirectories combined. The scanner must emit a final `Done` batch and stop on
a callback error. Missing completion, repeated completion, oversized batches and
negative skipped counts fail safely. The callback persists short transactions;
directory traversal never holds one long database transaction.

Every persistence operation uses the original job ID, owner and generation.
The repository checks its current lease expiry using database time, including
before the final write commits. Renewing a lease does not change its generation.
An expired or replaced owner cannot checkpoint, complete or release the job.

| Condition | Worker action |
| --- | --- |
| Persistent cancellation from heartbeat or directory persistence | Cancel scanning; join heartbeat; finish as `cancelled` with a fresh bounded context. |
| Cancellation arrives during completion | On completion conflict, confirm the flag and valid lease once, then finish as `cancelled`. |
| Heartbeat fails or lease is lost | Cancel scanning, join heartbeat and leave recovery to the repository; never claim successful completion or retry persistence with an old lease. |
| Service stops | Cancel and join scanning/heartbeat, then release the valid lease to the queue while retaining checkpoints. A persisted cancellation is completed as cancelled by the repository. |
| Maximum runtime expires | Finish as `failed` with `job_timeout`; do not compare missing entries. |
| Scanner returns an error | Finish with a safe code; do not compare missing entries. |

The repository resumes completed directory checkpoints. An interrupted directory
is restarted with its partial rows cleared, so a file that disappeared before
recovery is not retained merely because an earlier partial batch saw it.
Recovery is bounded by the attempt policy. Persistence errors during completion
or release are logged safely and left to lease recovery.

Cancellation is checked at filesystem and callback boundaries. The scanner closes
its active directory handle when cancelled. A network filesystem's blocked open
or metadata system call may not be immediately interruptible by Go; the worker
waits for the scanner rather than leaking it behind a reported successful stop.

## Error and log contract

Worker failure codes are `scan_unavailable`, `scan_io`, `scan_limit` and
`job_timeout`. The repository can also report exhaustion of recovery attempts.
Unknown scanner errors become `scan_io`; unknown persistence errors become
`scan_unavailable`. Successful and cancelled jobs have no failure code.

Logs contain static messages, task IDs, state and safe codes. They do not include
absolute roots, relative media paths, raw scanner/SQL errors, credentials or
panic payloads. A failed terminal write is never logged as completed.

## Focused verification

Run with the repository's pinned local Go toolchain:

```powershell
./scripts/run-go.ps1 test -count=1 ./internal/app ./internal/platform/jobs ./internal/architecture
```

```sh
./.bin/go test -race -count=1 ./internal/app ./internal/platform/jobs ./internal/architecture
```

Worker tests cover fixed concurrency, checkpoint release, idle timers, priority
preference, heartbeat renewal, persistent cancellation, lost leases, heartbeat
timeouts, job timeouts, batch/callback failures, cancellation during completion,
private error redaction and shutdown that cannot yet join a blocked scanner.
Controllable timers exercise long deadlines without long sleeps. Each worker test
joins its runner and checks that its timers were released. PostgreSQL fencing,
rollback and recovery integration tests belong to the repository adapter; real
filesystem handle and path checks belong to the scanner adapter.
