# Run Event Format

`pgdrill.run-event/v1alpha1` is the append-only lifecycle contract emitted by
the engine when an `EventSink` is configured. It complements the terminal
`pgdrill.report/v1alpha1` report; it does not replace that report.

The standalone CLI does not persist an event journal by default yet. The
schema and engine delivery boundary exist so a later local journal or control
plane can consume the same lifecycle without parsing logs or wrapping CLI
output.

## Identity And Ordering

- `run_id` identifies one logical drill.
- `attempt_id` identifies one execution attempt of that run.
- `spec_digest` binds the attempt to the immutable drill input used by the
  terminal report.
- `sequence` starts at 1 and increases only after the configured sink accepts
  an event durably.
- Ordering is defined only within one attempt.
- `occurred_at` is UTC and records when the engine constructed the event, not
  when a downstream consumer processed it.

An event sink must return an error only when the event was not durably
accepted. This permits the engine to reuse the same sequence number safely.
Unknown write outcomes require sink-side idempotency and reconciliation before
a networked event store can be considered production-ready.

## Event Types

| Type | Required fields | Meaning |
| --- | --- | --- |
| `run_started` | run identity, sequence, time | The attempt entered the engine lifecycle. |
| `stage_started` | identity, sequence, time, `stage` | The stage was recorded before its operation was invoked. |
| `stage_completed` | identity, sequence, time, `stage`, `outcome` | The operation finished with `succeeded`, `failed`, or `aborted`. |
| `run_finished` | identity, sequence, time, terminal `status` | The engine reached `passed`, `failed`, or `aborted`. |

The finite stage vocabulary is shared with structured report failures. The
engine emits only stages that apply to the selected native or managed-target
path.

## Failure Semantics

A configured event sink is part of execution correctness:

- failure to record `stage_started` prevents that stage's normal side effects
- cleanup still executes when its start event cannot be recorded
- a cleanup completion event is suppressed when its start event was rejected,
  preventing an orphaned stage transition in the accepted stream
- if `run_started` is rejected, later events are suppressed so an accepted
  stream cannot begin with a terminal-only record
- failure to record a passed terminal event changes the result to `failed`,
  rewrites an already persisted report, and attempts a failed terminal event
  with the same unaccepted sequence number
- a rejected failed or aborted terminal event is retried once with the same
  status and unaccepted sequence number
- cancellation during cleanup remains `aborted` even when cleanup itself
  succeeds through a detached finalization context

The report sink and event sink are not a distributed transaction. Durable
multi-process or remote delivery therefore remains behind the future
idempotency and reconciliation gate described in
[ADR 0001](adr/0001-engine-v0.2-and-control-plane-boundary.md).

## Compatibility

Consumers must reject unknown schema versions and unknown enum values. Current
emitters populate `spec_digest` on every event; validators retain compatibility
with early `v1alpha1` events where this additive field was absent. Additive
optional fields may be introduced within `v1alpha1`; incompatible identity,
ordering, or state-transition changes require a new schema version.

Messages and attributes are diagnostic context, not machine-parsed protocol.
They must contain only redacted, bounded values and must not contain resolved
secrets.
