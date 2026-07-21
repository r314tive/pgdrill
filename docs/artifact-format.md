# Artifact Reference Format

`pgdrill` keeps bounded evidence summaries in the terminal report and stores
larger immutable payloads separately. Each top-level artifact uses the internal
schema:

```text
pgdrill.artifact-reference/v1alpha1
```

The reference is additive within `pgdrill.report/v1alpha1`. It remains under
`internal/` until local history or an out-of-process consumer proves the API.

## Reference

Each reference contains:

- `id`: canonical lowercase `sha256:<hex>` digest of the exact stored bytes
- `uri`: immutable location, normally relative to the report
- `size_bytes`: exact payload length, bounded to 64 MiB
- `media_type`: canonical MIME media type
- `retention_class`: `run`, `history`, or `audit`
- `redaction_state`: `redacted` or `not_required`

Evidence records link artifacts through `artifact_ids`. A report rejects
duplicate IDs, one URI assigned to different digests, dangling links, duplicate
links within one evidence record, and artifacts not referenced by any evidence.
Reports contain metadata and provenance only, never inline artifact bytes.

Relative URIs are canonical descendant paths. Credentials, query parameters,
fragments, parent traversal, control characters, and platform-specific
backslashes are rejected. Remote stores may return a lowercase absolute URI,
but it must remain secret-free and immutable; signed download URLs do not
belong in a durable report.

## Classification

The retention classes are policy inputs, not hard-coded expiration periods:

- `run`: eligible for removal after terminal attempt persistence
- `history`: retained with normal drill history
- `audit`: retained according to an external audit policy

The current directory store does not delete blobs automatically. Future local
history owns indexing and garbage collection, and must account for every report
reference before removing a content-addressed blob.

There is deliberately no durable `unredacted` state. A producer must redact the
payload before calling the sink or classify it as `not_required` because its
schema cannot carry secrets. CNPG manifests use `not_required`: the generated
manifest contains declarative cluster settings and object references, not
Secret values. Operators must not place credentials in custom labels.

## Directory Store

CLI-managed CNPG artifacts are stored below:

```text
<report.path>.artifacts/sha256/<digest-prefix>/<digest>
```

The report URI is relative and begins with the artifact directory base name,
so moving a report together with its sibling artifact directory preserves the
link. Writes stream through SHA-256 and a strict size limit into a private
temporary file. Publication uses an atomic no-overwrite link, making concurrent
writes of the same content converge on one blob. New directories and files are
owner-only, managed-path symlinks are rejected, and directory metadata is
synced after publication.

Reads verify the expected store URI, regular-file type, exact size, and SHA-256
digest. Existing blobs are verified before deduplication succeeds; corruption
or a symbolic-link substitution is a hard error.

## Mutation Ordering

The CNPG controller renders and persists its manifest before `kubectl create`.
Artifact-store failure therefore prevents infrastructure mutation. A
replacement executor can render the same attempt-scoped manifest during
read-only target reconciliation and recover the same content digest without
replaying `create`.
