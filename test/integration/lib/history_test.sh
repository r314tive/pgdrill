#!/usr/bin/env bash

set -Eeuo pipefail
umask 077

SCRIPT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
readonly SCRIPT_DIR

# shellcheck source=test/integration/lib/history.sh
source "${SCRIPT_DIR}/history.sh"

tmp_dir="$(mktemp -d)"
readonly tmp_dir
trap 'rm -rf "${tmp_dir}"' EXIT

readonly mock_pgdrill="${tmp_dir}/pgdrill"
readonly store="${tmp_dir}/history"
readonly artifact_store="${tmp_dir}/artifacts"
readonly output="${tmp_dir}/output"

cat >"${mock_pgdrill}" <<'EOF'
#!/usr/bin/env bash
set -Eeuo pipefail

command="${1:-}"
subcommand="${2:-}"
format="text"
for argument in "$@"; do
  if [[ "${previous:-}" == "-format" ]]; then
    format="${argument}"
  fi
  previous="${argument}"
done

case "${command}/${subcommand}/${format}" in
  history/show/json)
    cat <<'JSON'
{
  "schema_version": "pgdrill.history-view/v1alpha1",
  "run_id": "mock-run",
  "attempts": [
    {
      "attempt_id": "attempt-1",
      "events": [
        {
          "type": "run_finished",
          "status": "passed"
        }
      ],
      "report": {
        "status": "passed"
      }
    }
  ]
}
JSON
    ;;
  history/show/text)
    printf 'Run ID: mock-run\nAttempt: attempt-1\nStatus: passed\n'
    ;;
  history/list/json)
    cat <<'JSON'
{
  "schema_version": "pgdrill.history-view/v1alpha1",
  "attempts": [
    {
      "attempt_id": "attempt-1",
      "status": "passed",
      "report_available": true
    }
  ]
}
JSON
    ;;
  history/list/text)
    printf 'Attempts: 1\n'
    ;;
  history/verify/json)
    cat <<'JSON'
{
  "schema_version": "pgdrill.history-verification/v1alpha1",
  "attempts": 1,
  "maintenance_required": false
}
JSON
    ;;
  history/verify/text)
    printf 'Attempts: 1\nMaintenance required: false\n'
    ;;
  artifact/verify/json)
    cat <<'JSON'
{
  "schema_version": "pgdrill.artifact-verification/v1alpha1",
  "blobs": 1,
  "referenced_blobs": 1,
  "maintenance_required": false
}
JSON
    ;;
  artifact/verify/text)
    printf 'Blobs: 1\nReferenced blobs: 1\nMaintenance required: false\n'
    ;;
  artifact/gc/json)
    cat <<'JSON'
{
  "schema_version": "pgdrill.artifact-gc-plan/v1alpha1",
  "summary": {
    "candidate_blobs": 0
  }
}
JSON
    ;;
  *)
    printf 'unexpected mock invocation: %s\n' "$*" >&2
    exit 1
    ;;
esac
EOF
chmod 0755 "${mock_pgdrill}"
mkdir -p "${store}" "${artifact_store}" "${output}"
printf '{"schema_version":"pgdrill.history-store/v1alpha1","layout_version":1}\n' \
  >"${store}/store.json"

pgdrill_integration_verify_history_attempt \
  "${mock_pgdrill}" \
  "${store}" \
  mock-run \
  attempt-1 \
  "${output}/attempt"
pgdrill_integration_capture_history_store \
  "${mock_pgdrill}" \
  "${store}" \
  "${output}" \
  1
pgdrill_integration_verify_artifact_store \
  "${mock_pgdrill}" \
  "${artifact_store}" \
  "${store}" \
  "${output}" \
  1

[[ -s "${output}/attempt.json" ]]
[[ -s "${output}/attempt.txt" ]]
[[ -s "${output}/history-list.json" ]]
[[ -s "${output}/history-list.txt" ]]
[[ -s "${output}/history-verify.json" ]]
[[ -s "${output}/history-verify.txt" ]]
[[ -s "${output}/history-store.tar.gz" ]]
[[ -s "${output}/artifact-verify.json" ]]
[[ -s "${output}/artifact-verify.txt" ]]
[[ -s "${output}/artifact-gc-plan.json" ]]
grep -F 'history/store.json' "${output}/history-store-contents.txt" >/dev/null

if pgdrill_integration_capture_history_store \
  "${mock_pgdrill}" \
  "${store}" \
  "${output}" \
  2 >/dev/null 2>&1; then
  printf 'expected history attempt-count mismatch to fail\n' >&2
  exit 1
fi

printf '[integration/history-test] PASS\n'
