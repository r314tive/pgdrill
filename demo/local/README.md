# Local Release-Artifact Rehearsals

These rehearsals run the real WAL-G or pgBackRest PostgreSQL integration
scenario with an exact pgdrill Linux release archive from a clean commit. The
archive may be a local release candidate or a published release. This is the
fastest end-to-end fallback for presenter practice and release verification
when a hosted environment is unavailable.

It proves the exact binary can:

- discover and validate a real backup through the selected provider;
- require WAL-G WAL integrity or both pgBackRest `check` and selected-set
  `verify`, depending on the profile;
- restore a separate PostgreSQL target;
- replay a row committed only after the base backup;
- perform timestamp PITR to a boundary between two archived transactions,
  retaining the earlier row and excluding the later row;
- pass readiness, SQL, `pg_amcheck`, and schema-dump probes;
- evaluate RTO, RPO, backup-age, recovery-target, and cleanup policy;
- retain a checksummed report and command evidence.

It does not prove Yandex Cloud networking, multi-VM isolation, NFS
permissions, administrator access controls, or customer compatibility.

## Prerequisites

- A Git checkout of pgdrill.
- A running Docker daemon with Linux containers.
- `curl`, `git`, `tar`, and either `sha256sum` or `shasum`.
- The release archive matching the Docker daemon architecture.

Go is not required when a prebuilt archive is supplied. WAL-G and the
immutable PostgreSQL image are downloaded only when their pinned local caches
are absent; the restore drill itself runs in a network-isolated container.

## Run

Supply the archive for the architecture used by the local Docker daemon, its
SHA-256 digest, and the full commit embedded in that archive. For a local
candidate, build from a clean checkout with the pinned Go toolchain:

```sh
VERSION=v0.3.0-dev
COMMIT="$(git rev-parse HEAD)"
make -s release-check VERSION="$VERSION" RELEASE_COMMIT="$COMMIT"
ARCH="$(go env GOARCH)"
ARCHIVE="$PWD/dist/pgdrill_${VERSION#v}_linux_${ARCH}.tar.gz"
ARCHIVE_SHA256="$(
  awk -v archive="${ARCHIVE##*/}" '$2 == archive { print $1 }' \
    "$PWD/dist/pgdrill_${VERSION#v}_checksums.txt"
)"
make -s demo-rehearsal \
  VERSION="$VERSION" \
  DEMO_RELEASE_COMMIT="$COMMIT" \
  DEMO_RELEASE_ARCHIVE="$ARCHIVE" \
  DEMO_RELEASE_SHA256="$ARCHIVE_SHA256"
```

`demo-rehearsal` uses WAL-G by default. Run the same exact artifact through
pgBackRest with:

```sh
make -s demo-rehearsal-pgbackrest \
  VERSION="$VERSION" \
  DEMO_RELEASE_COMMIT="$COMMIT" \
  DEMO_RELEASE_ARCHIVE="$ARCHIVE" \
  DEMO_RELEASE_SHA256="$ARCHIVE_SHA256"
```

For a published release, download the archive and checksum file first. For
example, with an authenticated GitHub CLI and an arm64 Docker daemon:

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
`.cache/integration/walg/runs/` or
`.cache/integration/pgbackrest/runs/` and prints the exact artifact and
text-report paths.

The current published prerelease is
[`v0.2.0-rc.2`](https://github.com/r314tive/pgdrill/releases/tag/v0.2.0-rc.2).
Always take the digest from that release instead of copying the example when
rehearsing another published version. A local candidate rehearsal is not
publication evidence and must be described as local Docker evidence.
