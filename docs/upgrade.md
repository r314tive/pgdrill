# Upgrade, Rollback, And Local State

This document describes the current pre-GA binary and local-state procedure.
It is intentionally narrower than a future long-term-support policy.

## Supported Floor

The first declared local-history compatibility floor is
`v0.3.0-alpha.1`:

- store schema: `pgdrill.history-store/v1alpha1`
- layout: `1`
- retained fixture:
  `internal/history/testdata/v0.3.0-alpha.1/history-store.tar.gz`
- fixture SHA-256:
  `dc44cbb9a86f2911f049ca09bb3ff505915a8e86780794a0b0fe4e6791084d5b`

Current readers prove read compatibility with that exact real-drill store.
Legacy stores are read-only in the stable writer. The explicit copy migration
described below promotes the store container to `pgdrill.history-store/v1`
without rewriting immutable historical specs, events, or reports.

## Before Upgrade

1. Stop schedulers that can start new pgdrill attempts.
2. Let active attempts finish or retain their event-only state deliberately.
3. For an abandoned local attempt, stop its complete executor process group
   and use the current binary's digest-confirmed `attempt recover` flow before
   changing binaries. If recovery is deliberately deferred, preserve the exact
   config and sibling checkpoint directory with the incomplete history. See
   [attempt-recovery.md](attempt-recovery.md).
4. Verify the complete store:

   ```sh
   pgdrill history verify -store /var/lib/pgdrill/history
   ```

5. If `maintenance_required` is true, do not migrate. Resume the reported
   retention digest with the exact alpha binary and original policy, or
   restore a verified pre-operation backup, then verify again.
6. Verify each local artifact store against its complete history scope:

   ```sh
   pgdrill artifact verify \
     -store /var/lib/pgdrill/report.json.artifacts \
     -history-store /var/lib/pgdrill/history
   ```

   Resume a pending artifact GC digest before continuing. Legacy blobs without
   immutable claims are valid but remain protected from default GC.
7. Ensure the destination parent is not group- or world-writable and has
   enough free space for a complete second history store plus a temporary
   staging copy.
8. Archive sibling artifact and checkpoint stores on the same trust boundary.
   The history
   source itself remains the rollback copy, but an additional archive is still
   appropriate for audit retention:

   ```sh
   umask 077
   tar -C /var/lib/pgdrill -czf pgdrill-history-before-upgrade.tar.gz history
   tar -C /var/lib/pgdrill -czf pgdrill-artifacts-before-upgrade.tar.gz report.json.artifacts
   tar -C /var/lib/pgdrill -czf pgdrill-checkpoints-before-upgrade.tar.gz report.json.checkpoints
   sha256sum pgdrill-history-before-upgrade.tar.gz
   sha256sum pgdrill-artifacts-before-upgrade.tar.gz
   sha256sum pgdrill-checkpoints-before-upgrade.tar.gz
   ```

9. Retain the current binary or image digest, checksum, signed provenance
   bundle, version output, and configuration. Verify the replacement archive or
   OCI digest against the expected repository, release workflow, and tag before
   stopping the current executor.

The archive contains operational evidence and can contain infrastructure
identifiers. Do not upload it to a public issue or release.

## Upgrade And Verification

Replace only the pgdrill binary. First prove that the source is the declared
legacy generation:

```sh
pgdrill version
pgdrill history verify \
  -store /var/lib/pgdrill/history-alpha \
  -format json
```

The result must report:

- `store_schema_version: pgdrill.history-store/v1alpha1`
- `layout_version: 1`
- `migration_required: true`
- `maintenance_required: false`

Create and retain the migration plan:

```sh
pgdrill history migrate \
  -store /var/lib/pgdrill/history-alpha \
  -destination /var/lib/pgdrill/history-stable \
  -format json > pgdrill-history-migration-plan.json
```

Review the source and destination, source snapshot digest, counts, bytes, and
plan digest. Apply only that exact digest with the same paths:

```sh
pgdrill history migrate \
  -store /var/lib/pgdrill/history-alpha \
  -destination /var/lib/pgdrill/history-stable \
  -confirm sha256:<reviewed-plan-digest> \
  -format json > pgdrill-history-migration-result.json
```

The command holds a shared source lock for the copy, preserves every file
under `runs/` byte-for-byte, verifies the staged destination, and atomically
publishes it. It never changes the source. A repeated exact apply returns
`already_applied: true` after re-verifying provenance and historical payload.

Verify the destination before changing any scheduler or service configuration:

```sh
pgdrill history verify -store /var/lib/pgdrill/history-stable
pgdrill history list -store /var/lib/pgdrill/history-stable -limit 1000
pgdrill artifact verify \
  -store /var/lib/pgdrill/report.json.artifacts \
  -history-store /var/lib/pgdrill/history-stable
```

Unknown schemas, unknown fields, broken identities, non-private permissions,
corrupt reports, source drift, and destination collisions fail explicitly.
The destination must report `pgdrill.history-store/v1`,
`migration_required: false`, the confirmed migration plan digest, and the
expected run/attempt/report/event counts.

Stop writers before switching them from `history-alpha` to `history-stable`.
After the switch, run one bounded drill and verify the stable store again.
Retain the alpha source until that operational acceptance is complete.
Migrated alpha logical runs remain readable but closed to new attempts because
their spec schema is part of the immutable digest. Schedule equivalent work as
a new stable logical run.

## Rollback

Before switching writers, rollback requires no data operation: continue using
the untouched alpha source and retained alpha binary.

After new attempts have been written to the stable destination, do not point an
older binary at it. A rollback to the alpha source discards those new stable
attempts unless they are retained separately. Stop writers, preserve both
stores, explicitly accept that boundary, then restore the old binary and old
path. There is no reverse migration because the forward operation never
destroys or rewrites its source.

## Retention And Removal

`history prune` is dry-run by default and requires the exact plan digest to
remove eligible terminal history. It never removes:

- event-only or otherwise incomplete attempts
- the configured latest attempts in each run
- audit-linked attempts unless explicitly included
- artifact blobs or report files outside the history store

Because pruning is irreversible after completion, take and checksum a private
archive when rollback or audit retention is required. Artifact removal is a
second dry-run/confirm workflow:

```sh
pgdrill artifact gc \
  -store /var/lib/pgdrill/report.json.artifacts \
  -history-store /var/lib/pgdrill/history \
  -before 2026-08-01T00:00:00Z
```

Live references are never selected. Audit-classified, legacy, and abandoned
temporary files require separate explicit flags. Use a cutoff longer than the
maximum artifact-to-terminal-history publication interval, stop schedulers and
out-of-band imports, archive the store, then apply only the exact reviewed
digest. See [artifact-format.md](artifact-format.md).
