package httpapi

import (
	"encoding/json"
	"fmt"
	"html/template"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"forest/go-api/internal/config"
)

type uiPageKind int

const (
	uiPageNone uiPageKind = iota
	uiPageAdmin
	uiPageAdminInviteCampaign
)

type uiSiteSettings struct {
	Title              string
	Logo               string
	AdminPath          string
	AdminThemeSidebar  string
	AdminThemeHeader   string
	AdminThemeColor    string
	AdminBackgroundURL string
}

type adminShellData struct {
	Title         string
	VersionSuffix string
	SettingsJSON  template.JS
}

var (
	adminShellTemplate = template.Must(template.New("admin-shell").Parse(`<!DOCTYPE html>
<html>
<head>
    <meta charset="utf-8">
    <meta name="viewport" content="width=device-width,initial-scale=1,maximum-scale=1,minimum-scale=1,user-scalable=no">
    <title>{{.Title}}</title>
    <link rel="stylesheet" href="/assets/admin-new/index.css{{.VersionSuffix}}">
    <script>window.routerBase = "/";</script>
    <script>window.settings = {{.SettingsJSON}};</script>
</head>
<body>
<div id="root"></div>
<script type="module" src="/assets/admin-new/admin.js{{.VersionSuffix}}"></script>
</body>
</html>
`))
)

func maybeServeUIPage(cfg config.Config, w http.ResponseWriter, r *http.Request) bool {
	switch resolveUIPageKind(cfg, r.URL.Path) {
	case uiPageAdmin, uiPageAdminInviteCampaign:
		renderAdminShell(w, cfg)
		return true
	default:
		return false
	}
}

func resolveUIPageKind(cfg config.Config, requestPath string) uiPageKind {
	path := normalizeUIPath(requestPath)
	adminPath := normalizeAdminPath(cfg.AdminPath)
	adminRoot := "/" + adminPath

	switch path {
	case adminRoot:
		return uiPageAdmin
	case adminRoot + "/invite-campaign":
		return uiPageAdminInviteCampaign
	}

	if strings.HasPrefix(path, adminRoot+"/") {
		return uiPageAdmin
	}

	return uiPageNone
}

func renderAdminShell(w http.ResponseWriter, cfg config.Config) {
	settings := loadUISiteSettings(cfg)
	publicDir := resolvePublicDir(cfg.PublicDir)
	versionSuffix := assetVersionSuffix(
		filepath.Join(publicDir, "assets", "admin-new", "admin.js"),
		filepath.Join(publicDir, "assets", "admin-new", "index.css"),
	)

	renderHTML(w, adminShellTemplate, adminShellData{
		Title:         settings.Title,
		VersionSuffix: versionSuffix,
		SettingsJSON: marshalPageSettings(map[string]any{
			"title": settings.Title,
			"theme": map[string]any{
				"sidebar": settings.AdminThemeSidebar,
				"header":  settings.AdminThemeHeader,
				"color":   settings.AdminThemeColor,
			},
			"version":        strings.TrimPrefix(versionSuffix, "?v="),
			"background_url": settings.AdminBackgroundURL,
			"logo":           settings.Logo,
			"secure_path":    settings.AdminPath,
		}),
	})
}

func loadUISiteSettings(cfg config.Config) uiSiteSettings {
	publicDir := resolvePublicDir(cfg.PublicDir)
	projectRoot := filepath.Clean(filepath.Join(publicDir, ".."))
	adminValues := loadUIJSONMap(filepath.Join(projectRoot, "config", "admin.json"))

	return uiSiteSettings{
		Title:              firstNonEmpty(uiString(adminValues["app_name"]), cfg.AppName, "Forest"),
		Logo:               firstNonEmpty(uiString(adminValues["logo"]), cfg.Logo),
		AdminPath:          normalizeAdminPath(cfg.AdminPath),
		AdminThemeSidebar:  firstNonEmpty(uiString(adminValues["frontend_theme_sidebar"]), "light"),
		AdminThemeHeader:   firstNonEmpty(uiString(adminValues["frontend_theme_header"]), "dark"),
		AdminThemeColor:    firstNonEmpty(uiString(adminValues["frontend_theme_color"]), "default"),
		AdminBackgroundURL: uiString(adminValues["frontend_background_url"]),
	}
}

func renderHTML(w http.ResponseWriter, tmpl *template.Template, data any) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := tmpl.Execute(w, data); err != nil {
		writePlainText(w, http.StatusInternalServerError, err.Error())
	}
}

func marshalPageSettings(value any) template.JS {
	raw, err := json.Marshal(value)
	if err != nil {
		return template.JS("{}")
	}
	return template.JS(raw)
}

func assetVersionSuffix(paths ...string) string {
	var latest int64
	for _, path := range paths {
		info, err := os.Stat(path)
		if err != nil {
			continue
		}
		if modTime := info.ModTime().Unix(); modTime > latest {
			latest = modTime
		}
	}
	if latest == 0 {
		return ""
	}
	return fmt.Sprintf("?v=%d", latest)
}

func loadUIJSONMap(path string) map[string]any {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	values := map[string]any{}
	if err := json.Unmarshal(raw, &values); err != nil {
		return nil
	}
	return values
}

func uiString(value any) string {
	switch typed := value.(type) {
	case string:
		return strings.TrimSpace(typed)
	default:
		return ""
	}
}

func normalizeUIPath(path string) string {
	if path == "" {
		return "/"
	}
	if path != "/" {
		path = strings.TrimRight(path, "/")
		if path == "" {
			return "/"
		}
	}
	return path
}

func normalizeAdminPath(path string) string {
	path = strings.Trim(strings.TrimSpace(path), "/")
	if path == "" {
		return "localadmin"
	}
	return path
}

func shouldReturnForbiddenUIPage(r *http.Request) bool {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		return false
	}

	path := normalizeUIPath(r.URL.Path)
	if path == "/" {
		return true
	}

	cleanPath := strings.TrimPrefix(path, "/")
	if cleanPath == "" || strings.HasPrefix(cleanPath, "api/") {
		return false
	}

	return !strings.Contains(filepath.Base(cleanPath), ".")
}

func closeUIConnection(w http.ResponseWriter) bool {
	hijacker, ok := w.(http.Hijacker)
	if !ok {
		return false
	}
	conn, _, err := hijacker.Hijack()
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
}

func resolvePublicDir(publicDir string) string {
	if filepath.IsAbs(publicDir) {
		return filepath.Clean(publicDir)
	}
	cwd, err := os.Getwd()
	if err != nil {
		return filepath.Clean(publicDir)
	}
	return filepath.Clean(filepath.Join(cwd, publicDir))
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "" {
			return value
		}
	}
	return ""
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
