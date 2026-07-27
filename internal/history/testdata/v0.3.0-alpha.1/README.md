# History compatibility fixture

This archive is the private local-history store retained by the exact
`v0.3.0-alpha.1` WAL-G demo rehearsal:

- pgdrill commit: `0b8358cde90abfcf6b96964ce7ecd6443dbfb1c3`
- run directory: `.cache/integration/walg/runs/20260727T202904Z-68231`
- archive SHA-256:
  `dc44cbb9a86f2911f049ca09bb3ff505915a8e86780794a0b0fe4e6791084d5b`
- PostgreSQL: `18.3`
- WAL-G: `3.0.8`
- attempts: latest recovery and timestamp PITR, both passed with 26 ordered
  lifecycle events and terminal reports

The archive is committed as immutable upgrade evidence. Tests extract it into
a private temporary directory and read it with the current implementation.
Do not regenerate it from current Go structs.
