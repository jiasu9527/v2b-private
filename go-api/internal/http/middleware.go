package httpapi

import (
	"log"
	"net/http"
	"runtime/debug"
	"time"
)

func withMiddleware(next http.Handler) http.Handler {
	return recoverMiddleware(loggingMiddleware(next))
}

func loggingMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		next.ServeHTTP(w, r)
		log.Printf("%s %s %s", r.Method, r.URL.Path, time.Since(start))
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
