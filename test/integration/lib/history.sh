#!/usr/bin/env bash

# Shared assertions for integration scenarios that persist pgdrill history.
# This file is sourced by both host and container scripts, so it deliberately
# does not change shell options or the caller's umask.

pgdrill_integration_history_error() {
  printf '[integration/history] ERROR: %s\n' "$*" >&2
  return 1
}

pgdrill_integration_verify_history_attempt() {
  local _history_binary="$1"
  local _history_store="$2"
  local _history_run_id="$3"
  local _history_attempt_id="$4"
  local _history_artifact_prefix="$5"
  local _history_json_artifact="${_history_artifact_prefix}.json"
  local _history_text_artifact="${_history_artifact_prefix}.txt"

  [[ -x "${_history_binary}" ]] ||
    pgdrill_integration_history_error "pgdrill binary is not executable: ${_history_binary}" ||
    return 1
  [[ -d "${_history_store}" ]] ||
    pgdrill_integration_history_error "history store does not exist: ${_history_store}" ||
    return 1

  if ! "${_history_binary}" history show \
    -store "${_history_store}" \
    -attempt-id "${_history_attempt_id}" \
    -format json \
    "${_history_run_id}" >"${_history_json_artifact}"; then
    pgdrill_integration_history_error \
      "cannot read history for ${_history_run_id}/${_history_attempt_id}" ||
      return 1
  fi
  if ! "${_history_binary}" history show \
    -store "${_history_store}" \
    -attempt-id "${_history_attempt_id}" \
    "${_history_run_id}" >"${_history_text_artifact}"; then
    pgdrill_integration_history_error \
      "cannot render history for ${_history_run_id}/${_history_attempt_id}" ||
      return 1
  fi

  grep -F "\"run_id\": \"${_history_run_id}\"" "${_history_json_artifact}" >/dev/null ||
    pgdrill_integration_history_error "history run id does not match ${_history_run_id}" ||
    return 1
  grep -F "\"attempt_id\": \"${_history_attempt_id}\"" "${_history_json_artifact}" >/dev/null ||
    pgdrill_integration_history_error "history attempt id does not match ${_history_attempt_id}" ||
    return 1
  grep -F '"status": "passed"' "${_history_json_artifact}" >/dev/null ||
    pgdrill_integration_history_error \
      "history report is not passed for ${_history_run_id}/${_history_attempt_id}" ||
    return 1
  grep -F '"type": "run_finished"' "${_history_json_artifact}" >/dev/null ||
    pgdrill_integration_history_error \
      "history has no terminal event for ${_history_run_id}/${_history_attempt_id}" ||
    return 1
}

pgdrill_integration_capture_history_store() {
  local _history_binary="$1"
  local _history_store="$2"
  local _history_output_dir="$3"
  local _history_expected_attempts="$4"
  local _history_list_json="${_history_output_dir}/history-list.json"
  local _history_list_text="${_history_output_dir}/history-list.txt"
  local _history_verify_json="${_history_output_dir}/history-verify.json"
  local _history_verify_text="${_history_output_dir}/history-verify.txt"
  local _history_archive="${_history_output_dir}/history-store.tar.gz"
  local _history_archive_index="${_history_output_dir}/history-store-contents.txt"
  local _history_observed_attempts
  local _history_store_parent
  local _history_store_name

  case "${_history_expected_attempts}" in
    '' | *[!0-9]*)
      pgdrill_integration_history_error \
        "expected attempt count must be a non-negative integer" ||
        return 1
      ;;
  esac
  [[ -d "${_history_output_dir}" ]] ||
    pgdrill_integration_history_error "history output directory does not exist: ${_history_output_dir}" ||
    return 1

  if ! "${_history_binary}" history list \
    -store "${_history_store}" \
    -limit 1000 \
    -format json >"${_history_list_json}"; then
    pgdrill_integration_history_error "cannot list history store ${_history_store}" ||
      return 1
  fi
  if ! "${_history_binary}" history list \
    -store "${_history_store}" \
    -limit 1000 >"${_history_list_text}"; then
    pgdrill_integration_history_error "cannot render history store ${_history_store}" ||
      return 1
  fi

  _history_observed_attempts="$(grep -c '^[[:space:]]*"attempt_id":' "${_history_list_json}" || true)"
  if [[ "${_history_observed_attempts}" != "${_history_expected_attempts}" ]]; then
    pgdrill_integration_history_error \
      "history contains ${_history_observed_attempts} attempts, expected ${_history_expected_attempts}" ||
      return 1
  fi
  if grep -F '"report_available": false' "${_history_list_json}" >/dev/null; then
    pgdrill_integration_history_error "history contains an attempt without a terminal report" ||
      return 1
  fi
  if ! "${_history_binary}" history verify \
    -store "${_history_store}" \
    -format json >"${_history_verify_json}"; then
    pgdrill_integration_history_error "cannot fully verify history store ${_history_store}" ||
      return 1
  fi
  if ! "${_history_binary}" history verify \
    -store "${_history_store}" >"${_history_verify_text}"; then
    pgdrill_integration_history_error "cannot render history verification for ${_history_store}" ||
      return 1
  fi
  grep -F "\"attempts\": ${_history_expected_attempts}" "${_history_verify_json}" >/dev/null ||
    pgdrill_integration_history_error \
      "history verification attempt count does not match ${_history_expected_attempts}" ||
    return 1
  grep -F '"maintenance_required": false' "${_history_verify_json}" >/dev/null ||
    pgdrill_integration_history_error "history verification requires maintenance" ||
    return 1

  _history_store_parent="$(cd -- "$(dirname -- "${_history_store}")" && pwd)"
  _history_store_name="$(basename -- "${_history_store}")"
  if [[ "${_history_store_name}" == "." || "${_history_store_name}" == "/" ]]; then
    pgdrill_integration_history_error "history store path cannot be archived safely: ${_history_store}" ||
      return 1
  fi
  if ! tar -C "${_history_store_parent}" -czf "${_history_archive}" "${_history_store_name}"; then
    pgdrill_integration_history_error "cannot archive history store ${_history_store}" ||
      return 1
  fi
  if ! tar -tzf "${_history_archive}" >"${_history_archive_index}"; then
    pgdrill_integration_history_error "cannot inspect history archive ${_history_archive}" ||
      return 1
  fi
  grep -F "${_history_store_name}/store.json" "${_history_archive_index}" >/dev/null ||
    pgdrill_integration_history_error "history archive has no store metadata" ||
    return 1
}
