#!/usr/bin/env bash
set -euo pipefail
ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
TMP_DIR="$(mktemp -d)"
trap 'rm -rf "${TMP_DIR}"' EXIT
mkdir -p "${TMP_DIR}/bin" "${TMP_DIR}/downloads"
printf 'probe-amd64\n' > "${TMP_DIR}/downloads/forest-probe-linux-amd64"
sha256sum "${TMP_DIR}/downloads/forest-probe-linux-amd64" | sed "s#${TMP_DIR}/downloads/##" > "${TMP_DIR}/downloads/forest-probe-linux-amd64.sha256"
cat > "${TMP_DIR}/bin/curl" <<'SH'
#!/usr/bin/env bash
set -euo pipefail
url="$2"; output="$4"; name="$(basename "${url}")"
case "${name}" in amd64|arm64) name="forest-probe-linux-${name}";; esac
case "${name}" in amd64.sha256|arm64.sha256) name="forest-probe-linux-${name}";; esac
cp "${PROBE_DOWNLOAD_DIR}/${name}" "${output}"
printf '%s\n' "${url}" >> "${PROBE_LOG}"
SH
cat > "${TMP_DIR}/bin/systemctl" <<'SH'
#!/usr/bin/env bash
printf '%s\n' "$*" >> "${PROBE_LOG}"
SH
cat > "${TMP_DIR}/bin/uname" <<'SH'
#!/usr/bin/env bash
[[ "$1" == -s ]] && echo Linux || echo x86_64
SH
chmod +x "${TMP_DIR}/bin/curl" "${TMP_DIR}/bin/systemctl" "${TMP_DIR}/bin/uname"
run_install() {
  PATH="${TMP_DIR}/bin:${PATH}" PROBE_DOWNLOAD_DIR="${TMP_DIR}/downloads" PROBE_LOG="${TMP_DIR}/log" \
  FOREST_PROBE_INSTALL_DIR="${TMP_DIR}/usr/local/bin" FOREST_PROBE_CONFIG_DIR="${TMP_DIR}/etc/forest-probe" \
  FOREST_PROBE_SYSTEMD_DIR="${TMP_DIR}/etc/systemd/system" UNAME_BIN="${TMP_DIR}/bin/uname" API_URL="https://panel.example.com/api/v1" TOKEN="safe_token-1" INTERVAL=45 \
  bash "${ROOT_DIR}/scripts/forest-probe-install.sh"
}
run_install
run_install
[[ -x "${TMP_DIR}/usr/local/bin/forest-probe" ]]
[[ "$(stat -f '%Lp' "${TMP_DIR}/etc/forest-probe/config.json")" == "600" ]]
rg -F '"api_url":"https://panel.example.com/api/v1"' "${TMP_DIR}/etc/forest-probe/config.json" >/dev/null
rg -F '"interval":"45s"' "${TMP_DIR}/etc/forest-probe/config.json" >/dev/null
rg -F 'ExecStart=/usr/local/bin/forest-probe -config /etc/forest-probe/config.json' "${TMP_DIR}/etc/systemd/system/forest-probe.service" >/dev/null
[[ "$(rg -c 'daemon-reload' "${TMP_DIR}/log")" == 2 ]]
[[ "$(rg -c 'enable --now forest-probe.service' "${TMP_DIR}/log")" == 2 ]]
if PATH="${TMP_DIR}/bin:${PATH}" API_URL='file:///tmp/nope' TOKEN='bad token' bash "${ROOT_DIR}/scripts/forest-probe-install.sh" >/dev/null 2>&1; then
  echo 'unsafe API URL/token unexpectedly accepted' >&2; exit 1
fi
echo 'forest probe install test passed'
