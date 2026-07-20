# Probes

Probes run after the restored PostgreSQL instance starts. They should stay
explicit enough to explain what the drill actually proved.

## Explicit Probes

Explicit probe config remains the most precise form:

```yaml
probes:
  - type: pg_isready
    timeout: 10s
  - type: sql
    name: select_1
    query: "select 1"
    timeout: 10s
  - type: pg_dump
    mode: schema
    timeout: 30s
```

Use explicit probes when binaries, query text, `pg_amcheck` arguments, or
`pg_dump` mode must differ per probe.

Every probe has a command deadline. An omitted timeout defaults to `1h`; set an
explicit value from measured runtime for large `pg_amcheck` or `pg_dump`
checks. See [configuration.md](configuration.md) for the complete deadline
policy.

## Presets

Presets expand into ordinary probes before execution:

- `readiness`: `pg_isready`
- `smoke`: `pg_isready` and SQL `select 1`
- `structural`: `pg_isready`, `pg_amcheck`, and schema-only `pg_dump`

The `pg_dump` probe writes its generated payload to the platform null device.
The command still reads and serializes the selected database objects, but schema
or data contents are not copied into the drill report; only command/status
evidence is retained.

Preset config supports only common fields:

- `name`: optional prefix added to generated probe names
- `timeout`: applied to every generated probe
- `redact_values`: copied to every generated probe

Example:

```yaml
probes:
  - preset: smoke
    name: quick
    timeout: 10s
```

This expands to `quick_pg_isready` and `quick_select_1`.
