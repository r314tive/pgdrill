# Doctor And Preflight

`pgdrill doctor` is a read-only environment preflight. It loads the same strict
YAML or JSON config as the execution commands, derives the external executables
that the selected target will use, and runs a bounded native version command for
each distinct binary.

```sh
pgdrill doctor -f pgdrill.yaml
pgdrill doctor -f pgdrill.yaml -format json
pgdrill doctor -f pgdrill.yaml -timeout 10s
```

## Scope

For `target.type: local`, doctor requires a complete drill config and checks:

- the selected provider client
- `postgres`
- `pg_verifybackup` when restore verification is enabled
- every client required by the expanded probe list

For `target.type: kubernetes`, doctor follows the implemented CNPG target
verification path and checks:

- `kubectl` in client-only mode
- every client required by the expanded probe list

Provider configuration is not checked for the Kubernetes target because
`pgdrill target verify` restores from a CNPG `Backup` resource and does not call
the configured provider adapter. The unimplemented `container` target fails
preflight explicitly.

Probe presets are expanded before requirements are calculated. Repeated uses of
the same tool and binary are merged into one command while preserving the list
of components that depend on it.

## Read-Only Boundary

Doctor does not discover backup catalogs, contact PostgreSQL, query the
Kubernetes API, create resources, or test credentials. A passing result means:

- the config is accepted for the implemented target path
- every required executable can be started
- every native version command exits successfully within the timeout
- requested and resolved executable paths, output, timing, and exit status were
  captured as redacted command evidence

It does not prove repository access, WAL continuity, restore correctness,
PostgreSQL startup, probe success against a server, or production compatibility.
Those claims require provider checks and a completed restore drill.

The version invocations follow the upstream command contracts: WAL-G and Barman
use `--version`; pgBackRest and pg_probackup use their `version` commands;
PostgreSQL client/server binaries use `--version`; and kubectl uses
`version --client --output=json`. See the upstream references for
[WAL-G](https://wal-g.readthedocs.io/),
[Barman](https://docs.pgbarman.org/release/3.17.0/user_guide/commands.html),
[pgBackRest](https://pgbackrest.org/command.html),
[pg_probackup](https://postgrespro.github.io/pg_probackup/),
[PostgreSQL clients](https://www.postgresql.org/docs/current/reference-client.html),
and [kubectl](https://kubernetes.io/docs/reference/kubectl/generated/kubectl_version/).

## Output Contract

Text is the default operator view. JSON output uses the schema identifier:

```text
pgdrill.doctor/v1alpha1
```

The top-level object identifies the pgdrill build, cluster, effective provider,
and target, then contains `status`, timestamps, normalized checks, and the
redacted command evidence supporting each check. A missing binary, timeout, or
non-zero version command produces `status: failed`, but doctor continues through
the remaining tools. Cancellation produces the partial result with
`status: aborted`.

Exit codes match the rest of the CLI:

- `0`: every tool check passed
- `1`: invalid config or one or more tool checks failed
- `2`: invalid CLI usage
- `130`: interrupted or canceled
