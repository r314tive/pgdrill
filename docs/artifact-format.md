# Artifact Reference Format

`pgdrill` keeps bounded evidence summaries in the terminal report and stores
larger immutable payloads separately. Each top-level artifact uses the internal
schema:

```text
pgdrill.artifact-reference/v1
```

The reference is additive within `pgdrill.report/v1`. Readers retain
`v1alpha1` report and artifact-reference compatibility for the documented
pre-GA history floor. Local history now
retains and validates these references, but the Go type remains under
`internal/` until an out-of-process consumer proves a stable public API.

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

The artifact directory store never deletes blobs during report writes. Local
history retention and artifact garbage collection are separate,
digest-confirmed operations: pruning a terminal attempt removes references,
while `artifact gc` removes only blobs that are absent from the complete
retained reference scope.

There is deliberately no durable `unredacted` state. A producer must redact the
payload before calling the sink or classify it as `not_required` because its
schema cannot carry secrets. CNPG manifests use `not_required`: the generated
manifest contains declarative cluster settings and object references, not
Secret values. Operators must not place credentials in custom labels.

## Directory Store

CLI-managed CNPG artifacts are stored below:

```text
<report.path>.artifacts/
  store.json
  sha256/<digest-prefix>/<digest>
  claims/sha256/<digest-prefix>/<digest>/<claim-digest>.json
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

`store.json` binds the stable `pgdrill.artifact-store/v1` schema, layout
version, and URI base.
Every successful `Put` writes an immutable classification claim and updates
the blob's last-observed timestamp under the same exclusive store lock. Reuse
of an old content digest therefore becomes recent before a producer can return
its reference. Claims accumulate rather than weaken: a blob ever observed as
`audit` stays audit-protected by default even when another report classified
the same bytes as `history`.

Stores written before this metadata layer remain readable. Blobs without
claims are reported as legacy and are never selected by default. The untagged
`pgdrill.artifact-store/v1alpha1` generation is readable but read-only; it was
never declared as a release compatibility floor.

## Verification

Verification requires both the local artifact store and the canonical history
store that completely owns its references:

```sh
pgdrill artifact verify \
  -store /var/lib/pgdrill/report.json.artifacts \
  -history-store /var/lib/pgdrill/history
```

The command holds the history store under a shared lock, then hashes every
active blob, validates all immutable claims and local references, rejects
missing or corrupted blobs, and reports foreign references without treating
them as owned by this store. It also validates interrupted GC state.
`maintenance_required` is true for abandoned temporary files or an unfinished
GC operation.

The explicit history path is a safety boundary, not a convenience hint. Every
run that can reference this artifact store must persist its terminal report to
that history store before GC is allowed. A standalone report, another history
store, or an out-of-band import is not discovered automatically. For a
one-off run that did not enable `-history-dir`, first import its terminal
report into a dedicated scope:

```sh
pgdrill history import \
  -store /var/lib/pgdrill/history \
  /var/lib/pgdrill/report.json
```

## Garbage Collection

Planning is read-only:

```sh
pgdrill artifact gc \
  -store /var/lib/pgdrill/report.json.artifacts \
  -history-store /var/lib/pgdrill/history \
  -before 2026-08-01T00:00:00Z
```

Apply only the exact plan digest with the same cutoff and options:

```sh
pgdrill artifact gc \
  -store /var/lib/pgdrill/report.json.artifacts \
  -history-store /var/lib/pgdrill/history \
  -before 2026-08-01T00:00:00Z \
  -confirm sha256:<plan-digest>
```

The plan hashes the full store and canonical reference set. Apply reacquires
the history shared lock and artifact exclusive lock, recomputes the plan, and
rejects stale confirmation before mutation. Selection is conservative:

- a currently referenced blob is never eligible
- `last_observed_at` must be strictly earlier than `-before`
- any blob with an `audit` claim is protected unless `-include-audit` is set
- a blob without claims is protected unless `-include-legacy` is set
- abandoned temporary files are protected unless `-include-temporary` is set

The cutoff must exceed the longest expected interval between artifact
publication and terminal-history persistence. Stop schedulers and out-of-band
history imports for maintenance; the local lock protocol cannot coordinate an
unrelated report file or remote database.

Deletion first moves each exact blob and claim directory into same-filesystem
private trash, records immutable progress, and removes it. The operation can
resume after process loss between blob rename, claim rename, progress
publication, completion, and final cleanup. `artifact verify` exposes the
digest required to resume, and new artifact publication is rejected while that
maintenance is pending. An unrelated safe reference-scope change is reported
as `reference_scope_changed`; a new reference to any selected candidate fails
closed. GC removes only this local directory-store content;
remote/object-store artifact lifecycle is not implemented.

## Mutation Ordering

The CNPG controller renders and persists its manifest before `kubectl create`.
Artifact-store failure therefore prevents infrastructure mutation. A
replacement executor can render the same attempt-scoped manifest during
read-only target reconciliation and recover the same content digest without
replaying `create`.
