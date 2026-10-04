package api

import (
	"io/fs"
	"net/http"
	"path"
	"strings"
)

type spaHandler struct {
	files fs.FS
	ready bool
}

func newSPA(files fs.FS) *spaHandler {
	_, err := fs.Stat(files, "index.html")
	return &spaHandler{files: files, ready: err == nil}
}

func (h *spaHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !h.ready {
		http.Error(w, "frontend assets are not built: run \"npm run build\" in frontend/, or use the Vite dev server", http.StatusServiceUnavailable)
		return
	}

	name := strings.TrimPrefix(path.Clean(r.URL.Path), "/")
	if info, err := fs.Stat(h.files, name); name == "" || name == "." || err != nil || info.IsDir() {
		if strings.HasPrefix(name, "assets/") || path.Ext(name) != "" {
			http.NotFound(w, r)
			return
		}
		name = "index.html"
	}
	http.ServeFileFS(w, r, h.files, name)
}
