// Package providers is the payment provider as the wallet sees it, plus an in-memory fake.
package providers

import "context"

type PayoutOutcome string

const (
	Accepted PayoutOutcome = "Accepted"
	Rejected PayoutOutcome = "Rejected"
)

type PayoutStatus string

const (
	StatusPending   PayoutStatus = "Pending"
	StatusSucceeded PayoutStatus = "Succeeded"
	StatusFailed    PayoutStatus = "Failed"
	StatusNotFound  PayoutStatus = "NotFound"
)

type ChargeInitiation struct {
	ProviderReferenceID string
}

type PayoutInitiation struct {
	Outcome             PayoutOutcome
	ProviderReferenceID *string
	FailureReason       *string
}

func AcceptedPayout(providerReferenceID string) PayoutInitiation {
	return PayoutInitiation{Outcome: Accepted, ProviderReferenceID: &providerReferenceID}
}

func RejectedPayout(reason string) PayoutInitiation {
	return PayoutInitiation{Outcome: Rejected, FailureReason: &reason}
}

type PayoutInstruction struct {
	// ReferenceID is our key for the payout: the provider's idempotency key.
	ReferenceID   string
	Amount        int64
	BankCode      string
	AccountNumber string
	AccountName   string
}

// PaymentProvider: transient faults (network, 5xx) come back as errors; a definite refusal is a Rejected
// PayoutInitiation. Payouts are idempotent per ReferenceID.
type PaymentProvider interface {
	InitiateCharge(ctx context.Context, referenceID string, amount int64) (ChargeInitiation, error)
	InitiatePayout(ctx context.Context, instruction PayoutInstruction) (PayoutInitiation, error)
	GetPayoutStatus(ctx context.Context, referenceID string) (PayoutStatus, error)
}
