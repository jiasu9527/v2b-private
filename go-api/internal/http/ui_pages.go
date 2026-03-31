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
	Title            string
	VersionSuffix    string
	SettingsJSON     template.JS
	IncludeCustomJS  bool
	IncludeCustomCSS bool
}

type invitePageData struct {
	Title         string
	VersionSuffix string
	ConfigJSON    template.JS
}

var (
	adminShellTemplate = template.Must(template.New("admin-shell").Parse(`<!DOCTYPE html>
<html>
<head>
    <link rel="stylesheet" href="/assets/admin/components.chunk.css{{.VersionSuffix}}">
    <link rel="stylesheet" href="/assets/admin/umi.css{{.VersionSuffix}}">
    {{if .IncludeCustomCSS}}<link rel="stylesheet" href="/assets/admin/custom.css{{.VersionSuffix}}">{{end}}
    <meta charset="utf-8">
    <meta name="viewport" content="width=device-width,initial-scale=1,maximum-scale=1,minimum-scale=1,user-scalable=no">
    <title>{{.Title}}</title>
    <script>window.routerBase = "/";</script>
    <script>window.settings = {{.SettingsJSON}};</script>
</head>
<body>
<div id="root"></div>
<script src="/assets/admin/vendors.async.js{{.VersionSuffix}}"></script>
<script src="/assets/admin/components.async.js{{.VersionSuffix}}"></script>
<script src="/assets/admin/umi.js{{.VersionSuffix}}"></script>
{{if .IncludeCustomJS}}<script src="/assets/admin/custom.js{{.VersionSuffix}}"></script>{{end}}
</body>
</html>
`))
	inviteAdminTemplate = template.Must(template.New("invite-admin").Parse(`<!DOCTYPE html>
<html>
<head>
    <link rel="stylesheet" href="/assets/admin/components.chunk.css{{.VersionSuffix}}">
    <link rel="stylesheet" href="/assets/admin/umi.css{{.VersionSuffix}}">
    <link rel="stylesheet" href="/assets/admin/custom.css{{.VersionSuffix}}">
    <link rel="stylesheet" href="/assets/invite-campaign-common.css{{.VersionSuffix}}">
    <meta charset="utf-8">
    <meta name="viewport" content="width=device-width,initial-scale=1,maximum-scale=1,minimum-scale=1,user-scalable=no">
    <title>{{.Title}} - 任务管理</title>
    <script>window.InviteCampaignAdminPage = {{.ConfigJSON}};</script>
</head>
<body class="campaign-page">
<div id="invite-campaign-admin-app" class="campaign-shell">
    <div class="campaign-card campaign-loading">正在加载任务列表...</div>
</div>
<script src="/assets/admin-invite-campaign-page.js{{.VersionSuffix}}"></script>
</body>
</html>
`))
)

func maybeServeUIPage(cfg config.Config, w http.ResponseWriter, r *http.Request) bool {
	switch resolveUIPageKind(cfg, r.URL.Path) {
	case uiPageAdmin:
		renderAdminShell(w, cfg)
		return true
	case uiPageAdminInviteCampaign:
		renderInviteAdminPage(w, cfg)
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
		filepath.Join(publicDir, "assets", "admin", "vendors.async.js"),
		filepath.Join(publicDir, "assets", "admin", "components.async.js"),
		filepath.Join(publicDir, "assets", "admin", "umi.js"),
		filepath.Join(publicDir, "assets", "admin", "custom.js"),
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
		IncludeCustomJS:  fileExists(filepath.Join(publicDir, "assets", "admin", "custom.js")),
		IncludeCustomCSS: fileExists(filepath.Join(publicDir, "assets", "admin", "custom.css")),
	})
}

func renderInviteAdminPage(w http.ResponseWriter, cfg config.Config) {
	settings := loadUISiteSettings(cfg)
	publicDir := resolvePublicDir(cfg.PublicDir)
	versionSuffix := assetVersionSuffix(
		filepath.Join(publicDir, "assets", "admin-invite-campaign-page.js"),
		filepath.Join(publicDir, "assets", "invite-campaign-common.css"),
	)

	renderHTML(w, inviteAdminTemplate, invitePageData{
		Title:         settings.Title,
		VersionSuffix: versionSuffix,
		ConfigJSON: marshalPageSettings(map[string]any{
			"apiBase":    "/api/v1",
			"securePath": settings.AdminPath,
			"loginPath":  "/" + settings.AdminPath,
			"backPath":   "/" + settings.AdminPath,
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
