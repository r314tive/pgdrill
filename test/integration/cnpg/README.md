# Disposable CloudNativePG Integration Drill

This scenario creates an isolated KinD cluster, installs a checksum-pinned
CloudNativePG 1.26.3 manifest, starts PostgreSQL 15.17 with a disposable MinIO
repository, takes a real CNPG backup, commits a post-backup WAL sentinel, and
runs `pgdrill target verify` against the temporary cluster.

The drill proves, within this exact local topology:

- discovery of the latest completed CNPG `Backup` and source image
- operator-managed recovery through `barmanObjectStore`
- replay of a transaction committed after the base backup
- structured CloudNativePG operator-version evidence
- in-pod PostgreSQL and probe-client version checks
- readiness, SQL, `pg_amcheck`, and schema-only `pg_dump` probes
- required recovery policy verdicts
- ownership-scoped Cluster and PVC cleanup
- persisted ordered history with a matching passed report and terminal event

This exact CNPG 1.26.3 scenario uses the native `barmanObjectStore` API. The
operator reports that path as deprecated and scheduled for removal in CNPG
1.29. pgdrill therefore makes no adjacent-version claim: Barman Cloud Plugin
discovery, recovery manifest generation, and cleanup require a separate
implementation and compatibility gate before newer CNPG versions are
advertised.

It requires Docker with Linux containers, `curl`, `git`, Go, and `jq`.
Checksum-pinned KinD and kubectl binaries are downloaded into the ignored
`.cache/integration/cnpg` tree when absent. Container-image inputs use immutable
multi-platform digests. The harness verifies the selected platform image and
its image ID, assigns a test-only local tag to that exact image, and loads the
tag into the disposable cluster through a platform-scoped containerd import
with network pulls disabled. This avoids Docker Desktop exporting an
incomplete multi-platform index through KinD's default all-platform import
while retaining the digest-to-runtime-image mapping in `runtime.txt`.

Run it with:

```sh
make test-integration-cnpg
```

Set `PGDRILL_INTEGRATION_VERSION` to bind a clean run to a candidate version.
Set `PGDRILL_INTEGRATION_REQUIRE_CLEAN=true` to reject a dirty source tree.
`make release-candidate-check VERSION=v0.3.0-alpha.1` applies both settings and
runs this scenario after the release and native-provider gates.

The script never uses the host's active Kubernetes context for mutations.
KinD receives a dedicated kubeconfig, the generated cluster name is unique, and
the script verifies that the host context did not change. The disposable
cluster is deleted on success, failure, or interruption. Its ephemeral
kubeconfig is removed before artifacts are checksummed.

The fixed MinIO credentials in `infra.yaml` are test-only values scoped to the
disposable cluster. Do not replace them with real credentials. Reviewed run
artifacts are retained under:

```text
.cache/integration/cnpg/runs/<timestamp>/
```

The retained set includes full and text history views, a bounded history list,
full-store verification views, the private raw store, an archive of that
store, and recursive checksums.

As with the native scenarios, a passing run is developer evidence until its
report is reviewed, bound to an exact clean commit, and deliberately promoted
to `compatibility/evidence`.
