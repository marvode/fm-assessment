package domain

import (
	"time"

	"github.com/google/uuid"
)

// Entry is one side of a posting. Append-only: entries are never updated or deleted.
type Entry struct {
	ID            uuid.UUID
	TransactionID uuid.UUID
	AccountID     uuid.UUID
	Direction     Direction
	Amount        int64
	Currency      string
	BalanceBefore int64
	BalanceAfter  int64
	CreatedAt     time.Time
}

func newEntry(transactionID, accountID uuid.UUID, direction Direction, amount int64, currency string, before, after int64, now time.Time) Entry {
	return Entry{
		ID:            newID(),
		TransactionID: transactionID,
		AccountID:     accountID,
		Direction:     direction,
		Amount:        amount,
		Currency:      currency,
		BalanceBefore: before,
		BalanceAfter:  after,
		CreatedAt:     now,
	}
}

// Post is the double-entry posting: it moves the transaction amount from debitAccount to creditAccount as exactly one
// Debit and one Credit. Every check runs before any account changes, so a failure leaves both accounts untouched.
// debitFromHold means the amount was already held on debitAccount.
func Post(t *Transaction, debitAccount, creditAccount *Account, debitFromHold bool, now time.Time) ([]Entry, error) {
	amount := t.Amount
	if amount <= 0 {
		return nil, ErrInvalidAmount
	}
	if debitAccount.Currency != Currency || creditAccount.Currency != Currency {
		return nil, ErrUnsupportedCurrency
	}
	if debitAccount.ID == creditAccount.ID {
		return nil, ErrSameAccount
	}
	if err := debitAccount.CanDebit(amount, debitFromHold); err != nil {
		return nil, err
	}
	return []Entry{
		debitAccount.Debit(t.ID, amount, debitFromHold, now),
		creditAccount.Credit(t.ID, amount, now),
	}, nil
}
