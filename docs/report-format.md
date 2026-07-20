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

- schema version and drill ID
- provider and selected backup, when provider discovery selected the input
- restore target and recovery target
- start and finish timestamps
- final drill status
- normalized checks and evidence records

Target-only drills may have an empty `provider` and `backup.provider`. Consumers
must not infer a provider from the restore target or a CNPG `Backup` reference.

Command evidence contains redacted arguments, environment values, output, and a
structured exit status. Raw command output is available only to in-process
adapters while a command is being normalized and must not be reconstructed from
the durable report.

## Consumer Rules

- Check `schema_version` before interpreting the object.
- Use normalized `status`, checks, and structured command exit status rather
  than parsing human-readable messages.
- Treat omitted optional fields and additional unknown fields as compatible
  within `v1alpha1`.
- Preserve the source report when deriving metrics or presentation views.

`pgdrill report show` and `pgdrill report metrics` use the same reader and
therefore enforce this compatibility policy consistently.
