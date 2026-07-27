# Compatibility And Validation

`pgdrill` has published its first Engine v0.2 release candidate. This document
separates build portability, automated test coverage, and real-environment
validation so a green unit test is not mistaken for a production support
claim.

## Machine-Readable Evidence

The source of truth is
[`compatibility/matrix.yaml`](../compatibility/matrix.yaml), using
`pgdrill.compatibility-matrix/v1alpha1`. It distinguishes:

- `fixture`: committed native output plus provider conformance; no native tool
  version is claimed
- `controlled`: target lifecycle and reconciliation against controlled
  executables or clients
- `field`: a dated external observation with exact pgdrill version and commit,
  component, PostgreSQL, and platform versions

Every entry records demonstrated capabilities, direct evidence references, and
explicit limitations. Each field entry represents one exact implementation,
pgdrill commit, PostgreSQL, platform, and recovery-target point; another point
requires another entry. Repository tests resolve those references and all
current adapters run the same canonical provider suite. The local and CNPG
targets run native and managed process-loss reconciliation suites respectively.
Native-provider field entries must reference a passed drill report. Repository
tests parse referenced reports and cross-check provider or target identity,
recovery target, observation date, PostgreSQL/tool versions, claimed CNPG
operator version when applicable, pgdrill version, and full commit. Release
packaging validates and includes the matrix and this document.

## Release Platforms

The release pipeline builds static `CGO_ENABLED=0` CLI archives for:

- Linux amd64 and arm64
- macOS amd64 and arm64

Windows cross-compilation currently succeeds, but Windows runtime behavior and
the required PostgreSQL backup tools have not been field-tested. Windows
archives are therefore not published.

## Adapter Confidence

WAL-G, Barman, pgBackRest, and pg_probackup catalog parsers have fixture-driven
tests, including numeric and textual WAL-G LSN representations, keyed Barman
backup objects, and multi-history pgBackRest metadata. Restore planning and
provider checks have command-construction and evidence tests. A shared suite
also enforces canonical IDs, selection, report/evidence integrity, foreign
provider rejection, and restore planning for all six canonical recovery-target
types. These tests prove normalization and protocol behavior against committed
fixtures; they do not prove compatibility with any historical or future native
tool version.

Before claiming a native version as validated:

1. capture `pgdrill version` and `pgdrill doctor -f <config> -format json`
2. run catalog discovery against a disposable or read-only repository
3. run the provider check profile used in production
4. complete a real restore and the required probes
5. retain the JSON report with secrets redacted

Add new output shapes as sanitized fixtures when they change parser behavior.

### Consolidated v0.1.0-alpha.10 Validation

On 2026-07-22, one clean pgdrill `v0.1.0-alpha.10` commit
`a7b92a2aecaf82217bb2bfd9ffe9f52da055954c` was exercised through all four
native-provider drills and a disposable CNPG drill.

WAL-G 3.0.8, Barman 3.19.1, pgBackRest 2.58.0, and pg_probackup 2.5.16 all
used the same deterministic Linux arm64 release archive, whose SHA-256 was
`fe7f2f2f4e4b7aa614822b9477f24b3b096710d68b616b26d10b6bbc34d68791`.
Each restored PostgreSQL 18.3 through post-backup WAL, passed its provider
checks, post-restore probes, required policy verdicts, and owned cleanup.

The corresponding deterministic macOS arm64 artifact then drove an isolated
KinD 0.31.0 / Kubernetes 1.32.11 environment with CloudNativePG 1.26.3,
PostgreSQL 15.17, and MinIO. The CNPG drill recovered a post-backup WAL
sentinel, passed four in-pod probes and all required policy verdicts, retained
the generated manifest, and removed the owned Cluster and PVC.

The reports, runtime inventories, source/WAL boundaries, limitations, and
local checksums are retained as separate exact field entries under
[`compatibility/evidence`](../compatibility/evidence). This closes the
single-commit consolidation gate for alpha.10.

### v0.2.0-rc.1 Release Gate

On 2026-07-27, the exact clean `v0.2.0-rc.1` commit
`e9cb257c8312020166b5dff9c91f9bd9cde4ca25` passed the aggregate release gate,
all four native-provider drills, and the disposable CNPG drill before the
annotated tag was published. Branch CI, tag verification, deterministic
release construction, and GitHub Release publication then passed.

All four published archives matched the downloaded release checksum file. The
published Linux arm64 archive independently completed the local WAL-G
rehearsal with 11 passed checks, five passed policy verdicts, post-backup WAL
replay, and owned cleanup. This verifies the release and one controlled local
execution point. It does not create a new storage, platform, PITR, hosted-cloud,
or customer compatibility claim; the narrower committed matrix entries below
remain the compatibility source of truth.

### WAL-G Field Validation

On 2026-07-21, pgdrill `v0.1.0-dev` at commit
`8d69347e688efe33d53371c0d94953a89fd20495` completed one native Linux arm64
drill with WAL-G 3.0.8 and PostgreSQL 18.3. A real `backup-push` captured 100
rows; a 101st sentinel row was committed after the base backup and archived in
the next WAL segment. The drill passed catalog discovery, `wal-verify
integrity`, `backup-fetch`, latest recovery, readiness, a SQL assertion that
required the post-backup sentinel, schema-only `pg_dump`, all five policy
verdicts, and ownership-scoped cleanup.

The exact secret-free config, validated report, checksums, image digest, and
limitations are retained under
[`compatibility/evidence/wal-g-v3.0.8-postgresql-18.3-linux-arm64`](../compatibility/evidence/wal-g-v3.0.8-postgresql-18.3-linux-arm64/README.md).
This is one local file-repository observation. It does not establish remote
object-storage, PITR, incremental/delta backup, cross-version, or production
RTO compatibility.

### Barman Field Validation

On 2026-07-21, pgdrill `v0.1.0-dev` at commit
`a9c6d4cdf7a7452e5e4021babd172e42320074f6` completed one native Linux arm64
drill with Barman 3.19.1 and PostgreSQL 18.3. The same-host Barman server made a
real local-rsync full backup, archived a later sentinel WAL segment, passed
`check`, `check-backup`, `show-backup`, manifest generation, and
`verify-backup`, restored with `--get-wal`, and passed readiness, sentinel SQL,
schema-only `pg_dump`, all five policy verdicts, and ownership-scoped cleanup.

This drill exposed and fixed a real Barman 3.19.1 catalog shape: JSON
`list-backups` uses a human display time plus an exact epoch
`end_time_timestamp`. The exact output is now a fixture, and policy-relevant
normalization uses the unambiguous epoch value. The full report and topology
inputs are retained under
[`compatibility/evidence/barman-v3.19.1-postgresql-18.3-linux-arm64`](../compatibility/evidence/barman-v3.19.1-postgresql-18.3-linux-arm64/README.md).
The observation does not cover remote SSH, streaming backup/archive, cloud
storage, incremental backup, or PITR modes.

### pgBackRest Field Validation

On 2026-07-21, pgdrill `v0.1.0-dev` at commit
`bd5fbb48ab28426ca67c7368b75f67cee72042f9` completed one native Linux arm64
drill with pgBackRest 2.58.0 and PostgreSQL 18.3. A real full backup captured
101 rows; a 102nd sentinel row was committed only afterward and recovered
through archived WAL. Native `check` and `verify --set` passed before restore,
followed by owned-postmaster readiness, the sentinel SQL assertion, data-only
`pg_dump`, all five policy verdicts, and ownership-scoped cleanup.

This drill also exposed and fixed a local-target timing defect: the configured
startup timeout had been treated as a fixed delay. The exact commit used for
the retained report instead returned when the owned `postmaster.pid` reached
`ready`, in 275 ms, and reserves the configured duration as a fail-closed
deadline. The full report and topology inputs are retained under
[`compatibility/evidence/pgbackrest-v2.58.0-postgresql-18.3-linux-arm64`](../compatibility/evidence/pgbackrest-v2.58.0-postgresql-18.3-linux-arm64/README.md).
The observation does not cover remote or object storage, encryption,
differential or incremental backups, PITR modes, or other platforms.

### pg_probackup Field Validation

On 2026-07-21, pgdrill `v0.1.0-dev` at commit
`bac67571ead058f70d653405529ca01e52a6f480` completed one native Linux arm64
drill with pg_probackup 2.5.16 and PostgreSQL 18.3. Both were built from exact
source snapshots with the pg_probackup PG18 core patch. A compressed full
STREAM backup captured 100 rows; a 101st sentinel row was committed only
afterward and recovered from continuous WAL archiving. Native selected-backup
and WAL validation passed before restore, followed by owned-postmaster
readiness, the sentinel SQL assertion, data-only `pg_dump`, all five policy
verdicts, and ownership-scoped cleanup.

The first harness attempt correctly failed its sentinel probe because WAL had
been switched before that transaction committed. After the commit-containing
segment was archived separately, a fresh attempt passed. The exact source
build, report, and inputs are retained under
[`compatibility/evidence/pg-probackup-v2.5.16-postgresql-18.3-linux-arm64`](../compatibility/evidence/pg-probackup-v2.5.16-postgresql-18.3-linux-arm64/README.md).
The observation does not cover remote SSH, incremental backup, other PITR
targets, other versions or platforms, or a non-superuser backup role.

`pgdrill doctor` proves that the config is structurally valid for its target,
that each required executable starts, and that its bounded version command
succeeds. It deliberately does not access repositories, database servers, or
the Kubernetes API and therefore does not replace catalog discovery, provider
checks, or a restore drill.

Timestamp PITR configuration is provider-neutral and must use RFC3339 with an
explicit timezone. The selector requires a known backup finish time earlier
than the target, following PostgreSQL's rule that a recovery stop point must be
after the end of the base backup. This filter does not establish WAL archive
continuity; retain the provider check and completed restore evidence.

## Restore Targets

The local target is covered by process, filesystem-boundary, cleanup, and probe
tests using controlled executables. The four native-provider field points
above additionally exercise real PostgreSQL startup and native repositories;
other version, storage, and recovery-target combinations remain external
gates.

The CNPG target has manifest, discovery, lifecycle, failure, evidence, and CLI
tests behind a `kubectl` compatibility client.

The current CNPG adapter implements only plain `latest` recovery. Other
recovery-target types and timeline/inclusive options fail before resource
creation. They are not compatibility claims until the manifest mapping and a
live PITR drill prove them.

### CNPG Field Validation

On 2026-07-20, the exact public `v0.1.0-alpha.9` Linux amd64 archive completed
one end-to-end drill in a disposable CNPG 1.26.0 environment running PostgreSQL
15.13. The drill selected the latest completed CNPG `Backup`, restored it
through the operator's `barmanObjectStore` recovery path, waited for the
temporary cluster to become Ready, version-checked `pg_isready` and `psql`
inside the restored pod, passed readiness and `select 1` probes over the local
Unix socket, captured evidence, and removed the owned Cluster and PVC. The
end-to-end report window was approximately 56 minutes and 39 seconds; this is
an observation from that environment, not an RTO guarantee.

The release archive checksum matched its published checksum manifest before
execution. Earlier controlled `v0.1.0-alpha.6` runs separately exercised
signal cancellation and cleanup and exposed the unauthenticated service-probe
gap that the in-pod local-socket transport replaced.

On 2026-07-22, `v0.1.0-alpha.10` at commit
`a7b92a2aecaf82217bb2bfd9ffe9f52da055954c` added a second, faster disposable
field point: CNPG 1.26.3 and PostgreSQL 15.17 on KinD/Kubernetes 1.32.11 Linux
arm64 with a MinIO `barmanObjectStore`. It recovered a transaction committed
after the base backup and passed readiness, SQL, `pg_amcheck`, schema-only
`pg_dump`, policy, immutable-manifest, and owned-cleanup checks. The exact
report and runtime inventory are retained under
[`compatibility/evidence/cnpg-v1.26.3-postgresql-15.17-kind-arm64-pgdrill-v0.1.0-alpha.10`](../compatibility/evidence/cnpg-v1.26.3-postgresql-15.17-kind-arm64-pgdrill-v0.1.0-alpha.10/README.md).

These are exact validation points, not a production support matrix. Timestamp
PITR, additional PostgreSQL majors, other CNPG/operator versions, storage
classes, object stores, and failure modes still require field drills.
Exercising CNPG's
`barmanObjectStore` bootstrap does not validate pgdrill's native Barman CLI
adapter against a real Barman repository.

## PostgreSQL Versions

`pgdrill` does not currently publish a blanket PostgreSQL major-version support
range. Local drills execute the configured PostgreSQL binaries. CNPG drills
reuse the source cluster image, including fallback discovery from its
`postgres` container, to avoid silently changing the PostgreSQL major version.
