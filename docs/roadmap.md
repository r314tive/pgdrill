# Roadmap

`pgdrill` should ship as a CLI-first recovery readiness engine. The first
usable product surface should work in cron, CI, Kubernetes Jobs, and incident
runbooks without requiring a server.

## Version Direction

- `v0.2`: harden and publish the single-attempt engine contract.
- Next pre-1.0 milestones: broaden real latest/PITR evidence, add daemon-free
  typed planning and local durable history, then stabilize schemas,
  distribution, upgrades, and pilot operations.
- `v1.0.0`: the stable self-managed product described by the
  [v1.0 release contract](v1.0-release-contract.md).
- `v1.x`: remote executors, schedules, notifications, multi-user control-plane
  capabilities, TUI, or web UI as justified by real operator workflows.

The sequence is contractual, not a promise that each item consumes exactly one
minor release. `v1.0.0` does not wait for a browser UI or hosted SaaS.

## Phase 1: Engine Skeleton

Status: complete for the initial CLI engine.

- Canonical model for backups, restore plans, checks, drill results, and
  evidence.
- Canonical recovery-target validation and timestamp-aware backup eligibility
  selection before repository mutation or restore planning.
- Core drill engine: discover, select backup, validate, plan, restore, start
  PostgreSQL, run probes, cleanup, write evidence.
- Command runner with timeout, bounded raw/evidence capture, redaction,
  truncation metadata, and structured exit status.
- WAL-G and Barman catalog discovery adapters with fixture tests.
- Strict YAML/JSON config loading.
- Provider registry.
- First CLI catalog surface: `pgdrill catalog list`.
- JSON report file sink.
- First CLI report surface: `pgdrill report show`.
- Prometheus metrics export from JSON reports: `pgdrill report metrics`.
- Local restore target workdir preparation, read-only ownership validation,
  symlink-safe file steps, command execution, and per-run guarded cleanup.
- Local restore target PostgreSQL startup/shutdown lifecycle.
- `pg_isready` probe.
- SQL probe through `psql`.
- `pg_amcheck` probe.
- `pg_dump` schema probe.
- Built-in probe presets: `readiness`, `smoke`, and `structural`.
- Optional `pg_verifybackup` restore-artifact check.
- Strict `pg_verifybackup` profile.
- Optional WAL-G `wal-verify` provider check.
- First CLI drill surface: `pgdrill run`.
- Read-only `pgdrill doctor` preflight with config-aware executable discovery,
  native version capture, and redacted structured evidence.
- Signal-aware CLI cancellation with `aborted` reports, structured canceled
  command evidence, bounded cleanup, and stable automation exit codes.
- WAL-G local restore planning for `backup-fetch` and `wal-fetch` recovery
  configuration.
- Barman local restore planning for `barman restore`.
- Barman provider checks: `check` and `check-backup`.
- Barman selected-backup evidence: `show-backup`.
- Optional Barman manifest generation: `generate-manifest`.
- Optional Barman manifest check: `verify-backup`.
- pgBackRest catalog discovery: `info --output=json`.
- Optional pgBackRest provider validation: `check`.
- Optional pgBackRest repository verification: `verify`.
- pgBackRest local restore planning for `pgbackrest restore`.

## Phase 2: First Real Drill

Initial target: WAL-G to a local restore target. Barman is now the second
repeatable native path through the same engine lifecycle.

Status: usable for local-target smoke drills and field-exercised at exact WAL-G
3.0.8, Barman 3.19.1, pgBackRest 2.58.0, and pg_probackup 2.5.16 /
PostgreSQL 18.3 Linux arm64 points. All four also passed from one
`v0.1.0-alpha.10` commit and deterministic release archive.

- JSON evidence sink wired into `pgdrill run`.
- JSON evidence report written to disk.
- Versioned `pgdrill.report/v1alpha1` contract shared by CLI and metrics
  consumers.
- Structured lifecycle failure stage and evidence links shared by JSON, text
  output, and Prometheus metrics.
- Automatic provider/target/probe version preflight retained in every CLI drill
  report before repository access or target mutation.
- Bounded operation deadlines with separate provider/catalog and physical
  restore timeout policies plus guarded Kubernetes polling.
- Required post-restore proof plus bounded `pg_isready` retry semantics with
  per-attempt evidence.
- Semantic provider, restore-check, and expanded-probe validation before any
  native preflight or repository access.

The CLI should become usable here:

```sh
pgdrill run -f pgdrill.yaml
pgdrill doctor -f pgdrill.yaml
pgdrill report show path/to/report.json
pgdrill report metrics path/to/report.json
pgdrill catalog list -f pgdrill.yaml
```

## Phase 3: Kubernetes / CNPG Target

Status: implemented and field-exercised in disposable CNPG 1.26.0 /
PostgreSQL 15.13 and CNPG 1.26.3 / PostgreSQL 15.17 environments. Both exact
observations are recorded in the versioned evidence matrix; broader field
coverage remains pending.

- CNPG verify-cluster name generation and manifest primitives.
- First CNPG target CLI surface: `pgdrill target manifest`.
- CNPG lifecycle controller boundary with create, wait, capture, and cleanup
  semantics.
- `kubectl` compatibility client behind the CNPG lifecycle interface.
- CNPG `kubectl` discovery for latest completed `Backup` and source image.
- Source-image fallback through the source pod's `postgres` container.
- Read-only CNPG manifest discovery through `pgdrill target manifest -discover`.
- Guarded CNPG target verification through `pgdrill target verify`.
- Provider-independent target configuration and discovery command evidence in
  target verification reports.
- Temporary CNPG cluster restore target with standard JSON reports.
- Source image reuse for verify clusters.
- Post-ready probe-client preflight and probe execution inside the restored
  CNPG pod over its local Unix socket.
- Shared local/CNPG probe-report and cancellation semantics.
- Full-recovery fail-fast handling.
- Kubernetes events, pod descriptions, logs, and PVC state as evidence.
- Bounded Kubernetes event evidence through `events_tail`.
- Explicit cluster/PVC cleanup evidence.
- Create-only target ownership with deterministic attempt labels inherited by
  CNPG resources, plus idempotent selector-scoped cleanup after ambiguous
  `kubectl create` failures.
- Cancellation-safe CNPG diagnostics, cleanup, and report persistence.
- CronJob-friendly examples.
- Exact public `v0.1.0-alpha.9` Linux amd64 artifact exercised through latest
  backup recovery, in-pod client preflight, readiness and SQL probes, evidence
  capture, and ownership-scoped cleanup.
- Exact `v0.1.0-alpha.10` commit exercised through a pinned KinD 0.31.0 /
  Kubernetes 1.32.11 / CNPG 1.26.3 / PostgreSQL 15.17 drill with
  post-backup WAL replay, four probes, immutable manifest evidence, policy,
  and cleanup.

## Phase 4: More Providers And Probes

Status: initial four-provider surface and semantic config validation
implemented. WAL-G, Barman, pgBackRest, and pg_probackup now have one exact
native field point each; broader storage, version, and PITR coverage remains
in progress.

- pg_probackup catalog discovery through `show --format=json`.
- Optional pg_probackup selected-backup and recovery-target validation.
- pg_probackup local restore planning with canonical PITR target mapping.
- Optional generic `pg_verifybackup` restore check in pg_probackup plans.
- Richer Barman manifest handling if real repositories expose more cases than
  `generate-manifest`.
- Additional `pg_verifybackup` profiles, if real drills prove they are useful.

## Phase 5: Engine v0.2 Hardening

Status: published as `v0.2.0-rc.1` after protocol hardening, exact alpha.10
consolidation, and the reproducible aggregate gate passed from clean commit
`e9cb257c8312020166b5dff9c91f9bd9cde4ca25`. Fleet planning contracts remain
architecture only.

Completed foundation:

- Validated `pgdrill.run-event/v1alpha1` model with run/attempt identity and
  accepted-write sequence semantics.
- One lifecycle recorder for native local drills and managed targets.
- Fail-closed event delivery around side effects, cancellation-safe cleanup,
  and terminal report/event reconciliation.
- Managed-target core contracts for read-only resolution, operator-owned
  restore/start, post-restore checks, and cleanup.
- Segregated native roles for backup discovery, catalog validation, and restore
  planning. Current adapters remain composite implementations, while
  `core.Engine` accepts each role independently.
- Internal immutable `pgdrill.drill-spec/v1alpha1` snapshots with canonical
  JSON, secret-free component revisions, deterministic SHA-256 digests,
  canonical latest/exact backup selection, and explicit attempt identity.
- Native and managed reports persist the complete spec and digest; lifecycle
  events bind every emitted transition to the same digest, and report readers
  reject spec tampering or cross-field identity drift.
- Deterministic attempt ownership and operation keys, fail-closed pre-mutation
  intents, atomic local checkpoint persistence, local operation receipts,
  read-only CNPG ownership reconciliation, and executor-loss fault injection.
- Bounded content-addressed artifact stores and references with strict
  redaction/retention classification, report provenance validation, and exact
  CNPG manifest persistence before target creation.
- Immutable recovery-policy assertions and versioned fail-closed verdicts for
  RTO, RPO, backup age, recovery-target satisfaction, and configured cleanup.
- Local PostgreSQL startup waits for the owned postmaster `ready` or `standby`
  state with a bounded deadline instead of adding the entire startup timeout to
  every drill's measured RTO.
- Managed recovery-target protocol confirmation; CNPG rejects unsupported PITR
  intent instead of silently executing latest recovery.
- Reusable provider conformance across WAL-G, Barman, pgBackRest, and
  pg_probackup, including canonical discovery/selection/evidence contracts and
  restore planning for all recovery-target types.
- Reusable native and managed target conformance with fresh-executor mutation
  reconciliation, durable ownership proof, and owned cleanup.
- Strict `pgdrill.compatibility-matrix/v1alpha1` evidence with separate
  fixture, controlled, and exact-version field levels, validated references,
  and inclusion in release archives.
- CNPG orchestration moved from `cmd/pgdrill` into
  `internal/application/cnpgverify` and `core.ManagedEngine`.
- Explicit engine/control-plane boundary in
  [ADR 0001](adr/0001-engine-v0.2-and-control-plane-boundary.md).
- A pinned, rootless, network-isolated WAL-G/PostgreSQL Docker drill under
  `test/integration` that recreates a real base backup, post-backup WAL replay,
  provider validation, restored-server probes, policy evaluation, and cleanup
  without coupling demo infrastructure to engine packages.
- A pinned Barman 3.19.1/PostgreSQL 18.3 companion drill that creates a real
  local-rsync backup, exercises archived WAL through Barman's generated
  `restore_command`, requires manifest verification and restored-cluster
  probes, and retains the same release-bound checksummed artifact set.
- A pinned pgBackRest 2.58.0/PostgreSQL 18.3 companion drill that creates a
  real filesystem-repository full backup, retrieves the exact post-backup WAL
  segment, requires `check` and selected-set `verify`, and restores through the
  same local lifecycle and evidence contract.
- A source-pinned pg_probackup 2.5.16/PostgreSQL 18.3 companion drill that
  applies the upstream PostgreSQL 18 patch, creates a compressed full STREAM
  backup, retrieves the exact post-backup WAL through `archive-get`, requires
  native backup/WAL validation, and restores through the same local lifecycle
  and evidence contract.
- Shared host-side integration mechanics for deterministic release archives,
  explicit dirty builds, rootless network-isolated Docker execution, and
  recursive artifact checksums, while provider semantics remain separate.
- A checksum-pinned KinD/Kubernetes/CNPG/PostgreSQL/MinIO integration drill
  that uses an isolated kubeconfig, loads digest-validated platform images,
  creates a real object-store backup, requires post-backup WAL replay and
  in-pod server/client checks, verifies owned cleanup, removes ephemeral
  kubeconfig state, and retains checksummed artifacts.
- A clean-tree `release-candidate-check` that runs the deterministic release
  gate, ShellCheck, all four native-provider drills, and the disposable CNPG
  drill with one version and full Git commit.

Remaining external engine gate:

1. Broaden every provider beyond its first local latest-recovery point across
   storage backends, versions, platforms, backup modes, and PITR targets.

`pgdrill.report/v1alpha1` remains the durable terminal contract during this
migration. The event sink is injectable but the CLI does not persist an event
journal by default yet.

## Demo And Pilot Readiness

Status: repository baseline and local published-artifact rehearsal implemented;
the first live Yandex Cloud rehearsal is pending and no cloud compatibility
claim is recorded yet.

- Evidence-led demo contract with explicit proof and non-proof boundaries.
- Customer discovery and one-scenario pilot acceptance checklist.
- Three-VM Yandex Cloud WAL-G topology with one public runner, private source
  and repository, allowlisted SSH, dedicated administrator identities, shared
  egress NAT, and read-only repository access from the drill runner.
- Pinned WAL-G download and checksum, PostgreSQL 18 host bootstrap, synthetic
  base-backup/post-backup-WAL boundary, deterministic runner wrappers, and
  local evidence retrieval.
- Provider-independent local-target startup override preventing a restored
  cluster from inheriting an active archive command and writing back to the
  source backup repository.
- A local rehearsal that executes an exact checksum-verified published Linux
  archive through the real WAL-G/PostgreSQL drill and requires report,
  post-backup WAL, policy, and cleanup proof.
- Explicit repository boundaries between disposable developer integration
  tests, operator-facing demo topology, and retained compatibility evidence.

Remaining gates, in order:

1. Apply the exact Terraform plan in a disposable Yandex Cloud folder and
   retain infrastructure inventory plus a successful bootstrap transcript.
2. Produce two consecutive passed reports from the same published release
   artifact, including the post-backup WAL assertion and owned cleanup.
3. Exercise a dedicated invited-administrator account and confirm its bounded
   sudo surface before the customer session.
4. Add Yandex Object Storage only as a separate compatibility profile with
   executor-local credentials and explicit secret/state review.
5. Convert one real customer topology into a bounded pilot spec before adding
   generalized fleet or UI features.

## Phase 6: Fleet Control Plane

Status: architecture only. Do not implement a daemon before the Engine v0.2
spec, idempotency, reconciliation, and real-repository gates are complete. The
daemon-free planner and local history are part of the `v1.0.0` product
boundary; distributed controller/executor operation is not.

The control plane will compile typed fleet resources into independent immutable
engine runs:

- `BackupSource`: logical PostgreSQL cluster, repository driver/reference, and
  execution location.
- `TargetPool`: compatible disposable destinations and placement labels.
- `ProbeProfile`: required post-restore proof.
- `RecoveryPolicy`: selection, recovery target, assertions, and cleanup rules.
- `DrillSet`: source selectors, target pool, schedule, and concurrency policy.
- `DrillRun`: one concrete planner output and its attempt history.

Implementation order:

1. Daemon-free `plan` command that expands selectors and placement without
   mutating infrastructure.
2. Local durable run/event history and bounded artifact index.
3. Controller and executor binaries with leases, heartbeats, idempotency, and
   executor-local secret resolution.
4. Schedules, concurrency controls, RBAC, audit, notifications, and retention.

Keep these binaries in this repository and Go module while contracts evolve
together. Split a module or repository only when versioning, ownership,
security boundary, release cadence, or licensing genuinely diverges.
Topology semantics, persistence boundaries, and interface sequencing are
detailed in [control-plane-roadmap.md](control-plane-roadmap.md).

## Phase 7: Operator Interfaces

Status: CLI implemented; TUI and web UI deliberately deferred. Real drill
history and operator workflows must establish storage and comparison
requirements before another surface is justified.

Recommended order:

- CLI first: required for automation and simplest to make reliable.
- TUI second: browse plans, active attempts, local reports, and comparisons
  after durable history exists.
- Web UI last: only after a multi-user control plane creates real RBAC, audit,
  fleet-history, and hosted-mode requirements.

All interfaces consume the same run specs, events, reports, and control-plane
API. A UI must not become a second orchestration engine.

## v1.0 GA Gate

Status: target contract defined; not yet satisfied.

The detailed acceptance contract is
[docs/v1.0-release-contract.md](v1.0-release-contract.md). In summary, GA
requires:

- stable CLI and versioned external schemas with tested pre-GA migration;
- field-backed latest and timestamp PITR support cells for the advertised
  providers, versions, platforms, and repository topologies;
- local and CNPG target evidence with strict ownership and cleanup;
- daemon-free typed planning and local durable run history;
- signed archives and OCI images with checksums, SBOM, and provenance;
- independently verified exact-candidate runs, a reproducible demo, and at
  least one bounded external pilot.

Web UI, SaaS multi-tenancy, remote executors, a general DAG, and universal
provider/version coverage are explicitly outside the GA gate.

## Release Readiness

Status: implemented and exercised through the published `v0.2.0-rc.1`
prerelease, including its exact-candidate gate, green branch and tag workflows,
immutable annotated tag, published assets, and independent checksum
verification. Every future release requires the same gates.

- Non-mutating format, module, vet, and test gate.
- Minimum and pinned release Go toolchain checks.
- Race detector, CLI smoke, and workflow lint release gate.
- Deterministic Linux/macOS archives with embedded version metadata and SHA256
  checksums.
- Strict compatibility evidence validation and packaged compatibility document
  plus machine-readable matrix in every release archive.
- Changelog-derived release notes and annotated-tag validation.
- Read-only build job separated from the write-enabled publication job.
- Cross-host checksum parity between all four local release archives and the
  corresponding published `v0.2.0-rc.1` assets.
- One clean-tree aggregate candidate command binding release artifacts, all
  four native drills, and disposable CNPG to the same version and commit.
- Dependabot, contribution, security, compatibility, issue, and pull request
  policies.

## Plugin Protocol

Keep adapters in-process until at least WAL-G, Barman, and one restore target
exercise the interfaces. Add an external plugin protocol only after the model
and engine contracts stop changing under real restore drills.
