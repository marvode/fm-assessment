package api

import (
	"fmt"
	"log/slog"
	"net/http"
	"runtime/debug"
)

// RecoverPanics turns a panic in next into a logged 500 problem, so one bad request never takes the server's
// response with it.
func RecoverPanics(log *slog.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			recovered := recover()
			if recovered == nil {
				return
			}
			if recovered == http.ErrAbortHandler {
				panic(recovered)
			}
			log.Error("unhandled error", "method", r.Method, "path", r.URL.Path,
				"panic", fmt.Sprint(recovered), "stack", string(debug.Stack()))
			writeProblem(w, http.StatusInternalServerError, "Internal.Error", "An unexpected error occurred.")
		}()
		next.ServeHTTP(w, r)
	})
}
