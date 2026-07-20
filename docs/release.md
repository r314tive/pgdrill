# Release Process

`pgdrill` is pre-alpha, but every published build should be traceable to one
immutable source commit and one changelog entry.

## Versioning

- Use Semantic Versioning tags with a leading `v`, for example
  `v0.1.0-alpha.6`, `v0.2.0-rc.1`, or `v0.2.0`.
- Before `v1.0.0`, incompatible CLI, configuration, report JSON, or canonical
  model changes require at least a minor version bump and an explicit changelog
  note.
- Use incrementing prerelease identifiers for field-test builds. Do not move or
  reuse a tag after it has been pushed.
- Every release has an exact `## [<version>] - YYYY-MM-DD` section in
  `CHANGELOG.md`. `Unreleased` remains at the top for subsequent work.

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

Use `make format` to apply Go formatting. `make release-check` is the release
gate; it additionally runs pinned `actionlint`, the race detector, CLI smoke
tests, and release artifact generation. It fails immediately when the active
compiler does not exactly match `.go-version`.

```sh
make -s release-check VERSION=v0.1.0-alpha.9
```

## Release Artifacts

The release builder runs in Go and creates deterministic archives for a fixed
source commit, release compiler, version, and commit timestamp:

- Linux amd64
- Linux arm64
- macOS amd64
- macOS arm64

Each `.tar.gz` contains `pgdrill`, `README.md`, `LICENSE`, and the release
`.go-version` compiler pin. Archive paths, modes, ordering, timestamps,
architecture levels, Go workspace settings, and build flags are normalized.
The bundle also includes a SHA256 checksum file:

```text
pgdrill_<version>_linux_amd64.tar.gz
pgdrill_<version>_linux_arm64.tar.gz
pgdrill_<version>_darwin_amd64.tar.gz
pgdrill_<version>_darwin_arm64.tar.gz
pgdrill_<version>_checksums.txt
```

Build only the artifacts with:

```sh
make -s release-artifacts VERSION=v0.1.0-alpha.9
```

Verify them on Linux or macOS respectively:

```sh
(cd dist && sha256sum -c pgdrill_0.1.0-alpha.9_checksums.txt)
(cd dist && shasum -a 256 -c pgdrill_0.1.0-alpha.9_checksums.txt)
```

`release-snapshot` remains available as a quick host-only build and smoke
check. It is not a substitute for `release-check`.

## Release Checklist

1. Start from a clean worktree on the intended release commit.
2. Move the release changes from `Unreleased` into a dated version section and
   leave an empty `Unreleased` section.
3. Run the release gate and extract release notes:

```sh
VERSION=v0.1.0-alpha.9
make -s release-check VERSION="$VERSION"
make -s release-notes VERSION="$VERSION"
```

4. Inspect `dist/RELEASE_NOTES.md`, archive contents, checksums, CLI help, and
   `pgdrill version` from the native archive.
5. Commit the release preparation.
6. Rerun step 3 after the commit because commit metadata is part of every
   binary.
7. Create an annotated tag on that exact commit:

```sh
git tag -a "$VERSION" -m "pgdrill $VERSION"
```

8. Push the release commit, wait for branch CI, then push the tag as a separate
   explicit publication action.

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
drill before the feature can be described as production-ready.
