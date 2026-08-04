# Yandex Cloud Demo User Runbook

This runbook is for an invited participant using the bounded administrator
account on the pgdrill Yandex Cloud demo. It covers inspection and execution of
the prepared WAL-G and pgBackRest recovery drills. Infrastructure provisioning,
backup preparation, repair, and teardown remain operator responsibilities.

The environment contains synthetic data only. It is a technical demo, not a
production deployment or a benchmark.

## Access Details

Obtain these values from the demo operator through an agreed secure channel:

- your dedicated SSH login;
- the runner public IP address;
- the source private IP address;
- the private-key path on your workstation;
- the runner and source SSH host-key fingerprints;
- confirmation that your current public IP is allowlisted.

Do not share the private key or use another participant's login. The demo does
not require a Yandex Cloud console account.

Set local shell variables for the examples below:

```sh
export PGDRILL_DEMO_USER="your-login"
export PGDRILL_DEMO_RUNNER="runner-public-ip"
export PGDRILL_DEMO_KEY="$HOME/.ssh/your-demo-key"
chmod 600 "$PGDRILL_DEMO_KEY"
```

## Connect

Connect to the public runner:

```sh
ssh -i "$PGDRILL_DEMO_KEY" \
  -o IdentitiesOnly=yes \
  "$PGDRILL_DEMO_USER@$PGDRILL_DEMO_RUNNER"
```

For reliable source access through `ProxyJump`, add aliases like these to
`~/.ssh/config`. Replace every uppercase placeholder; SSH config does not
expand the shell variables above.

```sshconfig
Host pgdrill-demo-runner
  HostName RUNNER_PUBLIC_IP
  User YOUR_LOGIN
  IdentityFile ~/.ssh/YOUR_DEMO_KEY
  IdentitiesOnly yes

Host pgdrill-demo-source
  HostName SOURCE_PRIVATE_IP
  User YOUR_LOGIN
  IdentityFile ~/.ssh/YOUR_DEMO_KEY
  IdentitiesOnly yes
  ProxyJump pgdrill-demo-runner
```

On the first connection to each VM, compare the displayed host-key fingerprint
with the value supplied by the operator before accepting it. Stop and contact
the operator if an established host key changes unexpectedly.

Confirm the bounded account after login:

```sh
id
sudo -n -l
```

The account has no password and no general sudo access. It may run only the
fixed pgdrill demo commands listed below as the `postgres` operating-system
user. It can read retained demo reports through the reporting group but cannot
write them. Repository files, credentials, restore work directories, and the
restored PostgreSQL ports are not directly exposed to the account.

## Read-Only Inspection

Run preflight checks on the runner before starting a drill:

```sh
sudo -u postgres /usr/local/sbin/pgdrill-demo-doctor
sudo -u postgres /usr/local/sbin/pgdrill-demo-pgbackrest-doctor
```

Each command must exit successfully and print `Status  passed`. A successful
doctor result proves that the configured pgdrill, provider, PostgreSQL, and
probe executables are available. It does not prove that a backup can be
restored.

Inspect the latest completed reports, when present:

```sh
sudo -u postgres /usr/local/sbin/pgdrill-demo-report
sudo -u postgres /usr/local/sbin/pgdrill-demo-pgbackrest-report
```

Each report command validates the persisted JSON report before rendering it.
It also prints a Prometheus projection after the human-readable result.

To inspect the prepared source and backup catalog, connect through the runner
using the SSH alias configured above:

```sh
ssh pgdrill-demo-source
```

Then run either read-only status command:

```sh
sudo -u postgres /usr/local/sbin/pgdrill-demo-source-status
sudo -u postgres /usr/local/sbin/pgdrill-demo-pgbackrest-source-status
```

The status output includes the current source row count, the post-backup WAL
sentinel count, the last archived WAL name, and the provider-native backup
catalog. The prepared scenario should show 101 source rows and one sentinel.
The source account cannot create a new backup or change PostgreSQL.

## Run A Drill

Run only one provider at a time and coordinate the start with the operator or
other participants. A global lock rejects concurrent demo runs.

WAL-G:

```sh
sudo -u postgres /usr/local/sbin/pgdrill-demo-run
```

pgBackRest:

```sh
sudo -u postgres /usr/local/sbin/pgdrill-demo-pgbackrest-run
```

A successful run performs a real restore into its provider-specific disposable
work directory on the runner, starts PostgreSQL on a loopback-only port,
executes the configured probes and policy evaluation, shuts PostgreSQL down,
and proves owned cleanup. The runner mounts the backup repository read-only, so
a drill cannot modify the prepared backup set.

Every structurally valid terminal report is atomically copied to the provider's
`current` path, including a report whose status is `failed`. This makes the
latest observed outcome visible instead of leaving a stale success in place.
The immutable run-specific report and its checksum remain the evidence source
of truth.

Do not interrupt a run unless the operator asks you to. If the command exits
with an error, do not delete the work directory or repeatedly retry: the
failure may require checkpoint inspection and owner-only recovery.

## Read The Result

A successful current demo result has all of these properties:

- `Status` is `passed` and `Provider` matches the command you ran;
- the check summary contains no failed checks;
- `post_backup_wal_replayed` passed, proving recovery of the 101st row written
  after the base backup and delivered through archived WAL;
- the required `rto`, `rpo`, `backup_age`, `recovery_target`, and `cleanup`
  policy verdicts passed;
- every recorded operation is terminal and successful;
- target cleanup is successful and reconciled.

The configured time limits are demo policy values, not customer SLOs. A passed
result proves this recovery claim for the selected backup, this configuration,
this target, and this run. It does not prove that every backup is restorable or
that the same timing applies to production data volumes.

After a run, render the result again without repeating the restore:

```sh
sudo -u postgres /usr/local/sbin/pgdrill-demo-report
sudo -u postgres /usr/local/sbin/pgdrill-demo-pgbackrest-report
```

## Failure Handling

Stop after the first failed gate and preserve the complete terminal output.
Record the command, UTC timestamp, and exit status:

```sh
status=$?
printf 'exit_status=%s utc=%s\n' "$status" "$(date -u +%FT%TZ)"
```

Common failures:

| Symptom | Meaning | Action |
| --- | --- | --- |
| SSH connection times out | The current source IP is probably not allowlisted, or the runner is unavailable. | Send your current public IP to the operator. |
| `Permission denied (publickey)` | The login, private key, or installed public key does not match. | Check the supplied login and `IdentityFile`; contact the operator. |
| SSH host key changed | The VM may have been replaced, or the connection may be unsafe. | Stop and verify the new fingerprint with the operator. |
| `not allowed to execute` from sudo | The command differs from the fixed allowlist. | Use the exact command from this runbook. |
| `no current demo report exists` | That provider has not completed a report-producing run on the current VM. | Run doctor, then ask the operator whether the scenario is prepared. |
| `another pgdrill demo run is active` | Another participant or provider run holds the global lock. | Wait for it to finish; do not bypass the lock. |
| `owned restore work directory still exists` | A previous attempt needs owner inspection or recovery. | Stop and notify the operator; do not remove it. |
| Report or run exits non-zero | A recovery gate failed or evidence could not be validated. | Preserve output and stop; do not claim successful recovery. |

## End The Session

Run `exit` once in each remote session you opened:

```sh
exit
```

No manual cleanup is expected from an invited participant. The operator owns
evidence export, incident inspection, infrastructure repair, and final
Terraform teardown.
