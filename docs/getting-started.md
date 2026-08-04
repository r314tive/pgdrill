# Getting Started

This guide takes a PostgreSQL operator from an installed pgdrill binary to one
reviewed local restore drill. It assumes an existing backup produced by WAL-G,
Barman, pgBackRest, or pg_probackup.

pgdrill does not create the source backup. The first drill should use synthetic
or explicitly approved data and an execution host that is separate from the
source PostgreSQL server.

## 1. Choose The Execution Host

Use a dedicated VM, container host, or similarly isolated worker. The worker
needs:

- the pgdrill binary;
- the selected provider's client executable and configuration;
- PostgreSQL server and client binaries compatible with the restored backup;
- probe tools such as `pg_isready`, `pg_amcheck`, and `pg_dump` when configured;
- read access to the backup repository and WAL archive;
- an exclusive private work directory and loopback ports for restored servers;
- enough free storage for the restored cluster, replayed WAL, reports, and a
  deliberate failure margin.

Do not use a busy PostgreSQL host merely because it already has the client
tools. A restore can consume substantial disk, memory, I/O, and process slots.
Size from the restored data volume rather than the compressed backup size.

Run pgdrill as a dedicated, unprivileged operating-system account whenever the
provider and PostgreSQL tools permit it. The account must own its work, report,
history, and artifact paths; it should not own the source cluster or repository.

## 2. Install And Verify The Binary

Download the archive for the required OS and architecture from
[GitHub Releases](https://github.com/r314tive/pgdrill/releases). Verify it with
the checksum file from the same release before extracting it. Do not mix an
archive and checksum file from different tags.

Building from source requires the Go version in [`.go-version`](../.go-version):

```sh
git clone https://github.com/r314tive/pgdrill.git
cd pgdrill
make build
./bin/pgdrill version
```

For a system installation, copy the verified binary to a root-owned executable
path such as `/usr/local/bin/pgdrill`. Installing pgdrill does not install the
provider or PostgreSQL toolchain.

## 3. Create A Private Configuration

Generate the starter file as the execution account:

```sh
install -d -m 0700 "$HOME/.config/pgdrill"
pgdrill sample-config >"$HOME/.config/pgdrill/pgdrill.yaml"
chmod 0600 "$HOME/.config/pgdrill/pgdrill.yaml"
```

Treat the generated YAML as a schema example, not as a valid production policy.
Review every item below:

1. `cluster.name` identifies the recovery claim, not necessarily a DNS name.
2. `provider.type`, `provider.binary`, native configuration, repository, and
   environment refer to the intended backup system.
3. Provider-native integrity verification is enabled where the topology can
   support it. A disabled example setting is not a passed check.
4. `target.work_dir` is an exclusive disposable path. It must not be a live
   `PGDATA`, backup repository, symlink, mount shared with an application, or
   directory another cleanup process owns.
5. `recovery.target` is explicit: latest, timestamp, LSN, XID, or restore point
   as supported by the selected provider.
6. Probes encode the invariants that make the restored database useful. A
   bare `SELECT 1` proves connectivity, not application recovery.
7. Policy limits reflect an approved recovery expectation rather than values
   copied from an example.
8. `report.path` is private, durable, and outside the disposable work directory.

Use the process environment or the deployment platform's secret mechanism for
credentials. Do not commit credentials, private keys, connection passwords, or
unredacted customer reports to the repository.

See [configuration.md](configuration.md) for provider-specific fields and
[probes.md](probes.md) for reusable probe presets.

## 4. Run Read-Only Preflight

`doctor` parses and validates the configuration, resolves required executable
paths, and captures tool versions. It does not inspect the provider catalog,
contact Kubernetes, restore data, or create a target.

```sh
pgdrill doctor -f "$HOME/.config/pgdrill/pgdrill.yaml"
pgdrill doctor -f "$HOME/.config/pgdrill/pgdrill.yaml" -format json
```

Stop on a non-zero exit. Fix the missing or incompatible dependency before
continuing; do not use a symlink or wrapper to falsify a version check.

## 5. Inspect The Backup Catalog

Catalog discovery executes the provider's native read path and normalizes the
result without creating a restore target:

```sh
pgdrill catalog list -f "$HOME/.config/pgdrill/pgdrill.yaml"
pgdrill catalog list -f "$HOME/.config/pgdrill/pgdrill.yaml" -format json
```

Confirm the provider, backup identifier, completion status, timestamps,
PostgreSQL version, and recovery metadata. An empty, malformed, or ambiguous
catalog is a failed gate; it is not permission to guess a backup identifier.

## 6. Execute The First Drill

Before the first mutation, independently confirm:

- the source environment and repository are the intended test scope;
- the work directory is absent or contains only an owned recoverable attempt;
- the restored PostgreSQL ports bind only to the intended interface;
- no other drill uses the same target, report, history, or artifact paths;
- the available disk and memory margin remains sufficient for failure cleanup.

Then run:

```sh
pgdrill run \
  -f "$HOME/.config/pgdrill/pgdrill.yaml" \
  -run-id "manual-$(date -u +%Y%m%dT%H%M%SZ)"
```

The command discovers and selects a backup, validates the provider-specific
recovery path, restores into the owned target, starts PostgreSQL, runs probes,
evaluates policy, stops PostgreSQL, proves cleanup, and atomically writes the
terminal report.

Do not run a second attempt concurrently against the same paths. Do not remove
a leftover target manually after a failed or interrupted drill; inspect its
operation checkpoints and use the documented recovery protocol.

## 7. Accept Or Reject The Result

Render and validate the persisted report:

```sh
pgdrill report show ./pgdrill-report.json
pgdrill report metrics ./pgdrill-report.json
```

A recovery claim is successful only when all required facts agree:

- terminal drill status is `passed`;
- selected backup and recovery target match the intended claim;
- provider-native validation has the expected status;
- every required probe passed against the restored server;
- RTO, RPO, backup-age, recovery-target, and cleanup policy verdicts passed;
- mutation operations reached terminal, internally consistent states;
- owned target cleanup succeeded and absence was reconciled.

`unknown`, `not_configured`, missing evidence, or a skipped check is not a pass
unless the policy and documented topology explicitly permit that status. A
passed report applies only to this backup, configuration, target, toolchain,
and execution. It does not prove every backup or future run.

## 8. Add Durable History For Automation

Direct execution is journal-free by default. Scheduled operation should use a
private local history store and unique attempt identities:

```sh
run_id="nightly-main-$(date -u +%Y%m%d)"
attempt_id="${run_id}-$(date -u +%H%M%S)"

pgdrill run \
  -f "$HOME/.config/pgdrill/pgdrill.yaml" \
  -run-id "$run_id" \
  -attempt-id "$attempt_id" \
  -history-dir "$HOME/var/pgdrill/history"

pgdrill history verify -store "$HOME/var/pgdrill/history"
```

Schedule only after two consecutive manual runs pass and cleanup has been
independently checked. Serialize drills that share a repository throttle,
restore host, target path, or report destination.

See [operator-guide.md](operator-guide.md) before defining retention,
monitoring, interrupted-attempt recovery, or upgrades.

## Exit Codes

| Code | Meaning |
| --- | --- |
| `0` | Command or drill completed successfully. |
| `1` | Operational, validation, or recovery failure. |
| `2` | Invalid command-line usage. |
| `130` | Operation was interrupted or its context was canceled. |

Automation must evaluate both the exit code and the validated terminal report.
Never turn a missing report into a synthetic success.

## Next Steps

- [Operator guide](operator-guide.md)
- [Recovery policy](recovery-policy.md)
- [Report format](report-format.md)
- [Compatibility boundary](compatibility.md)
- [Technical demo](../demo/README.md)
