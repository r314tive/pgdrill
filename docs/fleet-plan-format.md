# Fleet And Plan Formats

The daemon-free planner compiles a strict, secret-free
`pgdrill.fleet/v1alpha1` inventory into a deterministic
`pgdrill.plan/v1alpha1` document. It does not contact a backup repository,
resolve credentials, start an executor, or mutate a restore target.

These are pre-GA contracts. Unknown fields and unknown schema versions are
rejected, and every fleet document must declare `schema_version` explicitly.
The GA compatibility and migration floor remains a release gate in [the v1.0
contract](v1.0-release-contract.md).

## Commands

```sh
pgdrill plan validate -f examples/fleet.yaml
pgdrill plan show -f examples/fleet.yaml
pgdrill plan show -f examples/fleet.yaml -format json
```

`plan validate` returns exit code `1` when structural compilation fails or any
selected source cannot be placed. `plan show` prints a structurally valid plan,
including placement rejections, so an operator can diagnose a partial or empty
expansion. Neither command performs infrastructure mutation.

## Fleet Resources

The current inventory contains five typed resource lists.

### Sources

A source records:

- stable `id`, operator-owned immutable `revision`, and labels
- `native` or `managed` engine mode
- logical PostgreSQL cluster
- source driver and optional native provider
- execution pool where repository access and restore execution can occur

Native sources require a known provider and a matching source driver. Managed
sources can use a target-system driver such as `cnpg`.

### Target Pools

A target pool has one immutable revision and execution pool. Each concrete
target records:

- stable ID, immutable revision, and placement labels
- engine target type and target driver
- non-secret target settings such as an owned local work directory
- explicit capacity for one compiled plan
- supported engine modes and source drivers

The current in-process engine requires source and target execution pools to
match. Compatibility must also be explicit in `modes` and `source_drivers`.
For native execution, the target driver must match its canonical target type.

### Probe Profiles

A probe profile snapshots an ordered list of canonical probe types and names.
An omitted name receives the canonical default. Probe execution details and
credentials remain executor-local configuration; planner output carries the
profile identity, revision, and required descriptors.

### Recovery Policies

A policy binds:

- compatible engine modes
- `latest_available` or exact `backup_id` selection
- one canonical recovery target
- RTO, RPO, backup-age, recovery-target, and cleanup assertions

The compiled engine spec contains the resolved policy values. The plan also
retains the policy ID and revision because the current internal drill spec does
not expose a separate policy reference.

### Drill Sets

A drill set selects sources, names one target pool, optionally narrows targets,
and references one probe profile and recovery policy. Source selectors require
at least an ID list or exact label matches. Target selectors may be empty,
which means every target in the named pool.

ID list values use OR semantics. IDs and `match_labels` are combined with AND
semantics. Expression languages, arbitrary scripts, and implicit regular
expressions are intentionally unsupported.

The complete runnable shape is in
[`examples/fleet.yaml`](../examples/fleet.yaml).

## Placement And Bounds

Resources and selector matches are normalized and sorted before compilation.
For each selected source, the planner chooses the compatible target with the
lowest current assignment count and uses target ID as the deterministic
tie-breaker. Capacity is consumed across all drill sets in one plan.

Two independent bounds apply:

- fleet `max_runs` limits the complete expansion
- drill-set `max_runs` limits one selector expansion

Both default to the fleet bound, whose default is 100 and hard maximum is
10,000. Exceeding a bound fails compilation instead of emitting a silently
truncated plan. A source with no compatible remaining target produces a typed
rejection and no run.

`mutation_count` equals the number of disposable restore-target attempts the
plan would authorize. It does not claim that every engine attempt contains only
one low-level provider, filesystem, or Kubernetes operation.

## Determinism And Identity

The planner canonicalizes the normalized fleet through JSON and records its
SHA-256 `input_digest`. Declared list order does not change the digest or
placement. Probe order remains semantic and is preserved.

Every planned run contains:

- deterministic logical `run_id`
- drill-set, source, target-pool, target, policy, and probe references with
  immutable revisions
- concrete canonical `pgdrill.drill-spec/v1alpha1`
- independently validated `spec_digest`

The plan has its own canonical SHA-256 digest. Recompiling equivalent normalized
input produces byte-equivalent identities and placement. A retry of one
planned run uses the same logical run and spec digest with a new attempt ID.

## Rejections

The current rejection codes are:

| Code | Meaning |
| --- | --- |
| `no_sources` | The source selector matched no inventory source. |
| `policy_mode_mismatch` | The referenced recovery policy does not allow the source mode. |
| `no_compatible_target` | No selected target satisfies execution-pool, mode, driver, and remaining-capacity constraints. |

Structural errors such as duplicate IDs, missing references, invalid recovery
targets, unknown fields, or expansion-limit overflow fail compilation directly.

## Security Boundary

The fleet schema has no command environment, credential, secret-reference
payload, or provider-specific executable configuration. Revisions bind
operator-managed configuration without embedding it. Executors remain
responsible for resolving credentials locally and verifying that the resolved
component still matches the planned revision.

Fleet and plan documents are durable operator-visible evidence. Do not place
credentials or tokens in IDs, labels, revisions, cluster names, execution-pool
names, or paths; free-form values are validated for shape, not classified as
secrets.

The planner is not yet a scheduler. Schedules, leases, concurrency across
separate plans, remote executors, spread constraints, and controller APIs remain
future control-plane work.
