# Operator Guide

This guide defines the operational boundary around recurring pgdrill recovery
drills. It assumes a first manual drill has already passed using the
[getting-started guide](getting-started.md).

pgdrill is an execution engine, not a scheduler, credential store, backup
system, or hosted control plane. Cron, systemd, Kubernetes, or another
orchestrator may launch it, but the engine remains responsible for the restore
lifecycle, evidence, policy evaluation, and owned cleanup.

## Deployment Boundary

Use a dedicated execution identity and, for non-trivial backups, a dedicated
worker. Keep these boundaries explicit:

| Resource | Required access |
| --- | --- |
| Source PostgreSQL | Normally none for a repository-driven restore; provider-native checks that require a PostgreSQL host need separately controlled access. |
| Backup repository | Provider-specific read access. |
| Restore work root | Read, write, create, start, stop, and owned cleanup. |
| PostgreSQL tools | Execute compatible server and client binaries. |
| Report/history/artifact roots | Private read/write access with durable storage. |
| Kubernetes verify target | Scoped API permissions for owned CNPG resources. |

Do not run a restore worker as the operating-system account that owns a live
source cluster unless an independently reviewed topology requires it. Do not
grant general root access merely to avoid defining provider and target
permissions.

## Suggested Filesystem Layout

One deployment can use a layout such as:

```text
/etc/pgdrill/
  cluster-a.yaml                 root-owned configuration, no inline secrets
/var/lib/pgdrill/
  work/cluster-a/                disposable owned target root
  history/                       immutable local run history
  reports/cluster-a/
    current.json                 terminal report
    current.json.checkpoints/    durable mutation checkpoints
    current.json.artifacts/      optional content-addressed evidence blobs
```

Use mode `0700` for private state roots unless a reviewed read-only reporting
group needs access. The report path must not be inside the disposable work
directory. Place the work root on storage sized for the restored cluster and
WAL replay, not on a nearly full root filesystem.

The local target performs marker, path, symlink, permission, process identity,
and absence checks. Those checks limit pgdrill cleanup; they do not make an
incorrectly selected shared filesystem safe.

## Credentials And Redaction

- Resolve credentials at execution time from the environment, mounted secret,
  workload identity, or an approved provider-native mechanism.
- Keep static credentials out of YAML, command arguments, unit files, shell
  history, reports, and source control.
- Restrict config and provider credential files to the execution identity.
- Treat reports as potentially sensitive even though command evidence is
  redacted: object names, cluster identifiers, timings, and probe output may
  still reveal infrastructure details.
- Test custom redaction rules with representative sanitized fixtures before a
  customer run.

Raw provider output is bounded before persistence. Truncation is evidence
metadata, not permission to assume omitted output was successful.

## Scheduling And Concurrency

Choose a schedule from the recovery objective and backup cadence. A drill that
runs less often than the claimed readiness interval cannot establish that
claim continuously.

For every scheduled attempt:

1. generate stable logical `run-id` and unique `attempt-id` values;
2. enable `-history-dir` on durable private storage;
3. give the run an exclusive target and report destination;
4. bound the scheduler runtime above pgdrill's restore and finalization limits;
5. preserve stdout, stderr, exit status, and the terminal report;
6. alert on non-zero exit, missing report, failed status, unknown required
   policy, stale last success, or unresolved cleanup;
7. verify history and any referenced artifacts outside the critical restore
   path.

Serialize work that shares constrained repository bandwidth, a local
PostgreSQL port range, or restore storage. External scheduler retries must use
a new attempt ID; reusing an attempt with mutation checkpoints is rejected
until recovery reconciliation is complete.

## Reports, History, And Artifacts

The terminal report is the primary result. Local history is an optional durable
journal. When a target emits external evidence, the artifact store retains that
content by digest and the report contains its immutable references.

Recommended post-run gates:

```sh
pgdrill report show /var/lib/pgdrill/reports/cluster-a/current.json
pgdrill history verify -store /var/lib/pgdrill/history
```

If the validated report contains artifact references, verify their complete
store against the same complete history scope:

```sh
pgdrill artifact verify \
  -store /var/lib/pgdrill/reports/cluster-a/current.json.artifacts \
  -history-store /var/lib/pgdrill/history
```

Export Prometheus text only from a report that first passes report validation:

```sh
pgdrill report metrics /var/lib/pgdrill/reports/cluster-a/current.json
```

Retention must preserve audit requirements and unresolved attempts. `history
prune` and `artifact gc` are dry-run planning operations by default. Applying a
plan requires its exact digest; artifact GC protects live, audit-classified,
legacy, and history-referenced blobs. Review
[history-format.md](history-format.md) and
[artifact-format.md](artifact-format.md) before enabling either command.

## Failure And Interruption Handling

Stop after the first failed gate. Preserve:

- UTC start and failure timestamps;
- exact pgdrill version and configuration fingerprint;
- run and attempt IDs;
- process exit status and terminal output;
- report, history, checkpoint, and artifact paths;
- available filesystem, process, and provider-native evidence.

Do not delete a restore directory because a process is absent. An uncatchable
executor loss can leave durable mutation intent without a terminal report.
After independently proving that the original executor and its process group
are stopped, use `pgdrill attempt recover` to produce a read-only plan. Apply
only the exact digest-confirmed plan for the same config, run, attempt, history,
and owned target.

See [attempt-recovery.md](attempt-recovery.md) for the complete protocol.

## Upgrade And Rollback

Before changing the binary, provider, PostgreSQL toolchain, config contract, or
local-state schema:

1. stop new scheduling and let active attempts finish;
2. verify history and artifact stores;
3. retain the current binary, checksum, config, and rollback procedure;
4. read [CHANGELOG.md](../CHANGELOG.md) and [upgrade.md](upgrade.md);
5. validate the new binary with `doctor` and catalog discovery;
6. run a controlled drill against a disposable target;
7. resume scheduling only after report and cleanup acceptance.

Pre-GA state migrations are explicit copy operations. Never point a newer
binary at the only copy of an older history store and assume in-place upgrade
behavior.

## Operational Acceptance Checklist

A deployment is ready for recurring use only when all statements are true:

- execution host capacity is measured against the restored data size;
- source, repository, target, report, history, and artifact ownership are
  documented;
- secrets are injected and absent from config, logs, reports, and history;
- `doctor` and catalog gates pass under the scheduled identity;
- two consecutive physical drills pass with independently checked cleanup;
- policy values have named owners and approved meanings;
- monitoring detects failures, stale success, missing reports, and unresolved
  cleanup;
- retention and recovery procedures were rehearsed on disposable state;
- exact provider/PostgreSQL/platform versions are inside a tested compatibility
  cell or are explicitly treated as pilot evidence;
- rollback can restore the previous binary and config without rewriting
  historical evidence.

Passing this checklist establishes an operated pgdrill deployment. It does not
by itself establish customer support, production RTO performance, or a general
provider compatibility range.
