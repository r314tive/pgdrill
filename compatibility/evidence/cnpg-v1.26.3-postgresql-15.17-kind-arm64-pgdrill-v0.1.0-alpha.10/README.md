# CNPG 1.26.3 / PostgreSQL 15.17 / pgdrill v0.1.0-alpha.10

## Validated Scope

On 2026-07-22, the deterministic macOS arm64 archive for pgdrill
`v0.1.0-alpha.10` at commit
`a7b92a2aecaf82217bb2bfd9ffe9f52da055954c` completed an end-to-end drill in an
isolated KinD 0.31.0 cluster running Kubernetes 1.32.11, CloudNativePG 1.26.3,
PostgreSQL 15.17, and a disposable MinIO repository.

The source cluster took a real `barmanObjectStore` backup after 100 rows. A
101st sentinel row was committed and archived afterward. pgdrill discovered
the completed backup and source image, created an ownership-scoped verify
cluster, recovered the sentinel transaction, passed readiness, SQL,
`pg_amcheck`, and schema-only `pg_dump`, captured the exact generated manifest,
passed all required policy verdicts, and removed the verify Cluster and PVC.

`runtime.txt` records checksums and immutable image digests. `source-state.txt`
records the completed backup, sentinel WAL, source row count, and resolved
operator/PostgreSQL image IDs. The retained report predates the explicit
`tool.postgres` CNPG preflight added for the Engine v0.2 candidate; it contains
four passed PostgreSQL 15.17 client-tool checks from the restored `postgres`
container. The versioned integration harness now requires the server
executable check as well.

Retained files:

- `report.json`: validated pgdrill terminal report
- `runtime.txt`: exact build, archive, KinD, kubectl, manifest, and image data
- `source-state.txt`: source row/WAL boundary and resolved image IDs
- `SHA256SUMS`: checksums for this evidence directory

This observation covers one disposable KinD/MinIO environment, one
`barmanObjectStore` backup, latest recovery, and the exact versions above. It
does not establish production Kubernetes, other storage classes or object
stores, PITR, failure-mode coverage, performance, or an RTO guarantee. It also
does not validate pgdrill's native Barman CLI adapter.
