package store

import (
	"time"

	"wallet/internal/ledger/domain"
	"wallet/internal/ledger/store/db"
)

// pgx returns timestamptz in the local zone; the wallet works in UTC.
func utc(t time.Time) time.Time { return t.UTC() }

func utcPtr(t *time.Time) *time.Time {
	if t == nil {
		return nil
	}
	u := t.UTC()
	return &u
}

func toAccount(r db.Account) *domain.Account {
	return &domain.Account{
		ID:               r.ID,
		UserID:           r.UserID,
		Type:             domain.AccountType(r.Type),
		Name:             r.Name,
		Currency:         r.Currency,
		Balance:          r.Balance,
		AvailableBalance: r.AvailableBalance,
		Version:          r.Version,
		CreatedAt:        utc(r.CreatedAt),
	}
}

func toTransaction(r db.Transaction) *domain.Transaction {
	return &domain.Transaction{
		ID:                  r.ID,
		AccountID:           r.AccountID,
		Type:                domain.TransactionType(r.Type),
		Status:              domain.TransactionStatus(r.Status),
		Amount:              r.Amount,
		ReferenceID:         r.ReferenceID,
		ProviderReferenceID: r.ProviderReferenceID,
		RequestHash:         r.RequestHash,
		CreatedAt:           utc(r.CreatedAt),
		CompletedAt:         utcPtr(r.CompletedAt),
	}
}

func toOutboxMessage(r db.OutboxMessage) OutboxMessage {
	return OutboxMessage{
		ID:          r.ID,
		Type:        r.Type,
		Payload:     r.Payload,
		OccurredAt:  utc(r.OccurredAt),
		ProcessedAt: utcPtr(r.ProcessedAt),
		Attempts:    r.Attempts,
		LastError:   r.LastError,
	}
}
