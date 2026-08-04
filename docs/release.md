# Release Process

`pgdrill` is pre-1.0. Every published build must be traceable to one immutable
source commit, one changelog entry, deterministic artifacts, and the
compatibility evidence claimed by that release.

## Versioning

- Use Semantic Versioning tags with a leading `v`, for example
  `v0.1.0-alpha.6`, `v0.2.0-rc.2`, or `v0.2.0`.
- Before `v1.0.0`, incompatible CLI, configuration, report JSON, or canonical
  model changes require at least a minor version bump and an explicit changelog
  note.
- Use incrementing prerelease identifiers for field-test builds. Do not move or
  reuse a tag after it has been pushed.
- Every release has an exact `## [<version>] - YYYY-MM-DD` section in
  `CHANGELOG.md`. `Unreleased` remains at the top for subsequent work.
- `v1.0.0` additionally requires every exit criterion in the
  [v1.0 release contract](v1.0-release-contract.md). A green build alone is not
  a GA support claim.

## Toolchain Contract

- `go.mod` declares the minimum supported Go language/toolchain version.
- `.go-version` pins the exact compiler used for release artifacts.
- CI checks the minimum supported Go patch release and the pinned release
  compiler separately.
- GitHub Actions are pinned to immutable commit SHAs and updated by Dependabot.

Changing the release compiler can change binary bytes and checksums. Update
`.go-version` deliberately, rerun the complete release gate, and record the
change in `CHANGELOG.md`.

## Local Gates

`make check` is the normal development gate. It is non-mutating and verifies:

- `gofmt` cleanliness
- `go mod tidy -diff`
- `go vet ./...`
- `go test ./...`
- Windows amd64 cross-compilation (runtime support remains unclaimed)
- the complete Go unit suite on Windows in branch and pull-request CI
- Bash syntax for the versioned demo scripts
- Bash syntax for disposable integration scripts

Use `make format` to apply Go formatting. `make release-check` is the release
gate; it additionally runs pinned `actionlint`, ShellCheck across every
versioned demo and integration script, the race detector, CLI smoke tests, and
release artifact generation. It fails immediately when the active compiler
does not exactly match `.go-version`.

The Yandex Cloud demo has an additional opt-in infrastructure gate because it
requires external Terraform and ShellCheck binaries:

```sh
make demo-infra-check
```

That target runs ShellCheck, initializes the locked Yandex Cloud provider with
the state backend disabled and lock file read-only, enforces Terraform
formatting, and validates the provider schema. It does not replace a reviewed
`terraform plan` or a live rehearsal.

Native tool changes have an additional opt-in local interoperability gate:

```sh
make integration-check
make test-integration-walg
make test-integration-walg-s3
make test-integration-barman
make test-integration-pgbackrest
make test-integration-pgprobackup
make test-integration-native
make test-integration-postgresql-17
make test-integration-cnpg
make test-integration-cnpg-plugin
```

`integration-check` requires ShellCheck. The executable tests prepare pinned
provider and PostgreSQL inputs, then perform rootless network-isolated real
backup and restore drills. They are intentionally excluded from `release-check`
because they require Docker and may download external artifacts; release owners
must run them explicitly for affected native paths. A pass from a dirty tree is
marked dirty and is never release evidence.

```sh
make -s release-check VERSION=v0.3.0-alpha.5
```

The aggregate prerelease-candidate gate requires a clean worktree and runs the
release gate, a non-published Linux amd64/arm64 OCI build and content
verification, ShellCheck, all four native-provider drills on PostgreSQL 18.3
and 17.10, the WAL-G/MinIO profile on 18.3, and both disposable KinD/CNPG
protocol drills:

```sh
make -s release-candidate-check VERSION=v0.3.0-alpha.5
```

Every integration process receives the same version and full Git commit.
Native drills execute the corresponding deterministic Linux archive; the CNPG
drivers execute the deterministic host archive while restoring into pinned
Linux KinD targets. Both WAL-G storage profiles additionally kill the complete
pgdrill process group after durable restore intent, require digest-confirmed
owned-target recovery, preserve the incomplete attempt, and pass a clean new
attempt. The S3 profile uses pinned MinIO/MinIO Client images on an internal
network, retains an object inventory, rejects credential leakage, and proves
harness cleanup. Checksummed run artifacts remain under `.cache/integration`.
They are reviewed release evidence, not automatic additions to the committed
compatibility matrix.

## Release Artifacts

The release builder runs in Go and creates deterministic archives for a fixed
source commit, release compiler, version, and commit timestamp:

- Linux amd64
- Linux arm64
- macOS amd64
- macOS arm64

Each `.tar.gz` contains `pgdrill`, the root project and legal documents, and
the canonical `docs/`, `examples/`, `demo/`, and `compatibility/` trees. Their
repository-relative paths are preserved so links in the packaged README and
documentation remain valid offline. The bundle also contains only the
allowlisted sanitized provider fixtures and conformance test files needed to
resolve compatibility-matrix evidence references. Local Terraform state,
provider caches, plans, test caches, editor files, ignored environment files,
and `*.tfvars` are excluded from the bundle. Support paths, contents, and
executable modes come from immutable blobs in the Git index; arbitrary
untracked or unstaged working-tree content is never collected. File count,
individual size, and total size are bounded before archive construction.

The release builder validates links in the packaged documentation, validates
the packaged compatibility matrix and its retained evidence, and compiles the
packaged fleet example before creating any archive. Archive paths, modes,
ordering, timestamps, architecture levels, Go workspace settings, and build
flags are normalized. The bundle also includes a SHA256 checksum file:

```text
pgdrill_<version>_linux_amd64.tar.gz
pgdrill_<version>_linux_arm64.tar.gz
pgdrill_<version>_darwin_amd64.tar.gz
pgdrill_<version>_darwin_arm64.tar.gz
pgdrill_<version>_checksums.txt
```

Build only the artifacts with:

```sh
make -s release-artifacts VERSION=v0.3.0-alpha.5
```

Verify them on Linux or macOS respectively:

```sh
(cd dist && sha256sum -c pgdrill_0.3.0-alpha.5_checksums.txt)
(cd dist && shasum -a 256 -c pgdrill_0.3.0-alpha.5_checksums.txt)
```

`release-snapshot` remains available as a quick host-only build and smoke
check. It is not a substitute for `release-check`.

## OCI, SBOM, And Provenance

The clean candidate gate constructs a non-published OCI layout from the exact
Linux release archives:

```sh
make -s container-check VERSION=v0.3.0-alpha.5
make -s container-smoke VERSION=v0.3.0-alpha.5
```

The build context is an allowlist containing only the release Dockerfile and
Linux archives. The Dockerfile selects the archive by BuildKit `TARGETARCH`,
copies the exact pgdrill binary, uses a digest-pinned Debian runtime manifest,
and runs as numeric UID/GID `65532`. It does not install provider, PostgreSQL,
or Kubernetes executables.

The Go OCI verifier rejects a layout unless all of the following hold:

- the content-addressed blobs and descriptor sizes are valid;
- the platform set is exactly Linux amd64 and arm64;
- version, full commit, timestamp, source, license, and base-image labels
  match the release inputs;
- the runtime user, work directory, entrypoint, and stop signal match the
  documented contract;
- each embedded pgdrill binary is byte-identical to its checksummed archive;
- each platform has SPDX SBOM and SLSA provenance statements produced with
  the digest-pinned BuildKit scanner.

The native smoke gate separately loads the host-architecture Linux image,
executes the non-root entrypoint, and requires exact version/full-commit
output. Before publication, the tag workflow repeats the full two-platform OCI
content verification against its exact tag archives. After pushing the index,
it repeats image smoke on Linux amd64 by immutable digest.

The tag workflow additionally generates one release-wide SPDX 2.3 document
from all four archives. GitHub's OIDC-backed Sigstore service signs:

- an SBOM attestation binding that document to the archive checksums;
- SLSA build provenance covering archives, checksums, and the SBOM;
- the pushed `ghcr.io/r314tive/pgdrill:<version>` manifest digest.

The signed bundles are retained as `.sigstore.json` GitHub Release assets
together with the image digest and resolved manifest metadata. The image also
carries BuildKit SBOM/provenance referrers. The release workflow pins every
third-party action, Syft version, runtime base, and image SBOM generator to an
immutable version or digest.

The release commit timestamp normalizes the two platform image payloads.
BuildKit's SLSA statements correctly retain each real invocation time, so the
attested top-level index is not claimed to be byte-reproducible across separate
builders. Verify and deploy the one published digest; compare platform-manifest
digests when checking payload reproducibility.

The GHCR package must be publicly readable. On its first publication, confirm
the package is linked to the repository and set its visibility to public. The
image job logs out and performs an anonymous manifest read before the GitHub
Release job can run; if visibility is still private, make it public and rerun
the failed workflow jobs without moving the tag.

## Release Checklist

1. Prepare the release changes and move them from `Unreleased` into a dated
   version section, leaving an empty `Unreleased` section.
2. Run `make check`, review the diff, and commit the release preparation.
3. Confirm that the resulting intended release commit has a clean worktree.
4. Run the exact-candidate gate and extract release notes:

```sh
VERSION=v0.3.0-alpha.5
make -s release-candidate-check VERSION="$VERSION"
make -s release-notes VERSION="$VERSION"
```

5. Inspect `dist/RELEASE_NOTES.md`, archive contents, checksums, CLI help,
   `pgdrill version` from the native archive, the verified OCI layout, and the
   five latest integration artifact directories.
6. If any source or release metadata changes after the gate, create a new
   commit and repeat steps 3 through 5 because commit metadata is part of every
   binary and report.
7. Create an annotated tag on the exact clean, tested commit:

```sh
git tag -a "$VERSION" -m "pgdrill $VERSION"
```

8. Push the release commit, wait for branch CI, then push the tag as a separate
   explicit publication action.
9. Wait for the archive-build, GHCR-image, and GitHub-Release jobs. Confirm that
   a prerelease is marked correctly and contains archives, checksums, SPDX
   SBOM, archive/SBOM/image Sigstore bundles, image identity, and resolved image
   manifest metadata.
10. Download the published assets into a fresh directory, verify every archive
    against the downloaded checksum file and retained attestations, run the
    native `pgdrill version`, anonymously resolve the image by digest, and
    confirm that the remote annotated tag dereferences to the exact tested
    commit.

For example:

```sh
VERSION=v0.3.0-alpha.5
NAME="${VERSION#v}"
COMMIT="$(git rev-list -n 1 "$VERSION")"
gh attestation verify "pgdrill_${NAME}_linux_amd64.tar.gz" \
  --repo r314tive/pgdrill \
  --bundle "pgdrill_${NAME}_provenance.sigstore.json" \
  --signer-workflow r314tive/pgdrill/.github/workflows/release.yml \
  --source-ref "refs/tags/$VERSION" \
  --source-digest "$COMMIT"
gh attestation verify "pgdrill_${NAME}_linux_amd64.tar.gz" \
  --repo r314tive/pgdrill \
  --bundle "pgdrill_${NAME}_sbom-attestation.sigstore.json" \
  --predicate-type https://spdx.dev/Document/v2.3 \
  --signer-workflow r314tive/pgdrill/.github/workflows/release.yml \
  --source-ref "refs/tags/$VERSION" \
  --source-digest "$COMMIT"
gh attestation verify "oci://ghcr.io/r314tive/pgdrill:$VERSION" \
  --repo r314tive/pgdrill \
  --signer-workflow r314tive/pgdrill/.github/workflows/release.yml \
  --source-ref "refs/tags/$VERSION" \
  --source-digest "$COMMIT"
```

## Tag Automation

`.github/workflows/release.yml` runs only for pushed `v*` tags. Before any
publication it verifies that:

- the tag is annotated and resolves to the checked-out commit
- the version is valid SemVer
- an exact non-empty changelog section exists
- the full release gate passes with the pinned compiler
- checksums and the native Linux archive are valid
- the release SBOM and archive provenance are signed and re-verified
- the exact Linux archives produce a two-platform GHCR image
- the image digest passes native smoke, signed-provenance, and anonymous-read
  verification

The archive build job has read-only repository access plus only the OIDC and
attestation permissions needed to sign its output. A separate image job waits
for that verified bundle and alone receives `packages: write`. A final job
receives both verified bundles and gets `contents: write` solely to create the
GitHub release. The publish job deliberately does not check out the repository;
it passes `github.repository` to GitHub CLI through `GH_REPO` instead of
relying on local Git metadata. Prerelease tags are published as prereleases and
are not marked latest.

If a pushed tag fails before publication, fix the source and use the next
prerelease identifier. Do not silently retarget the failed tag.

## Field Validation

A green artifact release does not prove provider or Kubernetes compatibility.
For provider-facing releases, record at least one real `catalog list` or drill
run for the changed adapter. CNPG changes require the applicable disposable
live-cluster protocol drill. `release-candidate-check` supplies the controlled
local baseline; a production support claim still requires separately scoped
customer or field evidence.
