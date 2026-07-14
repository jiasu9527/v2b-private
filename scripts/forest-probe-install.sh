#!/usr/bin/env bash
set -euo pipefail

API_URL="${API_URL:-}"
TOKEN="${TOKEN:-}"
INTERVAL="${INTERVAL:-30}"
DOWNLOAD_BASE="${DOWNLOAD_BASE:-}"
INSTALL_DIR="${FOREST_PROBE_INSTALL_DIR:-/usr/local/bin}"
CONFIG_DIR="${FOREST_PROBE_CONFIG_DIR:-/etc/forest-probe}"
SYSTEMD_DIR="${FOREST_PROBE_SYSTEMD_DIR:-/etc/systemd/system}"
UNAME_BIN="${UNAME_BIN:-uname}"

case "${API_URL}" in http://*|https://*) ;; *) echo "API_URL must use http or https" >&2; exit 1;; esac
case "${API_URL}" in *[\'\"\\$'\r\n ']* ) echo "API_URL contains unsafe characters" >&2; exit 1;; esac
case "${TOKEN}" in ''|*[!A-Za-z0-9._~-]* ) echo "TOKEN contains unsafe characters" >&2; exit 1;; esac
case "${INTERVAL}" in *[!0-9]*|'') echo "INTERVAL must be a positive number" >&2; exit 1;; esac
if (( INTERVAL < 1 || INTERVAL > 3600 )); then echo "INTERVAL must be between 1 and 3600" >&2; exit 1; fi
case "$("${UNAME_BIN}" -s)" in Linux) ;; *) echo "forest-probe only supports Linux" >&2; exit 1;; esac
case "$("${UNAME_BIN}" -m)" in x86_64|amd64) ARCH=amd64;; aarch64|arm64) ARCH=arm64;; *) echo "unsupported architecture: $("${UNAME_BIN}" -m)" >&2; exit 1;; esac

if [[ -z "${DOWNLOAD_BASE}" ]]; then
  scheme="${API_URL%%://*}"
  authority_path="${API_URL#*://}"
  origin="${scheme}://${authority_path%%/*}"
  DOWNLOAD_BASE="${origin}/probe/download/linux"
fi
case "${DOWNLOAD_BASE}" in http://*|https://*) ;; *) echo "DOWNLOAD_BASE must use http or https" >&2; exit 1;; esac

TMP_DIR="$(mktemp -d)"
trap 'rm -rf "${TMP_DIR}"' EXIT
artifact="forest-probe-linux-${ARCH}"
curl -fsSL "${DOWNLOAD_BASE}/${ARCH}" -o "${TMP_DIR}/${artifact}"
curl -fsSL "${DOWNLOAD_BASE}/${ARCH}.sha256" -o "${TMP_DIR}/${artifact}.sha256"
expected_sha="$(awk 'NR==1 {print $1}' "${TMP_DIR}/${artifact}.sha256")"
[[ "${expected_sha}" =~ ^[0-9a-fA-F]{64}$ ]] || { echo "invalid probe checksum" >&2; exit 1; }
actual_sha="$(sha256sum "${TMP_DIR}/${artifact}" | awk '{print $1}')"
[[ "${actual_sha}" == "${expected_sha}" ]] || { echo "probe checksum mismatch" >&2; exit 1; }
install -d -m 0755 "${INSTALL_DIR}" "${CONFIG_DIR}" "${SYSTEMD_DIR}"
install -m 0755 "${TMP_DIR}/${artifact}" "${INSTALL_DIR}/forest-probe"
umask 077
printf '{"api_url":"%s","token":"%s","interval":%s}\n' "${API_URL}" "${TOKEN}" "${INTERVAL}" > "${CONFIG_DIR}/config.json"
chmod 0600 "${CONFIG_DIR}/config.json"
cat > "${SYSTEMD_DIR}/forest-probe.service" <<'UNIT'
[Unit]
Description=Forest DNS Probe
After=network-online.target
Wants=network-online.target
[Service]
Type=simple
ExecStart=/usr/local/bin/forest-probe -config /etc/forest-probe/config.json
Restart=always
RestartSec=5
[Install]
WantedBy=multi-user.target
UNIT
systemctl daemon-reload
systemctl enable --now forest-probe.service
