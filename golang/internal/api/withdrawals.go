package api

import (
	"net/http"

	"wallet/internal/ledger/withdrawals"
)

type withdrawalRequest struct {
	ReferenceID   *string `json:"referenceId"`
	Amount        *int64  `json:"amount"`
	BankCode      *string `json:"bankCode"`
	AccountNumber *string `json:"accountNumber"`
	AccountName   *string `json:"accountName"`
}

func (a *api) requestWithdrawal(w http.ResponseWriter, r *http.Request) {
	id, ok := userID(w, r)
	if !ok {
		return
	}
	command, err := withdrawalCommand(w, r)
	if err != nil {
		a.writeError(w, r, err)
		return
	}
	command.UserID = id
	result, err := a.RequestWithdrawal.Handle(r.Context(), command)
	if err != nil {
		a.writeError(w, r, err)
		return
	}
	if result.Replayed {
		writeJSON(w, http.StatusOK, result.Transaction)
		return
	}
	writeJSON(w, http.StatusAccepted, result.Transaction)
}

func withdrawalCommand(w http.ResponseWriter, r *http.Request) (withdrawals.RequestCommand, error) {
	var body withdrawalRequest
	if err := decode(w, r, &body); err != nil {
		return withdrawals.RequestCommand{}, err
	}
	var command withdrawals.RequestCommand
	var err error
	if command.ReferenceID, err = required(body.ReferenceID, "ReferenceId"); err != nil {
		return command, err
	}
	if command.Amount, err = required(body.Amount, "Amount"); err != nil {
		return command, err
	}
	if command.BankCode, err = required(body.BankCode, "BankCode"); err != nil {
		return command, err
	}
	if command.AccountNumber, err = required(body.AccountNumber, "AccountNumber"); err != nil {
		return command, err
	}
	if command.AccountName, err = required(body.AccountName, "AccountName"); err != nil {
		return command, err
	}
	return command, nil
}
