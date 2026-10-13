// Package webhooks routes provider callbacks to the top-up and withdrawal handlers. Every route is idempotent.
package webhooks

import (
	"context"

	"github.com/google/uuid"

	"wallet/internal/ledger/domain"
	"wallet/internal/ledger/providers"
	"wallet/internal/ledger/store"
	"wallet/internal/ledger/topups"
	"wallet/internal/ledger/wallets"
	"wallet/internal/ledger/withdrawals"
)

const (
	ChargeSuccess   = "charge.success"
	ChargeFailed    = "charge.failed"
	TransferSuccess = "transfer.success"
	TransferFailed  = "transfer.failed"
)

type Webhook struct {
	Event               string
	ProviderReferenceID string
	// Reference is the key we gave the provider, echoed back (for a payout, providers.PayoutKey: the transaction id).
	// It lets a transfer webhook match before the payout worker has stored the provider reference.
	Reference *string
	Amount    int64
}

type Handler struct {
	store   *store.Store
	topUps  *topups.CompleteHandler
	settle  *withdrawals.SettleHandler
	release *withdrawals.ReleaseHandler
}

func NewHandler(s *store.Store, topUps *topups.CompleteHandler, settle *withdrawals.SettleHandler, release *withdrawals.ReleaseHandler) *Handler {
	return &Handler{store: s, topUps: topUps, settle: settle, release: release}
}

func (h *Handler) Handle(ctx context.Context, webhook Webhook) (wallets.TransactionResponse, error) {
	switch webhook.Event {
	case ChargeSuccess:
		return h.topUps.Succeed(ctx, webhook.ProviderReferenceID, webhook.Amount)
	case ChargeFailed:
		return h.topUps.Fail(ctx, webhook.ProviderReferenceID)
	case TransferSuccess, TransferFailed:
		transactionID, found, err := h.findWithdrawalID(ctx, webhook)
		if err != nil {
			return wallets.TransactionResponse{}, err
		}
		if !found {
			return wallets.TransactionResponse{}, domain.ErrTransactionNotFound
		}
		if webhook.Event == TransferSuccess {
			return h.settle.Handle(ctx, transactionID)
		}
		return h.release.Handle(ctx, transactionID, "Provider reported transfer.failed.")
	default:
		return wallets.TransactionResponse{}, domain.ErrUnknownWebhookEvent
	}
}

func (h *Handler) findWithdrawalID(ctx context.Context, webhook Webhook) (uuid.UUID, bool, error) {
	// The payout key is the transaction id, so a well-formed reference identifies the withdrawal directly.
	var keyedID *uuid.UUID
	if webhook.Reference != nil {
		if id, ok := providers.ParsePayoutKey(*webhook.Reference); ok {
			keyedID = &id
		}
	}
	return h.store.FindWithdrawalID(ctx, webhook.ProviderReferenceID, keyedID)
}
