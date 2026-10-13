package domain

import "github.com/google/uuid"

// WithdrawalDetails is where a withdrawal pays out to. One row per withdrawal transaction.
type WithdrawalDetails struct {
	TransactionID uuid.UUID
	BankCode      string
	AccountNumber string
	AccountName   string
}
