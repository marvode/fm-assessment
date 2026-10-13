package api

import "net/http"

func (a *api) createWallet(w http.ResponseWriter, r *http.Request) {
	id, ok := userID(w, r)
	if !ok {
		return
	}
	result, err := a.CreateWallet.Handle(r.Context(), id)
	if err != nil {
		a.writeError(w, r, err)
		return
	}
	if !result.Created {
		writeJSON(w, http.StatusOK, result.Wallet)
		return
	}
	w.Header().Set("Location", "/users/"+id.String()+"/wallet")
	writeJSON(w, http.StatusCreated, result.Wallet)
}

func (a *api) getWallet(w http.ResponseWriter, r *http.Request) {
	id, ok := userID(w, r)
	if !ok {
		return
	}
	wallet, err := a.GetWallet.Handle(r.Context(), id)
	if err != nil {
		a.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, wallet)
}

func (a *api) listTransactions(w http.ResponseWriter, r *http.Request) {
	id, ok := userID(w, r)
	if !ok {
		return
	}
	transactions, err := a.ListTransactions.Handle(r.Context(), id)
	if err != nil {
		a.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, transactions)
}
