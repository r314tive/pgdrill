# Local History Format

The optional local history is a daemon-free, append-only filesystem store for
logical runs and execution attempts. It consumes the same canonical drill
specs, lifecycle events, reports, and artifact references as direct execution;
it does not introduce another restore state machine.

The on-disk contract is currently `pgdrill.history-store/v1alpha1` with layout
version `1`. It is a pre-GA format. Unknown schema or layout versions fail
explicitly and are never rewritten automatically.

The documented pre-GA read-compatibility floor is `v0.3.0-alpha.1`. A frozen
store from that exact candidate is exercised by every current history-reader
test run. This does not make the alpha schema a GA contract.

## Enabling History

Direct execution remains independent of history. Opt in per command:

```sh
pgdrill run -f pgdrill.yaml \
  -run-id nightly-main \
  -attempt-id nightly-main-1 \
  -history-dir /var/lib/pgdrill/history

pgdrill target verify -f cnpg.yaml -discover -confirm-create \
  -history-dir /var/lib/pgdrill/history
```

Inspect or import records:

```sh
pgdrill history list -store /var/lib/pgdrill/history
pgdrill history show -store /var/lib/pgdrill/history nightly-main
pgdrill history show -store /var/lib/pgdrill/history \
  -attempt-id nightly-main-1 nightly-main
pgdrill history import -store /var/lib/pgdrill/history report.json
pgdrill history verify -store /var/lib/pgdrill/history
```

For inspection commands, the store path resolves in this order:
`-store`, `PGDRILL_HISTORY_DIR`, `XDG_STATE_HOME/pgdrill/history`, then
`~/.local/state/pgdrill/history`. A missing store lists as empty and is not
created by `history list`. `history show`, `verify`, and `prune` fail when the
addressed store does not exist.

## Identity And Immutability

One logical run is permanently bound to one `spec_digest`. It can contain many
attempt IDs, but an attempt is also permanently bound to the same run and spec.

- exact repeated writes are idempotent
- a conflicting run, attempt, sequence, spec, or report write fails
- event sequence starts at 1 with `run_started` and must be contiguous
- event timestamps cannot move backwards
- no event can follow `run_finished`
- a terminal event and terminal report must have the same status

This makes an accidental run-ID reuse visible instead of silently changing the
meaning of retained history.

## Layout

IDs are never used as path components. Run and attempt directories use SHA-256
names, while strict identity documents retain the original values:

```text
history/
  .lock
  store.json
  runs/
    <sha256(run-id)>/
      identity.json
      spec.json
      attempts/
        <sha256(run-id + attempt-id)>/
          identity.json
          report.json
          summary.json
          events/
            00000000000000000001.json
            00000000000000000002.json
  retention/
    operations/
      <confirmed-plan-digest>/
        plan.json
        progress/
    trash/
    pending-delete/
```

The store uses a process-shared advisory lock. New directories are mode `0700`
and files are mode `0600`; reads reject symbolic links, non-regular files, and
non-private store content. Files are fsynced and published atomically. Events,
specs, identities, and reports are immutable after publication.

`summary.json` is an immutable bounded index derived from the accepted events
and terminal report. It keeps `history list` independent from the potentially
larger command evidence in every report. Publishing a terminal report freezes
that attempt's event stream; exact retries remain idempotent, while new
sequences are rejected. `history show` recomputes the summary from the full
events and report and rejects disagreement.

## Crash Boundaries

The event journal and terminal report are intentionally independent:

- a process can leave events without a report
- an imported legacy report can exist without events
- a completed direct run writes its normal report first, then records the
  terminal snapshot in history

`history list` and `history show` expose these states rather than inventing
missing events. A configured event-sink failure remains fail-closed in the
engine. A post-run history snapshot failure is reported as an operational CLI
failure while the ordinary report remains available.

An interrupted `summary.json` publication is also explicit: list/show
recompute the same terminal event/report relationship when the immutable
summary index is absent. The compatibility suite separately proves event-only,
report-only, and report-plus-events-without-summary states.

Retention uses another bounded crash protocol under the same exclusive lock.
The confirmed plan is fsynced before data moves. Each selected attempt is
atomically renamed to private same-filesystem trash, followed by an immutable
progress marker; only then is the trash copy removed. Empty run metadata is
handled the same way. A retry with the same policy and digest resumes from the
manifest and markers. `history verify` reports a structurally valid interrupted
operation as `maintenance_required: true` instead of treating it as a new
plan.

The store is local durability, not a distributed transaction, lease service,
high-availability database, tamper-evident ledger, or substitute for protecting
the underlying filesystem.

## Bounded Data

The reader and writer enforce bounded identities, specs, individual events and
reports, aggregate event/report bytes, run counts, attempt counts, and event
counts. Aggregate limits are checked from file metadata before full records are
decoded. Terminal reports retain bounded artifact references and evidence;
artifact blobs remain in their existing content-addressed artifact store and
are not copied into history.

Text inspection shows attempts, terminal state, failed stage, checks, policy
verdicts, artifact references, and ordered events. `history list` validates
store, run, attempt, and summary identities, aggregate file bounds, event
counts, and report availability without decoding all report evidence. When a
summary is absent after an interrupted write, it falls back to the same
terminal event/report consistency checks as a full read.
`history show <run-id>` validates the complete run, while
`history show -attempt-id <attempt-id> <run-id>` reads and validates only that
addressed attempt so diagnostics do not scale with unrelated retained reports.
`history verify` intentionally takes the slower path and fully decodes every
retained run, event, and report. JSON output exposes each validated view for
automation.

## Retention And Data Removal

Retention is always a two-step operation. Planning is read-only:

```sh
pgdrill history prune \
  -store /var/lib/pgdrill/history \
  -before 2026-08-01T00:00:00Z \
  -keep-latest 2
```

The plan contains the exact selected attempts, protection counts, retained
artifact-reference count, and a canonical SHA-256 digest. Apply only the
reviewed digest with the identical policy:

```sh
pgdrill history prune \
  -store /var/lib/pgdrill/history \
  -before 2026-08-01T00:00:00Z \
  -keep-latest 2 \
  -confirm sha256:<plan-digest>
```

The exclusive lock recomputes the plan before the first mutation. A changed
store or policy produces a new digest and rejects stale confirmation.

Selection is deliberately conservative:

- only attempts with a valid terminal report are eligible
- `finished_at` must be strictly earlier than `-before`
- the latest `-keep-latest` terminal attempts in each logical run are protected
  (`attempt_id` ascending is the deterministic tie-break for equal timestamps)
- event-only/incomplete attempts are always protected
- attempts referencing `audit` artifacts are protected unless
  `-include-audit` is explicit
- an empty logical run is removed only when every attempt in it was selected

The command removes history identities, events, summaries, reports, and their
artifact references. It does **not** delete content-addressed artifact blobs or
ordinary report files outside the history store. Cross-run artifact garbage
collection remains a separate pre-GA gate because a blob can be referenced by
more than one retained report. Capture the plan and result externally when
they are required as audit evidence, and take a store backup before irreversible
removal.

## Versioning And Upgrade Boundary

`store.json` contains both schema and integer layout versions. Opening an
unknown version fails before records are read or modified. Empty directories
are bootstrapped only by the first write.

There is no earlier on-disk history version to migrate. The first supported
pre-GA floor is `v0.3.0-alpha.1`; its exact WAL-G latest/PITR store archive has
SHA-256
`dc44cbb9a86f2911f049ca09bb3ff505915a8e86780794a0b0fe4e6791084d5b`
and is read by the current test suite without regeneration.

Before `v1.0.0`, the alpha identifiers still need promotion to stable schema
identifiers and a backup-safe migration from this floor to that final layout.
Changing identity, immutability, ordering, retention, or directory semantics
requires a new store/layout version and migration tests. See
[upgrade.md](upgrade.md) for the current backup, verification, and rollback
procedure.
