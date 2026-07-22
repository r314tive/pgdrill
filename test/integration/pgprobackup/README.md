# Local pg_probackup Integration Drill

This test builds pg_probackup 2.5.16 at its exact source commit together with
the patched PostgreSQL 18.3 source, then creates a real local backup catalog and
a separate pgdrill restore target in one disposable Linux container. It is a
developer compatibility gate, not an operator demo or a production topology
claim.

The scenario:

1. verifies the PostgreSQL source archive, applies pg_probackup's PG18 patch,
   and builds PostgreSQL, pg_probackup, and `amcheck`;
2. initializes a checksummed source and local pg_probackup catalog;
3. inserts 100 rows and takes a compressed full STREAM backup;
4. commits row 101 only after that backup, archives the containing WAL segment,
   retrieves that exact segment, and runs native backup/WAL validation;
5. runs `pgdrill doctor` and catalog discovery;
6. repeats selected-backup `validate --wal`, restores with local `archive-get`,
   and starts the target on a separate port;
7. requires readiness, the 101-row WAL sentinel, `pg_amcheck`, schema dump,
   recovery policy, and owned cleanup to pass.

## Run

Prerequisites are Docker, Git, and the Go toolchain pinned by `.go-version`.

```sh
make test-integration-pgprobackup
```

The first preparation builds the provider runtime from the immutable
PostgreSQL base in `Dockerfile`. Network is needed only during that image build
to install compiler dependencies and fetch the exact source inputs. The
PostgreSQL archive is verified against a committed SHA-256, the pg_probackup
checkout is verified against its full commit, and the resulting definition
hash and image ID are recorded. The actual drill has no network, runs as UID
999 with all Linux capabilities dropped and a read-only root filesystem, and
uses disposable tmpfs state.

Each run writes `report.json`, doctor/catalog output, source and provider logs,
generated catalog configuration, source/build and runtime inventories,
operation checkpoints, and recursive checksums under the ignored
`.cache/integration/pgprobackup/runs/<timestamp>/` directory. An explicit
`PGDRILL_INTEGRATION_PGPROBACKUP_IMAGE` override must already exist locally;
the runtime still refuses unexpected pg_probackup or PostgreSQL versions and
records the override.

A dirty source tree uses a direct developer binary with `dirty` version and
commit metadata. A clean tree builds one deterministic release archive,
extracts the exact archived binary, and records both archive and binary
SHA-256 values. Only a reviewed clean-candidate run can be promoted into
`compatibility/evidence`.

Supported Docker daemon architectures are `linux/amd64` and `linux/arm64`.
`PGDRILL_INTEGRATION_VERSION` binds a clean candidate version.

## Scope Boundary

This test covers one same-host filesystem catalog, one compressed full STREAM
backup, continuous WAL archive push/get, latest recovery, one post-backup WAL
segment, a superuser connection, and one container boundary. It does not
establish remote SSH, incremental modes, other PITR targets, other versions or
platforms, production RTO, or customer readiness.
