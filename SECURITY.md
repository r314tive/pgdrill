# Security Policy

## Supported Versions

`pgdrill` is pre-1.0. Security fixes are applied to the latest released
prerelease and the default branch; older prerelease builds are not maintained.

## Reporting A Vulnerability

Use GitHub private vulnerability reporting at
<https://github.com/r314tive/pgdrill/security/advisories/new> when it is
available. If private reporting is unavailable, contact the maintainer through
the repository owner profile without including exploit details in a public
issue.

Include the affected pgdrill version or commit, execution platform, provider or
target, impact, reproduction steps, and any evidence after removing secrets.
Do not attach production backup data, credentials, connection strings, or raw
unredacted command output.

The project will acknowledge reports and coordinate disclosure, but does not
yet promise a fixed response SLA.

## Trusted Input Boundary

pgdrill intentionally executes configured provider, PostgreSQL, and Kubernetes
binaries and can run configured SQL probes. Configuration files and executable
paths are trusted operator input. Security issues include escaping configured
filesystem ownership boundaries, unsafe cleanup, redaction failures, command
argument injection beyond the declared invocation, report integrity problems,
or release supply-chain compromise.

Releases completed by the current tag workflow use checksummed deterministic
archives, SPDX SBOMs, and OIDC-backed signed provenance. Older published
releases may predate that workflow and must not be assumed to carry those
assets. The Linux OCI image is assembled only from the exact release archives
with digest-pinned runtime and scanner inputs. Consumers should verify the
retained attestation against the expected repository, release workflow, tag
ref, and source commit, then deploy an immutable image digest. See
[`docs/release.md`](docs/release.md) and
[`docs/container-image.md`](docs/container-image.md).
