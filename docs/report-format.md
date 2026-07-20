# Report Format

`pgdrill` writes one JSON object per drill. The object is the durable boundary
between the execution engine and CLI, metrics, future TUI, or future web
consumers.

## Schema Version

Current schema:

```text
pgdrill.report/v1alpha1
```

Every new report includes `schema_version`. Readers accept an older report with
the field absent and normalize it to the current schema so reports created
before versioning remain usable. A non-empty unknown schema is rejected instead
of being interpreted optimistically.

Readers may ignore unknown fields within `v1alpha1`; producers may add optional
fields without changing the schema identifier. Removing fields, changing field
types or meanings, or changing required semantics requires a new schema
version.

## Canonical Object

The top-level object is `model.DrillResult` and contains:

- schema version, executing pgdrill build identity, and drill ID
- provider and selected backup, when provider discovery selected the input
- restore target and recovery target
- start and finish timestamps
- final drill status
- structured failure stage, message, and related evidence IDs for failed or
  aborted drills
- normalized checks and evidence records

Target-only drills may have an empty `provider` and `backup.provider`. Consumers
must not infer a provider from the restore target or a CNPG `Backup` reference.

Command evidence contains redacted arguments, environment values, output, and a
structured exit status. `path` is the configured executable name or path;
`resolved_path`, when present, is the executable selected by the operating
system's process runner. Bare command names are resolved through `PATH`; an
explicit path can remain explicit. Raw command output is available only to
in-process adapters while a command is being normalized and must not be
reconstructed from the durable report.

Probe invocations automatically treat the complete runtime PostgreSQL
connection string as sensitive. Reports retain separate target runtime
attributes such as host and port while replacing the connection argument and
any repeated occurrences in command output or errors.

Command capture is bounded per stdout/stderr stream. The in-process raw limit is
64 MiB; exceeding it returns an operation error so parsers never consume a
partial catalog or status document. Durable `stdout` and `stderr` are redacted
previews capped at 1 MiB each. `stdout_bytes`/`stderr_bytes` record observed byte
counts and `stdout_truncated`/`stderr_truncated` make either preview or raw
truncation explicit. A successful native exit can therefore coexist with a
failed capture contract; consumers must not use `exit_status.success` alone as
the drill verdict.

The structured exit status distinguishes successful execution, ordinary
non-zero exit codes, timeouts, cancellation, and failure to start. Consumers
should use `timed_out` and `canceled` instead of matching platform-specific
error strings. A drill canceled by the operator or its parent scheduler has
top-level status `aborted`; it is distinct from a completed verification with
status `failed`.

## Failure Contract

New failed and aborted reports include a `failure` object:

```json
{
  "stage": "backup_selection",
  "message": "select backup: no eligible backup",
  "evidence_ids": ["wal-g:backup-list:..."]
}
```

`stage` is the machine-readable contract. Current stages are:

- `request_validation`
- `preflight`
- `backup_discovery`
- `backup_selection`
- `catalog_validation`
- `restore_planning`
- `target_preparation`
- `restore_execution`
- `postgres_start`
- `probe_execution`
- `target_discovery`
- `target_start`
- `target_cleanup`
- `report_write`

`message` is diagnostic text and may change; consumers must not parse it. The
optional `evidence_ids` list links records already present in top-level
`evidence`. Legacy reports can have `status: failed` or `status: aborted`
without `failure`; readers preserve compatibility and metrics expose their
failure stage as `unknown`.

Prometheus output includes one `pgdrill_failure_info` sample with a bounded
`stage` label. Successful reports use `stage="none"`; the diagnostic message is
never used as a metric label. Missing or unrecognized stage values are exported
as `stage="unknown"`.

## Consumer Rules

- Check `schema_version` before interpreting the object.
- Use normalized `status`, checks, and structured command exit status rather
  than parsing human-readable messages.
- Treat omitted optional fields and additional unknown fields as compatible
  within `v1alpha1`.
- Preserve the source report when deriving metrics or presentation views.

`pgdrill report show` and `pgdrill report metrics` use the same reader and
therefore enforce this compatibility policy consistently.
