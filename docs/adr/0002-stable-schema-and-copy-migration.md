# ADR 0002: Stable Schemas And Copy-On-Migrate History

- Status: accepted
- Date: 2026-07-28

## Context

The pre-GA engine persists reports, canonical drill specs, run events,
operation checkpoints, artifact references, and local history. The declared
history compatibility floor is the exact `v0.3.0-alpha.1`
`pgdrill.history-store/v1alpha1` fixture.

The drill-spec schema identifier is part of its canonical SHA-256 digest. That
digest is embedded in run and attempt identities, event streams, reports,
ownership IDs, and operation keys. Rewriting a retained alpha spec to a stable
identifier would therefore cascade through historical identities and alter
the evidence being migrated.

An in-place multi-file rewrite would also make rollback depend on every
rename, fsync, and interruption window succeeding. That is not an acceptable
upgrade boundary for recovery evidence.

## Decision

New producers use stable `v1` schema identifiers. Readers accept both the
stable generation and the documented `v1alpha1` generation where retained
pre-GA evidence requires it. Produced reports must use stable identifiers;
legacy identifiers are read compatibility, not a way to create new alpha
records.

History migration is copy-on-migrate:

1. fully validate the source while holding its shared store lock
2. reject active or pending retention maintenance
3. hash a strict, bounded manifest of the complete source tree
4. return a deterministic plan bound to source, destination, counts, bytes,
   source snapshot, and historical payload digest
5. require the exact plan digest before copying
6. copy historical run files byte-for-byte into a private sibling stage
7. write stable store metadata and immutable migration provenance
8. re-verify the stage and compare its historical payload digest
9. atomically rename the complete stage to the previously absent destination
10. leave the source untouched as the rollback copy

The command never migrates in place. Source and destination cannot contain one
another. A failed or killed copy can leave only a hidden staging directory;
the next exact apply removes that private stage and starts the copy again.
Publication occurs only after the complete stage is durable and verified.

Historical `v1alpha1` specs, events, reports, and identity envelopes remain in
their original bytes inside the stable store. This is intentional. The stable
store contract recognizes that immutable legacy generation, while every new
record uses stable identifiers.

Because the schema identifier participates in the spec digest, migrated alpha
logical runs are closed to new attempts. Retrying equivalent work creates a
new stable logical run and spec digest. Treating the identifiers as aliases
would make the persisted digest cease to describe its canonical JSON.

## Consequences

- The original alpha store is an exact rollback source and must be retained
  until the stable destination has passed operational acceptance.
- Operators must update pgdrill to use the destination after migration.
- An older binary must never write to the stable destination.
- Disk capacity is temporarily required for both stores and the staging copy.
- A stable store can contain closed historical alpha runs and new stable runs
  without changing prior spec digests.
- Migration provenance records the confirmed plan, source schema, source
  snapshot, and historical payload digest without storing secrets.
- Artifact blobs are not copied by history migration. Their content-addressed
  stores remain a separate lifecycle and must be verified against the selected
  history destination.

## Rejected Alternatives

### Rewrite every retained document

Rejected because changing the drill-spec schema changes its digest and all
derived identities and operation keys. It would manufacture a new history
rather than preserve collected evidence.

### In-place store metadata replacement

Rejected because it provides no independently recoverable source if a later
validation fails and makes rollback depend on an operator-created backup.

### Automatic migration on open

Rejected because read-only inspection must never mutate recovery evidence, and
because operators must review destination capacity, snapshot identity, and the
exact migration plan before publication.
