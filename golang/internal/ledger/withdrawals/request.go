// Package withdrawals holds funds for a payout and settles or releases the hold once the provider decides.
package withdrawals

import (
	"context"
	"fmt"

	"github.com/google/uuid"

	"wallet/internal/common"
	"wallet/internal/ledger/domain"
	"wallet/internal/ledger/messages"
	"wallet/internal/ledger/store"
	"wallet/internal/ledger/wallets"
)

type RequestCommand struct {
	UserID        uuid.UUID
	ReferenceID   string
	Amount        int64
	BankCode      string
	AccountNumber string
	AccountName   string
}

// RequestHandler holds the funds and records the withdrawal, its bank details and a PayoutRequested outbox event in
// one save, so the payout is queued exactly when the hold commits.
type RequestHandler struct {
	store *store.Store
	clock common.Clock
}

func NewRequestHandler(s *store.Store, clock common.Clock) *RequestHandler {
	return &RequestHandler{store: s, clock: clock}
}

func (h *RequestHandler) Handle(ctx context.Context, command RequestCommand) (wallets.IdempotentResponse, error) {
	if err := validate(command); err != nil {
		return wallets.IdempotentResponse{}, err
	}
	requestHash := domain.RequestHash(string(domain.Withdrawal), command.UserID, command.Amount,
		command.BankCode, command.AccountNumber, command.AccountName)

	return store.WithRetry(ctx, h.store, func(ctx context.Context, tx *store.Tx) (wallets.IdempotentResponse, error) {
		wallet, err := tx.WalletByUser(ctx, command.UserID)
		if err != nil {
			return wallets.IdempotentResponse{}, err
		}
		if wallet == nil {
			return wallets.IdempotentResponse{}, domain.ErrWalletNotFound
		}
		// Checked inside the retry: a racing identical request may have committed since the last attempt.
		replay, err := wallets.FindReplay(ctx, tx, wallet.ID, command.ReferenceID, requestHash)
		if err != nil {
			return wallets.IdempotentResponse{}, err
		}
		if replay != nil {
			return *replay, nil
		}
		if _, err := wallet.PlaceHold(command.Amount); err != nil {
			return wallets.IdempotentResponse{}, err
		}
		now := h.clock.Now()
		withdrawal := domain.StartWithdrawal(wallet.ID, command.Amount, command.ReferenceID, requestHash, now)
		tx.AddTransaction(withdrawal)
		tx.AddWithdrawal(domain.WithdrawalDetails{
			TransactionID: withdrawal.ID, BankCode: command.BankCode,
			AccountNumber: command.AccountNumber, AccountName: command.AccountName,
		})
		tx.AddOutbox(messages.NewPayoutRequested(withdrawal.ID, now))
		return wallets.IdempotentResponse{Transaction: wallets.FromTransaction(withdrawal)}, nil
	})
}

func validate(command RequestCommand) error {
	if command.Amount <= 0 {
		return domain.ErrInvalidAmount
	}
	if domain.BlankOrLonger(command.ReferenceID, domain.ReferenceIDMaxLength) {
		return domain.Validation("ReferenceId", fmt.Sprintf("ReferenceId is required and at most %d characters.", domain.ReferenceIDMaxLength))
	}
	if domain.BlankOrLonger(command.BankCode, domain.BankCodeMaxLength) {
		return domain.Validation("BankCode", fmt.Sprintf("BankCode is required and at most %d characters.", domain.BankCodeMaxLength))
	}
	if !isNUBAN(command.AccountNumber) {
		return domain.Validation("AccountNumber", "AccountNumber must be a 10-digit NUBAN.")
	}
	if domain.BlankOrLonger(command.AccountName, domain.AccountNameMaxLength) {
		return domain.Validation("AccountName", fmt.Sprintf("AccountName is required and at most %d characters.", domain.AccountNameMaxLength))
	}
	return nil
}

func isNUBAN(accountNumber string) bool {
	if len(accountNumber) != domain.AccountNumberLength {
		return false
	}
	for _, c := range accountNumber {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}
