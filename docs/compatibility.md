# Compatibility And Validation

`pgdrill` is pre-alpha. This document separates build portability, automated
test coverage, and real-environment validation so a green unit test is not
mistaken for a production support claim.

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
tests parse it and cross-check the provider, recovery target, observation date,
tool versions, pgdrill version, and full commit. Release packaging validates
and includes the matrix and this document.

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
tests using controlled executables. The WAL-G and Barman field points above
additionally exercise real PostgreSQL startup and native repositories; other
provider, version, storage, and recovery-target combinations remain external
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

This is one validation point, not a production support matrix. Timestamp PITR,
additional PostgreSQL majors, other CNPG/operator versions, storage classes,
and failure modes still require field drills. Exercising CNPG's
`barmanObjectStore` bootstrap does not validate pgdrill's native Barman CLI
adapter against a real Barman repository.

## PostgreSQL Versions

`pgdrill` does not currently publish a blanket PostgreSQL major-version support
range. Local drills execute the configured PostgreSQL binaries. CNPG drills
reuse the source cluster image, including fallback discovery from its
`postgres` container, to avoid silently changing the PostgreSQL major version.
