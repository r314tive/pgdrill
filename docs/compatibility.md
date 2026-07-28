# Compatibility And Validation

`pgdrill` has published its first Engine v0.2 release candidate. This document
separates build portability, automated test coverage, and real-environment
validation so a green unit test is not mistaken for a production support
claim.

## Machine-Readable Evidence

The source of truth is
[`compatibility/matrix.yaml`](../compatibility/matrix.yaml), using
`pgdrill.compatibility-matrix/v1`. The reader also accepts the pre-GA
`v1alpha1` generation. The matrix distinguishes:

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
packaging validates and includes the matrix and this document. A field entry
that claims `timestamp_pitr` must additionally contain a passed SQL boundary
probe with evidence, a recovery proof timestamp, and passed required verdicts
for all five recovery-policy assertions. A field entry may also reference a
typed runtime inventory. Cross-architecture claims require it and are checked
against the container architecture, Linux build target, exact candidate
archive, checksum, version, and full commit.

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

### v0.2.0-rc.2 Release And PITR Gate

On 2026-07-27, the exact clean `v0.2.0-rc.2` commit
`97ad852ecb2c9493c1c4a1e7718f61bf496efa17` passed the aggregate release gate,
all four native-provider drills, and the disposable CNPG drill. Its WAL-G path
completed both latest recovery and an inclusive timestamp PITR boundary. Branch
CI, annotated-tag verification, deterministic release construction, and GitHub
Release publication passed.

All four independently downloaded archives matched the published checksum
file. The published Linux arm64 archive then repeated the WAL-G drill without a
Go toolchain in the execution path. Timestamp recovery retained row 101,
committed after the base backup but before the target, and excluded archived row
102 committed after the target. The exact report and runtime inventory are
retained in the compatibility matrix.

This adds one WAL-G 3.0.8 / PostgreSQL 18.3 / Linux arm64 timestamp field point.
It does not imply timestamp support for other providers, versions, platforms,
backup modes, or storage backends.

### v0.3.0-alpha.8 Native Provider PITR Gate

On 2026-07-28, the exact clean commit
`1d742c67e1d3968449c13553348fff4b5ccb9b91` completed separate latest and
inclusive timestamp drills with Barman 3.19.1, pgBackRest 2.58.0, and
pg_probackup 2.5.16 against PostgreSQL 18.3 on Linux arm64. All three used the
same deterministic `v0.3.0-alpha.8` candidate archive, whose SHA-256 was
`fdae0478288c7f953c501fd18948ef83eb09a802adfbc3749f14cb0e70630515`.

Each run created a real full backup, recovered row 101 committed after that
backup but before the requested target, and excluded archived row 102 committed
after the target. WAL replay used the provider-native retrieval path:
`barman get-wal`, `pgbackrest archive-get`, or `pg_probackup archive-get`.
Catalog and backup validation, PostgreSQL startup, the exact SQL boundary,
`pg_amcheck`, schema-only `pg_dump`, all required policy verdicts, durable
history verification, and owned cleanup passed.

The reports, exact rendered configurations, runtime identities, source/WAL
boundaries, and checksums are retained as separate field entries under
[`compatibility/evidence`](../compatibility/evidence). These observations cover
one local repository topology, one full backup, one timestamp on timeline 1,
and one exact version/platform point per provider. The candidate archive was
not published or signed, and the results do not establish remote storage,
other backup modes or recovery targets, cross-version behavior, performance,
production RTO, or a support range.

### v0.3.0-alpha.10 Linux amd64 Functional Gate

On 2026-07-28, the exact clean commit
`2f6b72ac8e94911f1c6b70ec1ecdcd50ca8e35ae` completed latest and inclusive
timestamp drills with WAL-G 3.0.8, Barman 3.19.1, pgBackRest 2.58.0, and
pg_probackup 2.5.16 against PostgreSQL 18.3 on Linux amd64. All eight restore
points used the same deterministic `v0.3.0-alpha.10` candidate archive, whose
SHA-256 was
`5ef7ca808c26eacba7afc079a3ac4af16159c75f0296dac15e2a2e43756a0a38`.

Each provider recovered row 101 committed and archived after the full backup.
Each timestamp drill excluded archived row 102 committed after the requested
target. Provider-native catalog, backup, repository, manifest, or WAL checks
as applicable, PostgreSQL startup, SQL boundary assertions, `pg_amcheck`,
schema-only `pg_dump`, all five required policy verdicts, and owned cleanup
passed.

The Linux amd64 containers ran through Docker emulation on an arm64 daemon.
The retained typed runtime inventories bind that execution architecture to the
candidate version, full commit, archive name, and checksums. These are
functional Linux amd64 observations, not native-hardware performance or RTO
evidence. The repositories were local, the candidate was not published or
signed, and broader storage, version, backup-mode, and recovery-target claims
remain external gates.

### v0.3.0-alpha.12 WAL-G S3-Compatible Gate

On 2026-07-28, the exact clean commit
`9ea9a3b68ee12a457b1cb2195e9b268a7ea9203c` completed WAL-G 3.0.8 latest and
inclusive timestamp drills against PostgreSQL 18.3 and single-node MinIO on
Linux arm64. Both restore points used the deterministic
`v0.3.0-alpha.12` archive with SHA-256
`58eebabb2b447be6f672cb915d3e3f45e789cea79e863faeb2707363ce561107`.

The run retained 11 real base-backup and WAL objects totaling 7,097,193 bytes.
Latest recovery replayed the transaction committed after the full backup;
timestamp recovery retained that transaction and excluded a later archived
transaction. Provider validation, PostgreSQL startup, SQL assertions,
`pg_amcheck`, schema-only `pg_dump`, all required policy verdicts, owned target
cleanup, credential-leak rejection, and private Docker network cleanup passed.

The exact reports, secret-free configurations, runtime and object inventories,
source boundary, limitations, and checksums are retained under
[`compatibility/evidence/wal-g-v3.0.8-postgresql-18.3-s3-minio-linux-arm64-pgdrill-v0.3.0-alpha.12`](../compatibility/evidence/wal-g-v3.0.8-postgresql-18.3-s3-minio-linux-arm64-pgdrill-v0.3.0-alpha.12/README.md).
This closes one S3-compatible object-storage observation, not a support claim
for Amazon S3, Yandex Object Storage, other implementations, TLS/IAM modes,
Linux amd64, or production RTO. The candidate was not published or signed.

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
tests using controlled executables. The retained native-provider field points
above additionally exercise real PostgreSQL startup and native repositories;
other version, storage, and recovery-target combinations remain external
gates.

The CNPG target has manifest, discovery, lifecycle, failure, evidence, and CLI
tests behind a `kubectl` compatibility client. The typed adapter supports both
CNPG `Backup`-resource recovery and the Barman Cloud Plugin recovery manifest,
including exact `status.backupId` selection and source-plugin discovery.

The current CNPG adapter implements only plain `latest` recovery. Other
recovery-target types and timeline/inclusive options fail before resource
creation. They are not compatibility claims until the manifest mapping and a
live PITR drill prove them.

The Barman Cloud Plugin path has fixture/unit coverage and a checksum- and
digest-pinned live developer gate:

```sh
make test-integration-cnpg-plugin
```

The gate provisions CNPG 1.29.2, Barman Cloud Plugin 0.13.0, cert-manager
1.21.0, PostgreSQL 15.17, and MinIO in an isolated KinD cluster. It requires a
real plugin backup, exact backup-ID recovery, post-backup WAL replay, probes,
evidence/history verification, and cleanup. A passing developer run is not
automatically included in the field matrix.

On 2026-07-28, the exact clean `v0.3.0-alpha.6` candidate at commit
`60ff0349c3e38cf1934686c2cadcbb0dffe387e7` passed that gate on Linux arm64 and
was deliberately promoted as one narrow observation. Its reviewed report,
runtime inventory, source/WAL boundary, immutable recovery manifest, and
checksums are retained under
[`compatibility/evidence/cnpg-v1.29.2-barman-plugin-v0.13.0-postgresql-15.17-kind-arm64-pgdrill-v0.3.0-alpha.6`](../compatibility/evidence/cnpg-v1.29.2-barman-plugin-v0.13.0-postgresql-15.17-kind-arm64-pgdrill-v0.3.0-alpha.6/README.md).
The candidate was not a published or signed release asset, and this cell is
not an adjacent-version, production-Kubernetes, PITR, or RTO claim.

The manifest follows the upstream
[Barman Cloud Plugin recovery contract](https://cloudnative-pg.io/plugin-barman-cloud/docs/usage/#restoring-a-cluster);
the current native-removal schedule is recorded in the [CNPG 1.29.2 release
notes](https://cloudnative-pg.io/docs/1.29/release_notes/v1.29/#version-1292).

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

The native-provider integration harness has fail-closed profiles for exact
PostgreSQL 18.3 and 17.10 patch releases. Each profile pins platform manifests,
checks server and client versions before repository mutation, and writes to an
isolated cache. A passing harness run is development evidence only. It becomes
a compatibility point only after a clean candidate run is reviewed, retained,
and referenced by a `field` entry in the machine-readable matrix.
