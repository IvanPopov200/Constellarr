package api

import (
	"encoding/json"
	"mime"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/IvanPopov200/Constellarr/backend/internal/downloads"
	"github.com/IvanPopov200/Constellarr/backend/internal/movies"
	"github.com/IvanPopov200/Constellarr/backend/internal/web"
)

type Services struct {
	Downloads *downloads.Manager
	Movies    *movies.Service
}

func New(pool *pgxpool.Pool, services ...Services) http.Handler {
	mux := http.NewServeMux()
	if len(services) != 0 && services[0].Downloads != nil {
		registerDownloads(mux, services[0].Downloads)
	}
	if len(services) != 0 && services[0].Movies != nil {
		registerMovies(mux, services[0].Movies)
		registerMoviePosters(mux, services[0].Movies)
	}
	mux.HandleFunc("/api/v1/health", healthHandler(pool))
	mux.HandleFunc("/api/", notFound)
	mux.HandleFunc("/api", notFound)
	mux.HandleFunc("/healthz", liveness)
	mux.Handle("/", newSPA(web.Dist()))
	hosts := allowedHosts()
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !hosts.permits(r.Host) {
			writeJSON(w, http.StatusForbidden, map[string]string{"error": "request host is not allowed"})
			return
		}
		// Cross-site browser requests must not reach the API, including read-only poster and metadata reads.
		if strings.HasPrefix(r.URL.Path, "/api/") && r.Header.Get("Sec-Fetch-Site") == "cross-site" {
			writeJSON(w, http.StatusForbidden, map[string]string{"error": "cross-site requests are not allowed"})
			return
		}
		if r.Method == http.MethodPost || r.Method == http.MethodPut || r.Method == http.MethodDelete {
			if origin := r.Header.Get("Origin"); origin != "" {
				parsed, err := url.Parse(origin)
				if err != nil || !allowedOrigin(parsed, r.Host) {
					writeJSON(w, http.StatusForbidden, map[string]string{"error": "request origin is not allowed"})
					return
				}
			}
			contentType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
			if r.Method != http.MethodDelete && (err != nil || contentType != "application/json") {
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

// TLS may terminate at a reverse proxy, so origin checks do not depend on the request scheme.
func allowedOrigin(origin *url.URL, host string) bool {
	scheme := strings.ToLower(origin.Scheme)
	return (scheme == "http" || scheme == "https") && strings.EqualFold(origin.Host, host)
}

type hostPolicy map[string]struct{}

func allowedHosts() hostPolicy {
	hosts := hostPolicy{"localhost": {}}
	if host, err := os.Hostname(); err == nil && host != "" {
		hosts[normalizeHost(host)] = struct{}{}
	}
	for _, entry := range strings.Split(os.Getenv("ALLOWED_HOSTS"), ",") {
		if entry = strings.TrimSpace(entry); entry != "" {
			hosts[normalizeHost(entry)] = struct{}{}
		}
	}
	return hosts
}

func (hosts hostPolicy) permits(host string) bool {
	name := normalizeHost(host)
	if _, err := netip.ParseAddr(name); err == nil {
		return true
	}
	_, ok := hosts[name]
	return ok
}

func normalizeHost(host string) string {
	if name, _, err := net.SplitHostPort(host); err == nil {
		host = name
	}
	return strings.ToLower(strings.TrimSuffix(strings.Trim(host, "[]"), "."))
}

func notFound(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusNotFound, map[string]string{"error": "not found"})
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(posterURLs(body))
}
