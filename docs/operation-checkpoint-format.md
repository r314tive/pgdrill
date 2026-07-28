# Operation Checkpoint Format

`pgdrill` executes one immutable attempt and records every target mutation
independently from the terminal report. The internal checkpoint schema is:

```text
pgdrill.operation-checkpoint/v1
```

Current producers emit the stable identifier. The Go type remains under
`internal/`, and the JSON files are local executor recovery state rather than a
distributed wire API.

## Identity

An attempt identity contains the logical run ID, attempt ID, and immutable drill
spec digest. It deterministically derives:

- one opaque 128-bit ownership ID used by local and CNPG targets
- one SHA-256 operation key for each stage, kind, name, and ordinal

Operation names are bounded and secret-free. Restore-step keys depend on the
canonical attempt and stable step identity; they never hash resolved secret
values. Distinct attempts never share ownership or operation keys even when
they use the same drill spec.

Current mutation kinds are:

- `target_prepare`
- `restore_step`
- `postgres_start`
- `managed_target_start`
- `target_cleanup`

## State Machine

Before an ordinary mutation, the engine must durably save an `intent` record.
The callback is not invoked if the store cannot accept it. The terminal states
are:

- `succeeded`: completion was returned or independently proven
- `failed`: reconciliation proved the operation was not applied
- `unknown`: ownership or completion could not be proved safely

Only `intent -> terminal` and `unknown -> succeeded|failed` transitions are
accepted. Operation identity and `started_at` are immutable, timestamps cannot
move backwards, and a terminal state cannot regress. Reusing an operation key
does not authorize command replay: an existing checkpoint requires explicit
attempt reconciliation.

Cleanup is the one fail-safe exception. It still runs through a bounded detached
finalization context if its initial checkpoint write fails, because journal
availability must not prevent deletion of already owned resources. That attempt
cannot finish as passed, and the engine retries checkpoint persistence through
the detached context.

## Reconciliation

Targets return one bounded disposition after read-only observation:

- `completed`: target state proves the operation completed
- `not_applied`: target state proves the operation did not complete
- `unknown`: evidence is insufficient
- `conflict`: observed ownership belongs to another resource or attempt

The local target proves preparation with its exact private ownership marker
and proves restore steps with bounded, private, synced operation receipts under
`.pgdrill-operations`. A PostgreSQL-start receipt is accepted only when its
data and log paths remain inside the owned mode-`0700` work directory and a
matching `postmaster.pid` names the same data directory and a live process.
Missing or invalid receipts after a possibly started command remain `unknown`.

The CNPG target queries `Cluster` objects by the attempt ownership label. A
matching Ready instance proves managed startup; no match proves the create was
not applied; another name is a conflict. Reconciliation never calls `create`.
Cleanup observation uses the same selector.

`core.ReconcileAttempt` classifies orphaned `intent` and `unknown` records
without replaying mutation commands. `pgdrill attempt recover` exposes the
guarded local-target flow: it validates the immutable history/config identity,
produces a digest-bound plan, requires explicit stopped-executor confirmation,
reconciles source operations by observation, and runs a deterministic cleanup
operation with exact ownership and post-cleanup proof. The incomplete history
attempt remains immutable, and subsequent execution must use a new attempt ID.
See [attempt-recovery.md](attempt-recovery.md).

This local flow does not resume lifecycle event sequence numbers, acquire a
lease, fence a live executor, or make a drill policy decision. A future
controller must add lease and heartbeat semantics around the same engine
protocol rather than replaying commands.

## Local Persistence

CLI mutation checkpoints are stored below:

```text
<report.path>.checkpoints/<attempt-digest>/<operation-digest>.json
```

Directories are owner-only. Each file is bounded to 64 KiB, validated strictly,
written through a private temporary file, synced, atomically renamed, and
protected by an attempt-scoped advisory lock. Read-only `Load` and `List` do
not create missing attempt state. Store roots, attempt directories, lock files,
and checkpoint final paths reject symlinks or non-real objects before use.
Checkpoint messages are bounded protocol diagnostics and do not copy raw
command errors or command payloads.

The terminal report contains the final bounded operation records. Checkpoint
directories remain separate so an executor crash before report persistence
still leaves reconciliation state. Optional local history retains the lifecycle
record separately; incomplete attempts are protected by default retention.
