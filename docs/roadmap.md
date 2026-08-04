# Roadmap

`pgdrill` should ship as a CLI-first recovery readiness engine. The first
usable product surface should work in cron, CI, Kubernetes Jobs, and incident
runbooks without requiring a server.

## Version Direction

- `v0.2`: harden and publish the single-attempt engine contract.
- `v0.3`: stabilize the newly implemented daemon-free typed planner and local
  durable history while broadening real latest/PITR evidence.
- Later pre-1.0 work: complete stable schemas, distribution, upgrades, and
  pilot operations.
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
- Command runner with Unix process-group or platform process termination,
  bounded inherited-pipe wait, bounded raw/evidence capture, redaction,
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
PostgreSQL 18.3 Linux arm64 points. All four also passed latest and timestamp
recovery from one `v0.3.0-alpha.10` deterministic Linux amd64 archive through
Docker emulation; that is functional rather than performance evidence.

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
coverage remains pending. The current path is the native `barmanObjectStore`
API; CNPG deprecates it and currently schedules removal for 1.31, so Barman
Cloud Plugin support is a separate implementation/evidence cell rather than an
inferred continuation of the 1.26 result.

- CNPG verify-cluster name generation and manifest primitives.
- First CNPG target CLI surface: `pgdrill target manifest`.
- CNPG lifecycle controller boundary with create, wait, capture, and cleanup
  semantics.
- `kubectl` compatibility client behind the CNPG lifecycle interface.
- CNPG `kubectl` discovery for latest completed `Backup` and source image.
- Typed Barman Cloud Plugin recovery manifests with exact Barman backup-ID
  selection, source-plugin parameter discovery, and mismatch rejection.
- A checksum- and digest-pinned CNPG 1.29.2 / Barman Cloud Plugin 0.13.0 live
  KinD gate with real object storage, post-backup WAL replay, probes, history,
  artifact verification, and cleanup.
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

Remaining compatibility extension:

- The first exact clean CNPG 1.29.2 / Barman Cloud Plugin 0.13.0 Linux arm64
  observation is retained in the matrix. Broaden it to Linux amd64, the final
  published/signed candidate, additional object-storage and Kubernetes
  environments, failure cases, and supported PITR targets before advertising a
  range. CNPG 1.29.2 schedules native in-tree Barman Cloud removal for 1.31.0;
  adjacent versions are not inferred from this cell.

## Phase 4: More Providers And Probes

Status: initial four-provider surface and semantic config validation
implemented. WAL-G, Barman, pgBackRest, and pg_probackup have exact Linux
arm64 observations plus retained Linux amd64 emulated functional observations;
WAL-G additionally has one exact Linux arm64 single-node MinIO observation.
Broader storage, version, native-hardware, and PITR coverage remains in
progress. Disposable gates prove latest plus inclusive timestamp PITR
boundaries for all four providers, with exact clean-candidate evidence retained
in the compatibility matrix.

- pg_probackup catalog discovery through `show --format=json`.
- Optional pg_probackup selected-backup and recovery-target validation.
- pg_probackup local restore planning with canonical PITR target mapping.
- Optional generic `pg_verifybackup` restore check in pg_probackup plans.
- Richer Barman manifest handling if real repositories expose more cases than
  `generate-manifest`.
- Additional `pg_verifybackup` profiles, if real drills prove they are useful.

## Phase 5: Engine v0.2 Hardening

Status: published as `v0.2.0-rc.2` after protocol hardening, exact alpha.10
consolidation, and reproducible aggregate gates. Clean commit
`97ad852ecb2c9493c1c4a1e7718f61bf496efa17` additionally passed a real WAL-G
timestamp boundary before publication. The daemon-free planning and local
history foundation is additionally implemented on the post-`rc.2` main branch;
scheduling and distributed execution remain deferred.

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
- Pinned, rootless WAL-G/PostgreSQL Docker drills under `test/integration`
  that recreate a real base backup, post-backup WAL replay, provider
  validation, restored-server probes, policy evaluation, and cleanup against
  both a network-free filesystem repository and pinned MinIO over a private
  S3-compatible network, without coupling demo infrastructure to engine
  packages.
- Pinned Barman 3.19.1/PostgreSQL 18.3 and 17.10 companion profiles that
  create a real local-rsync backup, exercise archived WAL through Barman's
  generated
  `restore_command`, require repeatable manifest verification, prove latest
  and timestamp recovery boundaries, and retain the same release-bound
  checksummed artifact set.
- Pinned pgBackRest 2.58.0/PostgreSQL 18.3 and 17.10 companion profiles that
  create a real filesystem-repository full backup, retrieve the exact
  post-backup WAL
  segments, require `check` and selected-set `verify`, prove latest and
  timestamp recovery boundaries, and restore through the same local lifecycle
  and evidence contract.
- Source-pinned pg_probackup 2.5.16/PostgreSQL 18.3 and 17.10 companion
  profiles that apply the upstream PostgreSQL 18 patch only to the 18 build,
  create a compressed full STREAM backup, byte-compare the exact boundary WAL
  through `archive-get`, require
  native backup/WAL validation, prove latest and whole-second timestamp
  recovery boundaries, and restore through the same local lifecycle and
  evidence contract.
- Shared host-side integration mechanics for deterministic release archives,
  explicit dirty builds, rootless network-isolated Docker execution, and
  recursive artifact checksums, while provider semantics remain separate.
- Separate checksum-pinned native and Barman Cloud Plugin
  KinD/Kubernetes/CNPG/PostgreSQL/MinIO integration drills that use isolated
  kubeconfigs, load digest-validated platform images, create real object-store
  backups, require post-backup WAL replay and in-pod server/client checks,
  verify owned cleanup, remove ephemeral kubeconfig state, and retain
  checksummed artifacts.
- A clean-tree `release-candidate-check` that runs the deterministic release
  gate, ShellCheck, all four native-provider drills on PostgreSQL 18.3 and
  17.10, the additional WAL-G S3 profile, and both disposable CNPG protocol
  drills with one version and full Git commit.
- A deterministic real WAL-G executor-loss gate that sends `SIGKILL` to the
  complete drill process group after durable restore intent, preserves
  incomplete history, applies digest-confirmed observation-only recovery,
  proves exact owned cleanup, and requires a passed retry under a new attempt
  ID.

Remaining external engine gate:

1. Broaden beyond one single-node MinIO point and extend the other providers
   beyond local repositories across appropriate remote storage.
2. Repeat both advertised PostgreSQL majors from the exact final candidate on
   native Linux amd64 hardware; extend additional patch releases, backup
   modes, and non-timestamp PITR targets where the provider contract supports
   them.

`pgdrill.report/v2` is now the durable terminal producer contract. Readers
preserve the previous `v1` contract and documented `v1alpha1` compatibility
floor. The CLI remains journal-free by default and can persist ordered events
plus terminal snapshots through explicit `-history-dir`.

## Demo And Pilot Readiness

Status: repository baseline and local release-artifact rehearsal implemented.
On 2026-07-29, the exact locally built Linux amd64 `v0.3.0-dev` candidate at
commit `444c525c8c104f70ada9b66e8c1b633c6d4e8a0d` completed bootstrap and two
consecutive live Yandex Cloud WAL-G rehearsals. Both reports passed 13 checks,
all five required policy assertions, post-backup WAL replay, operation
checkpoint validation, and owned cleanup.

The same topology also completed two consecutive pgBackRest 2.58.0 rehearsals
against PostgreSQL 18.4 from exact commit
`92e8bfcc82165d4ad11a80dbda90790bdc4d0b7d` and Linux amd64 archive SHA-256
`f3b1404d3404d689aa16a0f85060af077b6055e372efbfb2c9fecb39fb662552`.
Both reports passed 13 checks and all five policy assertions, applied distinct
post-backup WAL segments, and proved cleanup. Source evidence records two
native `pgbackrest check` passes and selected-set verification; the
repository-only runner records `pgbackrest check` as skipped because it has no
PostgreSQL-host access, while its `pgbackrest verify` passes.

These owner-only NFS observations are not published-release,
invited-administrator, customer-pilot, production-RTO, or general Yandex Cloud
compatibility claims.

The observed Yandex Cloud environment was destroyed on 2026-08-02 after local
evidence export and a reviewed Terraform destroy plan. No continuously running
hosted demo is part of the product claim; a future session must provision and
accept a fresh disposable environment.

- Evidence-led demo contract with explicit proof and non-proof boundaries.
- Customer discovery and one-scenario pilot acceptance checklist.
- Three-VM Yandex Cloud WAL-G/pgBackRest topology with one public runner,
  private source and repository, allowlisted SSH, dedicated administrator
  identities, shared egress NAT, provider-separated repository subtrees, and
  read-only repository access from the drill runner.
- Pinned WAL-G and pgBackRest versions, PostgreSQL 18 host bootstrap,
  synthetic base-backup/post-backup-WAL boundaries, deterministic
  provider-specific wrappers, and local success/failure evidence retrieval.
- Provider-independent local-target startup override preventing a restored
  cluster from inheriting an active archive command and writing back to the
  source backup repository.
- Local rehearsals that execute an exact checksum-verified Linux archive
  through real WAL-G or pgBackRest latest and timestamp-PITR drills and require
  report, provider checks, post-backup WAL, before/after boundary, policy, and
  cleanup proof.
- Explicit repository boundaries between disposable developer integration
  tests, operator-facing demo topology, and retained compatibility evidence.

Completed hosted-rehearsal gates:

1. Apply the exact Terraform plan in a disposable Yandex Cloud folder and
   retain infrastructure inventory plus a successful bootstrap transcript.
2. Produce two consecutive passed reports for each hosted provider profile
   from one exact candidate artifact per profile, including the post-backup WAL
   assertion and owned cleanup.

Remaining gates, in order:

1. Publish or otherwise freeze the exact artifact selected for the customer
   session and repeat the final rehearsal if it differs from the observed
   candidate.
2. Exercise a dedicated invited-administrator account and confirm its bounded
   sudo surface before the customer session.
3. Add Yandex Object Storage only as a separate compatibility profile with
   executor-local credentials and explicit secret/state review.
4. Convert one real customer topology into a bounded pilot spec before adding
   generalized fleet or UI features.

## Phase 6: Fleet Control Plane

Status: daemon-free typed planning, local durable history, stable schema
identifiers, copy-on-migrate history, and manual killed-attempt recovery are
implemented on main. The disposable provider process-loss gate is complete;
broader real-repository evidence remains external work. Distributed
controller/executor operation is not part of the `v1.0.0` boundary.

The control plane will compile typed fleet resources into independent immutable
engine runs:

- `BackupSource`: logical PostgreSQL cluster, repository driver/reference, and
  execution location.
- `TargetPool`: compatible disposable destinations and placement labels.
- `ProbeProfile`: required post-restore proof.
- `RecoveryPolicy`: selection, recovery target, assertions, and cleanup rules.
- `DrillSet`: source selectors, target pool, schedule, and concurrency policy.
- `DrillRun`: one concrete planner output and its attempt history.

Implemented foundation:

1. Strict `pgdrill.fleet/v1` resources and deterministic
   `pgdrill.plan/v1` output, with read compatibility for the pre-GA fleet
   generation.
2. Exact ID/label selectors, execution-pool/driver/mode compatibility,
   capacity-aware concrete placement, global/per-set expansion bounds,
   immutable revisions and digests, mutation count, and typed rejections.
3. Read-only `plan validate/show` CLI commands with no secret resolution or
   infrastructure access.
4. Private `pgdrill.history-store/v1` persistence for immutable specs,
   multiple attempts, ordered idempotent events, terminal reports, and bounded
   artifact references.
5. `history list/show/import/verify/prune` plus opt-in local persistence for
   native and CNPG execution; direct runs remain independent. Prune is
   deterministic, digest-confirmed, audit-protecting, and resumable after
   process loss.
6. History-backed acceptance in all disposable WAL-G, Barman, pgBackRest,
   pg_probackup, and CNPG drills, with retained full views, list views, full
   verification views, and raw store archives.
7. A frozen exact `v0.3.0-alpha.1` WAL-G store establishes the first declared
   history read-compatibility floor.
8. Local directory artifacts now carry immutable classification claims and
   support full verification plus age-gated, history-reference-aware,
   digest-confirmed, crash-resumable garbage collection. Live, audit, legacy,
   and temporary-file protection is explicit and tested.
9. Stable `v1` report, event, drill-spec, operation, policy, artifact, fleet,
   plan, compatibility-matrix, and history identifiers are emitted by current
   producers. A digest-confirmed `history migrate` copies the frozen
   `v0.3.0-alpha.1` floor to a stable store, preserves historical files
   byte-for-byte, survives interrupted copy, and retains the source for
   rollback.
10. `pgdrill attempt recover` now turns an incomplete local attempt into a
    digest-confirmed reconciliation/cleanup decision without replaying provider
    mutation or rewriting history. A real WAL-G process-group kill proves the
    unresolved-operation, owned-cleanup, and clean-retry path.

Next implementation order:

1. Keep the `v1.0.0` path focused on publishing and independently verifying
   the implemented signed archive/OCI supply chain, exact published-candidate
   provider evidence, reproducible demo operation, and an external pilot; none
   requires a daemon.
2. After the single-host engine and recovery schemas settle, add controller and
   executor binaries with leases, heartbeats, idempotency, and executor-local
   secret resolution as a separately gated `v1.x` capability.
3. Add schedules and concurrency controls before RBAC, audit, notifications,
   TUI, or web UI.

Keep these binaries in this repository and Go module while contracts evolve
together. Split a module or repository only when versioning, ownership,
security boundary, release cadence, or licensing genuinely diverges.
Topology semantics, persistence boundaries, and interface sequencing are
detailed in [control-plane-roadmap.md](control-plane-roadmap.md).

## Phase 7: Operator Interfaces

Status: direct, planning, verification, and digest-confirmed local-history
retention CLI implemented; TUI and web UI deliberately deferred. Real operator
use must establish comparison and cancellation workflows before another
surface is justified.

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
- digest-confirmed recovery and immutable evidence for killed local attempts;
- daemon-free typed planning and local durable run history;
- signed archives and OCI images with checksums, SBOM, and provenance;
- independently verified exact-candidate runs, a reproducible demo, and at
  least one bounded external pilot.

Web UI, SaaS multi-tenancy, remote executors, a general DAG, and universal
provider/version coverage are explicitly outside the GA gate.

## Release Readiness

Status: implemented and exercised through the published `v0.2.0-rc.2`
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
- A digest-pinned, archive-only, non-root Linux amd64/arm64 OCI build with a Go
  verifier for platform, label, binary, SBOM, and provenance identity.
- Tag-workflow SPDX generation plus OIDC-backed signed archive/SBOM/image
  provenance, with archive build, GHCR write, and GitHub Release write split
  across least-privilege jobs. This is implemented on `main`; an exact tag must
  still publish and independently verify it before it becomes release evidence.
- Cross-host checksum parity between all four local release archives and the
  corresponding published `v0.2.0-rc.2` assets.
- One clean-tree aggregate candidate command binding release archives, the
  verified multi-architecture OCI layout, all four native drills, and both
  disposable CNPG protocol drills to the same version and commit.
- Dependabot, contribution, security, compatibility, issue, and pull request
  policies.

## Plugin Protocol

Keep adapters in-process until at least WAL-G, Barman, and one restore target
exercise the interfaces. Add an external plugin protocol only after the model
and engine contracts stop changing under real restore drills.
