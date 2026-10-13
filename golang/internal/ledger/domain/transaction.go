package domain

import (
	"fmt"
	"time"

	"github.com/google/uuid"
)

// Transaction is one money movement (a top-up or a withdrawal) and its lifecycle.
type Transaction struct {
	ID        uuid.UUID
	AccountID uuid.UUID
	Type      TransactionType
	Status    TransactionStatus
	Amount    int64
	// ReferenceID is the client's idempotency key, unique per wallet.
	ReferenceID         string
	ProviderReferenceID *string
	// RequestHash fingerprints the original request, to tell a safe retry from a reused ReferenceID.
	RequestHash string
	CreatedAt   time.Time
	CompletedAt *time.Time
}

func StartTopUp(accountID uuid.UUID, amount int64, referenceID, requestHash, providerReferenceID string, now time.Time) *Transaction {
	t := start(TopUp, accountID, amount, referenceID, requestHash, now)
	t.ProviderReferenceID = &providerReferenceID
	return t
}

func StartWithdrawal(accountID uuid.UUID, amount int64, referenceID, requestHash string, now time.Time) *Transaction {
	return start(Withdrawal, accountID, amount, referenceID, requestHash, now)
}

func start(typ TransactionType, accountID uuid.UUID, amount int64, referenceID, requestHash string, now time.Time) *Transaction {
	return &Transaction{
		ID:          newID(),
		AccountID:   accountID,
		Type:        typ,
		Status:      Pending,
		Amount:      amount,
		ReferenceID: referenceID,
		RequestHash: requestHash,
		CreatedAt:   now,
	}
}

func (t *Transaction) IsFinal() bool { return t.Status != Pending }

func (t *Transaction) AssignProviderReference(providerReferenceID string) {
	t.ProviderReferenceID = &providerReferenceID
}

// ClearProviderReference forgets a provider reference the provider no longer recognises, so the payout can be
// submitted again.
func (t *Transaction) ClearProviderReference() { t.ProviderReferenceID = nil }

func (t *Transaction) MarkSucceeded(now time.Time) { t.complete(Succeeded, now) }

func (t *Transaction) MarkFailed(now time.Time) { t.complete(Failed, now) }

func (t *Transaction) complete(status TransactionStatus, now time.Time) {
	if t.IsFinal() {
		// Handlers check IsFinal first; reaching this is a bug, not an expected failure.
		panic(fmt.Sprintf("transaction %s is already %s", t.ID, t.Status))
	}
	t.Status = status
	t.CompletedAt = &now
}
