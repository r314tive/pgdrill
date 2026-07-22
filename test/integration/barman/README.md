# Local Barman Integration Drill

This test creates a real PostgreSQL 18.3 source, a Barman 3.19.1 local-rsync
repository, and a separate pgdrill local restore target in one disposable Linux
container. It is a developer compatibility gate, not an operator demo or a
production topology claim.

The scenario:

1. initializes a checksummed source cluster with file archiving and installs
   `amcheck` before the backup;
2. inserts 100 rows and takes a real Barman local-rsync full backup;
3. commits row 101 only after the base backup and archives its WAL segment;
4. runs `pgdrill doctor` and catalog discovery;
5. requires Barman `check`, `check-backup`, `show-backup`,
   `generate-manifest`, and `verify-backup` checks;
6. runs `barman restore --get-wal` into an independent target and starts it on
   another port, with the scenario config explicitly propagated to Barman's
   generated WAL restore command;
7. requires readiness, the 101-row WAL sentinel, `pg_amcheck`, schema dump,
   recovery policy, and owned cleanup to pass.

## Run

Prerequisites are Docker, Git, and the Go toolchain pinned by `.go-version`.

```sh
make test-integration-barman
```

The first preparation builds a provider runtime from the immutable PostgreSQL
base in `Dockerfile` and exact Barman, python3-barman, and rsync package
versions. That image build uses the configured Debian and PGDG repositories.
The resulting definition hash and image ID are recorded. The actual drill has
no network, runs as UID 999 with all Linux capabilities dropped and a read-only
root filesystem, and uses disposable tmpfs state.

Each run writes `report.json`, doctor/catalog output, PostgreSQL and Barman
logs, package and runtime inventories, operation checkpoints, and recursive
checksums under the ignored `.cache/integration/barman/runs/<timestamp>/`
directory. An explicit `PGDRILL_INTEGRATION_BARMAN_IMAGE` override must already
exist locally; the runtime still refuses unexpected Barman or PostgreSQL
versions and records the override.

A dirty source tree uses a direct developer binary with `dirty` version and
commit metadata. A clean tree builds one deterministic release archive,
extracts the exact archived binary, and records both archive and binary
SHA-256 values. Only a reviewed clean-candidate run can be promoted into
`compatibility/evidence`.

Supported Docker daemon architectures are `linux/amd64` and `linux/arm64`.
`PGDRILL_INTEGRATION_VERSION` binds a clean candidate version.

## Scope Boundary

This test covers one same-host local-rsync full backup, file-based WAL
archiving, latest recovery, one post-backup WAL segment, and one container
boundary. It does not establish SSH/remote-server behavior, streaming
archiving, cloud storage, incremental backups, timestamp PITR, multi-host
isolation, production RTO, or customer readiness.
