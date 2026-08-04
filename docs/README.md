# Documentation

This index separates pgdrill's user, operator, contributor, and protocol
documentation. Start with the shortest path that matches the work you need to
perform; protocol documents are not prerequisites for a first drill.

## Users

| Topic | Document |
| --- | --- |
| Install pgdrill and execute a first local restore drill | [Getting started](getting-started.md) |
| Understand configuration semantics and defaults | [Configuration](configuration.md) |
| Validate required executables before repository access | [Preflight](preflight.md) |
| Define readiness, SQL, amcheck, and dump checks | [Probes](probes.md) |
| Define RTO, RPO, backup-age, recovery-target, and cleanup assertions | [Recovery policy](recovery-policy.md) |
| Inspect terminal reports and Prometheus output | [Report format](report-format.md) |
| Select local or CloudNativePG target workflows | [Restore targets](restore-targets.md) |

## Operators

| Topic | Document |
| --- | --- |
| Deploy, schedule, retain evidence, and handle failures | [Operator guide](operator-guide.md) |
| Recover cleanup after an interrupted local attempt | [Attempt recovery](attempt-recovery.md) |
| Store and verify immutable local run history | [History format](history-format.md) |
| Verify and garbage-collect content-addressed artifacts | [Artifact format](artifact-format.md) |
| Upgrade, roll back, and migrate pre-GA local state | [Upgrade guide](upgrade.md) |
| Run pgdrill from the published OCI image | [Container image](container-image.md) |

## Providers And Compatibility

| Topic | Document |
| --- | --- |
| Provider adapter behavior and native commands | [Adapters](adapters.md) |
| Demonstrated versions and evidence levels | [Compatibility](compatibility.md) |
| Machine-readable exact-version matrix | [Compatibility matrix](../compatibility/matrix.yaml) |
| Reviewed retained observations | [Evidence index](../compatibility/README.md) |

Compatibility entries are intentionally narrow. A passing cell proves the
recorded provider, PostgreSQL version, platform, storage profile, recovery
target, and pgdrill revision; it does not imply an untested version range.

## Automation Contracts

| Contract | Document |
| --- | --- |
| Immutable normalized drill input | [Drill spec](drill-spec-format.md) |
| Ordered lifecycle events | [Run events](run-event-format.md) |
| Durable mutation intent and terminal state | [Operation checkpoints](operation-checkpoint-format.md) |
| Fleet input and deterministic placement output | [Fleet plan](fleet-plan-format.md) |

## Architecture And Project Direction

| Topic | Document |
| --- | --- |
| Package boundaries and engine lifecycle | [Architecture](architecture.md) |
| Engine/control-plane ownership decision | [ADR 0001](adr/0001-engine-v0.2-and-control-plane-boundary.md) |
| Stable schema and copy-migration decision | [ADR 0002](adr/0002-stable-schema-and-copy-migration.md) |
| Current implementation roadmap | [Roadmap](roadmap.md) |
| Distributed control-plane sequence | [Control-plane roadmap](control-plane-roadmap.md) |
| Required evidence before `v1.0.0` | [v1.0 release contract](v1.0-release-contract.md) |
| Release construction and publication | [Release guide](release.md) |

## Demonstrations

- [Technical demo contract](../demo/README.md)
- [Local exact-artifact rehearsal](../demo/local/README.md)
- [Yandex Cloud operator guide](../demo/yandex-cloud/README.md)
- [Yandex Cloud participant runbook](../demo/yandex-cloud/USER-RUNBOOK.md)
- [Customer discovery checklist](../demo/customer-discovery.md)

The demo material uses synthetic data and disposable targets. It must not be
presented as customer compatibility, production support, or a production RTO
measurement.

## Contributors

Start with [CONTRIBUTING.md](../CONTRIBUTING.md). User-visible behavior changes
also require an entry in [CHANGELOG.md](../CHANGELOG.md). Security reports
follow [SECURITY.md](../SECURITY.md).
