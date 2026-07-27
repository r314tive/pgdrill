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
- Bash syntax for the versioned demo scripts
- Bash syntax for disposable integration scripts

Use `make format` to apply Go formatting. `make release-check` is the release
gate; it additionally runs pinned `actionlint`, the race detector, CLI smoke
tests, and release artifact generation. It fails immediately when the active
compiler does not exactly match `.go-version`.

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
make test-integration-barman
make test-integration-pgbackrest
make test-integration-pgprobackup
make test-integration-native
make test-integration-cnpg
```

`integration-check` requires ShellCheck. The executable tests prepare pinned
provider and PostgreSQL inputs, then perform rootless network-isolated real
backup and restore drills. They are intentionally excluded from `release-check`
because they require Docker and may download external artifacts; release owners
must run them explicitly for affected native paths. A pass from a dirty tree is
marked dirty and is never release evidence.

```sh
make -s release-check VERSION=v0.3.0-alpha.3
```

The aggregate prerelease-candidate gate requires a clean worktree and runs the
release gate, ShellCheck, all four native-provider drills, and the disposable
KinD/CNPG drill:

```sh
make -s release-candidate-check VERSION=v0.3.0-alpha.3
```

Every integration process receives the same version and full Git commit.
Native drills execute the corresponding deterministic Linux archive; the CNPG
driver executes the deterministic host archive while restoring into a pinned
Linux KinD target. Checksummed run artifacts remain under `.cache/integration`.
They are reviewed release evidence, not automatic additions to the committed
compatibility matrix.

## Release Artifacts

The release builder runs in Go and creates deterministic archives for a fixed
source commit, release compiler, version, and commit timestamp:

- Linux amd64
- Linux arm64
- macOS amd64
- macOS arm64

Each `.tar.gz` contains `pgdrill`, `README.md`, `LICENSE`, the release
`.go-version` compiler pin, `COMPATIBILITY.md`, `FLEET_PLAN.md`, `HISTORY.md`,
`UPGRADE.md`, the validated `compatibility-matrix.yaml`, and
`fleet.example.yaml`. The
release builder compiles the packaged fleet example and rejects placement
rejections before creating archives. Archive paths, modes, ordering,
timestamps, architecture levels, Go workspace settings, and build flags are
normalized. The bundle also includes a SHA256 checksum file:

```text
pgdrill_<version>_linux_amd64.tar.gz
pgdrill_<version>_linux_arm64.tar.gz
pgdrill_<version>_darwin_amd64.tar.gz
pgdrill_<version>_darwin_arm64.tar.gz
pgdrill_<version>_checksums.txt
```

Build only the artifacts with:

```sh
make -s release-artifacts VERSION=v0.3.0-alpha.3
```

Verify them on Linux or macOS respectively:

```sh
(cd dist && sha256sum -c pgdrill_0.3.0-alpha.3_checksums.txt)
(cd dist && shasum -a 256 -c pgdrill_0.3.0-alpha.3_checksums.txt)
```

`release-snapshot` remains available as a quick host-only build and smoke
check. It is not a substitute for `release-check`.

## Release Checklist

1. Prepare the release changes and move them from `Unreleased` into a dated
   version section, leaving an empty `Unreleased` section.
2. Run `make check`, review the diff, and commit the release preparation.
3. Confirm that the resulting intended release commit has a clean worktree.
4. Run the exact-candidate gate and extract release notes:

```sh
VERSION=v0.3.0-alpha.3
make -s release-candidate-check VERSION="$VERSION"
make -s release-notes VERSION="$VERSION"
```

5. Inspect `dist/RELEASE_NOTES.md`, archive contents, checksums, CLI help,
   `pgdrill version` from the native archive, and the five latest integration
   artifact directories.
6. If any source or release metadata changes after the gate, create a new
   commit and repeat steps 3 through 5 because commit metadata is part of every
   binary and report.
7. Create an annotated tag on the exact clean, tested commit:

```sh
git tag -a "$VERSION" -m "pgdrill $VERSION"
```

8. Push the release commit, wait for branch CI, then push the tag as a separate
   explicit publication action.
9. Wait for both release workflow jobs, then confirm that the GitHub Release is
   a prerelease when appropriate and contains exactly the expected archives
   plus checksum file.
10. Download the published assets into a fresh directory, verify every archive
    against the downloaded checksum file, run the native `pgdrill version`, and
    confirm that the remote annotated tag dereferences to the exact tested
    commit.

## Tag Automation

`.github/workflows/release.yml` runs only for pushed `v*` tags. Before any
publication it verifies that:

- the tag is annotated and resolves to the checked-out commit
- the version is valid SemVer
- an exact non-empty changelog section exists
- the full release gate passes with the pinned compiler
- checksums and the native Linux archive are valid

The build job has read-only repository permissions. A separate job receives
only the verified bundle and gets `contents: write` solely to create the GitHub
release. The publish job deliberately does not check out the repository; it
passes `github.repository` to GitHub CLI through `GH_REPO` instead of relying
on local Git metadata. Prerelease tags are published as prereleases and are not
marked latest.

If a pushed tag fails before publication, fix the source and use the next
prerelease identifier. Do not silently retarget the failed tag.

## Field Validation

A green artifact release does not prove provider or Kubernetes compatibility.
For provider-facing releases, record at least one real `catalog list` or drill
run for the changed adapter. CNPG changes require a disposable live-cluster
drill. `release-candidate-check` supplies the controlled local baseline; a
production support claim still requires separately scoped customer or field
evidence.
