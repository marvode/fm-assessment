package api

import (
	"net/http"

	"wallet/internal/ledger/domain"
	"wallet/internal/ledger/providers"
	"wallet/internal/ledger/webhooks"
)

// completeDevProvider plays the fake provider's webhook so the whole flow can be tried by hand. Registered only in
// development.
func (a *api) completeDevProvider(w http.ResponseWriter, r *http.Request) {
	var succeeded bool
	switch r.URL.Query().Get("outcome") {
	case "success":
		succeeded = true
	case "failed":
		succeeded = false
	default:
		a.writeError(w, r, domain.Validation("Outcome", "outcome must be 'success' or 'failed'."))
		return
	}
	record, ok := a.FakeProvider.FindByProviderReference(r.PathValue("providerReferenceId"))
	if !ok {
		http.NotFound(w, r)
		return
	}
	event := webhooks.ChargeFailed
	switch {
	case record.IsPayout && succeeded:
		event = webhooks.TransferSuccess
	case record.IsPayout:
		event = webhooks.TransferFailed
	case succeeded:
		event = webhooks.ChargeSuccess
	}
	if record.IsPayout {
		status := providers.StatusFailed
		if succeeded {
			status = providers.StatusSucceeded
		}
		a.FakeProvider.SetPayoutStatus(record.ReferenceID, status)
	}
	reference := record.ReferenceID
	result, err := a.Webhooks.Handle(r.Context(), webhooks.Webhook{
		Event: event, ProviderReferenceID: record.ProviderReferenceID, Reference: &reference, Amount: record.Amount,
	})
	if err != nil {
		a.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}
