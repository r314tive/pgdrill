# Recovery Policy

Recovery policy turns measured drill facts into explicit, machine-readable
assertions. It is part of the immutable drill spec, so changing a limit or a
required outcome changes the spec digest and creates different execution
intent.

## Configuration

All assertions are opt-in. A duration must use Go duration syntax and be at
least `1ms`.

```yaml
policy:
  maximum_rto: 2h
  maximum_rpo: 15m
  maximum_backup_age: 24h
  require_recovery_target: true
  require_cleanup: true
```

An omitted assertion produces an explicit `not_configured` verdict. It is not
reported as `passed`. A configured assertion with insufficient evidence
produces `unknown`, and both `unknown` and `failed` block a passed drill.

## Evaluation Contract

Every current producer writes
`pgdrill.recovery-policy-evaluation/v1alpha1`. It contains one verdict in fixed
order for each assertion:

- `rto`
- `rpo`
- `backup_age`
- `recovery_target`
- `cleanup`

Each verdict records whether it was required, its status, a finite evidence
basis, the configured duration limit where applicable, a typed observation,
and diagnostic text. The evaluation also stores the exact
`recovery_proven_at` fact when recovery completed. Consumers must use the typed
fields and must not parse the message.

### RTO

RTO is measured from `DrillResult.started_at` until PostgreSQL has started and
all required post-restore probes have passed. Cleanup and report persistence
happen after that recovery proof and are not included in RTO. Command and
readiness timeouts remain safety bounds; they are not substituted for the
measured RTO verdict.

### Backup Age

Backup age is the interval from the selected backup's `finished_at` to drill
start. A missing finish timestamp is `unknown`. A measured age above the limit
is `failed`.

### RPO

RPO evaluation is deliberately evidence-sensitive:

| Recovery target | Observation | Result semantics |
| --- | --- | --- |
| `timestamp` | drill start minus requested timestamp | Exact pass/fail after successful recovery proof |
| `latest` | selected backup finish | Conservative lower bound; within limit passes, older than the limit is `unknown` because newer archived WAL may exist |
| `immediate` | selected backup start | Conservative lower bound with the same fail-closed rule |
| `lsn`, `xid`, `restore_point` | no verified timestamp mapping | `unknown` |

The engine never turns an old base backup into an RPO failure when it lacks the
timestamp of the newest recoverable WAL. That would confuse backup age with
recovery point age. It also never turns that uncertainty into a pass.

### Recovery Target

The recovery-target assertion passes only after the restore plan or managed
target has confirmed the same canonical target, PostgreSQL has started, and all
required post-restore probes have passed. Its basis is
`post_restore_probes`. This proves a runnable database under the requested
PostgreSQL recovery contract; source-currentness and temporal distance remain
separate RPO assertions.

Managed targets must echo the exact recovery target they applied. The current
CNPG adapter supports only plain `latest` recovery. Timestamp, LSN, XID,
restore-point, timeline, and inclusive CNPG intent are rejected before target
creation until the manifest adapter implements and verifies those fields.

### Cleanup

Cleanup passes when the attempt has no owned target or has a terminal
successful `target_cleanup` checkpoint. A failed checkpoint is `failed`; a
missing or uncertain checkpoint after target activity is `unknown`.

This assertion verifies completion of the configured target cleanup contract.
It does not override target retention settings. For example,
`target.remove_work_dir: false` may retain a stopped local work directory, and
`cleanup_pvc: false` may retain PVCs while the CNPG Cluster cleanup succeeds.
Use target configuration or a future TargetPool policy to define what must be
deleted, then use `require_cleanup` to require that configured teardown to
finish successfully.

## Terminal Status And Presentation

Policy is evaluated after target cleanup on the `policy_evaluation` lifecycle
stage. A blocking verdict produces top-level `status: failed` and structured
failure stage `policy_evaluation`. If another stage already failed, available
policy facts are still recorded without replacing the primary failure stage.

`pgdrill report show` prints the typed verdicts. Prometheus export uses bounded
assertion, status, and basis labels:

- `pgdrill_policy_verdict_info`
- `pgdrill_policy_limit_seconds`
- `pgdrill_policy_observed_seconds`
- `pgdrill_policy_satisfied`
- `pgdrill_recovery_proven_timestamp_seconds`

Run IDs, messages, backup IDs, and evidence IDs are not metric labels.
