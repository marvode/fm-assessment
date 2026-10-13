package api

import (
	"net/http"

	"wallet/internal/ledger/topups"
)

type topUpRequest struct {
	ReferenceID *string `json:"referenceId"`
	Amount      *int64  `json:"amount"`
}

func (a *api) initiateTopUp(w http.ResponseWriter, r *http.Request) {
	id, ok := userID(w, r)
	if !ok {
		return
	}
	command, err := topUpCommand(w, r)
	if err != nil {
		a.writeError(w, r, err)
		return
	}
	command.UserID = id
	result, err := a.InitiateTopUp.Handle(r.Context(), command)
	if err != nil {
		a.writeError(w, r, err)
		return
	}
	if result.Replayed {
		writeJSON(w, http.StatusOK, result.Transaction)
		return
	}
	w.Header().Set("Location", "/users/"+id.String()+"/wallet/transactions")
	writeJSON(w, http.StatusCreated, result.Transaction)
}

func topUpCommand(w http.ResponseWriter, r *http.Request) (topups.InitiateCommand, error) {
	var body topUpRequest
	if err := decode(w, r, &body); err != nil {
		return topups.InitiateCommand{}, err
	}
	referenceID, err := required(body.ReferenceID, "ReferenceId")
	if err != nil {
		return topups.InitiateCommand{}, err
	}
	amount, err := required(body.Amount, "Amount")
	if err != nil {
		return topups.InitiateCommand{}, err
	}
	return topups.InitiateCommand{ReferenceID: referenceID, Amount: amount}, nil
}
