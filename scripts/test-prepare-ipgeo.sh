#!/usr/bin/env bash
set -euo pipefail

TEST_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
TEST_TMP="$(mktemp -d)"
trap 'rm -rf "${TEST_TMP}"' EXIT
mkdir -p "${TEST_TMP}/bin" "${TEST_TMP}/data"

cat > "${TEST_TMP}/bin/curl" <<'FAKE_CURL'
#!/usr/bin/env bash
set -euo pipefail
if [[ "${IPGEO_TEST_DOWNLOAD_MODE}" == "failure" ]]; then
  exit 28
fi
while [[ "$#" -gt 0 ]]; do
  if [[ "$1" == "--output" ]]; then
    printf '%s' 'untrusted replacement' > "$2"
    exit 0
  fi
  shift
done
exit 1
FAKE_CURL
chmod +x "${TEST_TMP}/bin/curl"

for test_mode in failure checksum; do
  printf '%s' 'original IPv4' > "${TEST_TMP}/data/ip2region_v4.xdb"
  printf '%s' 'original IPv6' > "${TEST_TMP}/data/ip2region_v6.xdb"
  if PATH="${TEST_TMP}/bin:${PATH}" FOREST_IPGEO_DIR="${TEST_TMP}/data" IPGEO_TEST_DOWNLOAD_MODE="${test_mode}" \
    bash "${TEST_ROOT}/scripts/prepare-ipgeo.sh" > "${TEST_TMP}/output" 2>&1; then
    echo "expected ${test_mode} to fail" >&2
    exit 1
  fi
  [[ "$(cat "${TEST_TMP}/data/ip2region_v4.xdb")" == 'original IPv4' ]]
  [[ "$(cat "${TEST_TMP}/data/ip2region_v6.xdb")" == 'original IPv6' ]]
  if compgen -G "${TEST_TMP}/data/.download.*" > /dev/null; then
    echo "temporary download files were not removed" >&2
    exit 1
  fi
done
echo "PASS: download failures and checksum mismatches preserve existing databases."
