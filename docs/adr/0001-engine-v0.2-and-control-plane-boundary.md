# ADR 0001: Engine v0.2 And Control Plane Boundary

- Status: accepted
- Date: 2026-07-21

## Context

`pgdrill` has one mature native-provider path in `core.Engine` and one guarded
CNPG path assembled by the CLI. Both produce the same durable report, but they
do not yet share one lifecycle implementation. The current report is terminal:
an operator receives the complete state only after a potentially long restore.

A future control plane must also expand inventory, placement, policy, and
schedules into concrete runs. Those concerns must not turn the recovery engine
into a general workflow scheduler.

## Decision

The engine executes exactly one immutable recovery drill attempt. It owns:

1. request and protocol validation
2. ordered lifecycle transitions
3. provider/source discovery and backup selection
4. restore planning and target execution
5. post-restore checks
6. evidence and artifact provenance
7. cancellation, cleanup, and terminal report persistence

The control plane owns inventory, selectors, placement, schedules,
concurrency, leases, and history. It compiles those inputs into independent
engine runs; it does not reach into provider or target implementations.

The first engine v0.2 migration keeps `pgdrill.report/v1alpha1` readable and
adds an append-only `pgdrill.run-event/v1alpha1` stream. A later immutable
`DrillSpec` contract will be finalized only after `BackupProvider` is split
into source discovery, restore planning, and target execution responsibilities.
Publishing that spec before correcting the current abstraction would freeze
the native-local shape into the distributed protocol.

## Engine Invariants

- A run ID identifies a logical run; an attempt ID identifies one execution.
- Event sequence numbers are positive and strictly increasing within an
  attempt.
- A lifecycle stage is entered before its side effects and completed exactly
  once with `succeeded`, `failed`, or `aborted`.
- A terminal result and `run_finished` event agree on status.
- Mutations use attempt-scoped ownership and idempotency identities.
- Unknown mutation outcomes are reconciled; they are not blindly retried.
- Cleanup and report persistence use bounded finalization contexts independent
  of a canceled operation context.
- Secrets are resolved at the executor and never become report or event data.
- The engine remains useful as a standalone CLI without a controller.

## Control Plane Domain

The initial control plane model will use typed resources rather than arbitrary
server-to-server edges:

- `BackupSource`: logical PostgreSQL cluster, repository driver, repository
  reference, and execution location
- `TargetPool`: compatible disposable destinations and placement metadata
- `ProbeProfile`: required post-restore proof
- `RecoveryPolicy`: backup selection, recovery target, RTO/RPO assertions, and
  cleanup requirements
- `DrillSet`: source selectors, target-pool reference, and concurrency policy
- `DrillRun`: one immutable planner output consumed by the engine

Data normally flows from a backup repository to a disposable target, not from
a live database server to another server. Source labels and destination pools
cover one-to-one, many-to-pool, matrix, spread, and exclusion policies without
introducing a general DAG protocol.

## Repository Boundary

The engine, planner, controller, and agent remain in this repository and Go
module while their contracts evolve together. They may become separate
binaries. A separate module or repository is justified only by independent
versioning, ownership, security, release cadence, or licensing requirements.

Versioned wire types belong under `api/` once they are ready to be consumed
outside this module. Engine implementation packages remain internal.

## Migration Plan

### Engine v0.2

1. add validated run events and a lifecycle recorder
2. move CNPG result, cleanup, and finalization orchestration out of the CLI
3. drive native and managed-target paths through the same lifecycle recorder
4. separate backup source, restore planner, and restore target contracts
5. add immutable spec digests, artifact references, and crash reconciliation
6. publish provider/target conformance suites and real-repository matrices
7. add explicit recovery-policy verdicts

### Control Plane

1. add a daemon-free fleet planner and `plan` output
2. persist run/event history and bounded artifact references
3. add controller/agent leases, heartbeats, and local secret resolution
4. add schedules, RBAC, audit, notifications, and retention
5. add a TUI for history before considering a web UI

## Consequences

This keeps restore correctness and fleet orchestration independently testable.
It also delays a public `DrillSpec` until the engine contracts reflect both
native and operator-managed restores. The short-term cost is an incremental
migration with both old terminal reports and new run events supported together.
