# Local Published-Artifact Rehearsal

This rehearsal runs the real WAL-G/PostgreSQL integration scenario with an
already-published pgdrill Linux archive. It is the fastest end-to-end fallback
for presenter practice and release verification when the hosted Yandex Cloud
environment is not yet available.

It proves the published binary can:

- discover and validate a real WAL-G backup;
- restore a separate PostgreSQL target;
- replay a row committed only after the base backup;
- perform timestamp PITR to a boundary between two archived transactions,
  retaining the earlier row and excluding the later row;
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
For example, with an authenticated GitHub CLI and an arm64 Docker daemon:

```sh
VERSION=v0.2.0-rc.2
ARCH=arm64
RELEASE_DIR="$PWD/.cache/demo/releases/$VERSION"
mkdir -p "$RELEASE_DIR"
gh release download "$VERSION" \
  --repo r314tive/pgdrill \
  --dir "$RELEASE_DIR" \
  --pattern "pgdrill_${VERSION#v}_linux_${ARCH}.tar.gz" \
  --pattern "pgdrill_${VERSION#v}_checksums.txt"
ARCHIVE="$RELEASE_DIR/pgdrill_${VERSION#v}_linux_${ARCH}.tar.gz"
ARCHIVE_SHA256="$(
  awk -v archive="${ARCHIVE##*/}" '$2 == archive { print $1 }' \
    "$RELEASE_DIR/pgdrill_${VERSION#v}_checksums.txt"
)"
COMMIT="$(git rev-parse "$VERSION^{commit}")"
make -s demo-rehearsal \
  VERSION="$VERSION" \
  DEMO_RELEASE_COMMIT="$COMMIT" \
  DEMO_RELEASE_ARCHIVE="$ARCHIVE" \
  DEMO_RELEASE_SHA256="$ARCHIVE_SHA256"
```

Use the `linux_amd64` archive on an amd64 Docker daemon. The command rejects a
wrong filename, target architecture, digest, version, or commit. It retains
the complete latest and timestamp-PITR runs under
`.cache/integration/walg/runs/` and prints the exact artifact and text-report
paths.

The current published prerelease is
[`v0.2.0-rc.2`](https://github.com/r314tive/pgdrill/releases/tag/v0.2.0-rc.2).
Always take the digest from that release instead of copying the example when
rehearsing another version.
