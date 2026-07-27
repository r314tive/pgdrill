# Local WAL-G Integration Drill

This test creates a real PostgreSQL 18.3 source, a WAL-G 3.0.8 filesystem
repository, and a separate pgdrill local restore target in one disposable Linux
container. It is the fast developer-level counterpart to the isolated VM demo,
not a replacement for that topology.

The scenario:

1. initializes a checksummed source cluster with archiving enabled and installs
   `amcheck` before the backup;
2. inserts 100 rows and takes a real WAL-G full backup;
3. commits row 101 only after the base backup and archives its WAL segment;
4. runs `pgdrill doctor` and catalog discovery;
5. restores latest recovery and requires the 101-row WAL sentinel;
6. records a timestamp boundary, commits and archives row 102 after it, then
   restores to that timestamp;
7. requires the PITR target to contain row 101 but not row 102;
8. requires WAL integrity, readiness, `pg_amcheck`, schema dump, five recovery
   policy verdicts, and owned cleanup for both attempts.

## Run

Prerequisites are Docker, `curl`, Git, and the Go toolchain pinned by
`.go-version`.

```sh
make test-integration-walg
```

Shell changes should additionally pass the opt-in static lint when ShellCheck
is installed:

```sh
make integration-check
```

For the complete local developer gate, including unit, race, CLI smoke, and
this drill:

```sh
make test-local
```

The first host preparation downloads only an architecture-specific WAL-G
binary and an immutable multi-platform PostgreSQL image; later runs reuse the
verified cache. SHA-256 is checked before WAL-G is executed. The container
itself forbids image pulls, runs rootless with all Linux capabilities dropped,
uses a read-only root filesystem and disposable tmpfs state, and has no
network.

Each run writes latest and timestamp-PITR reports, the rendered PITR
configuration, doctor/catalog output, logs, exact runtime inventory, durable
operation checkpoints, per-attempt history views, a two-attempt history list,
full-store verification views, an archive of the raw private history store,
and recursive checksums under the
ignored
`.cache/integration/walg/runs/<timestamp>/` directory. A dirty source tree is
allowed for development, but both version and commit metadata are suffixed
with `dirty`; such output must not be promoted to compatibility evidence.

A clean source tree takes the stronger path: the repository's deterministic
release builder creates a single-platform archive, the harness verifies and
extracts it, and that exact archived binary executes the drill. Runtime
inventory records both archive and binary SHA-256 values. Dirty trees use a
direct developer build because they cannot truthfully produce commit-bound
release evidence.

Supported Docker daemon architectures are `linux/amd64` and `linux/arm64`.
`PGDRILL_INTEGRATION_VERSION` can bind a clean candidate version, while
`PGDRILL_INTEGRATION_POSTGRES_IMAGE` is an explicit image override for
diagnostics. Any override changes the observed compatibility point and must be
recorded if the result is retained.

## Scope Boundary

This test covers a full backup, latest recovery, timestamp PITR between two
archived transactions, filesystem storage, and one process/container boundary.
It does not establish remote-object-store behavior, encryption, incremental
backups, other PITR target types, multi-host isolation, production RTO, or
customer readiness.
