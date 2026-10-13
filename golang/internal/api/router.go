// Package api is the HTTP surface: the same routes, status codes and problem responses as the .NET controllers.
package api

import (
	"context"
	"log/slog"
	"net/http"

	"github.com/google/uuid"

	"wallet/internal/ledger/providers"
	"wallet/internal/ledger/topups"
	"wallet/internal/ledger/wallets"
	"wallet/internal/ledger/webhooks"
	"wallet/internal/ledger/withdrawals"
)

// HealthCheck returns an error when a dependency is unusable.
type HealthCheck func(ctx context.Context) error

type Deps struct {
	Log               *slog.Logger
	Development       bool
	CreateWallet      *wallets.CreateHandler
	GetWallet         *wallets.GetHandler
	ListTransactions  *wallets.ListHandler
	InitiateTopUp     *topups.InitiateHandler
	RequestWithdrawal *withdrawals.RequestHandler
	Webhooks          *webhooks.Handler
	FakeProvider      *providers.FakePaymentProvider
	HealthChecks      []HealthCheck
}

type api struct {
	Deps
}

func NewRouter(d Deps) http.Handler {
	a := &api{d}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /users/{userId}/wallet", a.createWallet)
	mux.HandleFunc("GET /users/{userId}/wallet", a.getWallet)
	mux.HandleFunc("GET /users/{userId}/wallet/transactions", a.listTransactions)
	mux.HandleFunc("POST /users/{userId}/top-ups", a.initiateTopUp)
	mux.HandleFunc("POST /users/{userId}/withdrawals", a.requestWithdrawal)
	mux.HandleFunc("POST /webhooks/provider", a.receiveWebhook)
	mux.HandleFunc("GET /alive", a.alive)
	mux.HandleFunc("GET /health", a.health)
	if d.Development {
		mux.HandleFunc("POST /dev/provider/{providerReferenceId}/complete", a.completeDevProvider)
	}
	return RecoverPanics(d.Log, mux)
}

// userID parses the {userId} path segment. Like the .NET :guid route constraint, a non-UUID is a plain 404.
func userID(w http.ResponseWriter, r *http.Request) (uuid.UUID, bool) {
	id, err := uuid.Parse(r.PathValue("userId"))
	if err != nil {
		http.NotFound(w, r)
		return uuid.Nil, false
	}
	return id, true
}
