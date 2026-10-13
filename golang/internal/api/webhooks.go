package api

import (
	"net/http"

	"wallet/internal/ledger/webhooks"
)

type webhookRequest struct {
	Event               *string `json:"event"`
	ProviderReferenceID *string `json:"providerReferenceId"`
	Reference           *string `json:"reference"`
	Amount              int64   `json:"amount"`
}

func (a *api) receiveWebhook(w http.ResponseWriter, r *http.Request) {
	var body webhookRequest
	if err := decode(w, r, &body); err != nil {
		a.writeError(w, r, err)
		return
	}
	event, err := required(body.Event, "Event")
	if err != nil {
		a.writeError(w, r, err)
		return
	}
	providerReferenceID, err := required(body.ProviderReferenceID, "ProviderReferenceId")
	if err != nil {
		a.writeError(w, r, err)
		return
	}
	result, err := a.Webhooks.Handle(r.Context(), webhooks.Webhook{
		Event: event, ProviderReferenceID: providerReferenceID, Reference: body.Reference, Amount: body.Amount,
	})
	if err != nil {
		a.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}
