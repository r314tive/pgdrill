# Integration Tests

This tree contains reproducible developer tests that execute real external
tools. It is intentionally separate from both product code and presentation
infrastructure:

- `internal/**` tests exercise Go contracts, fixtures, and controlled fakes.
- `test/integration/**` creates disposable local systems and proves real tool
  interoperability.
- `demo/**` provisions an operator-facing environment for a technical session.
- `compatibility/evidence/**` retains reviewed observations that support an
  exact compatibility claim.

An integration pass is not automatically compatibility evidence. Promote a
result only after binding it to a clean release-candidate commit, reviewing the
scope and artifacts, and updating the compatibility matrix deliberately.

The tests may download pinned public tool artifacts and container images during
preparation. The actual drill runs without external network access whenever the
provider permits it. The WAL-G S3 profile uses only a disposable internal
Docker network between PostgreSQL, WAL-G, MinIO, and MinIO Client containers.

Host-side release-candidate binding, Docker isolation defaults, and artifact
checksumming live in `lib/runtime.sh`. Provider setup, backup semantics,
restore commands, and acceptance assertions stay in their scenario directory.
`lib/history.sh` performs only CLI-level acceptance: every successful scenario
requires a readable full attempt, a terminal `run_finished` event, a passed
terminal report, the exact expected attempt count, a clean full-store
`history verify`, and an inspectable archive of the private local history
store. The artifact-producing CNPG scenario additionally requires a clean
full-blob `artifact verify` against that history scope and a future-cutoff GC
plan with zero candidates.

By default, a clean checkout produces and executes a deterministic release
archive from `HEAD`. Native scenarios can instead consume an existing Linux
archive by setting all of:

- `PGDRILL_INTEGRATION_RELEASE_ARCHIVE`
- `PGDRILL_INTEGRATION_RELEASE_ARCHIVE_SHA256`
- `PGDRILL_INTEGRATION_VERSION`
- `PGDRILL_INTEGRATION_COMMIT`

The runtime rejects an archive whose filename, architecture, checksum, version,
or full commit binding is inconsistent. The operator-facing wrapper for this
mode is documented under [demo/local](../../demo/local/README.md).

Native scenarios target the Docker daemon architecture by default. Set
`PGDRILL_INTEGRATION_TARGET_ARCH=amd64` or `arm64` to exercise another
architecture when the daemon has the corresponding emulation support. The
harness builds and runs every binary and provider image for that exact target
and records both the daemon architecture and `build_target` in `runtime.txt`.
An immutable platform-specific base-image manifest is selected and locally
verified before `--pull never` execution.
An emulated observation proves functional interoperability at that
Linux/architecture point; it is not native-hardware performance or RTO
evidence.

Current scenarios:

- [WAL-G with filesystem or S3-compatible storage to a local PostgreSQL target](walg/README.md)
- [Barman to a local PostgreSQL target](barman/README.md)
- [pgBackRest to a local PostgreSQL target](pgbackrest/README.md)
- [pg_probackup to a local PostgreSQL target](pgprobackup/README.md)
- [CloudNativePG to a disposable KinD target](cnpg/README.md)

Every native-provider scenario proves latest recovery with post-backup WAL
replay and inclusive timestamp PITR with a transaction on each side of the
requested boundary. These disposable observations remain narrower than
retained compatibility claims.
