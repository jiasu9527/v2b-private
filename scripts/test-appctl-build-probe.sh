#!/usr/bin/env bash
set -euo pipefail
REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
TMP_DIR="$(mktemp -d)"; trap 'rm -rf "${TMP_DIR}"' EXIT
mkdir -p "${TMP_DIR}/scripts" "${TMP_DIR}/go-api"
cp "${REPO_ROOT}/scripts/appctl" "${TMP_DIR}/scripts/appctl"; chmod +x "${TMP_DIR}/scripts/appctl"
cat > "${TMP_DIR}/fake-go" <<'SH'
#!/usr/bin/env bash
printf 'GOOS=%s GOARCH=%s %s\n' "${GOOS:-}" "${GOARCH:-}" "$*" >> "${GO_LOG}"
if [[ "$1" == build ]]; then
  for ((i=1; i <= $#; i++)); do
    if [[ "${!i}" == -o ]]; then j=$((i+1)); mkdir -p "$(dirname "${!j}")"; printf binary > "${!j}"; fi
  done
fi
SH
chmod +x "${TMP_DIR}/fake-go"
GO_LOG="${TMP_DIR}/go.log" GO_BIN="${TMP_DIR}/fake-go" "${TMP_DIR}/scripts/appctl" build-probe
for arch in amd64 arm64; do
  [[ -f "${TMP_DIR}/storage/probe/forest-probe-linux-${arch}" ]]
  [[ -f "${TMP_DIR}/storage/probe/forest-probe-linux-${arch}.sha256" ]]
  rg -F "GOOS=linux GOARCH=${arch} build -o" "${TMP_DIR}/go.log" >/dev/null
done
echo 'appctl build-probe test passed'
