# Drill Spec Format

`pgdrill.drill-spec/v1alpha1` is the immutable, secret-free input snapshot for
one recovery drill. It is currently an internal engine contract, not a public
cross-process API. Stable external types will move under `api/` only when an
independent controller or executor consumes them.

## Identity Levels

The three identities have different meanings:

- `run_id` identifies one logical drill occurrence.
- `attempt_id` identifies one execution or retry of that run.
- `spec_digest` identifies the canonical drill input.

Run and attempt IDs are not fields in `DrillSpec` and are not part of its
digest. Retrying an unchanged run therefore creates a new attempt ID while
retaining the same spec digest. Changing source, selection, target, recovery,
policy, or probe-profile intent changes the digest.

## Canonical Content

The spec records:

- execution mode: `native` or `managed`
- logical cluster name when configured
- source ID, driver, revision, and native provider identity when applicable
- backup selection: `latest_available` or one canonical `backup_id`
- target ID, driver, revision, and canonical target spec
- normalized recovery target
- normalized recovery policy assertions and duration limits
- probe-profile ID, driver, revision, and ordered probe descriptors

Component revisions bind inline execution configuration without embedding it.
They are not credentials and must not be treated as secret references.

## Canonicalization

`internal/runspec` constructs the canonical JSON used for SHA-256:

- absent schema and selection defaults are materialized
- identity strings are trimmed
- empty maps and slices have one representation
- JSON map keys are sorted by the encoder
- timestamp targets are converted to UTC RFC3339Nano
- LSN values are uppercase
- XID and numeric timeline values use canonical decimal form
- equivalent policy durations use `time.Duration.String()` form
- probe names are resolved and their order is preserved

The digest is encoded as `sha256:<lowercase hex>`. The immutable wrapper owns
defensive copies of maps, slices, pointer values, and canonical bytes. Returned
documents and byte buffers are copies, so caller mutation cannot change a
previously computed digest.

## Secret Boundary

The CLI composition layer fingerprints normalized execution configuration into
component revisions. Before hashing it:

- values of sensitive environment variables are replaced
- values matching configured redaction literals are replaced
- redaction lists themselves are removed
- secret values are never copied into `DrillSpec`, reports, or events

Non-secret repository locators, provider validation options, target settings,
timeouts, and resolved probe definitions affect the appropriate component
revision. Credential rotation under the same execution references does not.

This is an identity contract, not a standalone credential bundle. Executors
still resolve environment and credentials locally.

## Validation Boundaries

Before normal side effects, the engine validates the spec schema, mode,
component references, selection semantics, recovery target, recovery policy,
target type, and probe descriptors against the configured runtime implementations. Native
selection returns the canonical object from the discovered catalog. Managed
resolution must confirm the same backup intent, target type, and probe profile
before target startup.

New terminal reports persist `spec`, `spec_digest`, and `attempt_id`. Report
readers recompute the digest and reject tampering or disagreement with report
cluster, provider, target, recovery target, or exact selected backup. New run
events carry the same digest on every event.

Policy semantics and the versioned evaluation object are defined in
[recovery-policy.md](recovery-policy.md).
