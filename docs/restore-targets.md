# Restore Targets

Restore targets own the lifecycle of disposable recovery environments. Provider
adapters should not know whether a restore happens in a local directory,
container, or Kubernetes cluster.

## Kubernetes / CNPG Notes

CloudNativePG is useful as a Kubernetes restore target because a temporary
`Cluster` can be bootstrapped from an existing backup and then queried as a real
PostgreSQL instance.

The target implementation should keep these operational rules:

- Discover the source cluster image and reuse it for the verify cluster. Do not
  hardcode a PostgreSQL image or major version.
- Build deterministic, DNS-safe verify cluster names with a bounded length and a
  short hash suffix.
- Create a one-instance verify cluster with explicit CPU, memory, and storage
  requests so drills do not inherit production sizing accidentally.
- Wait separately for recovery pod creation and PostgreSQL pod readiness.
- Fail fast if the CNPG full-recovery job fails before the instance pod appears.
- Run the probe set only after the instance pod is Ready.
- Capture evidence before cleanup: verify cluster YAML, pod list, PVC list,
  recent events, full-recovery logs, bootstrap-controller logs, and postgres
  logs.
- Capture evidence on success as well as failure so the most recent successful
  drill proves what actually restored.
- Cleanup cluster and PVCs explicitly, record cleanup evidence, and make
  cleanup-on-failure configurable.

The Go target should prefer the Kubernetes API. A `kubectl` compatibility layer
can be useful for early prototypes or constrained environments, but it should
not become the control plane.

## Probe Mapping

Useful CNPG target probes:

- quick SQL readiness: `SELECT 1`
- configurable SQL invariants for application-specific proof
- `pg_isready` for connection readiness
- `pg_amcheck` for structural checks where available
- `pg_dump` schema or data smoke checks when logical readability matters

`vacuumdb --all --analyze-in-stages` can be used as an optional compatibility
probe, but it should not replace explicit correctness probes.

## Local Target

The local target is the first executable restore target. It prepares a
disposable work directory, runs command-based restore steps, starts a restored
PostgreSQL process on `127.0.0.1`, records runtime evidence with pid, port, and
log path, and stops the process during cleanup.

The target does not remove `work_dir` by default. Removal must be explicitly
enabled and is guarded by a pgdrill-owned marker file.

File-writing restore steps are limited to the target `work_dir`.
