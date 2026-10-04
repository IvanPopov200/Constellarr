package api

import (
	"context"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

const databasePingTimeout = 2 * time.Second

type healthResponse struct {
	Status   string `json:"status"`
	Database string `json:"database"`
}

// Keep connection errors out of responses to avoid exposing credentials.
func healthHandler(pool *pgxpool.Pool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), databasePingTimeout)
		defer cancel()

		response := healthResponse{Status: "ok", Database: "connected"}
		status := http.StatusOK
		if err := pool.Ping(ctx); err != nil {
			response = healthResponse{Status: "degraded", Database: "unavailable"}
			status = http.StatusServiceUnavailable
		}
		writeJSON(w, status, response)
	}
}
