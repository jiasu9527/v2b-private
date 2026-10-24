#!/usr/bin/env bash
set -euo pipefail

TEST_REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
TEST_TMP="$(mktemp -d)"
trap 'rm -rf "${TEST_TMP}"' EXIT
mkdir -p "${TEST_TMP}/scripts" "${TEST_TMP}/go-api"
cp "${TEST_REPO_ROOT}/scripts/appctl" "${TEST_TMP}/scripts/appctl"

# Both build tooling and resource preparation are local stubs. This test must
# never compile the project, download a database or contact a package registry.
cat > "${TEST_TMP}/fake-go" <<'FAKE_GO'
#!/usr/bin/env bash
set -euo pipefail
if [[ "$1" == build ]]; then
  output=""
  target="${!#}"
  for ((i=1; i <= $#; i++)); do
    if [[ "${!i}" == -o ]]; then
      j=$((i+1))
      output="${!j}"
    fi
  done
  [[ -n "${output}" ]]
  mkdir -p "$(dirname "${output}")"
  printf 'fake binary\n' > "${output}"
  printf 'build %s\n' "${target}" >> "${TEST_EVENT_LOG}"
fi
FAKE_GO
chmod +x "${TEST_TMP}/fake-go"

cat > "${TEST_TMP}/scripts/prepare-ipgeo.sh" <<'FAKE_PREPARE'
#!/usr/bin/env bash
set -euo pipefail
printf 'prepare %s\n' "${TEST_PREPARE_RESULT}" >> "${TEST_EVENT_LOG}"
if [[ "${TEST_PREPARE_RESULT}" != 0 ]]; then
  echo 'simulated offline resource download failure' >&2
  exit "${TEST_PREPARE_RESULT}"
fi
mkdir -p "${FOREST_IPGEO_DIR}"
printf 'prepared\n' > "${FOREST_IPGEO_DIR}/ready"
FAKE_PREPARE

for test_result in 0 23; do
  test_events="${TEST_TMP}/events-${test_result}"
  test_output="${TEST_TMP}/output-${test_result}"
  test_geo_dir="${TEST_TMP}/geo-${test_result}"
  if ! TEST_EVENT_LOG="${test_events}" TEST_PREPARE_RESULT="${test_result}" \
    FOREST_IPGEO_DIR="${test_geo_dir}" GO_BIN="${TEST_TMP}/fake-go" \
    bash "${TEST_TMP}/scripts/appctl" build > "${test_output}" 2>&1; then
    cat "${test_output}" >&2
    echo "appctl build should succeed when geo preparation returns ${test_result}" >&2
    exit 1
  fi
  [[ "$(cat "${test_events}")" == "$(printf 'build ./cmd/server\nbuild ./cmd/probe\nbuild ./cmd/probe\nprepare %s' "${test_result}")" ]]
  [[ -f "${TEST_TMP}/go-api/bin/forest" ]]
  [[ -f "${TEST_TMP}/storage/probe/forest-probe-linux-amd64.sha256" ]]
  [[ -f "${TEST_TMP}/storage/probe/forest-probe-linux-arm64.sha256" ]]
  if [[ "${test_result}" == 0 ]]; then
    [[ -f "${test_geo_dir}/ready" ]]
    if rg -F '地区库暂未完整准备' "${test_output}" >/dev/null; then
      echo 'successful preparation unexpectedly emitted a warning' >&2
      exit 1
    fi
  else
    [[ ! -e "${test_geo_dir}/ready" ]]
    rg -F '地区库暂未完整准备，网站仍可正常运行' "${test_output}" >/dev/null
  fi
done
echo 'PASS: appctl build prepares geography resources and tolerates preparation failure.'
