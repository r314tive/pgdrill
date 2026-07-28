# Compatibility Evidence Matrix

`matrix.yaml` is the machine-readable source of truth for compatibility
evidence. Its current schema is `pgdrill.compatibility-matrix/v1`; the reader
retains compatibility with the pre-GA `v1alpha1` generation.

Evidence levels have intentionally narrow meanings:

- `fixture`: committed native output and contract tests; no tool-version claim
- `controlled`: lifecycle behavior against controlled executables or clients
- `field`: a dated external observation with exact component, pgdrill commit,
  PostgreSQL, and platform versions

An entry records demonstrated capabilities, not a blanket support promise. A
field entry represents one exact implementation, pgdrill commit, PostgreSQL,
platform, and recovery-target point; add another entry for another point.
Every entry must include limitations. Repository tests strictly decode the
matrix and resolve every file, Go test function, and Markdown heading
reference. Native-provider field entries must retain a passed drill report;
referenced reports are parsed and cross-checked against provider or target
identity, recovery target, date, PostgreSQL/tool versions, CNPG operator
version when applicable, pgdrill version, and full commit. Release packaging
repeats the same validation.

Field entries may also retain a typed `runtime_inventory` reference. It binds
the observed Linux target, container architecture, candidate archive name and
checksums, pgdrill version, and full commit. Entries that claim
`cross_architecture_functional` must include this evidence and prove that the
Docker daemon architecture differs from the executed Linux architecture. Such
an observation is functional evidence only, not native-hardware performance or
RTO evidence.

Entries claiming `s3_compatible_object_storage` must also retain typed runtime
inventory with an S3-compatible backend, endpoint, bucket, and network
topology. The drill report must prove a successful WAL-G `backup-fetch` with an
S3 prefix, custom endpoint, and path-style addressing while excluding
credential fields and filesystem storage from command evidence.

Add a native version only after retaining a completed real drill report. Add a
new field entry rather than widening an older observation to untested versions.
