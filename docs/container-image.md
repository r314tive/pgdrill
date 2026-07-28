# Container Image

Release tags publish a multi-architecture OCI image at:

```text
ghcr.io/r314tive/pgdrill:<version>
```

The image contains the exact statically linked Linux `pgdrill` binary from the
checksummed release archive for its platform. Published manifests cover
`linux/amd64` and `linux/arm64`. The image runs as numeric UID/GID `65532`,
uses `/tmp` as its working directory, and forwards `SIGTERM` directly to the
pgdrill process.

## Runtime Boundary

The image is a distribution of pgdrill, not a bundled PostgreSQL backup stack.
It deliberately does not choose or silently pin WAL-G, Barman, pgBackRest,
pg_probackup, PostgreSQL client, or kubectl versions for an operator.

Commands that only inspect pgdrill state work directly:

```sh
docker run --rm ghcr.io/r314tive/pgdrill:v0.3.0-alpha.5 version
docker run --rm \
  -v "$PWD/examples:/config:ro" \
  ghcr.io/r314tive/pgdrill:v0.3.0-alpha.5 \
  plan validate -f /config/fleet.yaml
```

A real drill still needs its configured provider, target, and probe
executables. Build a reviewed derived image with exact tool versions or mount
the binaries and configuration at runtime. Run `pgdrill doctor` inside that
same execution environment before repository access. The presence of an
executable in a derived image is not a compatibility claim; the exact
provider/PostgreSQL/storage cell must still exist in the compatibility matrix.

Do not bake repository credentials into an image layer. Resolve secrets at
runtime through the execution environment or mounted secret files, and mount
history, checkpoint, report, artifact, and restore-target paths explicitly
with the required ownership.

## Integrity

Release images are identified by immutable digest. The tag workflow attaches
BuildKit SBOM/provenance records and a signed GitHub artifact attestation to the
manifest digest. Verify the signed provenance before use:

```sh
gh attestation verify \
  oci://ghcr.io/r314tive/pgdrill:v0.3.0-alpha.5 \
  --repo r314tive/pgdrill \
  --signer-workflow r314tive/pgdrill/.github/workflows/release.yml \
  --source-ref refs/tags/v0.3.0-alpha.5 \
  --source-digest FULL_RELEASE_COMMIT
```

After verification, resolve the digest and deploy the
`ghcr.io/r314tive/pgdrill@sha256:...` reference rather than a mutable tag.
The OCI labels bind the version, full source commit, commit timestamp, source
repository, and pinned Debian runtime-base manifest.

## Local Build Gate

Build deterministic release archives first, then build both OCI platform
manifests without publishing:

```sh
make -s release-artifacts VERSION=v0.3.0-alpha.5
make -s container-check VERSION=v0.3.0-alpha.5
make -s container-smoke VERSION=v0.3.0-alpha.5
```

`container-check` writes an ignored OCI layout archive under `dist/`. The
native `container-smoke` gate loads the host-architecture Linux image, runs its
entrypoint, verifies exact version/commit output, and removes the local tag.
The clean-tree aggregate `release-candidate-check` includes both gates. The tag
workflow repeats the full two-platform verifier against the exact tag archives,
then independently rebuilds and pushes the image only after all checks pass.

After authenticated smoke and signed-provenance verification, the image job
logs out from GHCR and resolves the digest with a fresh credential-free Docker
configuration, retaining only CLI plugin discovery when required. This is the
publication gate for anonymous pull access; a first package may need its GHCR
visibility changed to public before rerunning the failed jobs.

`SOURCE_DATE_EPOCH` binds each platform image payload to the release commit
timestamp, so repeated builds from the same archives and pinned base retain the
same platform-manifest digests. The top-level attested index is intentionally
build-specific: SLSA provenance records the actual invocation time, so its
digest is expected to change between independent builds. The published index
digest remains immutable and is the deployment identity.
