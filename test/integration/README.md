# Integration Tests

This tree contains reproducible developer tests that execute real external
tools. It is intentionally separate from both product code and presentation
infrastructure:

- `internal/**` tests exercise Go contracts, fixtures, and controlled fakes.
- `test/integration/**` creates disposable local systems and proves real tool
  interoperability.
- `demo/**` provisions an operator-facing environment for a technical session.
- `compatibility/evidence/**` retains reviewed observations that support an
  exact compatibility claim.

An integration pass is not automatically compatibility evidence. Promote a
result only after binding it to a clean release-candidate commit, reviewing the
scope and artifacts, and updating the compatibility matrix deliberately.

The tests may download pinned public tool artifacts and container images during
preparation. The actual drill should run without network access whenever the
provider permits it.

Host-side release-candidate binding, Docker isolation defaults, and artifact
checksumming live in `lib/runtime.sh`. Provider setup, backup semantics,
restore commands, and acceptance assertions stay in their scenario directory.

Current scenarios:

- [WAL-G to a local PostgreSQL target](walg/README.md)
- [Barman to a local PostgreSQL target](barman/README.md)
- [pgBackRest to a local PostgreSQL target](pgbackrest/README.md)
