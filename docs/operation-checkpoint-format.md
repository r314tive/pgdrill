# Operation Checkpoint Format

`pgdrill` executes one immutable attempt and records every target mutation
independently from the terminal report. The internal checkpoint schema is:

```text
pgdrill.operation-checkpoint/v1alpha1
```

It remains under `internal/` while the engine protocol evolves. The JSON files
are durable local recovery state, not a stable remote API yet.

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

The local target proves preparation with its exact ownership marker and proves
restore steps with private, synced operation receipts under
`.pgdrill-operations`. A PostgreSQL-start receipt is accepted only with a
matching owned `postmaster.pid` and live process. Missing receipts after a
possibly started command remain `unknown`.

The CNPG target queries `Cluster` objects by the attempt ownership label. A
matching Ready instance proves managed startup; no match proves the create was
not applied; another name is a conflict. Reconciliation never calls `create`.
Cleanup observation uses the same selector.

`core.ReconcileAttempt` classifies orphaned `intent` and `unknown` records
without replaying mutation commands. It does not resume lifecycle event
sequence numbers, acquire a lease, or make a policy decision. A future
controller must use the result to choose bounded cleanup or a new attempt.

## Local Persistence

CLI mutation checkpoints are stored below:

```text
<report.path>.checkpoints/<attempt-digest>/<operation-digest>.json
```

Directories are owner-only. Each file is bounded to 64 KiB, validated strictly,
written through a private temporary file, synced, atomically renamed, and
protected by an attempt-scoped advisory lock. Final-path symlinks are rejected
on reads. Checkpoint messages are bounded protocol diagnostics and do not copy
raw command errors or command payloads.

The terminal report contains the final bounded operation records. Checkpoint
directories remain separate so an executor crash before report persistence
still leaves reconciliation state. Retention and indexing belong to the future
local history/control-plane layer.
