package httpapi

import (
	"html/template"
	"net/http"
	"path/filepath"

	"forest/go-api/internal/config"
)

var telegramMiniTemplate = template.Must(template.New("telegram-mini").Parse(`<!DOCTYPE html>
<html lang="zh-CN"><head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1,viewport-fit=cover">
<meta name="referrer" content="no-referrer">
<title>AI 运维</title>
<link rel="stylesheet" href="/assets/admin-new/index.css{{.VersionSuffix}}">
<script>window.settings = {telegram_mini_app: true};</script>
<script src="https://telegram.org/js/telegram-web-app.js"></script>
</head><body><div id="root"></div>
<script type="module" src="/assets/admin-new/admin.js{{.VersionSuffix}}"></script>
</body></html>`))

func renderTelegramMiniShell(w http.ResponseWriter, r *http.Request, cfg config.Config) {
	disableResponseCache(w)
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	// Telegram Web embeds Mini Apps; the ordinary admin shell is not exposed
	// through this route. No credentials or hidden administrator path are emitted.
	w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self' 'unsafe-inline' https://telegram.org; style-src 'self' 'unsafe-inline'; img-src 'self' data: https:; connect-src 'self'; font-src 'self' data:; base-uri 'none'; object-src 'none'; form-action 'self'; frame-ancestors https://web.telegram.org https://*.telegram.org https://*.t.me")
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"message": "Method not allowed"})
		return
	}
	if r.Method == http.MethodHead {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		return
	}
	public := resolvePublicDir(cfg.PublicDir)
	renderHTML(w, telegramMiniTemplate, adminShellData{VersionSuffix: assetVersionSuffix(filepath.Join(public, "assets", "admin-new", "admin.js"), filepath.Join(public, "assets", "admin-new", "index.css"))})
}
