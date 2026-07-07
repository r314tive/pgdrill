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

### CNPG Manifest Primitives

The first CNPG implementation step is a typed manifest builder, not a shell
controller. It builds the temporary `Cluster` resource that a future Kubernetes
target executor can apply, watch, inspect, and delete through the Kubernetes
API.

Implemented primitives:

- deterministic verify-cluster names: `verify-<source-prefix>-<hash8>`,
  bounded to the CNPG-friendly 50-character limit
- strict config parsing for `target.kubernetes` and `target.cnpg`
- source image, backup name, storage size/class, resource requests/limits, and
  optional node affinity in the generated CNPG `Cluster`
- stable pgdrill labels for ownership, drill ID, and source cluster
- derived instance pod and full-recovery job names for future evidence
  collection

The manifest builder expects the caller to provide the selected CNPG `Backup`
resource name and PostgreSQL image. The executor should discover those from the
source cluster and selected backup before rendering the manifest.

Example target config shape:

```yaml
target:
  type: kubernetes
  kubernetes:
    namespace: d003-db
    wait_timeout: 20m
    poll_interval: 5s
    cleanup_pvc: true
    cleanup_on_fail: true
  cnpg:
    source_cluster: altbox
    backup_name: altbox-backup-20260707
    image_name: ghcr.io/cloudnative-pg/postgresql:16
    storage_size: 20Gi
    cpu_request: 500m
    memory_request: 1Gi
```

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
