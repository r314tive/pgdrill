# CNPG 1.29.2 / Barman Cloud Plugin 0.13.0 / PostgreSQL 15.17 / pgdrill v0.3.0-alpha.6

## Validated Scope

On 2026-07-28, the deterministic macOS arm64 archive for pgdrill
`v0.3.0-alpha.6` at commit
`60ff0349c3e38cf1934686c2cadcbb0dffe387e7` completed an end-to-end drill in an
isolated KinD 0.31.0 cluster running Kubernetes 1.35.0, CloudNativePG 1.29.2,
Barman Cloud Plugin 0.13.0, cert-manager 1.21.0, PostgreSQL 15.17, and a
disposable MinIO repository. The release archive SHA-256 was
`0b12dbe3aec61f2cf045ba9c43951b14f94ff7b271190a318d831759117a8aa8`.

The source cluster took a real plugin backup after 100 rows. A 101st sentinel
row was committed and archived afterward. pgdrill discovered the completed
CNPG `Backup`, exact Barman backup ID `20260728T012336`, source image, plugin
identity/version, ObjectStore reference, and server name. It generated a
recovery manifest containing that exact backup ID and a read-only
`externalClusters[].plugin` reference. The verify Cluster did not enable
`spec.plugins` or WAL archiving.

The restored cluster replayed sentinel WAL
`000000010000000000000005`, passed 11 checks including PostgreSQL server/client
versions, SQL, `pg_amcheck`, and schema-only `pg_dump`, and passed all three
required policy verdicts. Recovery proof took 47.561 seconds inside this
disposable environment. pgdrill then removed the owned verify Cluster and PVC.

`runtime.txt` records build identity, archive and binary checksums, upstream and
pinned manifest checksums, and immutable image identities. `source-state.txt`
records the backup/WAL boundary and runtime image IDs. The report's immutable
YAML artifact is retained at its content-addressed URI below.

Retained files:

- `report.json`: validated pgdrill terminal report
- `runtime.txt`: exact build, archive, tool, manifest, and image inventory
- `source-state.txt`: source row/WAL boundary and resolved image IDs
- `report.json.artifacts/sha256/65/65730c17574018a700815e945c413caa42f8f5f82f12b52f108a34f68b6e1564`:
  generated read-only recovery manifest
- `SHA256SUMS`: recursive checksums for this evidence directory

This observation covers one local macOS arm64 driver and one Linux arm64
KinD/MinIO target, one plugin backup, and latest recovery with post-backup WAL
replay. It does not establish production Kubernetes support, other CPU
architectures, CNPG/plugin/cert-manager versions, storage classes, object
stores, PITR, failure-mode coverage, performance, or an RTO guarantee. The
candidate archive was not a published or signed release asset, and this
observation does not validate pgdrill's native Barman CLI adapter.
