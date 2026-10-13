// Package wallets opens wallets, reads balances and history, and holds the response and idempotency helpers the
// top-up and withdrawal slices share.
package wallets

import (
	"time"

	"github.com/google/uuid"

	"wallet/internal/ledger/domain"
)

type WalletResponse struct {
	AccountID        uuid.UUID `json:"accountId"`
	UserID           uuid.UUID `json:"userId"`
	Balance          int64     `json:"balance"`
	AvailableBalance int64     `json:"availableBalance"`
	Currency         string    `json:"currency"`
}

func FromAccount(a *domain.Account) WalletResponse {
	response := WalletResponse{AccountID: a.ID, Balance: a.Balance, AvailableBalance: a.AvailableBalance, Currency: a.Currency}
	if a.UserID != nil {
		response.UserID = *a.UserID
	}
	return response
}

type TransactionResponse struct {
	ID                  uuid.UUID  `json:"id"`
	Type                string     `json:"type"`
	Status              string     `json:"status"`
	Amount              int64      `json:"amount"`
	Currency            string     `json:"currency"`
	ReferenceID         string     `json:"referenceId"`
	ProviderReferenceID *string    `json:"providerReferenceId"`
	CreatedAt           time.Time  `json:"createdAt"`
	CompletedAt         *time.Time `json:"completedAt"`
}

func FromTransaction(t *domain.Transaction) TransactionResponse {
	return TransactionResponse{
		ID:                  t.ID,
		Type:                string(t.Type),
		Status:              string(t.Status),
		Amount:              t.Amount,
		Currency:            domain.Currency,
		ReferenceID:         t.ReferenceID,
		ProviderReferenceID: t.ProviderReferenceID,
		CreatedAt:           t.CreatedAt,
		CompletedAt:         t.CompletedAt,
	}
}

// IdempotentResponse is the transaction a command produced. Replayed is true when an earlier identical request
// already produced it.
type IdempotentResponse struct {
	Transaction TransactionResponse
	Replayed    bool
}

type Creation struct {
	Wallet  WalletResponse
	Created bool
}
