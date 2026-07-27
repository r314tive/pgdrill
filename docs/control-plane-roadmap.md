# Control Plane And Interface Roadmap

Status: daemon-free planner, local history, and manual interrupted-attempt
recovery are implemented on the post-`rc.2` main branch; distributed
control-plane sections remain a design draft and are not a supported wire API.

This document describes how `pgdrill` can grow from a single-run CLI engine
into a fleet product without moving restore correctness into a scheduler or UI.
The prerequisite engine gates are tracked in [the main roadmap](roadmap.md) and
[ADR 0001](adr/0001-engine-v0.2-and-control-plane-boundary.md).

## Product Boundary

The engine proves one concrete recovery attempt. The control plane decides
which attempts should exist, where they may run, and when they may overlap.

The engine owns:

- immutable run input and attempt identity
- provider and target protocol validation
- restore execution, checks, evidence, and cleanup
- lifecycle events and a terminal report
- idempotency and reconciliation of its own mutations

The control plane owns:

- source and target inventory
- selectors and placement
- schedules and concurrency
- leases, retry policy, and attempt history
- RBAC, audit, notifications, and retention

Neither the controller nor an interface may bypass engine validation or
construct a passed report directly.

## Topology Model

The user-facing model should describe intent with typed resources, not an
arbitrary graph of shell commands or server-to-server copies.

### BackupSource

A logical PostgreSQL cluster and its backup repository:

- stable source identity and labels
- provider driver and non-secret repository reference
- execution pool able to reach the repository
- PostgreSQL/provider compatibility metadata discovered by an executor

The live database host is inventory context. A drill normally reads a backup
repository and does not copy data from the live server.

### TargetPool

A set of disposable restore destinations:

- target driver and execution pool
- capacity class, PostgreSQL compatibility, and placement labels
- namespace, account, region, network, or filesystem boundary references
- cleanup policy and maximum lease duration

Secrets and concrete credentials are resolved by the selected executor and are
never embedded in planner output.

### ProbeProfile

A named, versioned set of required checks and their timeouts. A run records the
resolved profile revision so later edits cannot change the meaning of history.

### RecoveryPolicy

The proof expected from a run:

- backup selection and recovery target
- maximum backup age and RPO
- maximum restore and readiness duration
- required probe profile
- required evidence and cleanup outcome

### DrillSet

Fleet intent:

- source selector
- target-pool reference or selector
- schedule and concurrency policy
- placement constraints, spread keys, and exclusions
- recovery-policy reference

### DrillRun

One immutable planner output consumed by the engine. It contains concrete
source, backup-selection intent, target placement, policy/profile revisions,
and idempotency identity. A retry creates another attempt under the same
logical run; changing resolved inputs creates a new run.

## Topology Coverage

The current planner supports:

- one source to one fixed target
- many selected sources to one compatible target pool
- exact source/target ID filters combined with exact label matches
- explicit execution-pool, engine-mode, source-driver, and native-target
  compatibility
- deterministic least-assigned placement with per-target capacity
- fleet-wide and per-drill-set expansion bounds

The following topology behavior remains future work:

- one source intentionally expanded across several target classes or regions
- a full source-by-target compatibility matrix
- spread across zones or executors
- anti-affinity and failure-domain exclusions
- schedules and concurrency across separately compiled plans

The planner must reject an empty or incompatible expansion and expose the
reason before scheduling. It must also provide a bounded expansion preview so a
broad selector cannot create an accidental fleet-wide drill storm.

The accepted pre-GA inventory shape is documented in
[fleet-plan-format.md](fleet-plan-format.md), with a runnable example in
[`examples/fleet.yaml`](../examples/fleet.yaml):

```yaml
schema_version: pgdrill.fleet/v1
max_runs: 20
drill_sets:
  - id: production-weekly
    revision: sha256:...
    source_selector:
      match_labels:
        environment: production
        recovery-tier: critical
    target_pool: isolated-local
    probe_profile: standard
    recovery_policy: weekly-full-proof
    max_runs: 10
```

## Planning And Execution Flow

1. Resolve and snapshot source, target, policy, and probe-profile revisions.
2. Expand selectors and validate compatibility without infrastructure mutation.
3. Produce immutable runs with canonical digests and idempotency identities.
4. Acquire a run-attempt lease and assign an execution pool.
5. The executor resolves secrets locally and invokes the engine in process.
6. Persist ordered events, bounded artifact references, and the terminal report.
7. Reconcile leases and owned resources after executor loss or unknown mutation
   outcomes.
8. Evaluate policy verdicts and emit notifications from canonical state.

Retries must not mean blindly running the previous shell command again. The
controller first reconciles the attempt checkpoint and ownership identity, then
either resumes a safe operation or starts a new attempt.

## Persistence Boundary

The minimum durable records are:

- immutable run spec and digest
- attempt identity, lease, executor, and heartbeat
- append-only run events
- terminal `pgdrill.report/v1`
- content-addressed or immutable artifact references with size, digest, media
  type, retention class, and redaction state

Large logs and manifests belong in an artifact store, not in controller rows or
event payloads. Reports retain bounded evidence summaries and references.

The first daemon-free local history uses a private append-only directory store
instead of adding SQLite/CGO or another release dependency. It proves identity,
ordering, atomic immutable publication, strict inspection, and crash-visible
partial states. Its layout and limits are documented in
[history-format.md](history-format.md). A networked controller should use a
transactional database plus an artifact store only after local persistence,
retention, migration, and reconciliation contracts are proven.

## Interface Sequence

### Existing CLI

Keep direct one-run commands stable for cron, CI, Kubernetes Jobs, and incident
work. The engine must remain fully usable without a daemon.

### Planning And History CLI

Implemented read-only planning and local inspection commands:

```text
pgdrill plan validate -f fleet.yaml
pgdrill plan show -f fleet.yaml
pgdrill history list
pgdrill history show <run-id>
pgdrill attempt recover -f pgdrill.yaml \
  -run-id <run-id> -attempt-id <attempt-id> \
  -history-store <path>
```

`plan show` must display concrete expansion, placement, policy revisions, and
mutation count without resolving secret values or creating resources.
`history list/show` validates the on-disk store while exposing attempts, failed
stages, policy verdicts, evidence counts, artifact references, and lifecycle
events. Direct runs use history only when `-history-dir` is explicit.
`attempt recover` consumes the same immutable identity and local operation
journal, but remains a manual digest-confirmed cleanup path rather than a
scheduler or automatic retry loop.

### TUI

Add after local durable history. It should optimize operator workflows:
active attempts, failed stages, evidence links, comparisons, cancellation, and
safe rerun. It consumes the same planner and history APIs and contains no
orchestration logic.

### Web UI

Add only with a real multi-user controller. Its justification is RBAC, audit,
fleet history, approvals, and hosted operation, not visual polish alone. API
and CLI remain complete product surfaces.

## Repository And Module Decision

Keep the engine, planner, local store, controller, executor, CLI, and future TUI
in this repository and Go module while contracts change together. Use separate
commands and internal packages:

```text
cmd/pgdrill
cmd/pgdrill-controller
cmd/pgdrill-executor
internal/planner
internal/controlplane
internal/history
internal/executor
```

Move stable cross-process wire types to a versioned `api/` package only when an
out-of-process consumer exists. Do not expose `internal/model` as a public SDK
prematurely.

A separate module or repository becomes justified only when at least one of
these is real:

- independent compatibility and release cadence
- separate security or deployment boundary
- independent maintainers and ownership
- licensing or commercial distribution boundary
- external consumers that cannot upgrade with the engine

Different binaries, container images, or editions do not by themselves require
different repositories.

## Delivery Gates

Completed prerequisite: the engine now captures an internal immutable drill
spec with a canonical digest and explicit attempt identity. It remains internal
until an out-of-process consumer exists.

Completed prerequisite: engine mutations now have deterministic attempt-scoped
operation and ownership identities, durable intent checkpoints, target
reconciliation dispositions, and process-loss fault-injection coverage. This
does not yet provide leases, heartbeat recovery, or automatic event-stream
resume.

Completed prerequisite: reports now carry bounded content-addressed artifact
references with exact size, media type, retention class, redaction state, and
evidence provenance. The local directory store proves atomic publication and
verified reads; it is not the future fleet artifact service.

Completed prerequisite: recovery assertions now live in the immutable drill
spec and produce typed fail-closed verdicts for RTO, RPO, backup age,
recovery-target satisfaction, and configured cleanup. Fleet policy references
are now resolved into immutable plan records and terminal history.

Completed prerequisite: every current provider and executable target path now
runs a reusable protocol conformance suite, including fresh-executor mutation
reconciliation. A strict versioned matrix separates fixture, controlled, and
field evidence; native real-repository entries remain an external gate.

Completed prerequisite: the planner now compiles strict typed fleet resources
into bounded deterministic engine specs with concrete target placement,
immutable revisions/digests, mutation count, and typed rejection records
without infrastructure access or secret resolution.

Completed prerequisite: the optional local history now persists immutable
run/spec/attempt identities, ordered idempotent events, terminal reports, and
bounded artifact references. It rejects unknown store versions and corrupted
identity/order instead of silently repairing them.

Completed prerequisite: the local store now has full verification plus
deterministic digest-confirmed retention. Incomplete/latest/audit-linked
attempts are protected by default, history deletion is resumable across
process-loss boundaries, and a frozen real `v0.3.0-alpha.1` store establishes
the first read-compatibility floor.

Completed prerequisite: the local directory artifact store now persists
immutable classification claims and last-observed state under lock. Full
verification resolves a complete history snapshot; garbage collection is
age-gated, dry-run/digest-confirmed, protects live/audit/legacy blobs, and
resumes across deterministic process-loss windows. This remains a local
single-host lifecycle, not the future fleet artifact service.

Completed prerequisite: every disposable native-provider and CNPG integration
drill now enables that store, reads the complete attempt back through the CLI,
requires a matching passed report and terminal event, and retains both bounded
views and the raw private store archive.

Completed prerequisite: current producers emit stable `v1` schema identifiers.
The frozen floor remains readable, and digest-confirmed copy migration
preserves its historical files byte-for-byte while publishing a separately
verified stable store. Actual child-process kills now cover migration, history
retention, and artifact GC publication boundaries.

Completed prerequisite: a disposable WAL-G drill is now killed at a
deterministic provider-mutation boundary after durable intent. The CLI
preserves incomplete history, requires exact digest and stopped-executor
confirmation, reconciles without replay, proves owned cleanup, and passes a new
attempt. This proves the single-host recovery primitive, not lease fencing.

1. Complete real-repository and live-target compatibility gates required by
   the CLI-first `v1.0.0` contract.
2. Run one executor/controller on a single host with leases and automatic
   process-loss recovery as post-GA control-plane work.
3. Add remote executors only after single-host lease and heartbeat recovery is
   proven.
4. Add TUI, then multi-user controller capabilities, then web UI if validated
   workflows require it.

No gate is satisfied by UI mockups or fixture-only provider tests.
