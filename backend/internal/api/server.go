package api

import (
	"encoding/json"
	"mime"
	"net/http"
	"net/url"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/IvanPopov200/Constellarr/backend/internal/downloads"
	"github.com/IvanPopov200/Constellarr/backend/internal/web"
)

type Services struct {
	Downloads *downloads.Manager
}

func New(pool *pgxpool.Pool, services ...Services) http.Handler {
	mux := http.NewServeMux()
	if len(services) != 0 && services[0].Downloads != nil {
		registerDownloads(mux, services[0].Downloads)
	}
	mux.HandleFunc("/api/v1/health", healthHandler(pool))
	mux.HandleFunc("/api/", notFound)
	mux.HandleFunc("/api", notFound)
	mux.HandleFunc("/healthz", liveness)
	mux.Handle("/", newSPA(web.Dist()))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost || r.Method == http.MethodPut {
			if origin := r.Header.Get("Origin"); origin != "" {
				parsed, err := url.Parse(origin)
				if err != nil || parsed.Host != r.Host {
					writeJSON(w, http.StatusForbidden, map[string]string{"error": "request origin is not allowed"})
					return
				}
			}
			contentType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
			if err != nil || contentType != "application/json" {
				writeJSON(w, http.StatusUnsupportedMediaType, map[string]string{"error": "use application/json"})
				return
			}
		}
		mux.ServeHTTP(w, r)
	})
}

func liveness(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func notFound(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusNotFound, map[string]string{"error": "not found"})
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}
