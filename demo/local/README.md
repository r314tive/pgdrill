# Local Published-Artifact Rehearsal

This rehearsal runs the real WAL-G/PostgreSQL integration scenario with an
already-published pgdrill Linux archive. It is the fastest end-to-end fallback
for presenter practice and release verification when the hosted Yandex Cloud
environment is not yet available.

It proves the published binary can:

- discover and validate a real WAL-G backup;
- restore a separate PostgreSQL target;
- replay a row committed only after the base backup;
- pass readiness, SQL, `pg_amcheck`, and schema-dump probes;
- evaluate RTO, RPO, backup-age, recovery-target, and cleanup policy;
- retain a checksummed report and command evidence.

It does not prove Yandex Cloud networking, VM isolation, NFS permissions,
administrator access controls, or customer compatibility.

## Prerequisites

- A Git checkout of pgdrill.
- A running Docker daemon with Linux containers.
- `curl`, `git`, `tar`, and either `sha256sum` or `shasum`.
- The release archive matching the Docker daemon architecture.

Go is not required when the published archive is supplied. WAL-G and the
immutable PostgreSQL image are downloaded only when their pinned local caches
are absent; the restore drill itself runs in a network-isolated container.

## Run

Download the release archive for the architecture used by the local Docker
daemon and obtain its SHA-256 digest from the matching release checksum file.
Then run from the repository root:

```sh
make -s demo-rehearsal \
  VERSION=v0.2.0-rc.1 \
  DEMO_RELEASE_COMMIT=e9cb257c8312020166b5dff9c91f9bd9cde4ca25 \
  DEMO_RELEASE_ARCHIVE=/path/to/pgdrill_0.2.0-rc.1_linux_arm64.tar.gz \
  DEMO_RELEASE_SHA256=a0ae4d18e88794f24e5c97bab44c9b8e43fd9a9be06482fb6d47e318d304589c
```

Use the `linux_amd64` archive on an amd64 Docker daemon. The command rejects a
wrong filename, target architecture, digest, version, or commit. It retains
the complete run under `.cache/integration/walg/runs/` and prints the exact
artifact and text-report paths.

The current published prerelease is
[`v0.2.0-rc.1`](https://github.com/r314tive/pgdrill/releases/tag/v0.2.0-rc.1).
Always take the digest from that release instead of copying the example when
rehearsing another version.
