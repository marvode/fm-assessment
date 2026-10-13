package domain

import (
	"time"

	"github.com/google/uuid"
)

// Account is a balance holder. Balance is settled money and always equals the sum of the account's ledger entries.
// AvailableBalance is Balance minus active withdrawal holds. Version is the optimistic-concurrency token: every
// change bumps it, and store.Run only saves an account whose stored Version is still the one it loaded.
type Account struct {
	ID               uuid.UUID
	UserID           *uuid.UUID
	Type             AccountType
	Name             *string
	Currency         string
	Balance          int64
	AvailableBalance int64
	Version          int64
	CreatedAt        time.Time
}

func OpenUserWallet(userID uuid.UUID, now time.Time) *Account {
	return &Account{ID: newID(), UserID: &userID, Type: AccountUser, Currency: Currency, CreatedAt: now}
}

func OpenSystem(id uuid.UUID, name string, now time.Time) *Account {
	return &Account{ID: id, Name: &name, Type: AccountSystem, Currency: Currency, CreatedAt: now}
}

func (a *Account) PlaceHold(amount int64) (int64, error) {
	if amount <= 0 {
		return 0, ErrInvalidAmount
	}
	if a.AvailableBalance < amount {
		return 0, ErrInsufficientFunds
	}
	a.AvailableBalance -= amount
	a.Version++
	return a.AvailableBalance, nil
}

func (a *Account) ReleaseHold(amount int64) (int64, error) {
	if amount <= 0 {
		return 0, ErrInvalidAmount
	}
	if a.AvailableBalance+amount > a.Balance {
		return 0, ErrHoldMissing
	}
	a.AvailableBalance += amount
	a.Version++
	return a.AvailableBalance, nil
}

// CanDebit: system accounts may go negative. A user wallet needs available funds, or, when fromHold is true, a hold
// covering the amount.
func (a *Account) CanDebit(amount int64, fromHold bool) error {
	if a.Type == AccountSystem {
		return nil
	}
	if fromHold {
		if a.Balance-amount >= a.AvailableBalance {
			return nil
		}
		return ErrHoldMissing
	}
	if a.AvailableBalance >= amount {
		return nil
	}
	return ErrInsufficientFunds
}

func (a *Account) Credit(transactionID uuid.UUID, amount int64, now time.Time) Entry {
	before := a.Balance
	a.Balance += amount
	a.AvailableBalance += amount
	a.Version++
	return newEntry(transactionID, a.ID, Credit, amount, a.Currency, before, a.Balance, now)
}

// Debit lowers the balance. When fromHold is true, AvailableBalance already dropped when the hold was placed, so
// only Balance moves.
func (a *Account) Debit(transactionID uuid.UUID, amount int64, fromHold bool, now time.Time) Entry {
	before := a.Balance
	a.Balance -= amount
	if !fromHold {
		a.AvailableBalance -= amount
	}
	a.Version++
	return newEntry(transactionID, a.ID, Debit, amount, a.Currency, before, a.Balance, now)
}
