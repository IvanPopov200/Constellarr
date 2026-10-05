package operations

import (
	"net/http"
	"time"
)

// WrapHTTP records request counts, status codes and durations.
func (s *Service) WrapHTTP(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		started := time.Now()
		recorder := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		s.metrics.startRequest()
		defer func() {
			s.metrics.finishRequest()
			s.metrics.observeRequest(r.Method, recorder.status, time.Since(started))
		}()
		next.ServeHTTP(recorder, r)
	})
}

type statusRecorder struct {
	http.ResponseWriter
	status int
	wrote  bool
}

func (r *statusRecorder) WriteHeader(status int) {
	if !r.wrote {
		r.wrote = true
		r.status = status
	}
	r.ResponseWriter.WriteHeader(status)
}

func (r *statusRecorder) Write(body []byte) (int, error) {
	r.wrote = true
	return r.ResponseWriter.Write(body)
}

// Unwrap lets http.ResponseController reach streaming and deadline methods.
func (r *statusRecorder) Unwrap() http.ResponseWriter { return r.ResponseWriter }
