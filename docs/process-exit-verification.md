# Process exit observation regression

## Evidence and change

PR #13 run [36808879373](https://github.com/MoYuanCN/Jelee/actions/runs/36808879373) failed `TestDescendantsDieOnCancelAndNormalParentExit/tree-exit` after 1.04 seconds, reporting a descendant still running. The test's per-PID deadline is three seconds. Its loop and final assertion independently called `processAlive`: a false observation could end the loop, followed by a true observation failing the assertion immediately. The log does not establish which OS transition or read condition produced those observations.

The test now retains the observation that ended its wait. It still checks every recorded descendant, uses the existing three-second deadline, and fails when the last observation at the deadline is alive. On Linux, only a missing proc entry is accepted after a read error; other read errors remain unresolved and cannot silently pass cleanup. Zombie handling is unchanged. The production process runner and its process group / Windows Job Object cleanup are unchanged.

The same source's push run [36808875521](https://github.com/MoYuanCN/Jelee/actions/runs/36808875521) passed Windows/Linux foundation, PostgreSQL integration/race and real probe/NFO acceptance. This does not invalidate the failed PR run. Both runs still failed the full branding gate.

## Validation

The targeted Linux race regression passed ten repetitions (both cancellation and normal parent exit), 12.446s. The full Linux process package passed with `-race -count=1`, 82.474s. The Windows process package passed with `-count=1`, 1.792s; Windows race was not run. Logs are retained under `.testdata/process-exit-*`; source and log hashes are recorded in [the evidence](evidence/process-exit.json).

This is a test observation correction, not evidence of a production cleanup defect or proof that every possible source of test flakiness has been eliminated.
