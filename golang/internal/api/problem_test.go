package api_test

import (
	"bytes"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"

	"wallet/internal/api"
)

func TestErrorCodesMapToHTTPStatuses(t *testing.T) {
	cases := map[string]int{
		"Money.InvalidAmount":            400,
		"Money.UnsupportedCurrency":      400,
		"Validation.AccountNumber":       400,
		"Wallet.NotFound":                404,
		"Transaction.NotFound":           404,
		"Transaction.DuplicateReference": 409,
		"Concurrency.Exhausted":          409,
		"Ledger.HoldMissing":             409,
		"Wallet.InsufficientFunds":       422,
		"Something.Unexpected":           500,
	}
	for code, status := range cases {
		require.Equal(t, status, api.StatusFor(code), code)
	}
}

func TestRecoverPanicsReturnsA500Problem(t *testing.T) {
	var logs bytes.Buffer
	calls := 0
	handler := api.RecoverPanics(slog.New(slog.NewJSONHandler(&logs, nil)), http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		if calls == 1 {
			panic("transaction is already Succeeded")
		}
		w.WriteHeader(http.StatusNoContent)
	}))

	first := httptest.NewRecorder()
	handler.ServeHTTP(first, httptest.NewRequest(http.MethodPost, "/webhooks/provider", nil))
	second := httptest.NewRecorder()
	handler.ServeHTTP(second, httptest.NewRequest(http.MethodPost, "/webhooks/provider", nil))

	require.Equal(t, http.StatusInternalServerError, first.Code)
	require.Equal(t, "application/problem+json", first.Header().Get("Content-Type"))
	require.NotContains(t, first.Body.String(), "already Succeeded", "internal detail must not leak")
	require.Contains(t, logs.String(), `"msg":"unhandled error"`)
	require.Equal(t, http.StatusNoContent, second.Code)
}
