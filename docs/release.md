# Release Process

`pgdrill` is pre-alpha, but releases should already be boring and auditable.
The product is about recovery confidence, so the repository should model the
same discipline it asks from users.

## Versioning

- Use Semantic Versioning tags: `vMAJOR.MINOR.PATCH`.
- Use `v0.1.0-alpha.1` for the first public pre-alpha snapshot once the
  current engine skeleton is committed and `release-snapshot` is green.
- Before `v1.0.0`, incompatible CLI, report JSON, or canonical model changes
  should use a minor version bump and must be documented in `CHANGELOG.md`.
- Patch releases are for bug fixes, test fixes, documentation corrections, and
  non-breaking internal refactors.
- Use `v0.x.y-rc.n` tags for release candidates when a drill workflow needs
  field testing before a final tag.

## Done Criteria

Before creating a tag:

- `CHANGELOG.md` has an entry for the release.
- CLI help and examples match the implemented commands.
- `make check` passes from a clean checkout.
- New behavior has focused tests or a documented reason why it cannot be tested
  locally.
- Any known limitations are visible in docs or release notes.

## Build Metadata

Release builds should set version metadata with linker flags:

```sh
go build \
  -ldflags "-X github.com/r314tive/pgdrill/internal/version.Version=v0.1.0 -X github.com/r314tive/pgdrill/internal/version.Commit=$(git rev-parse --short HEAD) -X github.com/r314tive/pgdrill/internal/version.Date=$(date -u +%Y-%m-%dT%H:%M:%SZ)" \
  ./cmd/pgdrill
```

The default development build reports `0.0.0 (unknown, unknown)`.

The Makefile wraps the same metadata contract:

```sh
make -s release-snapshot VERSION=v0.1.0-alpha.1
```

The snapshot target runs `make check`, builds
`dist/pgdrill_<version>_<goos>_<goarch>/pgdrill`, and runs CLI smoke commands
against the built binary.

## Release Checklist

1. Update `CHANGELOG.md`.
2. Run `make check`.
3. Build and smoke-test the CLI with release metadata:

```sh
make -s release-snapshot VERSION=v0.1.0-alpha.1
```

4. Confirm `pgdrill version` reports the intended version, commit, and build
   date.
5. Inspect the staged diff and make sure only release-intended files are
   included.
6. Commit the release-prep changes.
7. Tag the committed release:

```sh
git tag -a v0.1.0-alpha.1 -m "pgdrill v0.1.0-alpha.1"
```

8. Push the commit and tag.
9. Publish release notes from the changelog entry.

Manual smoke commands, if the Makefile target cannot be used:

```sh
pgdrill version
pgdrill explain -format json
pgdrill sample-config
pgdrill catalog help
pgdrill report help
pgdrill run -h
```

10. For provider-facing releases, run `pgdrill catalog list` or `pgdrill run`
    against at least one configured fixture or real backup repository.

## Git Hygiene

Do not tag from an unreviewed worktree. A clean release handoff should include:

- one implementation commit or a small stack of cohesive commits;
- a matching changelog entry;
- a green `make check`;
- a green `make -s release-snapshot VERSION=<version>`;
- an annotated tag on the exact commit being released.
