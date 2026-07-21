# Compatibility And Validation

`pgdrill` is pre-alpha. This document separates build portability, automated
test coverage, and real-environment validation so a green unit test is not
mistaken for a production support claim.

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
provider checks have command-construction and evidence tests. These tests prove
normalization against the committed fixtures; they do not prove compatibility
with every historical or future native tool version.

Before claiming a native version as validated:

1. capture `pgdrill version` and `pgdrill doctor -f <config> -format json`
2. run catalog discovery against a disposable or read-only repository
3. run the provider check profile used in production
4. complete a real restore and the required probes
5. retain the JSON report with secrets redacted

Add new output shapes as sanitized fixtures when they change parser behavior.

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
tests using controlled executables. Real PostgreSQL startup and provider
repositories still require environment-specific validation.

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
