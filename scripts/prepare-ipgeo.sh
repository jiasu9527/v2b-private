#!/usr/bin/env bash
# Download only public database artifacts; user IP addresses never leave the host.
set -euo pipefail

IPGEO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
IPGEO_DIR="${FOREST_IPGEO_DIR:-${IPGEO_ROOT}/storage/ipgeo}"
IPGEO_REVISION="f8b0792b0cd729f5b2593f690aeed225e49c1967"
IPGEO_SOURCE="https://raw.githubusercontent.com/lionsoul2014/ip2region/${IPGEO_REVISION}/data"

if command -v sha256sum >/dev/null 2>&1; then
  ipgeo_sha256() { sha256sum "$1" | awk '{print $1}'; }
elif command -v shasum >/dev/null 2>&1; then
  ipgeo_sha256() { shasum -a 256 "$1" | awk '{print $1}'; }
else
  echo "在线地区库未准备：找不到 sha256sum/shasum，安装后重试 scripts/prepare-ipgeo.sh。" >&2
  exit 1
fi

ipgeo_download() {
  if command -v curl >/dev/null 2>&1; then
    curl --fail --silent --show-error --location --proto '=https' --proto-redir '=https' \
      --connect-timeout 15 --max-time 180 "$1" --output "$2"
  elif command -v wget >/dev/null 2>&1; then
    wget --https-only --quiet --timeout=30 --tries=2 --output-document="$2" "$1"
  else
    echo "在线地区库未准备：请安装 curl 或 wget。" >&2
    return 1
  fi
}

mkdir -p "${IPGEO_DIR}"
IPGEO_TMP="$(mktemp -d "${IPGEO_DIR}/.download.XXXXXX")"
trap 'rm -rf "${IPGEO_TMP}"' EXIT
IPGEO_FAILED=0

while read -r ipgeo_file ipgeo_sha; do
  ipgeo_target="${IPGEO_DIR}/${ipgeo_file}"
  if [[ -f "${ipgeo_target}" ]] && [[ "$(ipgeo_sha256 "${ipgeo_target}")" == "${ipgeo_sha}" ]]; then
    continue
  fi
  echo "准备在线人数地区库：${ipgeo_file}"
  if ! ipgeo_download "${IPGEO_SOURCE}/${ipgeo_file}" "${IPGEO_TMP}/${ipgeo_file}"; then
    echo "${ipgeo_file} 下载失败，保留已有地区库；可稍后运行 scripts/prepare-ipgeo.sh 重试。" >&2
    IPGEO_FAILED=1
    continue
  fi
  if [[ "$(ipgeo_sha256 "${IPGEO_TMP}/${ipgeo_file}")" != "${ipgeo_sha}" ]]; then
    echo "${ipgeo_file} SHA256 校验失败，未替换已有地区库。" >&2
    IPGEO_FAILED=1
    continue
  fi
  chmod 0644 "${IPGEO_TMP}/${ipgeo_file}"
  mv -f "${IPGEO_TMP}/${ipgeo_file}" "${ipgeo_target}"
done <<'IPGEO_FILES'
ip2region_v4.xdb 27ecba8c9a6d5e3b280aedbe7ee15c65372b179fbd0dad3845d8c608f2fcace1
ip2region_v6.xdb 939f6b46bd2b8bec3cf7c5ceb8ba782266ae9b1f35b5ba7916700dec0b7506ed
IPGEO_FILES

if [[ "${IPGEO_FAILED}" -ne 0 ]]; then
  exit 1
fi
echo "离线 IPv4/IPv6 地区库已就绪。"
