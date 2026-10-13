package domain

import "wallet/internal/common"

// Every expected wallet failure. Codes are stable: the API maps them to HTTP statuses.
var (
	ErrInvalidAmount        = common.NewError("Money.InvalidAmount", "Amount must be a positive number of kobo.")
	ErrUnsupportedCurrency  = common.NewError("Money.UnsupportedCurrency", "Only NGN is supported.")
	ErrWalletNotFound       = common.NewError("Wallet.NotFound", "The user has no wallet.")
	ErrInsufficientFunds    = common.NewError("Wallet.InsufficientFunds", "The available balance is too low.")
	ErrTransactionNotFound  = common.NewError("Transaction.NotFound", "No matching transaction.")
	ErrDuplicateReference   = common.NewError("Transaction.DuplicateReference", "This referenceId was already used for a different request.")
	ErrConcurrencyExhausted = common.NewError("Concurrency.Exhausted", "The wallet kept changing concurrently; retry the request.")
	ErrHoldMissing          = common.NewError("Ledger.HoldMissing", "There is no matching hold for this amount.")
	ErrSameAccount          = common.NewError("Ledger.SameAccount", "A posting needs two different accounts.")
	ErrUnknownWebhookEvent  = common.NewError("Validation.Event", "Unknown webhook event.")
)

func Validation(field, message string) *common.Error {
	return common.NewError("Validation."+field, message)
}
