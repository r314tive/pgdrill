#!/usr/bin/env bash

set -Eeuo pipefail
umask 077

SCRIPT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
readonly SCRIPT_DIR
ROOT="$(cd -- "${SCRIPT_DIR}/../../.." && pwd)"
readonly ROOT
readonly CACHE_ROOT="${PGDRILL_INTEGRATION_CACHE:-${ROOT}/.cache/integration/cnpg}"
readonly INPUTS="${SCRIPT_DIR}"
readonly VERSION_BASE="${PGDRILL_INTEGRATION_VERSION:-v0.0.0-integration}"
readonly PGDRILL_INTEGRATION_LOG_PREFIX="integration/cnpg-host"

# shellcheck source=test/integration/lib/runtime.sh
source "${ROOT}/test/integration/lib/runtime.sh"
# shellcheck source=test/integration/lib/history.sh
source "${ROOT}/test/integration/lib/history.sh"

log() {
  pgdrill_integration_log "$@"
}

die() {
  pgdrill_integration_die "$@"
}

pgdrill_integration_require_commands awk curl docker find git go grep jq sed seq tar uname

git -C "${ROOT}" rev-parse --is-inside-work-tree >/dev/null 2>&1 ||
  die "integration test must run from a Git checkout"

readonly KIND_VERSION="0.31.0"
readonly KUBERNETES_VERSION="1.32.11"
readonly CNPG_VERSION="1.26.3"
readonly POSTGRES_VERSION="15.17"
readonly KIND_NODE_IMAGE="kindest/node:v1.32.11@sha256:5fc52d52a7b9574015299724bd68f183702956aa4a2116ae75a63cb574b35af8"
readonly OPERATOR_TAG="ghcr.io/cloudnative-pg/cloudnative-pg:1.26.3"
readonly OPERATOR_IMAGE="ghcr.io/cloudnative-pg/cloudnative-pg@sha256:8cf62c3d55a5db0ca934d118132b4b825b9d684ca0dce2b0235c2279adf14dad"
readonly POSTGRES_IMAGE="ghcr.io/cloudnative-pg/postgresql:15.17-202605110912-system-bookworm@sha256:ce92730c48402c2377ea438d978dd3d76702800a2d215fcbdd53ab83d4833524"
readonly MINIO_IMAGE="quay.io/minio/minio@sha256:a1ea29fa28355559ef137d71fc570e508a214ec84ff8083e39bc5428980b015e"
readonly MINIO_MC_IMAGE="quay.io/minio/mc@sha256:aead63c77f9db9107f1696fb08ecb0faeda23729cde94b0f663edf4fe09728e3"
readonly OPERATOR_RUNTIME_IMAGE="pgdrill.local/cloudnative-pg:sha256-8cf62c3d55a5db0ca934d118132b4b825b9d684ca0dce2b0235c2279adf14dad"
readonly POSTGRES_RUNTIME_IMAGE="pgdrill.local/postgresql:15.17-pgdrill-ce92730c48402c2377ea438d978dd3d76702800a2d215fcbdd53ab83d4833524"
readonly MINIO_RUNTIME_IMAGE="pgdrill.local/minio:sha256-a1ea29fa28355559ef137d71fc570e508a214ec84ff8083e39bc5428980b015e"
readonly MINIO_MC_RUNTIME_IMAGE="pgdrill.local/minio-mc:sha256-aead63c77f9db9107f1696fb08ecb0faeda23729cde94b0f663edf4fe09728e3"
readonly CNPG_MANIFEST_URL="https://raw.githubusercontent.com/cloudnative-pg/cloudnative-pg/release-1.26/releases/cnpg-1.26.3.yaml"
readonly CNPG_MANIFEST_SHA256="e465d8c3474a3fa6533938082c041322a208a66746f23d42083ed375a9339a6a"
readonly NAMESPACE="pgdrill-cnpg"

host_os="$(pgdrill_integration_host_os)"
readonly host_os
host_arch="$(pgdrill_integration_host_arch)"
readonly host_arch
docker_arch="$(pgdrill_integration_docker_arch)"
readonly docker_arch
[[ "${docker_arch}" == "${host_arch}" ]] ||
  die "Docker architecture ${docker_arch} does not match host architecture ${host_arch}"

case "${host_os}/${host_arch}" in
  darwin/amd64)
    readonly KIND_SHA256="a8b3cf77b2ad77aec5bf710d1a2589d9117576132af812885cad41e9dede4d4e"
    readonly KUBECTL_SHA256="8d0b610df71632d0e9b9c1aa16dde5ec666c05bf24e401ecf20fd27af16879ad"
    ;;
  darwin/arm64)
    readonly KIND_SHA256="88bf554fe9da6311c9f8c2d082613c002911a476f6b5090e9420b35d84e70c5c"
    readonly KUBECTL_SHA256="a39978a062f0df17d4a5551bd2e3a91eda90039196653935c50140be547141d3"
    ;;
  linux/amd64)
    readonly KIND_SHA256="eb244cbafcc157dff60cf68693c14c9a75c4e6e6fedaf9cd71c58117cb93e3fa"
    readonly KUBECTL_SHA256="48581d0e808bd8b7d3c3fc014e86b170e25a987df04c8a879b982b28a5180815"
    ;;
  linux/arm64)
    readonly KIND_SHA256="8e1014e87c34901cc422a1445866835d1e666f2a61301c27e722bdeab5a1f7e4"
    readonly KUBECTL_SHA256="b1c91c106ec20e61c5dff869e9a39e6af4fb96572bddaac9cce307dfa3ed2348"
    ;;
  *)
    die "unsupported CNPG integration platform ${host_os}/${host_arch}"
    ;;
esac

readonly RUNTIME_DIR="${CACHE_ROOT}/runtime/${host_os}/${host_arch}"
readonly KIND="${RUNTIME_DIR}/kind"
readonly KUBECTL="${RUNTIME_DIR}/kubectl"
readonly UPSTREAM_MANIFEST="${CACHE_ROOT}/downloads/cnpg-${CNPG_VERSION}.yaml"

prepare_download() {
  local name="$1"
  local url="$2"
  local expected="$3"
  local destination="$4"
  local mode="$5"
  local download

  if [[ -f "${destination}" ]] && pgdrill_integration_verify_file "${expected}" "${destination}"; then
    chmod "${mode}" "${destination}"
    log "using cached checksum-pinned ${name}"
    return
  fi

  mkdir -p "$(dirname -- "${destination}")"
  download="$(mktemp "${destination}.download.XXXXXX")"
  log "downloading checksum-pinned ${name}"
  if ! curl --fail --silent --show-error --location --proto '=https' --tlsv1.2 \
    --output "${download}" "${url}"; then
    rm -f "${download}"
    die "download ${name}"
  fi
  if ! pgdrill_integration_verify_file "${expected}" "${download}"; then
    rm -f "${download}"
    die "${name} checksum verification failed"
  fi
  chmod "${mode}" "${download}"
  mv "${download}" "${destination}"
}

prepare_image() {
  local image="$1"
  local image_arch

  if docker image inspect "${image}" >/dev/null 2>&1; then
    log "using cached image ${image}"
  else
    log "pulling immutable image ${image}"
    docker pull --platform "linux/${host_arch}" "${image}" >/dev/null
  fi
  image_arch="$(docker image inspect --format '{{.Architecture}}' "${image}")"
  [[ "${image_arch}" == "${host_arch}" ]] ||
    die "image ${image} architecture ${image_arch} does not match host ${host_arch}"
}

prepare_runtime_image() {
  local source_image="$1"
  local runtime_image="$2"
  local source_id runtime_id

  prepare_image "${source_image}"
  docker tag "${source_image}" "${runtime_image}"
  source_id="$(docker image inspect --format '{{.Id}}' "${source_image}")"
  runtime_id="$(docker image inspect --format '{{.Id}}' "${runtime_image}")"
  [[ "${runtime_id}" == "${source_id}" ]] ||
    die "runtime tag ${runtime_image} does not resolve to pinned image ${source_image}"
}

prepare_download \
  "KinD ${KIND_VERSION}" \
  "https://kind.sigs.k8s.io/dl/v${KIND_VERSION}/kind-${host_os}-${host_arch}" \
  "${KIND_SHA256}" \
  "${KIND}" \
  0755
prepare_download \
  "kubectl ${KUBERNETES_VERSION}" \
  "https://dl.k8s.io/release/v${KUBERNETES_VERSION}/bin/${host_os}/${host_arch}/kubectl" \
  "${KUBECTL_SHA256}" \
  "${KUBECTL}" \
  0755
prepare_download \
  "CloudNativePG ${CNPG_VERSION} manifest" \
  "${CNPG_MANIFEST_URL}" \
  "${CNPG_MANIFEST_SHA256}" \
  "${UPSTREAM_MANIFEST}" \
  0644

[[ "$("${KIND}" version)" == "kind v${KIND_VERSION} "* ]] ||
  die "unexpected KinD version"
[[ "$("${KUBECTL}" version --client -o json | jq -r .clientVersion.gitVersion)" == "v${KUBERNETES_VERSION}" ]] ||
  die "unexpected kubectl version"

prepare_image "${KIND_NODE_IMAGE}"
prepare_runtime_image "${OPERATOR_IMAGE}" "${OPERATOR_RUNTIME_IMAGE}"
prepare_runtime_image "${POSTGRES_IMAGE}" "${POSTGRES_RUNTIME_IMAGE}"
prepare_runtime_image "${MINIO_IMAGE}" "${MINIO_RUNTIME_IMAGE}"
prepare_runtime_image "${MINIO_MC_IMAGE}" "${MINIO_MC_RUNTIME_IMAGE}"

log "checking native Barman Cloud prerequisites in the PostgreSQL image"
docker run \
  --rm \
  --pull never \
  --platform "linux/${host_arch}" \
  --network none \
  --read-only \
  --cap-drop ALL \
  --security-opt no-new-privileges=true \
  --entrypoint /bin/sh \
  "${POSTGRES_IMAGE}" \
  -c 'for binary in barman-cloud-backup barman-cloud-backup-delete barman-cloud-backup-list barman-cloud-check-wal-archive barman-cloud-restore barman-cloud-wal-archive barman-cloud-wal-restore; do command -v "${binary}" || exit 1; done'

pgdrill_integration_prepare_host_pgdrill "${ROOT}" "${CACHE_ROOT}" "${VERSION_BASE}"
pgdrill_version="$("${PGDRILL_INT_BINARY}" version)"
readonly pgdrill_version
[[ "${pgdrill_version}" == "pgdrill ${PGDRILL_INT_VERSION} (${PGDRILL_INT_COMMIT}, "* ]] ||
  die "pgdrill build identity mismatch: ${pgdrill_version}"

run_stamp="$(date -u +%Y%m%dt%H%M%Sz)-$$"
readonly run_stamp
readonly RUN_DIR="${CACHE_ROOT}/runs/${run_stamp}"
readonly CLUSTER_NAME="pgdrill-cnpg-$$"
readonly CONTEXT="kind-${CLUSTER_NAME}"
readonly KUBECONFIG_PATH="${RUN_DIR}/kubeconfig"
readonly CONFIG="${RUN_DIR}/pgdrill.yaml"
readonly REPORT="${RUN_DIR}/report.json"
readonly HISTORY="${RUN_DIR}/history"
readonly OPERATOR_MANIFEST="${RUN_DIR}/cnpg-${CNPG_VERSION}-pinned.yaml"
readonly INFRA_MANIFEST="${RUN_DIR}/infra-pinned.yaml"
readonly SOURCE_MANIFEST="${RUN_DIR}/source-pinned.yaml"
readonly VERIFY_CLUSTER="verify-source-${run_stamp}"
readonly drill_id="integration-cnpg-${run_stamp}"

mkdir -p "${RUN_DIR}"
host_context="$("${KUBECTL}" config current-context 2>/dev/null || true)"
readonly host_context
cluster_created=false
run_completed=false

k() {
  "${KUBECTL}" --kubeconfig "${KUBECONFIG_PATH}" --context "${CONTEXT}" "$@"
}

load_runtime_image() {
  local image="$1"
  local node="${CLUSTER_NAME}-control-plane"

  docker save "${image}" |
    docker exec --privileged -i "${node}" \
      ctr --namespace=k8s.io images import \
      --platform "linux/${host_arch}" \
      --digests \
      --snapshotter=overlayfs \
      - 2>&1 |
    tee -a "${RUN_DIR}/kind-load.log"
}

capture_cluster() {
  [[ "${cluster_created}" == "true" ]] || return 0
  set +e
  k get nodes -o wide >"${RUN_DIR}/nodes.txt" 2>&1
  k get deployments,pods,jobs,services,pvc -A -o wide >"${RUN_DIR}/resources.txt" 2>&1
  k get clusters.postgresql.cnpg.io,backups.postgresql.cnpg.io -n "${NAMESPACE}" -o yaml >"${RUN_DIR}/cnpg-resources.yaml" 2>&1
  k get events -A --sort-by=.metadata.creationTimestamp >"${RUN_DIR}/events.txt" 2>&1
  k logs -n cnpg-system deployment/cnpg-controller-manager --all-containers --tail=2000 >"${RUN_DIR}/operator.log" 2>&1
  k logs -n "${NAMESPACE}" deployment/minio --tail=1000 >"${RUN_DIR}/minio.log" 2>&1
  k logs -n "${NAMESPACE}" source-1 -c postgres --tail=2000 >"${RUN_DIR}/source-postgres.log" 2>&1
  set -e
}

delete_cluster() {
  [[ "${cluster_created}" == "true" ]] || return 0
  "${KIND}" delete cluster --name "${CLUSTER_NAME}" >"${RUN_DIR}/kind-delete.log" 2>&1 || true
  cluster_created=false
}

cleanup() {
  local status="$?"
  local current_context
  trap - EXIT INT TERM

  capture_cluster
  delete_cluster
  current_context="$("${KUBECTL}" config current-context 2>/dev/null || true)"
  if [[ "${current_context}" != "${host_context}" ]]; then
    printf '[integration/cnpg-host] ERROR: host kubectl context changed from %s to %s\n' \
      "${host_context}" "${current_context}" >&2
    status=1
  fi
  rm -f "${KUBECONFIG_PATH}"
  if ! (pgdrill_integration_finalize_artifacts "${RUN_DIR}" "${CACHE_ROOT}"); then
    status=1
  fi

  if [[ "${status}" -eq 0 && "${run_completed}" == "true" ]]; then
    log "PASS: CNPG backup, post-backup WAL recovery, probes, policy, and cleanup completed"
  else
    log "FAIL: disposable CNPG drill did not complete"
  fi
  log "artifacts retained at ${RUN_DIR}"
  exit "${status}"
}
trap cleanup EXIT INT TERM

sed \
  -e "s|${OPERATOR_TAG}|${OPERATOR_RUNTIME_IMAGE}|g" \
  -e 's|imagePullPolicy: Always|imagePullPolicy: Never|g' \
  "${UPSTREAM_MANIFEST}" >"${OPERATOR_MANIFEST}"
grep -F "${OPERATOR_RUNTIME_IMAGE}" "${OPERATOR_MANIFEST}" >/dev/null ||
  die "operator manifest does not use the validated runtime image"
if grep -F "${OPERATOR_TAG}" "${OPERATOR_MANIFEST}" >/dev/null; then
  die "operator manifest retains an unpinned image"
fi
if grep -F 'imagePullPolicy: Always' "${OPERATOR_MANIFEST}" >/dev/null; then
  die "operator manifest retains forced network pulls"
fi
sed \
  -e "s|${MINIO_IMAGE}|${MINIO_RUNTIME_IMAGE}|g" \
  -e "s|${MINIO_MC_IMAGE}|${MINIO_MC_RUNTIME_IMAGE}|g" \
  -e 's|imagePullPolicy: IfNotPresent|imagePullPolicy: Never|g' \
  "${INPUTS}/infra.yaml" >"${INFRA_MANIFEST}"
sed \
  -e "s|${POSTGRES_IMAGE}|${POSTGRES_RUNTIME_IMAGE}|g" \
  -e 's|imagePullPolicy: IfNotPresent|imagePullPolicy: Never|g' \
  "${INPUTS}/source.yaml" >"${SOURCE_MANIFEST}"
grep -F "imageName: ${POSTGRES_IMAGE}" "${INPUTS}/source.yaml" >/dev/null ||
  die "source manifest does not use the expected PostgreSQL image"
grep -F "image: ${MINIO_IMAGE}" "${INPUTS}/infra.yaml" >/dev/null ||
  die "infrastructure manifest does not use the expected MinIO image"
grep -F "image: ${MINIO_MC_IMAGE}" "${INPUTS}/infra.yaml" >/dev/null ||
  die "infrastructure manifest does not use the expected MinIO client image"
grep -F "imageName: ${POSTGRES_RUNTIME_IMAGE}" "${SOURCE_MANIFEST}" >/dev/null ||
  die "generated source manifest does not use the validated runtime image"
grep -F "image: ${MINIO_RUNTIME_IMAGE}" "${INFRA_MANIFEST}" >/dev/null ||
  die "generated infrastructure manifest does not use the validated MinIO image"
grep -F "image: ${MINIO_MC_RUNTIME_IMAGE}" "${INFRA_MANIFEST}" >/dev/null ||
  die "generated infrastructure manifest does not use the validated MinIO client image"

cat >"${CONFIG}" <<EOF
cluster:
  name: cnpg-local-source

target:
  type: kubernetes
  labels:
    environment: local-integration
  kubernetes:
    namespace: ${NAMESPACE}
    kubeconfig: ${KUBECONFIG_PATH}
    context: ${CONTEXT}
    kubectl_binary: ${KUBECTL}
    command_timeout: 2m
    wait_timeout: 20m
    poll_interval: 2s
    cleanup_pvc: true
    cleanup_on_fail: true
    capture_logs: true
    events_tail: 300
    postgres_log_tail: 3000
  cnpg:
    source_cluster: source
    verify_cluster_name: ${VERIFY_CLUSTER}
    storage_size: 1Gi
    cpu_request: 100m
    memory_request: 256Mi
    memory_limit: 1Gi

recovery:
  target: latest

policy:
  maximum_rto: 20m
  require_recovery_target: true
  require_cleanup: true

probes:
  - type: pg_isready
    timeout: 30s
  - type: sql
    name: post_backup_wal_replayed
    query: "select 1 / case when count(*) = 101 and bool_or(payload = 'post-backup-wal-sentinel') then 1 else 0 end from public.pgdrill_integration_probe"
    timeout: 30s
  - type: amcheck
    name: structural_amcheck
    mode: database
    args:
      on_error_stop: "true"
    timeout: 2m
  - type: pg_dump
    name: schema_dump
    mode: schema
    timeout: 2m

report:
  format: json
  path: ${REPORT}
EOF

{
  printf 'host_context_before=%s\n' "${host_context}"
  printf 'kind=%s\n' "$("${KIND}" version)"
  printf 'kubectl=%s\n' "$("${KUBECTL}" version --client -o json | jq -r .clientVersion.gitVersion)"
  printf 'cnpg=%s\n' "${CNPG_VERSION}"
  printf 'kubernetes=%s\n' "${KUBERNETES_VERSION}"
  printf 'postgresql=%s\n' "${POSTGRES_VERSION}"
  printf 'pgdrill=%s\n' "${pgdrill_version}"
  printf 'kind_binary_sha256=%s\n' "$(pgdrill_integration_sha256_file "${KIND}")"
  printf 'kubectl_binary_sha256=%s\n' "$(pgdrill_integration_sha256_file "${KUBECTL}")"
  printf 'cnpg_upstream_manifest_sha256=%s\n' "$(pgdrill_integration_sha256_file "${UPSTREAM_MANIFEST}")"
  printf 'cnpg_pinned_manifest_sha256=%s\n' "$(pgdrill_integration_sha256_file "${OPERATOR_MANIFEST}")"
  pgdrill_integration_print_runtime_inventory "${docker_arch}"
  for image in \
    "${KIND_NODE_IMAGE}" \
    "${OPERATOR_IMAGE}" "${OPERATOR_RUNTIME_IMAGE}" \
    "${POSTGRES_IMAGE}" "${POSTGRES_RUNTIME_IMAGE}" \
    "${MINIO_IMAGE}" "${MINIO_RUNTIME_IMAGE}" \
    "${MINIO_MC_IMAGE}" "${MINIO_MC_RUNTIME_IMAGE}"; do
    docker image inspect \
      --format 'tags={{json .RepoTags}} id={{.Id}} digests={{json .RepoDigests}} architecture={{.Architecture}}' \
      "${image}"
  done
} >"${RUN_DIR}/runtime.txt"

log "creating isolated KinD cluster ${CLUSTER_NAME}"
"${KIND}" create cluster \
  --name "${CLUSTER_NAME}" \
  --image "${KIND_NODE_IMAGE}" \
  --kubeconfig "${KUBECONFIG_PATH}" \
  --wait 5m 2>&1 | tee "${RUN_DIR}/kind-create.log"
cluster_created=true
[[ "$(k config current-context)" == "${CONTEXT}" ]] ||
  die "isolated kubeconfig context mismatch"

log "loading validated platform-specific workload images into the isolated cluster"
for image in \
  "${OPERATOR_RUNTIME_IMAGE}" \
  "${POSTGRES_RUNTIME_IMAGE}" \
  "${MINIO_RUNTIME_IMAGE}" \
  "${MINIO_MC_RUNTIME_IMAGE}"; do
  load_runtime_image "${image}"
done

log "installing CloudNativePG ${CNPG_VERSION}"
k apply --server-side -f "${OPERATOR_MANIFEST}" >"${RUN_DIR}/operator-apply.log"
k rollout status deployment/cnpg-controller-manager -n cnpg-system --timeout=5m |
  tee "${RUN_DIR}/operator-rollout.log"

log "starting the disposable MinIO repository"
k apply -f "${INFRA_MANIFEST}" >"${RUN_DIR}/infra-apply.log"
k rollout status deployment/minio -n "${NAMESPACE}" --timeout=5m |
  tee "${RUN_DIR}/minio-rollout.log"
k wait --for=condition=complete job/minio-create-bucket -n "${NAMESPACE}" --timeout=5m |
  tee "${RUN_DIR}/bucket-job.log"

log "creating the checksummed source cluster"
k apply -f "${SOURCE_MANIFEST}" >"${RUN_DIR}/source-apply.log"
k wait --for=condition=Ready cluster/source -n "${NAMESPACE}" --timeout=10m |
  tee "${RUN_DIR}/source-ready.log"

log "creating the 100-row base-backup boundary"
k exec -i -n "${NAMESPACE}" source-1 -c postgres -- \
  psql -U postgres -d postgres -v ON_ERROR_STOP=1 <<'SQL' | tee "${RUN_DIR}/source-prepare.log"
CREATE EXTENSION amcheck;
CREATE TABLE public.pgdrill_integration_probe (
  id integer PRIMARY KEY,
  payload text NOT NULL,
  committed_at timestamptz NOT NULL DEFAULT clock_timestamp()
);
INSERT INTO public.pgdrill_integration_probe (id, payload)
SELECT id, 'base-backup-row-' || id
FROM generate_series(1, 100) AS id;
CHECKPOINT;
SQL

log "taking a real CNPG object-store backup"
k apply -f "${INPUTS}/backup.yaml" >"${RUN_DIR}/backup-apply.log"
k wait --for=jsonpath='{.status.phase}'=completed backup/source-backup \
  -n "${NAMESPACE}" --timeout=10m | tee "${RUN_DIR}/backup-complete.log"

log "committing and archiving the post-backup WAL sentinel"
k exec -n "${NAMESPACE}" source-1 -c postgres -- \
  psql -U postgres -d postgres -v ON_ERROR_STOP=1 \
  -c "INSERT INTO public.pgdrill_integration_probe (id, payload) VALUES (101, 'post-backup-wal-sentinel');" |
  tee "${RUN_DIR}/sentinel-insert.log"
sentinel_wal="$(k exec -n "${NAMESPACE}" source-1 -c postgres -- psql -U postgres -d postgres -Atqc 'SELECT pg_walfile_name(pg_current_wal_lsn());')"
readonly sentinel_wal
k exec -n "${NAMESPACE}" source-1 -c postgres -- \
  psql -U postgres -d postgres -Atqc 'SELECT pg_switch_wal();' >/dev/null

archived=false
for attempt in $(seq 1 120); do
  last_archived="$(k exec -n "${NAMESPACE}" source-1 -c postgres -- psql -U postgres -d postgres -Atqc "SELECT COALESCE(last_archived_wal, '') FROM pg_stat_archiver;" || true)"
  printf 'attempt=%s sentinel=%s last_archived=%s\n' \
    "${attempt}" "${sentinel_wal}" "${last_archived}" >>"${RUN_DIR}/wal-archive.log"
  if [[ "${last_archived}" == "${sentinel_wal}" || "${last_archived}" > "${sentinel_wal}" ]]; then
    archived=true
    break
  fi
  sleep 2
done
[[ "${archived}" == "true" ]] ||
  die "post-backup WAL ${sentinel_wal} was not archived"

log "running the candidate artifact through managed CNPG recovery"
"${PGDRILL_INT_BINARY}" doctor -f "${CONFIG}" -format json >"${RUN_DIR}/doctor.json"
"${PGDRILL_INT_BINARY}" target verify \
  -f "${CONFIG}" \
  -discover \
  -confirm-create \
  -drill-id "${drill_id}" \
  -attempt-id attempt-1 \
  -history-dir "${HISTORY}" 2>&1 | tee "${RUN_DIR}/run.log"

[[ -f "${REPORT}" ]] || die "pgdrill did not persist report.json"
"${PGDRILL_INT_BINARY}" report show "${REPORT}" | tee "${RUN_DIR}/report.txt"
report_version="$(jq -r .pgdrill_version "${REPORT}")"
[[ "${report_version}" == "pgdrill ${PGDRILL_INT_VERSION} (${PGDRILL_INT_COMMIT}, "* ]] ||
  die "report build identity mismatch: ${report_version}"
jq -e --arg cnpg "${CNPG_VERSION}" --arg pg "${POSTGRES_VERSION}" '
  .status == "passed" and
  .provider == "" and
  .target.type == "kubernetes" and
  .recovery_target.type == "latest" and
  ([.checks[] | select(.name == "cnpg-instance-ready" and .status == "passed" and .attributes.operator_version == $cnpg)] | length) == 1 and
  ([.checks[] | select(.name == "tool.postgres" and .status == "passed" and (.message | contains($pg)))] | length) == 1 and
  ([.checks[] | select(.name == "post_backup_wal_replayed" and .status == "passed")] | length) == 1 and
  ([.checks[] | select(.name == "structural_amcheck" and .status == "passed")] | length) == 1 and
  ([.checks[] | select(.name == "schema_dump" and .status == "passed")] | length) == 1 and
  ([.policy_evaluation.verdicts[] | select(.required and .status != "passed")] | length) == 0 and
  ([.operations[] | select(.state != "succeeded")] | length) == 0 and
  (.artifacts | length) == 1
' "${REPORT}" >/dev/null || die "report acceptance assertions failed"
pgdrill_integration_verify_history_attempt \
  "${PGDRILL_INT_BINARY}" \
  "${HISTORY}" \
  "${drill_id}" \
  attempt-1 \
  "${RUN_DIR}/history-attempt"
pgdrill_integration_capture_history_store \
  "${PGDRILL_INT_BINARY}" \
  "${HISTORY}" \
  "${RUN_DIR}" \
  1
pgdrill_integration_verify_artifact_store \
  "${PGDRILL_INT_BINARY}" \
  "${REPORT}.artifacts" \
  "${HISTORY}" \
  "${RUN_DIR}" \
  1

if k get cluster "${VERIFY_CLUSTER}" -n "${NAMESPACE}" >/dev/null 2>&1; then
  die "verify cluster remains after pgdrill cleanup"
fi
if [[ -n "$(k get pvc -n "${NAMESPACE}" -l "cnpg.io/cluster=${VERIFY_CLUSTER}" -o name)" ]]; then
  die "verify-cluster PVC remains after pgdrill cleanup"
fi

{
  printf 'source_rows=%s\n' "$(k exec -n "${NAMESPACE}" source-1 -c postgres -- psql -U postgres -d postgres -Atqc 'SELECT count(*) FROM public.pgdrill_integration_probe;')"
  printf 'sentinel_wal=%s\n' "${sentinel_wal}"
  printf 'backup_phase=%s\n' "$(k get backup/source-backup -n "${NAMESPACE}" -o jsonpath='{.status.phase}')"
  printf 'source_image_id=%s\n' "$(k get pod/source-1 -n "${NAMESPACE}" -o jsonpath='{.status.containerStatuses[?(@.name=="postgres")].imageID}')"
  printf 'operator_image_id=%s\n' "$(k get pod -n cnpg-system -l app.kubernetes.io/name=cloudnative-pg -o jsonpath='{.items[0].status.containerStatuses[0].imageID}')"
} >"${RUN_DIR}/source-state.txt"

run_completed=true
