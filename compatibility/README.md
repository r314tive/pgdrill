# Compatibility Evidence Matrix

`matrix.yaml` is the machine-readable source of truth for compatibility
evidence. Its schema is `pgdrill.compatibility-matrix/v1alpha1`.

Evidence levels have intentionally narrow meanings:

- `fixture`: committed native output and contract tests; no tool-version claim
- `controlled`: lifecycle behavior against controlled executables or clients
- `field`: a dated external observation with exact component, pgdrill,
  PostgreSQL, and platform versions

An entry records demonstrated capabilities and recovery-target modes, not a
blanket support promise. Every entry must include limitations. Repository tests
strictly decode the matrix and resolve every file, Go test function, and
Markdown heading reference. Release packaging repeats the same validation.

Add a native version only after retaining a completed real drill report. Add a
new field entry rather than widening an older observation to untested versions.
