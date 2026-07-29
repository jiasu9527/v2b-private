package httpapi

import (
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"forest/go-api/internal/config"
)

var probeInstallToken = regexp.MustCompile(`^[A-Za-z0-9._~-]{1,512}$`)

func handleProbeInstall(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	apiURL, token, interval, ok := probeInstallParameters(r)
	if !ok {
		http.Error(w, "invalid probe installation parameters", http.StatusBadRequest)
		return
	}
	downloadBase := strings.TrimRight(apiURL, "/") + "/api/v1/probe/download/linux"
	w.Header().Set("Content-Type", "text/x-shellscript; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	fmt.Fprintf(w, probeInstallScript, shellQuote(apiURL), shellQuote(token), interval, shellQuote(downloadBase))
}

func probeInstallParameters(r *http.Request) (string, string, int, bool) {
	apiURL, err := url.ParseRequestURI(r.URL.Query().Get("api_url"))
	if err != nil || apiURL.Host == "" || (apiURL.Scheme != "http" && apiURL.Scheme != "https") || apiURL.User != nil || strings.ContainsAny(apiURL.String(), "'\"\\\r\n") {
		return "", "", 0, false
	}
	token := r.URL.Query().Get("token")
	if !probeInstallToken.MatchString(token) {
		return "", "", 0, false
	}
	interval := 30
	if raw := r.URL.Query().Get("interval"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 1 || parsed > 3600 {
			return "", "", 0, false
		}
		interval = parsed
	}
	return apiURL.String(), token, interval, true
}

func shellQuote(value string) string { return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'" }

func handleProbeDownload(w http.ResponseWriter, r *http.Request, cfg config.Config) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	name, ok := map[string]string{
		"/api/v1/probe/download/linux/amd64":        "forest-probe-linux-amd64",
		"/api/v1/probe/download/linux/arm64":        "forest-probe-linux-arm64",
		"/api/v1/probe/download/linux/amd64.sha256": "forest-probe-linux-amd64.sha256",
		"/api/v1/probe/download/linux/arm64.sha256": "forest-probe-linux-arm64.sha256",
	}[r.URL.Path]
	if !ok {
		http.NotFound(w, r)
		return
	}
	path := filepath.Join(cfg.ProbeStorageDir, name)
	info, err := os.Stat(path)
	if err != nil || info.IsDir() {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", name))
	w.Header().Set("Cache-Control", "public, max-age=300")
	http.ServeFile(w, r, path)
}

const probeInstallScript = `#!/usr/bin/env bash
set -euo pipefail
API_URL=%s
TOKEN=%s
INTERVAL=%d
DOWNLOAD_BASE=%s
case "$(uname -s)" in Linux) ;; *) echo "forest-probe only supports Linux" >&2; exit 1;; esac
case "$(uname -m)" in x86_64|amd64) ARCH=amd64;; aarch64|arm64) ARCH=arm64;; *) echo "unsupported architecture: $(uname -m)" >&2; exit 1;; esac
INSTALL_DIR=/usr/local/bin
CONFIG_DIR=/etc/forest-probe
TMP_DIR=$(mktemp -d)
trap 'rm -rf "$TMP_DIR"' EXIT
ARTIFACT="forest-probe-linux-$ARCH"
curl -fsSL "$DOWNLOAD_BASE/$ARCH" -o "$TMP_DIR/$ARTIFACT"
curl -fsSL "$DOWNLOAD_BASE/$ARCH.sha256" -o "$TMP_DIR/$ARTIFACT.sha256"
EXPECTED_SHA=$(awk 'NR==1 {print $1}' "$TMP_DIR/$ARTIFACT.sha256")
[[ "$EXPECTED_SHA" =~ ^[0-9a-fA-F]{64}$ ]] || { echo "invalid probe checksum" >&2; exit 1; }
ACTUAL_SHA=$(sha256sum "$TMP_DIR/$ARTIFACT" | awk '{print $1}')
[[ "$ACTUAL_SHA" == "$EXPECTED_SHA" ]] || { echo "probe checksum mismatch" >&2; exit 1; }
install -d -m 0755 "$INSTALL_DIR" "$CONFIG_DIR"
install -m 0755 "$TMP_DIR/$ARTIFACT" "$INSTALL_DIR/forest-probe"
umask 077
printf '{"api_url":"%%s","token":"%%s","interval":%%s}\n' "$API_URL" "$TOKEN" "$INTERVAL" > "$CONFIG_DIR/config.json"
chmod 0600 "$CONFIG_DIR/config.json"
cat > /etc/systemd/system/forest-probe.service <<'UNIT'
[Unit]
Description=Forest Probe
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
systemctl enable forest-probe.service
systemctl restart forest-probe.service
`
