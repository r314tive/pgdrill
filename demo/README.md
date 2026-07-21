# Technical Demo Baseline

The first pgdrill product demo is an evidence-led restore drill, not a slide
deck and not a simulated web platform. It uses synthetic data and an isolated
target to show the complete chain from backup discovery to a policy-checked
terminal report.

## What The Demo Proves

- pgdrill discovers a real backup through the provider's native catalog.
- The provider validates the WAL chain selected for recovery.
- A disposable PostgreSQL target is restored and started independently from
  the source.
- A row committed only after the base backup is recovered from archived WAL.
- Readiness, SQL, `pg_amcheck`, and schema-dump probes execute against the
  restored server.
- The result retains exact versions, commands, timings, checks, operation
  checkpoints, policy verdicts, and cleanup evidence.
- An invited administrator can rerun the fixed drill and inspect the report
  without receiving unrestricted sudo.

## What It Does Not Prove

- Production readiness, availability, or a customer-approved RTO/RPO.
- Compatibility outside the exact versions, storage backend, platform, and
  recovery target exercised by the run.
- Yandex Object Storage, encryption, incremental backups, or timestamp PITR.
- Application correctness beyond the explicit probes in the drill spec.
- A hosted control plane, tenant isolation, RBAC administration, or a web UI.

## Demo Shape

The initial hosted baseline is
[WAL-G on isolated Yandex Cloud VMs](yandex-cloud/README.md). It deliberately
keeps the engine and the demo infrastructure separate: the engine remains an
ordinary CLI artifact, while Terraform and shell scripts only provision and
adapt the disposable environment.

A useful 25-minute session is:

1. Show the source/repository/runner boundaries and exact component inventory.
2. Show 100 rows at base-backup time and the separately archived row 101.
3. Run read-only `pgdrill doctor`, then the complete restore drill.
4. Inspect checks, RTO/RPO evidence, failure stage, raw command evidence, and
   target cleanup in the JSON report.
5. Map the same workflow to one concrete customer backup topology.

Do not spend the session walking through source code or presenting future UI
mockups. The report and the restored-data assertion are the product proof.

## Health Check Boundary

A conventional PostgreSQL health check assesses the currently running system:
configuration, capacity, security, query behavior, and operational risks.
pgdrill answers a narrower question that those observations cannot prove:
whether an existing backup can actually be restored to a required point and
pass explicit post-restore checks.

The useful integration boundary is therefore:

- health check identifies backup policy, topology, critical databases, and
  application invariants;
- pgdrill turns those requirements into executable recovery policy and probes;
- the drill report becomes evidence and a remediation input for the health
  check or follow-up audit.

Use [customer-discovery.md](customer-discovery.md) to keep the first customer
conversation scoped to one testable recovery claim.
