# Interrupted Attempt Recovery

`pgdrill attempt recover` reconciles and cleans up one interrupted local drill
without replaying its provider mutation or inventing a terminal report. It is a
manual single-host recovery protocol for attempts that have durable lifecycle
events and operation checkpoints but no terminal report.

Current schemas are:

```text
pgdrill.attempt-recovery-plan/v1
pgdrill.attempt-recovery-result/v1
```

The command currently supports `target.type: local`. CloudNativePG recovery
remains target-owned inside a running attempt; abandoned managed attempts need
a future lease-aware controller protocol.

## Preconditions

Before planning, retain the original:

- configuration file
- logical run ID and attempt ID
- explicit history store
- checkpoint store, normally `<report.path>.checkpoints`
- configured terminal report path
- local restore work directory and its ownership marker

Before applying, independently stop the original executor and every process in
its process group. `SIGKILL` cannot be trapped. Killing only the pgdrill parent
may leave a provider or PostgreSQL child alive, so a supervisor must terminate
the complete attempt process group or container before
`-confirm-executor-stopped` is truthful.

The flag is an operator assertion, not process-discovery proof. The current
single-host store has advisory locks but no lease or heartbeat capable of
fencing a live executor.

## Plan And Apply

Planning is read-only:

```sh
pgdrill attempt recover \
  -f /etc/pgdrill/main.yaml \
  -run-id nightly-main \
  -attempt-id nightly-main-1 \
  -history-store /var/lib/pgdrill/history
```

Use `-format json` for the complete machine-readable plan. Review the absolute
history, checkpoint, report, and target paths; attempt and spec identity;
checkpoint counts; cleanup operation; and plan digest.

Apply only the exact reviewed digest:

```sh
pgdrill attempt recover \
  -f /etc/pgdrill/main.yaml \
  -run-id nightly-main \
  -attempt-id nightly-main-1 \
  -history-store /var/lib/pgdrill/history \
  -confirm sha256:<plan-digest> \
  -confirm-executor-stopped
```

Use `-checkpoint-dir` only when the interrupted run used a checkpoint location
other than the one derived from `report.path`. `-history-store` is always
explicit; the command does not use history environment/default-path fallback
for destructive recovery.

The CLI refuses recovery when:

- the addressed history attempt is missing
- the current config digest differs from the recorded immutable spec digest
- the attempt has no lifecycle events or already has a terminal report
- the configured report path contains a valid terminal report for the same
  run, attempt, and spec, or contains an unreadable/invalid report
- the target is not local
- durable operation checkpoints are absent, malformed, foreign, or conflicting
- history, checkpoint, or target paths are not absolute canonical paths
- the apply digest is missing, malformed, or stale
- stopped-executor confirmation is absent

## Digest Scope

The plan digest binds:

- schema and digest domain
- immutable run, attempt, and spec identity
- absolute history and checkpoint stores
- absolute configured report path
- local target and recovery target
- cleanup policy
- every source operation identity
- the deterministic recovery-cleanup operation

Checkpoint state, timestamps, diagnostics, and the recovery-cleanup checkpoint
itself are deliberately excluded. Reconciliation may move `intent` to
`unknown`, or cleanup may persist its own intent, without changing the
reviewed scope. Adding, removing, or changing a source operation changes the
digest and rejects stale confirmation.

## Recovery Semantics

Apply performs these steps:

1. Rebuild and verify the exact plan under the current checkpoint snapshot.
2. Bind a fresh in-process target object to the original immutable attempt.
3. Reconcile unfinished source operations by observation only, then verify
   that the exact planned operation set is still present. Provider commands
   are never replayed.
4. Persist a deterministic cleanup intent.
5. Reconcile the cleanup state and require the exact attempt ownership marker.
6. Destroy only the proven-owned target when cleanup is still required.
7. Reconcile again and persist successful cleanup only after absence is proven.

Unknown provider mutation stays `unknown`. That is expected after a process is
killed between durable intent and a provider receipt. Recovery can therefore
return:

```text
source_reconciliation_complete: false
target_ready_for_retry: true
```

This means provider completion was not reconstructed, but exact owned cleanup
was proven and a new attempt can start safely. It does not retroactively turn
the interrupted attempt into a pass or a fail.

Ownership conflicts, missing or forged markers, symlink targets, invalid target
protocol output, checkpoint write failures, and unproven post-cleanup state
fail closed. Technical reconciliation errors are returned as command failures
even when the result can report that cleanup itself succeeded.

## History And Retry

Recovery never rewrites events, publishes a terminal report, or deletes the
incomplete history attempt. The interrupted attempt remains immutable evidence
of process loss and is protected from default history retention.

After `target_ready_for_retry: true`, run the same immutable spec with a new
attempt ID:

```sh
pgdrill run \
  -f /etc/pgdrill/main.yaml \
  -run-id nightly-main \
  -attempt-id nightly-main-2 \
  -history-dir /var/lib/pgdrill/history
```

Never reuse the interrupted attempt ID. Existing operation keys are a
reconciliation boundary, not permission to replay mutations.

When `target.remove_work_dir` is false, recovery may prove a retained stopped
target but reports `target_ready_for_retry: false`; the configured path is not
empty and must not be reused.

## Verification Boundary

The disposable WAL-G integration gate starts a real drill in its own Unix
process group, blocks at `backup-fetch` after durable restore intent, sends
`SIGKILL` to the group, and requires:

- incomplete history with no terminal report
- retained owned target and unresolved restore operation
- digest-confirmed recovery and proven owned cleanup
- immutable preservation of the interrupted attempt
- a fully passed clean retry under a new attempt ID

This proves the local protocol against one controlled provider boundary. It is
not a distributed fencing mechanism or a compatibility claim for every
provider, repository, platform, or process supervisor.
