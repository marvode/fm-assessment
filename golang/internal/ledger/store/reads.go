package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"wallet/internal/ledger/domain"
	"wallet/internal/ledger/store/db"
)

// Reads outside a unit of work: nothing they return is tracked or saved.

func (s *Store) WalletByUser(ctx context.Context, userID uuid.UUID) (*domain.Account, error) {
	row, err := s.queries.GetWalletByUser(ctx, userID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return toAccount(row), nil
}

func (s *Store) Account(ctx context.Context, id uuid.UUID) (*domain.Account, error) {
	row, err := s.queries.GetAccount(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("load account %s: %w", id, err)
	}
	return toAccount(row), nil
}

func (s *Store) Transaction(ctx context.Context, id uuid.UUID) (*domain.Transaction, error) {
	row, err := s.queries.GetTransaction(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return toTransaction(row), nil
}

// Transactions lists an account's transactions newest first.
func (s *Store) Transactions(ctx context.Context, accountID uuid.UUID, limit int32) ([]*domain.Transaction, error) {
	rows, err := s.queries.ListTransactions(ctx, db.ListTransactionsParams{AccountID: accountID, Limit: limit})
	if err != nil {
		return nil, err
	}
	transactions := make([]*domain.Transaction, len(rows))
	for i, row := range rows {
		transactions[i] = toTransaction(row)
	}
	return transactions, nil
}

func (s *Store) Withdrawal(ctx context.Context, transactionID uuid.UUID) (domain.WithdrawalDetails, error) {
	row, err := s.queries.GetWithdrawal(ctx, transactionID)
	if err != nil {
		return domain.WithdrawalDetails{}, fmt.Errorf("load withdrawal details %s: %w", transactionID, err)
	}
	return domain.WithdrawalDetails{
		TransactionID: row.TransactionID, BankCode: row.BankCode, AccountNumber: row.AccountNumber, AccountName: row.AccountName,
	}, nil
}

// StalePendingWithdrawals returns Pending withdrawals created at or before cutoff, oldest first.
func (s *Store) StalePendingWithdrawals(ctx context.Context, cutoff time.Time) ([]uuid.UUID, error) {
	return s.queries.StalePendingWithdrawals(ctx, cutoff)
}

// FindWithdrawalID matches a withdrawal by the provider's reference or, when keyedID is set, by its own id.
func (s *Store) FindWithdrawalID(ctx context.Context, providerReferenceID string, keyedID *uuid.UUID) (uuid.UUID, bool, error) {
	id, err := s.queries.FindWithdrawalID(ctx, db.FindWithdrawalIDParams{ProviderReferenceID: providerReferenceID, KeyedID: keyedID})
	if errors.Is(err, pgx.ErrNoRows) {
		return uuid.Nil, false, nil
	}
	if err != nil {
		return uuid.Nil, false, err
	}
	return id, true, nil
}
