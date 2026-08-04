# pgdrill

[![CI](https://github.com/r314tive/pgdrill/actions/workflows/ci.yml/badge.svg?branch=main)](https://github.com/r314tive/pgdrill/actions/workflows/ci.yml)
[![License](https://img.shields.io/badge/license-Apache--2.0-blue.svg)](LICENSE)

`pgdrill` is an open-source PostgreSQL recovery readiness engine.

It does not create backups and does not replace WAL-G, Barman, pgBackRest,
pg_probackup, or PostgreSQL verification tools. It orchestrates those tools to
answer a narrower operational question:

> Can a selected PostgreSQL backup be restored to the requested recovery point,
> pass explicit checks, and be cleaned up safely?

## Status

The current published release candidate is
[`v0.2.0-rc.2`](https://github.com/r314tive/pgdrill/releases/tag/v0.2.0-rc.2).
It is intended for controlled technical evaluation, not as a blanket
production-support or compatibility claim.

`main` contains additional pre-GA work that is not part of that archive. The
exact versions and environments exercised by the project are recorded in the
[compatibility matrix](compatibility/matrix.yaml) and retained
[field evidence](compatibility/README.md). Versions outside those cells remain
unclaimed until they are tested explicitly.

The planned `v1.0.0` boundary is CLI-first. A hosted control plane, TUI, and web
UI are not required for the engine release and remain separate roadmap work.
See the [v1.0 release contract](docs/v1.0-release-contract.md).

## Recovery Drill

A native drill executes one fail-closed lifecycle:

```text
discover -> select -> validate -> restore -> start PostgreSQL
         -> probe -> evaluate policy -> stop -> prove cleanup -> report
```

The terminal JSON report binds the result to the selected backup, provider,
recovery target, tool versions, commands, timings, probes, policy verdicts,
operation checkpoints, and cleanup evidence.

Implemented provider paths:

- WAL-G
- Barman
- pgBackRest
- pg_probackup
- CloudNativePG backup resources and the Barman Cloud Plugin through the
  guarded Kubernetes verification path

The complete local restore path uses a private, disposable work directory.
CloudNativePG uses a separately owned verify cluster. Provider-specific
behavior is retained in evidence instead of being reduced to a generic
success message.

## Safety Model

- Configuration is strict; unknown fields and invalid provider combinations
  fail before execution.
- `pgdrill doctor` validates local dependencies without reading the backup
  catalog or creating a restore target.
- External commands have bounded deadlines and structured exit evidence.
- Command evidence is bounded and redacted before it enters a report.
- Recovery policy fails closed: missing proof is `unknown`, not `passed`.
- Cleanup removes only resources whose ownership belongs to the current
  attempt and whose absence can be verified.
- Interrupted-attempt recovery observes durable state and cleans owned
  resources; it never blindly replays a provider command.

The restore execution identity still needs read access to the selected backup
repository and permission to create its isolated target. Do not point
`target.work_dir` at a PostgreSQL data directory, backup repository, or shared
application path.

## Quick Start

Install a checksummed archive from
[GitHub Releases](https://github.com/r314tive/pgdrill/releases), or build with
the Go version recorded in [`.go-version`](.go-version):

```sh
make build
./bin/pgdrill version
```

Put the verified binary on `PATH` before using the commands below.

Create a private starter configuration:

```sh
install -d -m 0700 "$HOME/.config/pgdrill"
pgdrill sample-config >"$HOME/.config/pgdrill/pgdrill.yaml"
chmod 0600 "$HOME/.config/pgdrill/pgdrill.yaml"
```

The generated file is a template, not a production-ready policy. Review the
provider repository, executable paths, integrity checks, disposable target,
recovery target, probes, policy limits, and report path before continuing.
Inject credentials through the execution environment or an external secret
mechanism rather than committing them to the file.

Run the gates in order:

```sh
pgdrill doctor -f "$HOME/.config/pgdrill/pgdrill.yaml"
pgdrill catalog list -f "$HOME/.config/pgdrill/pgdrill.yaml"
pgdrill run -f "$HOME/.config/pgdrill/pgdrill.yaml"
pgdrill report show ./pgdrill-report.json
```

`doctor` is read-only preflight. `catalog list` reads the provider catalog.
`run` performs the physical restore and target lifecycle. Stop after the first
failed gate and retain its terminal output and report.

The [getting-started guide](docs/getting-started.md) covers execution-host
sizing, configuration review, first-drill acceptance, failure handling, and
automation.

## Documentation

The [documentation index](docs/README.md) is the stable entry point.

| Need | Document |
| --- | --- |
| Install and run a first drill | [Getting started](docs/getting-started.md) |
| Operate scheduled drills and evidence | [Operator guide](docs/operator-guide.md) |
| Configure providers, targets, probes, and reports | [Configuration](docs/configuration.md) |
| Understand pass/fail semantics | [Recovery policy](docs/recovery-policy.md) |
| Interpret or integrate report JSON | [Report format](docs/report-format.md) |
| Review tested version boundaries | [Compatibility](docs/compatibility.md) |
| Rehearse the technical demo | [Demo guide](demo/README.md) |
| Understand engine internals | [Architecture](docs/architecture.md) |
| Contribute changes | [Contributing](CONTRIBUTING.md) |
| Report a vulnerability | [Security](SECURITY.md) |

## Non-Goals

- Implementing another backup format or retention system.
- Modifying the source PostgreSQL cluster during a restore drill.
- Treating backup presence or provider exit code alone as recovery proof.
- Claiming application correctness beyond configured probes.
- Claiming that demo RTO/RPO measurements apply to production data volumes.
- Requiring a hosted service to use the engine.

## Development

The default local gate is:

```sh
make check
```

Use `make help` for the maintained quality, integration, release, documentation,
and cleanup targets. Development rules and the complete verification matrix are
documented in [CONTRIBUTING.md](CONTRIBUTING.md).

## License

Apache License 2.0. See [LICENSE](LICENSE) and [NOTICE](NOTICE).
