package api_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"wallet/internal/api"
	"wallet/internal/common"
	"wallet/internal/ledger/ledgertest"
	"wallet/internal/ledger/providers"
	"wallet/internal/ledger/topups"
	"wallet/internal/ledger/wallets"
	"wallet/internal/ledger/webhooks"
	"wallet/internal/ledger/withdrawals"
)

type server struct {
	url      string
	provider *providers.FakePaymentProvider
}

func newServer(t *testing.T, development bool, checks ...api.HealthCheck) *server {
	t.Helper()
	s := ledgertest.NewStore(t)
	clock := common.SystemClock{}
	log := ledgertest.Logger()
	provider := providers.NewFakePaymentProvider()
	router := api.NewRouter(api.Deps{
		Log:               log,
		Development:       development,
		CreateWallet:      wallets.NewCreateHandler(s, clock),
		GetWallet:         wallets.NewGetHandler(s),
		ListTransactions:  wallets.NewListHandler(s),
		InitiateTopUp:     topups.NewInitiateHandler(s, provider, clock),
		RequestWithdrawal: withdrawals.NewRequestHandler(s, clock),
		Webhooks: webhooks.NewHandler(s, topups.NewCompleteHandler(s, clock, log),
			withdrawals.NewSettleHandler(s, clock), withdrawals.NewReleaseHandler(s, clock, log)),
		FakeProvider: provider,
		HealthChecks: checks,
	})
	ts := httptest.NewServer(router)
	t.Cleanup(ts.Close)
	return &server{url: ts.URL, provider: provider}
}

// do sends body as-is when it is a string, otherwise as JSON.
func (s *server) do(t *testing.T, method, path string, body any) *http.Response {
	t.Helper()
	var reader io.Reader
	switch b := body.(type) {
	case nil:
	case string:
		reader = strings.NewReader(b)
	default:
		encoded, err := json.Marshal(b)
		require.NoError(t, err)
		reader = strings.NewReader(string(encoded))
	}
	request, err := http.NewRequest(method, s.url+path, reader)
	require.NoError(t, err)
	request.Header.Set("Content-Type", "application/json")
	response, err := http.DefaultClient.Do(request)
	require.NoError(t, err)
	t.Cleanup(func() { _ = response.Body.Close() })
	return response
}

func read[T any](t *testing.T, response *http.Response) T {
	t.Helper()
	var value T
	require.NoError(t, json.NewDecoder(response.Body).Decode(&value))
	return value
}

func title(t *testing.T, response *http.Response) string {
	t.Helper()
	require.Equal(t, "application/problem+json", response.Header.Get("Content-Type"))
	return read[struct{ Title string }](t, response).Title
}

func withdrawal(referenceID string, amount int64) map[string]any {
	return map[string]any{"referenceId": referenceID, "amount": amount, "bankCode": "058",
		"accountNumber": "0123456789", "accountName": "Ada Lovelace"}
}

func (s *server) fund(t *testing.T, userID uuid.UUID, amount int64) {
	t.Helper()
	started := s.do(t, http.MethodPost, "/users/"+userID.String()+"/top-ups",
		map[string]any{"referenceId": "topup-" + uuid.NewString(), "amount": amount})
	require.Equal(t, http.StatusCreated, started.StatusCode)
	topUp := read[wallets.TransactionResponse](t, started)
	confirmed := s.do(t, http.MethodPost, "/webhooks/provider",
		map[string]any{"event": "charge.success", "providerReferenceId": *topUp.ProviderReferenceID, "amount": amount})
	require.Equal(t, http.StatusOK, confirmed.StatusCode)
}

func TestCreatingAWalletReturns201Then200(t *testing.T) {
	s, userID := newServer(t, false), uuid.New()

	created := s.do(t, http.MethodPost, "/users/"+userID.String()+"/wallet", nil)
	existing := s.do(t, http.MethodPost, "/users/"+userID.String()+"/wallet", nil)

	require.Equal(t, http.StatusCreated, created.StatusCode)
	require.Equal(t, "/users/"+userID.String()+"/wallet", created.Header.Get("Location"))
	require.Equal(t, http.StatusOK, existing.StatusCode)
	require.Equal(t, "NGN", read[wallets.WalletResponse](t, existing).Currency)
}

func TestAnUnknownWalletReturnsA404Problem(t *testing.T) {
	response := newServer(t, false).do(t, http.MethodGet, "/users/"+uuid.NewString()+"/wallet", nil)

	require.Equal(t, http.StatusNotFound, response.StatusCode)
	require.Equal(t, "Wallet.NotFound", title(t, response))
}

func TestATopUpConfirmedByWebhookCreditsTheWallet(t *testing.T) {
	s, userID := newServer(t, false), uuid.New()

	s.fund(t, userID, 5_000)

	wallet := read[wallets.WalletResponse](t, s.do(t, http.MethodGet, "/users/"+userID.String()+"/wallet", nil))
	require.EqualValues(t, 5_000, wallet.Balance)
	require.EqualValues(t, 5_000, wallet.AvailableBalance)
	history := read[[]wallets.TransactionResponse](t, s.do(t, http.MethodGet, "/users/"+userID.String()+"/wallet/transactions", nil))
	require.Len(t, history, 1)
	require.Equal(t, "TopUp", history[0].Type)
	require.Equal(t, "Succeeded", history[0].Status)
}

func TestAWithdrawalReturns202AndAnIdenticalRetryReturns200(t *testing.T) {
	s, userID := newServer(t, false), uuid.New()
	s.fund(t, userID, 10_000)

	first := s.do(t, http.MethodPost, "/users/"+userID.String()+"/withdrawals", withdrawal("wd-retry", 4_000))
	retry := s.do(t, http.MethodPost, "/users/"+userID.String()+"/withdrawals", withdrawal("wd-retry", 4_000))

	require.Equal(t, http.StatusAccepted, first.StatusCode)
	require.Equal(t, http.StatusOK, retry.StatusCode)
	require.Equal(t, read[wallets.TransactionResponse](t, first).ID, read[wallets.TransactionResponse](t, retry).ID)
	wallet := read[wallets.WalletResponse](t, s.do(t, http.MethodGet, "/users/"+userID.String()+"/wallet", nil))
	require.EqualValues(t, 6_000, wallet.AvailableBalance)
}

func TestAWithdrawalBeyondTheAvailableBalanceReturns422(t *testing.T) {
	s, userID := newServer(t, false), uuid.New()
	s.fund(t, userID, 1_000)

	response := s.do(t, http.MethodPost, "/users/"+userID.String()+"/withdrawals", withdrawal("wd-big", 1_001))

	require.Equal(t, http.StatusUnprocessableEntity, response.StatusCode)
	require.Equal(t, "Wallet.InsufficientFunds", title(t, response))
}

func TestReusingAReferenceWithADifferentBodyReturns409(t *testing.T) {
	s, userID := newServer(t, false), uuid.New()
	s.fund(t, userID, 10_000)
	s.do(t, http.MethodPost, "/users/"+userID.String()+"/withdrawals", withdrawal("wd-reused", 1_000))

	response := s.do(t, http.MethodPost, "/users/"+userID.String()+"/withdrawals", withdrawal("wd-reused", 2_000))

	require.Equal(t, http.StatusConflict, response.StatusCode)
	require.Equal(t, "Transaction.DuplicateReference", title(t, response))
}

func TestANonPositiveAmountReturns400(t *testing.T) {
	response := newServer(t, false).do(t, http.MethodPost, "/users/"+uuid.NewString()+"/top-ups",
		map[string]any{"referenceId": "zero", "amount": 0})

	require.Equal(t, http.StatusBadRequest, response.StatusCode)
	require.Equal(t, "Money.InvalidAmount", title(t, response))
}

func TestAnUnknownWebhookEventReturns400(t *testing.T) {
	response := newServer(t, false).do(t, http.MethodPost, "/webhooks/provider",
		map[string]any{"event": "refund.created", "providerReferenceId": "x", "amount": 1})

	require.Equal(t, http.StatusBadRequest, response.StatusCode)
	require.Equal(t, "Validation.Event", title(t, response))
}

func TestTheDevProviderEndpointIsHiddenOutsideDevelopment(t *testing.T) {
	response := newServer(t, false).do(t, http.MethodPost, "/dev/provider/chg_anything/complete?outcome=success", nil)

	require.Equal(t, http.StatusNotFound, response.StatusCode)
}

func TestTheDevProviderEndpointCompletesATopUpInDevelopment(t *testing.T) {
	s, userID := newServer(t, true), uuid.New()
	started := read[wallets.TransactionResponse](t, s.do(t, http.MethodPost, "/users/"+userID.String()+"/top-ups",
		map[string]any{"referenceId": "tu-1", "amount": 500}))
	path := "/dev/provider/" + *started.ProviderReferenceID + "/complete"

	bad := s.do(t, http.MethodPost, path+"?outcome=maybe", nil)
	unknown := s.do(t, http.MethodPost, "/dev/provider/chg_nope/complete?outcome=success", nil)
	completed := s.do(t, http.MethodPost, path+"?outcome=success", nil)

	require.Equal(t, http.StatusBadRequest, bad.StatusCode)
	require.Equal(t, "Validation.Outcome", title(t, bad))
	require.Equal(t, http.StatusNotFound, unknown.StatusCode)
	require.Equal(t, http.StatusOK, completed.StatusCode)
	require.Equal(t, "Succeeded", read[wallets.TransactionResponse](t, completed).Status)
}

func TestAliveAndHealthAreServedInEveryEnvironment(t *testing.T) {
	healthy := newServer(t, false, func(context.Context) error { return nil })
	unhealthy := newServer(t, false, func(context.Context) error { return errors.New("rabbitmq connection is closed") })

	alive := healthy.do(t, http.MethodGet, "/alive", nil)
	ok := healthy.do(t, http.MethodGet, "/health", nil)
	down := unhealthy.do(t, http.MethodGet, "/health", nil)

	require.Equal(t, http.StatusOK, alive.StatusCode)
	require.Equal(t, http.StatusOK, ok.StatusCode)
	okBody, _ := io.ReadAll(ok.Body)
	require.Equal(t, "Healthy", string(okBody))
	require.Equal(t, http.StatusServiceUnavailable, down.StatusCode)
	downBody, _ := io.ReadAll(down.Body)
	require.Equal(t, "Unhealthy", string(downBody))
}

func TestThereIsNoRootRouteAndNonUUIDUsersAreNotFound(t *testing.T) {
	s := newServer(t, false)

	require.Equal(t, http.StatusNotFound, s.do(t, http.MethodGet, "/", nil).StatusCode)
	require.Equal(t, http.StatusNotFound, s.do(t, http.MethodGet, "/users/not-a-guid/wallet", nil).StatusCode)
}

func TestBadBodiesAreValidationProblems(t *testing.T) {
	s, userID := newServer(t, false), uuid.New()
	topUps, withdrawals := "/users/"+userID.String()+"/top-ups", "/users/"+userID.String()+"/withdrawals"
	cases := []struct{ path, body, title string }{
		{topUps, `{"referenceId":`, "Validation.Body"},
		{topUps, `{"referenceId":"x","amount":"lots"}`, "Validation.Body"},
		{topUps, `{"referenceId":"x","amount":5} trailing`, "Validation.Body"},
		{topUps, `{"amount":5}`, "Validation.ReferenceId"},
		{topUps, `{"referenceId":null,"amount":5}`, "Validation.ReferenceId"},
		{topUps, `{"referenceId":"x"}`, "Validation.Amount"},
		{topUps, `null`, "Validation.ReferenceId"},
		{topUps, ``, "Validation.Body"},
		{withdrawals, `{"referenceId":"w","amount":5,"accountNumber":"0123456789","accountName":"A"}`, "Validation.BankCode"},
		{"/webhooks/provider", `{"providerReferenceId":"x","amount":1}`, "Validation.Event"},
		{"/webhooks/provider", `{"event":"charge.success","amount":1}`, "Validation.ProviderReferenceId"},
		{topUps, `{"referenceId":"a\u0000b","amount":5}`, "Validation.ReferenceId"},
		{withdrawals, `{"referenceId":"w","amount":5,"bankCode":"058","accountNumber":"0123456789","accountName":"A\u0000"}`, "Validation.AccountName"},
		{"/webhooks/provider", `{"event":"charge.success","providerReferenceId":"a\u0000","amount":1}`, "Validation.ProviderReferenceId"},
		{"/webhooks/provider", `{"event":"transfer.success","providerReferenceId":"a\u0000","amount":1}`, "Validation.ProviderReferenceId"},
	}
	for _, c := range cases {
		response := s.do(t, http.MethodPost, c.path, c.body)

		require.Equal(t, http.StatusBadRequest, response.StatusCode, c.body)
		require.Equal(t, c.title, title(t, response), c.body)
	}
}

func TestAnOversizedBodyIsRejected(t *testing.T) {
	s := newServer(t, false)
	// Just over the 1 MiB limit: the server drains the small remainder instead of resetting the connection mid-upload.
	body := `{"referenceId":"` + strings.Repeat("x", 1<<20) + `","amount":5}`

	response := s.do(t, http.MethodPost, "/users/"+uuid.NewString()+"/top-ups", body)

	require.Equal(t, http.StatusBadRequest, response.StatusCode)
	require.Equal(t, "Validation.Body", title(t, response))
}
