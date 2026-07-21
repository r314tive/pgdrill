# Customer Discovery And Pilot Gate

The first customer discussion should select one bounded restore claim. It
should not attempt to design the complete fleet control plane.

## Required Inputs

- PostgreSQL deployment type, major version, data size, and change rate.
- Backup provider, exact version, repository backend, backup modes, schedule,
  retention, encryption, and observed failure history.
- WAL/archive destination, retention boundary, and current continuity checks.
- Recovery target that matters: latest, timestamp, LSN, XID, or restore point.
- Existing restore procedure, last successful restore date, duration, and
  manual steps.
- Disposable target constraints: VM, container, Kubernetes/CNPG, network,
  storage, extensions, locales, and PostgreSQL image or packages.
- Required application proof: critical databases, extensions, schema objects,
  row-level invariants, and queries that are safe on restored data.
- Approved RTO, RPO, maximum backup age, cleanup requirement, and evidence
  retention.
- Access model for the executor, backup repository, secrets, target, and
  customer observers.
- Data classification rules: which command output and query results must never
  leave the customer environment.

## Pilot Contract

One pilot should have all of these before execution:

- one named source and one backup provider;
- one immutable, secret-free drill spec;
- one approved recovery target and backup-selection rule;
- one isolated target with explicit ownership and cleanup boundaries;
- at least one data-level post-backup recovery assertion;
- measured command deadlines distinct from the RTO assertion;
- named customer and operator owners;
- a report-retention and redaction decision;
- an agreed failure-escalation path;
- acceptance criteria that can be evaluated from a terminal report.

## Acceptance Criteria

The pilot is technically successful only when:

- preflight records exact tools and versions before repository access;
- the selected backup and requested recovery target are visible in evidence;
- the target starts and every required probe passes;
- every required recovery-policy verdict is `passed`;
- target cleanup is evidenced and no owned resource is left ambiguous;
- the report validates through `pgdrill report show`;
- the customer can distinguish confirmed scope from untested compatibility.

Commercial or roadmap interest is not a technical acceptance criterion.

## Follow-Up Decision

After the first run, choose one next step from observed friction:

- add a compatibility point for the customer's provider/storage/version;
- add a target adapter required by the customer's isolation model;
- add a reusable probe profile for application invariants;
- add local run history and planning because repeated fleet execution is now
  demonstrated;
- stop if the workflow does not solve an operationally owned problem.

Do not start a web UI solely because observers want convenient access to one
demo. A multi-user interface becomes justified when repeated runs establish
real requirements for scheduling, RBAC, audit, comparison, and retention.
