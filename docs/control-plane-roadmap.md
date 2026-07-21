# Control Plane And Interface Roadmap

Status: design draft, not a supported configuration or wire API.

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

## Supported Topologies

Selectors and placement cover the useful cases without a general DAG:

- one source to one fixed target
- many selected sources to one compatible target pool
- one source exercised across several target classes or regions
- source-by-target compatibility matrix
- spread across zones or executors
- anti-affinity and explicit source/target exclusions

The planner must reject an empty or incompatible expansion and expose the
reason before scheduling. It must also provide a bounded expansion preview so a
broad selector cannot create an accidental fleet-wide drill storm.

The following shape is illustrative only; it is not accepted configuration:

```yaml
kind: DrillSet
metadata:
  name: production-weekly
spec:
  sources:
    matchLabels:
      environment: production
      recovery-tier: critical
  targetPool: isolated-cnpg
  policy: weekly-full-proof
  placement:
    spreadBy: region
    excludeSameFailureDomain: true
  concurrency:
    maxActive: 2
    perSource: 1
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
- terminal `pgdrill.report/v1alpha1`
- content-addressed or immutable artifact references with size, digest, media
  type, retention class, and redaction state

Large logs and manifests belong in an artifact store, not in controller rows or
event payloads. Reports retain bounded evidence summaries and references.

SQLite is sufficient for the first daemon-free local history. A networked
controller should use a transactional database plus an artifact store only
after the local persistence and reconciliation contracts are proven.

## Interface Sequence

### Existing CLI

Keep direct one-run commands stable for cron, CI, Kubernetes Jobs, and incident
work. The engine must remain fully usable without a daemon.

### Planning CLI

Add read-only commands before a controller:

```text
pgdrill plan validate -f fleet.yaml
pgdrill plan show -f fleet.yaml
pgdrill history list
pgdrill history show <run-id>
```

`plan show` must display concrete expansion, placement, policy revisions, and
mutation count without resolving secret values or creating resources.

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

1. Add bounded artifact storage and references; idempotency, checkpoints, and
   crash-reconciliation classification now have fault-injection coverage.
2. Complete real-repository and live-target compatibility gates.
3. Ship daemon-free plan expansion and local history.
4. Run one executor/controller on a single host with process-loss recovery.
5. Add remote executors and leases only after single-host reconciliation works.
6. Add TUI, then multi-user controller capabilities, then web UI if validated
   workflows require it.

No gate is satisfied by UI mockups or fixture-only provider tests.
