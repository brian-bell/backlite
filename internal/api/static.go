package api

import (
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"
)

type webAppHandler struct {
	dir string
}

func (h webAppHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		http.NotFound(w, r)
		return
	}
	if reservedWebPath(r.URL.Path) {
		http.NotFound(w, r)
		return
	}

	cleanPath := strings.TrimPrefix(path.Clean("/"+r.URL.Path), "/")
	if cleanPath == "." || cleanPath == "" {
		cleanPath = "index.html"
	}

	filePath := filepath.Join(h.dir, filepath.FromSlash(cleanPath))
	if info, err := os.Stat(filePath); err == nil && !info.IsDir() {
		http.ServeFile(w, r, filePath)
		return
	}
	if missingWebFile(cleanPath) {
		http.NotFound(w, r)
		return
	}

	indexPath := filepath.Join(h.dir, "index.html")
	if info, err := os.Stat(indexPath); err != nil || info.IsDir() {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	http.ServeFile(w, r, indexPath)
}

func reservedWebPath(urlPath string) bool {
	return urlPath == "/api" ||
		strings.HasPrefix(urlPath, "/api/") ||
		urlPath == "/debug" ||
		strings.HasPrefix(urlPath, "/debug/") ||
		urlPath == "/health"
}

func missingWebFile(cleanPath string) bool {
	return strings.HasPrefix(cleanPath, "assets/") || path.Ext(cleanPath) != ""
}
