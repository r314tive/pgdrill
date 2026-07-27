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
There is no earlier history layout and no automatic migration. Other alpha
schemas remain prerelease contracts until the GA migration is implemented.

## Before Upgrade

1. Stop schedulers that can start new pgdrill attempts.
2. Let active attempts finish or retain their event-only state deliberately.
3. Verify the complete store:

   ```sh
   pgdrill history verify -store /var/lib/pgdrill/history
   ```

4. If `maintenance_required` is true, resume the reported retention digest
   with the same original policy before continuing.
5. Verify each local artifact store against its complete history scope:

   ```sh
   pgdrill artifact verify \
     -store /var/lib/pgdrill/report.json.artifacts \
     -history-store /var/lib/pgdrill/history
   ```

   Resume a pending artifact GC digest before continuing. Legacy blobs without
   immutable claims are valid but remain protected from default GC.
6. Archive the private history and sibling artifact stores on the same trust
   boundary:

   ```sh
   umask 077
   tar -C /var/lib/pgdrill -czf pgdrill-history-before-upgrade.tar.gz history
   tar -C /var/lib/pgdrill -czf pgdrill-artifacts-before-upgrade.tar.gz report.json.artifacts
   sha256sum pgdrill-history-before-upgrade.tar.gz
   sha256sum pgdrill-artifacts-before-upgrade.tar.gz
   ```

7. Retain the current binary, its checksum, version output, and configuration.

The archive contains operational evidence and can contain infrastructure
identifiers. Do not upload it to a public issue or release.

## Upgrade And Verification

Replace only the pgdrill binary, then run:

```sh
pgdrill version
pgdrill history verify -store /var/lib/pgdrill/history
pgdrill history list -store /var/lib/pgdrill/history -limit 1000
pgdrill artifact verify \
  -store /var/lib/pgdrill/report.json.artifacts \
  -history-store /var/lib/pgdrill/history
```

Unknown schemas, unknown fields, broken identities, non-private permissions,
and corrupt reports fail explicitly. The reader never rewrites an unknown
store automatically.

## Rollback

While both binaries use the same documented `v1alpha1` layout, rollback is a
binary replacement followed by `history verify`. Do not write new history with
an older binary after a newer release introduces a schema or layout it does
not understand.

For the future stable-layout migration, rollback is permitted only through the
documented backup restore or a separately tested reverse migration. The GA
release must not rely on an in-place rewrite with no recoverable source copy.

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
