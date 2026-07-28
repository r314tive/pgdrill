# WAL-G Integration Drills

These tests create a real PostgreSQL 18.3 source, a WAL-G 3.0.8 repository, and
a separate pgdrill local restore target. The default profile uses a disposable
filesystem repository in one Linux container. The S3 profile uses a separate
pinned MinIO server over a private Docker network with no published ports.
They are developer and release gates, not replacements for the isolated VM
demo topology.

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
make test-integration-walg-s3
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
binary and immutable platform-specific container manifests; later runs reuse
the verified cache. SHA-256 is checked before WAL-G is executed. Drill
containers forbid image pulls, run rootless with all Linux capabilities
dropped, and use a read-only root filesystem plus disposable tmpfs state. The
filesystem profile has no network. The S3 profile can reach only its internal
MinIO network; MinIO and `mc` are also rootless, read-only, capability-free,
and pinned separately for Linux amd64 and arm64.

S3 credentials exist only in the disposable execution environment. They are
not present in committed pgdrill configuration, and the host gate rejects any
retained artifact containing either credential. The S3 run additionally
retains a JSON object listing and aggregate object count/size, then requires
the owned MinIO container and private network to be absent before it can pass.

Each run writes latest and timestamp-PITR reports, the rendered PITR
configuration, doctor/catalog output, logs, exact runtime inventory, durable
operation checkpoints, per-attempt history views, a two-attempt history list,
full-store verification views, an archive of the raw private history store,
and recursive checksums under the
ignored `.cache/integration/walg/runs/<timestamp>/` directory for filesystem
storage or `.cache/integration/walg-s3/runs/<timestamp>/` for S3-compatible
storage. A dirty source tree is allowed for development, but both version and
commit metadata are suffixed with `dirty`; such output must not be promoted to
compatibility evidence.

A clean source tree takes the stronger path: the repository's deterministic
release builder creates a single-platform archive, the harness verifies and
extracts it, and that exact archived binary executes the drill. Runtime
inventory records both archive and binary SHA-256 values. Dirty trees use a
direct developer build because they cannot truthfully produce commit-bound
release evidence.

Supported target architectures are `linux/amd64` and `linux/arm64`. By
default the target matches the Docker daemon; `PGDRILL_INTEGRATION_TARGET_ARCH`
selects an explicit architecture when Docker emulation is available.
`PGDRILL_INTEGRATION_VERSION` can bind a clean candidate version, while
`PGDRILL_INTEGRATION_POSTGRES_IMAGE` is an explicit image override for
diagnostics. Any override changes the observed compatibility point and must be
recorded if the result is retained.

## Scope Boundary

Together these tests cover a full backup, latest recovery, timestamp PITR
between two archived transactions, filesystem and one S3-compatible storage
implementation, plus one process/container interruption boundary. The MinIO
profile proves WAL-G's S3 protocol path in one disposable single-node object
store. It does not establish Amazon S3, Yandex Object Storage, TLS, encryption,
IAM/workload identity, network-failure handling, incremental backups, other
PITR target types, multi-host isolation, production RTO, or customer
readiness.
