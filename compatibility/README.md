# Compatibility Evidence Matrix

`matrix.yaml` is the machine-readable source of truth for compatibility
evidence. Its schema is `pgdrill.compatibility-matrix/v1alpha1`.

Evidence levels have intentionally narrow meanings:

- `fixture`: committed native output and contract tests; no tool-version claim
- `controlled`: lifecycle behavior against controlled executables or clients
- `field`: a dated external observation with exact component, pgdrill commit,
  PostgreSQL, and platform versions

An entry records demonstrated capabilities, not a blanket support promise. A
field entry represents one exact implementation, pgdrill commit, PostgreSQL,
platform, and recovery-target point; add another entry for another point.
Every entry must include limitations. Repository tests
strictly decode the matrix and resolve every file, Go test function, and
Markdown heading reference. Native-provider field entries must retain a passed
drill report; repository validation parses that report and cross-checks its
provider, recovery target, date, tool versions, pgdrill version, and full
commit. Release packaging repeats the same validation.

Add a native version only after retaining a completed real drill report. Add a
new field entry rather than widening an older observation to untested versions.
