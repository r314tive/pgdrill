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

Schema recognition is not the only read gate. Current reports are validated for
canonical enum values, required identity and timestamps, timestamp ordering,
provider-scoped backup identity, valid WAL LSN/timeline values, terminal status
coherence, unique evidence/artifact IDs, and resolvable check, failure, and
artifact evidence links.
When a canonical drill spec is present, readers also recompute its digest and
reject non-canonical values or disagreement with report cluster, provider,
target, recovery target, or exact backup selection.
Unknown JSON fields remain forward-compatible, but a recognized schema with
contradictory contents is rejected rather than trusted.

## Canonical Object

The top-level object is `model.DrillResult` and contains:

- schema version, executing pgdrill build identity, logical drill ID, attempt
  ID, and configured cluster name when present
- the secret-free immutable drill spec and its canonical `sha256:` digest
- provider and selected backup, when provider discovery selected the input
- restore target and recovery target
- start and finish timestamps
- final drill status
- structured failure stage, message, and related evidence IDs for failed or
  aborted drills
- normalized checks and evidence records
- bounded immutable artifact references linked from evidence
- bounded terminal mutation operation records with deterministic keys,
  checkpoint state, and reconciliation status
- a versioned recovery-policy evaluation with typed limits, observations,
  evidence bases, and fail-closed verdicts

Target-only drills may have an empty `provider` and `backup.provider`. Consumers
must not infer a provider from the restore target or a CNPG `Backup` reference.

Current producers always write `attempt_id`, `spec_digest`, and `spec`. Readers
continue to accept reports created before these additive fields existed. If any
spec identity field is present, the complete identity must be coherent. The
spec digest excludes logical run and attempt IDs, so another attempt of the same
run retains the same digest. See [drill-spec-format.md](drill-spec-format.md).

The optional `operations` array contains additive
`pgdrill.operation-checkpoint/v1alpha1` records. Each operation key is a
canonical SHA-256 digest over the logical run, attempt, spec digest, lifecycle
stage, operation kind, name, and ordinal. New producers reject duplicate keys,
cross-attempt identities, non-terminal operation states, and a `passed` report
containing any operation that did not succeed. Older reports without operation
records remain readable. See
[operation-checkpoint-format.md](operation-checkpoint-format.md).

The optional `artifacts` array contains additive
`pgdrill.artifact-reference/v1alpha1` records. IDs are exact content digests;
payloads remain outside the report. Evidence links them through
`artifact_ids`, and every artifact must have provenance. See
[artifact-format.md](artifact-format.md).

Current producers always include
`pgdrill.recovery-policy-evaluation/v1alpha1`, even when every assertion is
disabled. Disabled assertions are `not_configured`, never synthetic passes.
Reports with a configured spec policy require a matching evaluation, and a
passed report cannot contain required `failed` or `unknown` verdicts. See
[recovery-policy.md](recovery-policy.md).

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

Long CNPG readiness waits retain each distinct raw command state without
duplicating unchanged snapshots on every poll. Compacted records expose
`poll_observations`, `poll_first_observed_at`, and `poll_last_observed_at` in
their attributes, so consumers can distinguish one observation from a stable
state seen repeatedly over a time range.

The structured exit status distinguishes successful execution, ordinary
non-zero exit codes, timeouts, cancellation, and failure to start. Consumers
should use `timed_out` and `canceled` instead of matching platform-specific
error strings. A drill canceled by the operator or its parent scheduler has
top-level status `aborted`; it is distinct from a completed verification with
status `failed`.

The JSON file sink writes a private temporary file, syncs it, atomically
replaces the configured report path, and syncs the parent directory. Newly
created report directories and files are owner-only by default. Replacing a
final symlink does not follow it; existing parent-directory aliases remain an
operator-controlled path choice. Producer validation runs before the sink
creates the report directory, so an invalid in-memory report cannot replace a
previous valid artifact.

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
- `policy_evaluation`
- `report_write`

`message` is diagnostic text and may change; consumers must not parse it. The
optional `evidence_ids` list links records already present in top-level
`evidence`. Legacy reports can have `status: failed` or `status: aborted`
without `failure`; readers preserve compatibility and metrics expose their
failure stage as `unknown`. New failed and aborted reports are rejected at the
producer boundary if structured failure details are absent.

Prometheus output includes one `pgdrill_failure_info` sample with a bounded
`stage` label. Successful reports use `stage="none"`; the diagnostic message is
never used as a metric label. Missing or unrecognized stage values are exported
as `stage="unknown"`.

Prometheus samples include the configured cluster name as a `cluster` label.
Legacy reports or configs without `cluster.name` use `cluster="unknown"`; the
drill ID is deliberately not a label because it would create an unbounded time
series for every execution.

Mutation checkpoints are exported through `pgdrill_operations_total`, grouped
only by bounded `kind`, `state`, and `reconciled` labels plus cluster and
provider. Operation names, keys, run IDs, and attempt IDs are deliberately not
metric labels.

Artifact metrics expose count and total bytes grouped only by bounded retention
and redaction classifications. Artifact IDs, URIs, and media types are not
metric labels.

Policy metrics expose verdict information and typed duration or boolean values.
Only finite assertion, verdict-status, and evidence-basis values become labels;
messages and run-specific identities do not.

Canonical enum labels are bounded as well. Unknown provider, target, recovery
target, probe, check-status, evidence-kind, operation, artifact-classification,
or failure-stage values export as `unknown` rather than creating arbitrary
metric series. Check names and cluster names remain operator-defined labels and
should come from stable configuration.

## Consumer Rules

- Check `schema_version` before interpreting the object.
- Use normalized `status`, checks, and structured command exit status rather
  than parsing human-readable messages.
- Treat omitted optional fields and additional unknown fields as compatible
  within `v1alpha1`.
- Preserve the source report when deriving metrics or presentation views.

`pgdrill report show` and `pgdrill report metrics` use the same reader and
therefore enforce this compatibility policy consistently.
