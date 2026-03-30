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
	uiPageFrontend
	uiPageAdmin
	uiPageUserInviteCampaign
	uiPageAdminInviteCampaign
)

type uiSiteSettings struct {
	Title              string
	Description        string
	AppURL             string
	Logo               string
	AdminPath          string
	AdminThemeSidebar  string
	AdminThemeHeader   string
	AdminThemeColor    string
	AdminBackgroundURL string
	FrontendTheme      string
	FrontendThemeColor string
	FrontendThemeSide  string
	FrontendThemeHead  string
	FrontendBackground string
	FrontendCustomHTML string
}

type frontendShellData struct {
	Title            string
	Theme            string
	VersionSuffix    string
	ThemeColorMeta   string
	SettingsJSON     template.JS
	CustomHTML       template.HTML
	IncludeCustomJS  bool
	IncludeCustomCSS bool
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
	Theme         string
	VersionSuffix string
	ConfigJSON    template.JS
}

var (
	frontendShellTemplate = template.Must(template.New("frontend-shell").Parse(`<!DOCTYPE html>
<html>
<head>
    <link rel="stylesheet" href="/theme/{{.Theme}}/assets/components.chunk.css{{.VersionSuffix}}">
    <link rel="stylesheet" href="/theme/{{.Theme}}/assets/umi.css{{.VersionSuffix}}">
    {{if .IncludeCustomCSS}}<link rel="stylesheet" href="/theme/{{.Theme}}/assets/custom.css{{.VersionSuffix}}">{{end}}
    <meta charset="utf-8">
    <meta name="viewport" content="width=device-width,initial-scale=1,maximum-scale=1,minimum-scale=1,user-scalable=no">
    <meta name="theme-color" content="{{.ThemeColorMeta}}">
    <title>{{.Title}}</title>
    <script>window.routerBase = "/";</script>
    <script>window.settings = {{.SettingsJSON}};</script>
    <script src="/theme/{{.Theme}}/assets/i18n/zh-CN.js{{.VersionSuffix}}"></script>
    <script src="/theme/{{.Theme}}/assets/i18n/zh-TW.js{{.VersionSuffix}}"></script>
    <script src="/theme/{{.Theme}}/assets/i18n/en-US.js{{.VersionSuffix}}"></script>
    <script src="/theme/{{.Theme}}/assets/i18n/ja-JP.js{{.VersionSuffix}}"></script>
    <script src="/theme/{{.Theme}}/assets/i18n/vi-VN.js{{.VersionSuffix}}"></script>
    <script src="/theme/{{.Theme}}/assets/i18n/ko-KR.js{{.VersionSuffix}}"></script>
    <script src="/theme/{{.Theme}}/assets/i18n/fa-IR.js{{.VersionSuffix}}"></script>
</head>
<body>
<div id="root"></div>
{{.CustomHTML}}
<script src="/theme/{{.Theme}}/assets/vendors.async.js{{.VersionSuffix}}"></script>
<script src="/theme/{{.Theme}}/assets/components.async.js{{.VersionSuffix}}"></script>
<script src="/theme/{{.Theme}}/assets/umi.js{{.VersionSuffix}}"></script>
{{if .IncludeCustomJS}}<script src="/theme/{{.Theme}}/assets/custom.js{{.VersionSuffix}}"></script>{{end}}
</body>
</html>
`))
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
	inviteUserTemplate = template.Must(template.New("invite-user").Parse(`<!DOCTYPE html>
<html>
<head>
    <link rel="stylesheet" href="/theme/{{.Theme}}/assets/components.chunk.css{{.VersionSuffix}}">
    <link rel="stylesheet" href="/theme/{{.Theme}}/assets/umi.css{{.VersionSuffix}}">
    <link rel="stylesheet" href="/assets/invite-campaign-common.css{{.VersionSuffix}}">
    <meta charset="utf-8">
    <meta name="viewport" content="width=device-width,initial-scale=1,maximum-scale=1,minimum-scale=1,user-scalable=no">
    <title>{{.Title}} - 邀请活动任务</title>
    <script>window.InviteCampaignUserPage = {{.ConfigJSON}};</script>
</head>
<body class="campaign-page">
<div class="campaign-shell">
    <div class="campaign-hero">
        <div>
            <h1>邀请活动任务</h1>
            <p>完成邀请任务后，购买绑定套餐时自动使用活动抵扣。</p>
        </div>
        <div class="campaign-actions">
            <a class="btn btn-alt-secondary" href="/#/invite">返回邀请页</a>
            <a class="btn btn-alt-secondary" href="/#/dashboard">返回仪表盘</a>
        </div>
    </div>
    <div id="invite-campaign-user-app" class="campaign-card campaign-loading">正在加载活动任务...</div>
</div>
<script src="/assets/user-invite-campaign-page.js{{.VersionSuffix}}"></script>
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
	case uiPageFrontend:
		renderFrontendShell(w, cfg)
		return true
	case uiPageAdmin:
		renderAdminShell(w, cfg)
		return true
	case uiPageUserInviteCampaign:
		renderInviteUserPage(w, cfg)
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
	case "/":
		return uiPageFrontend
	case "/invite-campaign":
		return uiPageUserInviteCampaign
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

func renderFrontendShell(w http.ResponseWriter, cfg config.Config) {
	settings := loadUISiteSettings(cfg)
	publicDir := resolvePublicDir(cfg.PublicDir)
	theme := settings.FrontendTheme
	versionSuffix := assetVersionSuffix(
		filepath.Join(publicDir, "theme", theme, "assets", "vendors.async.js"),
		filepath.Join(publicDir, "theme", theme, "assets", "components.async.js"),
		filepath.Join(publicDir, "theme", theme, "assets", "umi.js"),
		filepath.Join(publicDir, "theme", theme, "assets", "custom.js"),
	)

	renderHTML(w, frontendShellTemplate, frontendShellData{
		Title:          settings.Title,
		Theme:          theme,
		VersionSuffix:  versionSuffix,
		ThemeColorMeta: resolveThemeColorMeta(settings.FrontendThemeColor),
		SettingsJSON: marshalPageSettings(map[string]any{
			"title":          settings.Title,
			"assets_path":    "/theme/" + theme + "/assets",
			"theme":          map[string]any{"sidebar": settings.FrontendThemeSide, "header": settings.FrontendThemeHead, "color": settings.FrontendThemeColor},
			"version":        strings.TrimPrefix(versionSuffix, "?v="),
			"background_url": settings.FrontendBackground,
			"description":    settings.Description,
			"i18n":           []string{"zh-CN", "en-US", "ja-JP", "vi-VN", "ko-KR", "zh-TW", "fa-IR"},
			"logo":           settings.Logo,
		}),
		CustomHTML:       template.HTML(settings.FrontendCustomHTML),
		IncludeCustomJS:  fileExists(filepath.Join(publicDir, "theme", theme, "assets", "custom.js")),
		IncludeCustomCSS: fileExists(filepath.Join(publicDir, "theme", theme, "assets", "custom.css")),
	})
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

func renderInviteUserPage(w http.ResponseWriter, cfg config.Config) {
	settings := loadUISiteSettings(cfg)
	publicDir := resolvePublicDir(cfg.PublicDir)
	versionSuffix := assetVersionSuffix(
		filepath.Join(publicDir, "assets", "user-invite-campaign-page.js"),
		filepath.Join(publicDir, "assets", "invite-campaign-common.css"),
	)

	renderHTML(w, inviteUserTemplate, invitePageData{
		Title:         settings.Title,
		Theme:         settings.FrontendTheme,
		VersionSuffix: versionSuffix,
		ConfigJSON: marshalPageSettings(map[string]any{
			"apiBase":         "/api/v1",
			"loginPath":       "/#/login",
			"dashboardPath":   "/#/dashboard",
			"invitePath":      "/#/invite",
			"planPath":        "/#/plan",
			"orderPathPrefix": "/#/order/",
		}),
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

	themeName := firstNonEmpty(uiString(adminValues["frontend_theme"]), "default")
	themeName = resolveThemeName(publicDir, themeName)
	themeValues := loadUIJSONMap(filepath.Join(projectRoot, "config", "theme", themeName+".json"))

	return uiSiteSettings{
		Title:              firstNonEmpty(uiString(adminValues["app_name"]), cfg.AppName, "V2Board"),
		Description:        firstNonEmpty(uiString(adminValues["app_description"]), cfg.AppDescription),
		AppURL:             firstNonEmpty(uiString(adminValues["app_url"]), cfg.AppURL),
		Logo:               firstNonEmpty(uiString(adminValues["logo"]), cfg.Logo),
		AdminPath:          normalizeAdminPath(cfg.AdminPath),
		AdminThemeSidebar:  firstNonEmpty(uiString(adminValues["frontend_theme_sidebar"]), "light"),
		AdminThemeHeader:   firstNonEmpty(uiString(adminValues["frontend_theme_header"]), "dark"),
		AdminThemeColor:    firstNonEmpty(uiString(adminValues["frontend_theme_color"]), "default"),
		AdminBackgroundURL: uiString(adminValues["frontend_background_url"]),
		FrontendTheme:      themeName,
		FrontendThemeColor: firstNonEmpty(uiString(themeValues["theme_color"]), "default"),
		FrontendThemeSide:  firstNonEmpty(uiString(themeValues["theme_sidebar"]), "light"),
		FrontendThemeHead:  firstNonEmpty(uiString(themeValues["theme_header"]), "dark"),
		FrontendBackground: uiString(themeValues["background_url"]),
		FrontendCustomHTML: uiString(themeValues["custom_html"]),
	}
}

func resolveThemeName(publicDir, theme string) string {
	theme = strings.TrimSpace(theme)
	if theme == "" {
		theme = "default"
	}
	if themeExists(publicDir, theme) {
		return theme
	}
	if themeExists(publicDir, "default") {
		return "default"
	}
	return theme
}

func themeExists(publicDir, theme string) bool {
	if strings.TrimSpace(theme) == "" {
		return false
	}
	return fileExists(filepath.Join(publicDir, "theme", theme, "assets", "umi.js"))
}

func resolveThemeColorMeta(color string) string {
	switch strings.TrimSpace(color) {
	case "darkblue":
		return "#3b5998"
	case "black":
		return "#343a40"
	case "green":
		return "#319795"
	default:
		return "#0665d0"
	}
}

func renderHTML(w http.ResponseWriter, tmpl *template.Template, data any) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := tmpl.Execute(w, data); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
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
