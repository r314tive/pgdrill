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
readonly output="${tmp_dir}/output"

cat >"${mock_pgdrill}" <<'EOF'
#!/usr/bin/env bash
set -Eeuo pipefail

subcommand="${2:-}"
format="text"
for argument in "$@"; do
  if [[ "${previous:-}" == "-format" ]]; then
    format="${argument}"
  fi
  previous="${argument}"
done

case "${subcommand}/${format}" in
  show/json)
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
  show/text)
    printf 'Run ID: mock-run\nAttempt: attempt-1\nStatus: passed\n'
    ;;
  list/json)
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
  list/text)
    printf 'Attempts: 1\n'
    ;;
  verify/json)
    cat <<'JSON'
{
  "schema_version": "pgdrill.history-verification/v1alpha1",
  "attempts": 1,
  "maintenance_required": false
}
JSON
    ;;
  verify/text)
    printf 'Attempts: 1\nMaintenance required: false\n'
    ;;
  *)
    printf 'unexpected mock invocation: %s\n' "$*" >&2
    exit 1
    ;;
esac
EOF
chmod 0755 "${mock_pgdrill}"
mkdir -p "${store}" "${output}"
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

[[ -s "${output}/attempt.json" ]]
[[ -s "${output}/attempt.txt" ]]
[[ -s "${output}/history-list.json" ]]
[[ -s "${output}/history-list.txt" ]]
[[ -s "${output}/history-verify.json" ]]
[[ -s "${output}/history-verify.txt" ]]
[[ -s "${output}/history-store.tar.gz" ]]
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
