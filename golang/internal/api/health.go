package api

import (
	"context"
	"net/http"
	"time"
)

// alive is liveness: the process is up and serving. It checks no dependencies.
func (a *api) alive(w http.ResponseWriter, _ *http.Request) {
	writeText(w, http.StatusOK, "Healthy")
}

// health is readiness: every dependency check must pass. The plain-text body matches ASP.NET health checks.
func (a *api) health(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	for _, check := range a.HealthChecks {
		if err := check(ctx); err != nil {
			a.Log.Warn("health check failed", "error", err)
			writeText(w, http.StatusServiceUnavailable, "Unhealthy")
			return
		}
	}
	writeText(w, http.StatusOK, "Healthy")
}
