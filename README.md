# pgdrill

`pgdrill` is an open-source recovery readiness engine for PostgreSQL.

It does not try to replace WAL-G, Barman, pgBackRest, pg_probackup, or
PostgreSQL core verification tools. It orchestrates them to answer a more
operational question:

> Can this PostgreSQL cluster be restored right now, to the target we claim to
> support, within the recovery expectations we publish?

## Status

Engine v0.2 has a
[`v0.2.0-rc.2`](https://github.com/r314tive/pgdrill/releases/tag/v0.2.0-rc.2)
release candidate. It is suitable for controlled technical evaluation but not
a blanket production-support claim.

The CLI implements:

- strict configuration, read-only dependency preflight, and stable exit codes
- WAL-G, Barman, pgBackRest, and pg_probackup discovery, validation, and local
  restore planning
- owned local PostgreSQL restore/start/probe/cleanup drills
- guarded CloudNativePG backup discovery and disposable verify-cluster drills
- bounded redacted command evidence, immutable run specs, operation
  checkpoints, artifact references, and versioned JSON reports
- fail-closed recovery-policy verdicts for RTO, RPO, backup age, recovery
  target, and cleanup
- daemon-free typed fleet validation and deterministic bounded placement
- optional private local history for immutable specs, ordered events, terminal
  reports, policy verdicts, and artifact references
- full local artifact hashing plus age-gated, history-reference-aware,
  digest-confirmed garbage collection
- text report inspection and Prometheus export

Native and CNPG paths share one lifecycle, cancellation, reconciliation, and
reporting contract. Shared conformance suites cover every adapter and
executable target. Reproducible integration harnesses exercise all four native
providers plus a disposable KinD/CNPG environment through real base backups,
post-backup WAL replay, probes, policy, and cleanup.

The compatibility matrix records narrow fixture, controlled, and exact-version
field evidence. One clean `v0.1.0-alpha.10` commit has passed WAL-G 3.0.8,
Barman 3.19.1, pgBackRest 2.58.0, and pg_probackup 2.5.16 restores with
PostgreSQL 18.3 on Linux arm64, plus CNPG 1.26.3 / PostgreSQL 15.17 in a
disposable KinD environment. Other versions, storage backends, platforms, and
PITR modes remain unclaimed until separately exercised.

The exact `v0.2.0-rc.2` commit
`97ad852ecb2c9493c1c4a1e7718f61bf496efa17` passed the clean aggregate
release-candidate gate across all four native providers and disposable CNPG
before publication. Published checksums were then verified independently. The
published Linux arm64 archive passed local WAL-G latest recovery and timestamp
PITR, proving both sides of an archived transaction boundary. These are
release and controlled-demo gates, not broader compatibility claims.

The typed planner and local history are implemented on the current `main`
branch after `v0.2.0-rc.2`; they are not part of that published archive yet.
Fleet scheduling, leases, remote executors, a controller/agent protocol, TUI,
and web UI remain roadmap work. They will consume the engine contracts rather
than become a second orchestration implementation.

The intended stable-product boundary and its evidence requirements are defined
in the [v1.0 release contract](docs/v1.0-release-contract.md). `v1.0.0` is
CLI-first and does not wait for a web UI or hosted SaaS, but it does require
stable schemas, proven latest/PITR support cells, daemon-free planning and local
history, signed distribution, and external pilot evidence.

## Goals

- Verify backup catalogs and WAL continuity through provider-specific adapters.
- Run real restore drills into disposable targets.
- Start restored PostgreSQL instances and run structured validation probes.
- Produce durable evidence for audits, incidents, and SLO checks.
- Export machine-readable reports and metrics.
- Stay compatible with existing open-source PostgreSQL backup stacks.

## Initial Providers

Initial adapters are implemented for:

- WAL-G
- Barman
- pgBackRest
- pg_probackup

Additional providers can be added behind the same internal provider contract.

## Core Concepts

- **Provider**: a backup system such as WAL-G, Barman, pgBackRest, or
  pg_probackup.
- **Restore target**: a disposable place to restore into, such as a local
  directory, container, VM, or Kubernetes volume.
- **Recovery target**: latest available WAL, a timestamp, an LSN, an XID, or a
  named restore point.
- **Probe**: a post-restore check such as `pg_isready`, `pg_amcheck`, schema
  dump, row-count sampling, or custom SQL.
- **Evidence**: immutable facts collected during a drill: versions, commands,
  timings, logs, checks, and final status.
- **Failure stage**: a stable lifecycle stage and human-readable reason for a
  failed or aborted drill, linked to the evidence collected before failure.
- **Run event**: an optional ordered stage transition identified by logical run
  and execution attempt. Direct execution remains journal-free by default;
  `-history-dir` enables the local durable journal.
- **Operation checkpoint**: a durable intent and terminal mutation state bound
  to one attempt. It lets a replacement executor reconcile owned resources
  without assuming that a failed command had no effect.
- **Artifact reference**: a digest, immutable URI, exact size, media type,
  retention class, and redaction classification linked from bounded evidence.
- **Recovery policy**: immutable duration and outcome assertions evaluated from
  typed drill facts; insufficient evidence is `unknown`, not a pass.

The implemented full-drill target is `local`. Kubernetes is available through
the guarded CloudNativePG `target manifest` and `target verify` paths;
`container` remains a canonical, planned target type rather than an executable
path. `pgdrill explain -format json` exposes this distinction explicitly.

## Non-Goals

- Becoming another PostgreSQL backup tool.
- Hiding provider-specific behavior behind vague success messages.
- Claiming that a restored database is semantically correct without explicit
  probes that prove the required invariants.

## Installation

The prerelease pipeline targets Linux and macOS on amd64 and arm64.
Published archives and SHA256 checksums are available under
[GitHub Releases](https://github.com/r314tive/pgdrill/releases). Building from
source remains supported.

To build from source, install the Go version from `.go-version` and run:

```sh
make build
./bin/pgdrill version
```

`pgdrill` orchestrates external PostgreSQL tools. For local drills, the selected
provider, target, and probe binaries must be installed in the execution
environment. CNPG probe binaries run inside the restored `postgres` container;
the pgdrill runner needs `kubectl`, not a duplicate PostgreSQL client toolchain.
See [docs/compatibility.md](docs/compatibility.md) for the current validation
boundary and [compatibility/matrix.yaml](compatibility/matrix.yaml) for the
versioned machine-readable evidence matrix. Release archives include both.

Validate the config and capture the required client versions without touching a
backup repository, PostgreSQL server, or Kubernetes API:

```sh
pgdrill doctor -f pgdrill.yaml
pgdrill doctor -f pgdrill.yaml -format json
```

The exact scope and JSON contract are documented in
[docs/preflight.md](docs/preflight.md).

Configuration is strict and all external operations have bounded deadline
defaults. Known fields are also validated against provider and probe semantics
before external commands start. The provider/catalog deadline is separate from
the physical restore deadline; see
[docs/configuration.md](docs/configuration.md).
Recovery policy is independent from command timeouts and is documented in
[docs/recovery-policy.md](docs/recovery-policy.md).

## Development

```sh
make check
```

Release-affecting changes should also pass:

```sh
make -s release-check VERSION=v0.3.0-dev
```

Run any real local provider path independently, or all native integration gates
in sequence:

```sh
make test-integration-walg
make test-integration-barman
make test-integration-pgbackrest
make test-integration-pgprobackup
make test-integration-native
make test-integration-cnpg
```

`make test-local` combines the normal checks, race detector, CLI smoke, and all
network-isolated disposable native drills. Their artifacts remain under ignored
`.cache`; they are not compatibility evidence by themselves. See
[test/integration](test/integration/README.md) for the evidence boundary.

For a clean release-candidate commit with Docker available, run the complete
artifact, lint, native-provider, and disposable CNPG gate:

```sh
make -s release-candidate-check VERSION=v0.3.0-alpha.1
```

```sh
go run ./cmd/pgdrill version
go run ./cmd/pgdrill sample-config
go run ./cmd/pgdrill explain
go run ./cmd/pgdrill doctor -f examples/pgdrill.yaml
go run ./cmd/pgdrill catalog list -f examples/pgdrill.yaml
go run ./cmd/pgdrill run -f examples/pgdrill.yaml
go run ./cmd/pgdrill target manifest -f path/to/cnpg-manifest-config.yaml
go run ./cmd/pgdrill target manifest -f path/to/cnpg-manifest-config.yaml -discover
go run ./cmd/pgdrill target verify -f path/to/cnpg-verify-config.yaml -discover -confirm-create
go run ./cmd/pgdrill plan validate -f examples/fleet.yaml
go run ./cmd/pgdrill plan show -f examples/fleet.yaml
go run ./cmd/pgdrill history list -store path/to/history
go run ./cmd/pgdrill history show -store path/to/history run-id
go run ./cmd/pgdrill history verify -store path/to/history
go run ./cmd/pgdrill history prune -store path/to/history \
  -before 2026-08-01T00:00:00Z -keep-latest 2
go run ./cmd/pgdrill artifact verify \
  -store path/to/report.json.artifacts -history-store path/to/history
go run ./cmd/pgdrill artifact gc \
  -store path/to/report.json.artifacts -history-store path/to/history \
  -before 2026-08-01T00:00:00Z
go run ./cmd/pgdrill report show path/to/report.json
go run ./cmd/pgdrill report metrics path/to/report.json
```

Automation may provide stable correlation identities with the `-run-id` or
`-drill-id` flag and the `-attempt-id` flag. Reusing an attempt that already has
mutation checkpoints is rejected until its orphaned state has been reconciled;
it is not permission to replay commands.

Local history is opt-in for execution, so cron and CI jobs do not acquire a
new availability dependency:

```sh
pgdrill run -f pgdrill.yaml \
  -run-id nightly-main \
  -attempt-id nightly-main-1 \
  -history-dir /var/lib/pgdrill/history
pgdrill history list -store /var/lib/pgdrill/history
pgdrill history show -store /var/lib/pgdrill/history nightly-main
pgdrill history verify -store /var/lib/pgdrill/history
```

Local content-addressed artifacts have a separate lifecycle. `artifact verify`
hashes every blob and resolves references while holding the complete history
scope under a shared lock. `artifact gc` is dry-run by default, requires a
strict age cutoff, protects live, audit-classified, and legacy blobs, and
applies only an exact confirmed plan digest. See
[docs/artifact-format.md](docs/artifact-format.md).

The planner never resolves credentials or creates targets. Its strict
inventory and output contracts are documented in
[docs/fleet-plan-format.md](docs/fleet-plan-format.md); the on-disk journal,
crash boundaries, and upgrade policy are documented in
[docs/history-format.md](docs/history-format.md).
Binary and local-state upgrade, rollback, and retention preparation are
documented in [docs/upgrade.md](docs/upgrade.md).

Long-running commands handle `SIGINT` and `SIGTERM`. The active provider,
target, or probe command is canceled first; pgdrill then uses a bounded
finalization context for owned-target cleanup and atomic report persistence.
Interrupted drills are reported as `aborted` and return exit code `130`.

`pgdrill run` and `pgdrill target verify` execute target-aware native-tool
preflight automatically. Local dependencies fail before repository access or
target mutation. CNPG validates local `kubectl` first, then checks probe clients
inside the restored pod after it becomes Ready; both phases remain in the JSON
drill report.

CLI exit codes are stable automation inputs:

- `0`: command or drill completed successfully
- `1`: operational or verification failure
- `2`: invalid CLI usage
- `130`: operation interrupted or its context canceled

See [docs/roadmap.md](docs/roadmap.md) for the current implementation sequence
and CLI/UI direction. Probe configuration is documented in
[docs/probes.md](docs/probes.md).
CNPG target verification examples are available in
[examples/cnpg-target-verify.yaml](examples/cnpg-target-verify.yaml) and
[examples/kubernetes/cnpg-target-verify-cronjob.yaml](examples/kubernetes/cnpg-target-verify-cronjob.yaml).
A local pg_probackup drill example is available in
[examples/pgprobackup.yaml](examples/pgprobackup.yaml).
The evidence-led technical demo contract is documented in
[demo/README.md](demo/README.md), with a published-artifact local rehearsal
under [demo/local](demo/local/README.md) and a reproducible, access-scoped
Yandex Cloud WAL-G baseline under
[demo/yandex-cloud](demo/yandex-cloud/README.md).

Release discipline is described in [docs/release.md](docs/release.md), and
the versioned JSON report contract is documented in
[docs/report-format.md](docs/report-format.md). The optional lifecycle stream is
documented in [docs/run-event-format.md](docs/run-event-format.md), and the
internal immutable run input is documented in
[docs/drill-spec-format.md](docs/drill-spec-format.md). The
daemon-free fleet and plan contracts are documented in
[docs/fleet-plan-format.md](docs/fleet-plan-format.md), and local persistence
is documented in [docs/history-format.md](docs/history-format.md). The local
artifact verification and GC contract is documented in
[docs/artifact-format.md](docs/artifact-format.md). The current pre-GA upgrade
and rollback boundary is documented in
[docs/upgrade.md](docs/upgrade.md). The
engine/control-plane boundary is recorded in
[ADR 0001](docs/adr/0001-engine-v0.2-and-control-plane-boundary.md).
The typed topology and CLI/TUI/web sequence are expanded in
[docs/control-plane-roadmap.md](docs/control-plane-roadmap.md).
The GA boundary is tracked separately in
[docs/v1.0-release-contract.md](docs/v1.0-release-contract.md).
User-visible changes are tracked in [CHANGELOG.md](CHANGELOG.md). Contribution
and security reporting guidance is available in
[CONTRIBUTING.md](CONTRIBUTING.md) and [SECURITY.md](SECURITY.md).

## License

Apache License 2.0.
