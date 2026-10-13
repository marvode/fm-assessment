package store

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"wallet/internal/ledger/domain"
	"wallet/internal/ledger/store/db"
)

// Tx is one unit of work, like an EF DbContext. Entities it loads are tracked (loading the same id twice returns
// the same pointer), Add* stages inserts, and Run writes every change in one database transaction once the operation
// returns. A changed account is saved only if its stored version is still the loaded one; a changed transaction only
// if its stored status is. Losing either race fails the save with ErrConflict.
type Tx struct {
	q               *db.Queries
	accounts        map[uuid.UUID]*trackedAccount
	accountList     []*trackedAccount
	transactions    map[uuid.UUID]*trackedTransaction
	transactionList []*trackedTransaction
	newAccounts     []*domain.Account
	newTransactions []*domain.Transaction
	entries         []domain.Entry
	withdrawals     []domain.WithdrawalDetails
	outbox          []OutboxMessage
}

type trackedAccount struct {
	current  *domain.Account
	original domain.Account
}

type trackedTransaction struct {
	current  *domain.Transaction
	original domain.Transaction
}

func newTx(q *db.Queries) *Tx {
	return &Tx{q: q, accounts: map[uuid.UUID]*trackedAccount{}, transactions: map[uuid.UUID]*trackedTransaction{}}
}

func (t *Tx) Account(ctx context.Context, id uuid.UUID) (*domain.Account, error) {
	if tracked, ok := t.accounts[id]; ok {
		return tracked.current, nil
	}
	row, err := t.q.GetAccount(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("load account %s: %w", id, err)
	}
	return t.trackAccount(row), nil
}

func (t *Tx) WalletByUser(ctx context.Context, userID uuid.UUID) (*domain.Account, error) {
	row, err := t.q.GetWalletByUser(ctx, userID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return t.trackAccount(row), nil
}

func (t *Tx) Transaction(ctx context.Context, id uuid.UUID) (*domain.Transaction, error) {
	if tracked, ok := t.transactions[id]; ok {
		return tracked.current, nil
	}
	return t.loadTransaction(t.q.GetTransaction(ctx, id))
}

func (t *Tx) TransactionByProviderReference(ctx context.Context, providerReferenceID string, typ domain.TransactionType) (*domain.Transaction, error) {
	return t.loadTransaction(t.q.GetTransactionByProviderReference(ctx, db.GetTransactionByProviderReferenceParams{
		ProviderReferenceID: providerReferenceID,
		Type:                string(typ),
	}))
}

func (t *Tx) TransactionByReference(ctx context.Context, accountID uuid.UUID, referenceID string) (*domain.Transaction, error) {
	return t.loadTransaction(t.q.GetTransactionByReference(ctx, db.GetTransactionByReferenceParams{
		AccountID:   accountID,
		ReferenceID: referenceID,
	}))
}

func (t *Tx) AddAccount(a *domain.Account) { t.newAccounts = append(t.newAccounts, a) }
func (t *Tx) AddTransaction(tr *domain.Transaction) {
	t.newTransactions = append(t.newTransactions, tr)
}
func (t *Tx) AddEntries(entries ...domain.Entry)       { t.entries = append(t.entries, entries...) }
func (t *Tx) AddWithdrawal(w domain.WithdrawalDetails) { t.withdrawals = append(t.withdrawals, w) }
func (t *Tx) AddOutbox(m OutboxMessage)                { t.outbox = append(t.outbox, m) }

func (t *Tx) trackAccount(row db.Account) *domain.Account {
	if tracked, ok := t.accounts[row.ID]; ok {
		return tracked.current
	}
	account := toAccount(row)
	tracked := &trackedAccount{current: account, original: *account}
	t.accounts[row.ID] = tracked
	t.accountList = append(t.accountList, tracked)
	return account
}

func (t *Tx) loadTransaction(row db.Transaction, err error) (*domain.Transaction, error) {
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if tracked, ok := t.transactions[row.ID]; ok {
		return tracked.current, nil
	}
	transaction := toTransaction(row)
	tracked := &trackedTransaction{current: transaction, original: *transaction}
	t.transactions[row.ID] = tracked
	t.transactionList = append(t.transactionList, tracked)
	return transaction, nil
}

func (t *Tx) hasWrites() bool {
	if len(t.newAccounts)+len(t.newTransactions)+len(t.entries)+len(t.withdrawals)+len(t.outbox) > 0 {
		return true
	}
	for _, a := range t.accountList {
		if *a.current != a.original {
			return true
		}
	}
	for _, tr := range t.transactionList {
		if *tr.current != tr.original {
			return true
		}
	}
	return false
}

// flush writes inserts and conditional updates in dependency order (foreign keys point at accounts and transactions).
func (t *Tx) flush(ctx context.Context) error {
	for _, a := range t.newAccounts {
		err := t.q.InsertAccount(ctx, db.InsertAccountParams{
			ID: a.ID, UserID: a.UserID, Type: string(a.Type), Name: a.Name, Currency: a.Currency,
			Balance: a.Balance, AvailableBalance: a.AvailableBalance, Version: a.Version, CreatedAt: a.CreatedAt,
		})
		if err != nil {
			return writeErr(err)
		}
	}
	for _, tracked := range t.accountList {
		a := tracked.current
		if *a == tracked.original {
			continue
		}
		rows, err := t.q.UpdateAccount(ctx, db.UpdateAccountParams{
			Balance: a.Balance, AvailableBalance: a.AvailableBalance, Version: a.Version,
			ID: a.ID, ExpectedVersion: tracked.original.Version,
		})
		if err != nil {
			return writeErr(err)
		}
		if rows == 0 {
			return ErrConflict
		}
	}
	for _, tr := range t.newTransactions {
		err := t.q.InsertTransaction(ctx, db.InsertTransactionParams{
			ID: tr.ID, AccountID: tr.AccountID, Type: string(tr.Type), Status: string(tr.Status), Amount: tr.Amount,
			ReferenceID: tr.ReferenceID, ProviderReferenceID: tr.ProviderReferenceID, RequestHash: tr.RequestHash,
			CreatedAt: tr.CreatedAt, CompletedAt: tr.CompletedAt,
		})
		if err != nil {
			return writeErr(err)
		}
	}
	for _, tracked := range t.transactionList {
		tr := tracked.current
		if *tr == tracked.original {
			continue
		}
		rows, err := t.q.UpdateTransaction(ctx, db.UpdateTransactionParams{
			Status: string(tr.Status), ProviderReferenceID: tr.ProviderReferenceID, CompletedAt: tr.CompletedAt,
			ID: tr.ID, ExpectedStatus: string(tracked.original.Status),
		})
		if err != nil {
			return writeErr(err)
		}
		if rows == 0 {
			return ErrConflict
		}
	}
	for _, w := range t.withdrawals {
		err := t.q.InsertWithdrawal(ctx, db.InsertWithdrawalParams{
			TransactionID: w.TransactionID, BankCode: w.BankCode, AccountNumber: w.AccountNumber, AccountName: w.AccountName,
		})
		if err != nil {
			return writeErr(err)
		}
	}
	for _, e := range t.entries {
		err := t.q.InsertLedgerEntry(ctx, db.InsertLedgerEntryParams{
			ID: e.ID, TransactionID: e.TransactionID, AccountID: e.AccountID, Direction: string(e.Direction),
			Amount: e.Amount, Currency: e.Currency, BalanceBefore: e.BalanceBefore, BalanceAfter: e.BalanceAfter,
			CreatedAt: e.CreatedAt,
		})
		if err != nil {
			return writeErr(err)
		}
	}
	for _, m := range t.outbox {
		err := t.q.InsertOutboxMessage(ctx, db.InsertOutboxMessageParams{
			ID: m.ID, Type: m.Type, Payload: m.Payload, OccurredAt: m.OccurredAt,
		})
		if err != nil {
			return writeErr(err)
		}
	}
	return nil
}
