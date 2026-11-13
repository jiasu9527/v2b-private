package httpapi

import (
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
)

const adminAssetURLPrefix = "/assets/admin-new/"

var adminReleaseName = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,80}$`)

// The bundled public assets are a safe fallback for a fresh checkout. appctl
// writes freshly compiled admin assets outside Git, then atomically switches
// this runtime directory. Serving them as an overlay avoids a dirty checkout
// that would block the next --ff-only pull.
func adminRuntimeAssetDir(publicDir string) string {
	return filepath.Join(filepath.Dir(resolvePublicDir(publicDir)), ".local", "admin-new", "current")
}

func completeAdminRelease(dir string) bool {
	for _, name := range []string{"admin.js", "index.css", "index.html", ".published"} {
		info, err := os.Lstat(filepath.Join(dir, name))
		if err != nil || !info.Mode().IsRegular() || (name != ".published" && info.Size() == 0) {
			return false
		}
	}
	return true
}

func adminRuntimeAssetsAvailable(publicDir string) bool {
	dir := adminRuntimeAssetDir(publicDir)
	if release, err := os.Readlink(dir); err == nil {
		name := strings.TrimPrefix(release, "releases/")
		if name == release || !adminReleaseName.MatchString(name) || release != "releases/"+name {
			return false
		}
		info, err := os.Lstat(filepath.Join(filepath.Dir(dir), release))
		return err == nil && info.IsDir() && completeAdminRelease(dir)
	}
	info, err := os.Lstat(dir)
	return err == nil && info.IsDir() && completeAdminRelease(dir)
}

func adminAssetVersionSuffix(publicDir string) string {
	if adminRuntimeAssetsAvailable(publicDir) {
		dir := adminRuntimeAssetDir(publicDir)
		if release, err := os.Readlink(dir); err == nil {
			return "?v=" + url.QueryEscape(filepath.Base(release))
		}
		return assetVersionSuffix(filepath.Join(dir, "admin.js"), filepath.Join(dir, "index.css"))
	}
	return assetVersionSuffix(
		filepath.Join(resolvePublicDir(publicDir), "assets", "admin-new", "admin.js"),
		filepath.Join(resolvePublicDir(publicDir), "assets", "admin-new", "index.css"),
	)
}

func serveRuntimeAdminAsset(w http.ResponseWriter, r *http.Request, publicDir string) bool {
	if !strings.HasPrefix(r.URL.Path, adminAssetURLPrefix) || !adminRuntimeAssetsAvailable(publicDir) {
		return false
	}
	dir := adminRuntimeAssetDir(publicDir)
	if version := r.URL.Query().Get("v"); version != "" {
		if !adminReleaseName.MatchString(version) {
			http.NotFound(w, r)
			return true
		}
		// A page opened just before an atomic release switch must still load
		// matching JS/CSS. Git-bundled assets use numeric mtime versions, so
		// leave those to the ordinary public file server.
		if len(version) > 0 && strings.IndexFunc(version, func(r rune) bool { return r < '0' || r > '9' }) == -1 {
			return false
		}
		dir = filepath.Join(filepath.Dir(dir), "releases", version)
		if info, err := os.Lstat(dir); err != nil || !info.IsDir() || !completeAdminRelease(dir) {
			http.NotFound(w, r)
			return true
		}
	}
	rel := strings.TrimPrefix(r.URL.Path, adminAssetURLPrefix)
	if rel == "" || strings.HasPrefix(rel, ".") || strings.Contains(rel, "/.") || strings.Contains(rel, "\\") || path.Clean("/"+rel) != "/"+rel {
		http.NotFound(w, r)
		return true
	}
	asset := filepath.Join(dir, filepath.FromSlash(rel))
	// A published release can contain public files, but not a symlink escaping
	// into configuration, credentials, or another checkout directory.
	root, rootErr := filepath.EvalSymlinks(dir)
	resolved, assetErr := filepath.EvalSymlinks(asset)
	if rootErr != nil || assetErr != nil {
		http.NotFound(w, r)
		return true
	}
	within, err := filepath.Rel(root, resolved)
	if err != nil || within == ".." || strings.HasPrefix(within, ".."+string(filepath.Separator)) || filepath.IsAbs(within) {
		http.NotFound(w, r)
		return true
	}
	info, err := os.Stat(asset)
	if err != nil || !info.Mode().IsRegular() {
		http.NotFound(w, r)
		return true
	}
	w.Header().Set("X-Content-Type-Options", "nosniff")
	http.ServeFile(w, r, asset)
	return true
}
