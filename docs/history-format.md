# Local History Format

The optional local history is a daemon-free, append-only filesystem store for
logical runs and execution attempts. It consumes the same canonical drill
specs, lifecycle events, reports, and artifact references as direct execution;
it does not introduce another restore state machine.

The on-disk contract is currently `pgdrill.history-store/v1alpha1` with layout
version `1`. It is a pre-GA format. Unknown schema or layout versions fail
explicitly and are never rewritten automatically.

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
```

For inspection commands, the store path resolves in this order:
`-store`, `PGDRILL_HISTORY_DIR`, `XDG_STATE_HOME/pgdrill/history`, then
`~/.local/state/pgdrill/history`. A missing store lists as empty and is not
created by a read command.

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
JSON output exposes the validated view for automation.

No retention or deletion command exists yet. Operators must treat the history
directory and report artifact directories as one retention domain until a
tested garbage-collection policy is introduced.

## Versioning And Upgrade Boundary

`store.json` contains both schema and integer layout versions. Opening an
unknown version fails before records are read or modified. Empty directories
are bootstrapped only by the first write.

There is no legacy on-disk history version to migrate yet. Before `v1.0.0`, the
final prerelease must define the supported pre-GA floor and prove an explicit,
backup-safe migration or read-compatibility path. Changing identity,
immutability, ordering, or directory semantics requires a new store/layout
version and migration tests.
