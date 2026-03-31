package httpapi

import (
	"log"
	"net/http"
	"runtime/debug"
	"time"

	"forest/go-api/internal/config"
)

func withMiddleware(cfg config.Config, next http.Handler) http.Handler {
	return recoverMiddleware(requestLoggingMiddleware(cfg, next))
}

func requestLoggingMiddleware(cfg config.Config, next http.Handler) http.Handler {
	if !cfg.AccessLogEnabled && cfg.SlowRequestLogThreshold <= 0 {
		return next
	}

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		recorder := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(recorder, r)

		duration := time.Since(start)
		if cfg.AccessLogEnabled {
			log.Printf("%s %s %d %s", r.Method, r.URL.Path, recorder.status, duration)
			return
		}
		if cfg.SlowRequestLogThreshold > 0 && duration >= cfg.SlowRequestLogThreshold {
			log.Printf("slow request method=%s path=%s status=%d duration=%s", r.Method, r.URL.Path, recorder.status, duration)
		}
	})
}

func recoverMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if rec := recover(); rec != nil {
				log.Printf("panic recovered: %v\n%s", rec, string(debug.Stack()))
				writeJSON(w, http.StatusInternalServerError, map[string]any{
					"status": "error",
				})
			}
		}()

		next.ServeHTTP(w, r)
	})
}

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *statusRecorder) WriteHeader(status int) {
	r.status = status
	r.ResponseWriter.WriteHeader(status)
}
