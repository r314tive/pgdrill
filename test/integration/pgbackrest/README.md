# Local pgBackRest Integration Drill

This test creates a real PostgreSQL 18.3 or 17.10 source, a pgBackRest 2.58.0
local filesystem repository, and a separate pgdrill local restore target in
one disposable Linux container. PostgreSQL 18.3 is the default. It is a
developer compatibility gate, not an operator demo or a production topology
claim.

The scenario:

1. initializes a checksummed source cluster, creates a pgBackRest stanza, and
   installs `amcheck` before backup;
2. inserts 100 rows and takes a real full backup;
3. commits row 101 only after that backup, switches WAL, and fetches the exact
   segment back from the repository;
4. runs `pgdrill doctor` and catalog discovery;
5. requires pgBackRest `check` and selected-set `verify` checks;
6. restores the selected set into an independent target and starts it on a
   separate port with the scenario config available to `archive-get`;
7. requires readiness, the 101-row WAL sentinel, `pg_amcheck`, schema dump,
   recovery policy, and owned cleanup to pass;
8. records an exact timestamp, commits row 102 after it, retrieves that WAL
   from the repository, and requires a second restore to exclude row 102.

## Run

Prerequisites are Docker, Git, and the Go toolchain pinned by `.go-version`.

```sh
make test-integration-pgbackrest
PGDRILL_INTEGRATION_POSTGRES_VERSION=17.10 make test-integration-pgbackrest
```

The first preparation builds a provider runtime from the immutable PostgreSQL
base in `Dockerfile` and the exact pgBackRest package version. The build uses
the configured PGDG repository. The resulting definition hash and image ID are
recorded. The actual drill has no network, runs as UID 999 with all Linux
capabilities dropped and a read-only root filesystem, and uses disposable
tmpfs state.

Each run writes the raw `pgbackrest info --output=json` response, latest and
timestamp-PITR reports, doctor/catalog output, source and command logs, package
and runtime inventories, operation
checkpoints, the validated history attempt/list/full-verification views, an
archive of the raw private history store, and recursive checksums under the
ignored `.cache/integration/pgbackrest/runs/<timestamp>/` directory.
PostgreSQL 17.10 runs use
`.cache/integration/pgbackrest/postgresql-17.10/`. An explicit
`PGDRILL_INTEGRATION_PGBACKREST_IMAGE` override must already exist locally; the
runtime still refuses unexpected pgBackRest or PostgreSQL versions and records
the override.

A dirty source tree uses a direct developer binary with `dirty` version and
commit metadata. A clean tree builds one deterministic release archive,
extracts the exact archived binary, and records both archive and binary
SHA-256 values. Only a reviewed clean-candidate run can be promoted into
`compatibility/evidence`.

Supported target architectures are `linux/amd64` and `linux/arm64`. By
default the target matches the Docker daemon; `PGDRILL_INTEGRATION_TARGET_ARCH`
selects an explicit architecture when Docker emulation is available.
`PGDRILL_INTEGRATION_VERSION` binds a clean candidate version.
`PGDRILL_INTEGRATION_POSTGRES_VERSION` accepts exactly `17.10` or `18.3`.

## Scope Boundary

This test covers one same-host filesystem repository, one full backup, archive
push/get, latest recovery, inclusive timestamp PITR with a proved before/after
transaction boundary, and one container boundary. It does not establish remote
or object-storage behavior, encryption, differential or incremental backups,
other PITR targets, multi-host isolation, production RTO, or customer
readiness.
