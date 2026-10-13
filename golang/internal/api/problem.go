package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"wallet/internal/common"
)

type problem struct {
	Type   string `json:"type"`
	Title  string `json:"title"`
	Status int    `json:"status"`
	Detail string `json:"detail,omitempty"`
}

// StatusFor maps a stable error code to its HTTP status, exactly like .NET's ProblemResults.StatusCodeFor.
func StatusFor(code string) int {
	switch {
	case code == "Money.InvalidAmount", code == "Money.UnsupportedCurrency":
		return http.StatusBadRequest
	case code == "Wallet.NotFound", code == "Transaction.NotFound":
		return http.StatusNotFound
	case code == "Transaction.DuplicateReference", code == "Concurrency.Exhausted":
		return http.StatusConflict
	case code == "Wallet.InsufficientFunds":
		return http.StatusUnprocessableEntity
	case strings.HasPrefix(code, "Validation."):
		return http.StatusBadRequest
	case strings.HasPrefix(code, "Ledger."):
		return http.StatusConflict
	default:
		return http.StatusInternalServerError
	}
}

// writeError turns a handler's *common.Error into an RFC 7807 problem whose title is the stable error code. Anything
// else is unexpected: it is logged and answered with a generic 500.
func (a *api) writeError(w http.ResponseWriter, r *http.Request, err error) {
	var e *common.Error
	if errors.As(err, &e) {
		writeProblem(w, StatusFor(e.Code), e.Code, e.Message)
		return
	}
	a.Log.Error("unhandled error", "method", r.Method, "path", r.URL.Path, "error", err)
	writeProblem(w, http.StatusInternalServerError, "Internal.Error", "An unexpected error occurred.")
}

func writeProblem(w http.ResponseWriter, status int, title, detail string) {
	w.Header().Set("Content-Type", "application/problem+json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(problem{Type: "about:blank", Title: title, Status: status, Detail: detail})
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

func writeText(w http.ResponseWriter, status int, body string) {
	w.Header().Set("Content-Type", "text/plain")
	w.WriteHeader(status)
	_, _ = w.Write([]byte(body))
}
