#!/usr/bin/env bash
# End-to-end test of Wallet.Api against real Postgres and RabbitMQ.
#
# Starts throwaway containers, builds and runs the API in Development (which applies migrations and enables
# /dev/provider), exercises every endpoint and background flow, checks the ledger invariants in the database,
# then tears everything down. Exits non-zero if any check fails.
#
# Usage: dotnet/scripts/e2e.sh            (KEEP=1 leaves the containers running afterwards)
# Needs: docker, dotnet, curl, jq, uuidgen
#
# The sweeper runs every 5s with a 20s pending threshold so reconciliation can be observed in seconds.
# Production uses the WalletOptions defaults (5 minute interval, 15 minute threshold).
set -u
cd "$(dirname "$0")/.."

PORT=${PORT:-5590}; PG_PORT=${PG_PORT:-55432}; RMQ_PORT=${RMQ_PORT:-55672}
BASE=http://localhost:$PORT
PG=wallet-e2e-pg; RMQ=wallet-e2e-rmq
LOG=$(mktemp -t wallet-e2e-api.XXXXXX.log)
PASS=0; FAIL=0; API_PID=

for tool in docker dotnet curl jq uuidgen; do
  command -v "$tool" >/dev/null || { echo "missing required tool: $tool" >&2; exit 2; }
done

stop_api() {
  [ -n "$API_PID" ] || return 0
  kill "$API_PID" 2>/dev/null; wait "$API_PID" 2>/dev/null; API_PID=
}
cleanup() {
  stop_api
  if [ "${KEEP:-0}" != 1 ]; then docker rm -f "$PG" "$RMQ" >/dev/null 2>&1; fi
  echo "API log: $LOG"
}
trap cleanup EXIT

start_api() {
  ASPNETCORE_ENVIRONMENT=Development ASPNETCORE_URLS=$BASE \
  ConnectionStrings__appdb="Host=localhost;Port=$PG_PORT;Database=appdb;Username=postgres;Password=e2e" \
  ConnectionStrings__messaging="amqp://guest:guest@localhost:$RMQ_PORT" \
  Wallet__SweeperInterval=00:00:05 Wallet__PendingThreshold=00:00:20 \
    dotnet src/Wallet.Api/bin/Debug/net10.0/Wallet.Api.dll --contentRoot "$PWD/src/Wallet.Api" >>"$LOG" 2>&1 &
  API_PID=$!
  wait_for "curl -sf $BASE/health >/dev/null && echo up" up 60 || { echo "API did not become healthy; see $LOG" >&2; exit 1; }
}
wait_rmq() { wait_for "docker exec $RMQ rabbitmq-diagnostics -q check_port_connectivity >/dev/null 2>&1 && echo up" up 120; }

psql_q() { docker exec "$PG" psql -U postgres -d appdb -tAc "$1"; }

# req METHOD PATH [JSON] -> sets CODE and BODY
req() {
  local out
  if [ $# -ge 3 ]; then out=$(curl -s -X "$1" "$BASE$2" -H 'content-type: application/json' -d "$3" -w '\n%{http_code}')
  else out=$(curl -s -X "$1" "$BASE$2" -w '\n%{http_code}'); fi
  CODE=$(tail -n1 <<<"$out"); BODY=$(sed '$d' <<<"$out")
}
check() { # name expected actual
  if [ "$2" == "$3" ]; then PASS=$((PASS+1)); echo "  PASS $1"; else FAIL=$((FAIL+1)); echo "  FAIL $1 (expected '$2', got '$3')"; echo "       body: $BODY"; fi
}
j() { jq -r "$1" <<<"$BODY"; }
wallet() { req GET "/users/$1/wallet"; echo "$(j .balance)/$(j .availableBalance)"; }
wait_for() { # cmd expected timeout-seconds
  local i; for i in $(seq "$3"); do [ "$(eval "$1")" == "$2" ] && return 0; sleep 1; done; return 1; }

echo "== Setup"
docker rm -f "$PG" "$RMQ" >/dev/null 2>&1
docker run -d --name "$PG" -e POSTGRES_PASSWORD=e2e -p "$PG_PORT:5432" postgres:17 >/dev/null || exit 1
docker run -d --name "$RMQ" -p "$RMQ_PORT:5672" rabbitmq:4-management >/dev/null || exit 1
dotnet build src/Wallet.Api -v q -nologo >/dev/null || { echo "build failed" >&2; exit 1; }
wait_for "docker exec $PG pg_isready -U postgres >/dev/null 2>&1 && echo up" up 60 || exit 1
wait_rmq || exit 1
start_api
echo "  API running on $BASE (log: $LOG)"

U=$(uuidgen); U2=$(uuidgen); NOWALLET=$(uuidgen)
echo "== Infrastructure"
req GET /health; check "health endpoint" 200 "$CODE"
req GET /openapi/v1.json; check "OpenAPI document" 200 "$CODE"
req GET /scalar/v1; check "Scalar UI" 200 "$CODE"

echo "== Wallet creation"
req POST "/users/$U/wallet"; check "create wallet -> 201" 201 "$CODE"
check "new wallet zero balance NGN" "0/0/NGN" "$(j .balance)/$(j .availableBalance)/$(j .currency)"
req POST "/users/$U/wallet"; check "create again is idempotent -> 200" 200 "$CODE"
req GET "/users/$U/wallet"; check "get wallet -> 200" 200 "$CODE"
req GET "/users/$NOWALLET/wallet"; check "get missing wallet -> 404" 404 "$CODE"; check "error code" Wallet.NotFound "$(j .title)"
req GET "/users/not-a-guid/wallet"; check "non-guid user id -> 404" 404 "$CODE"
req GET "/users/$NOWALLET/wallet/transactions"; check "transactions of missing wallet -> 404" 404 "$CODE"
req POST "/users/$U2/wallet"; check "second wallet -> 201" 201 "$CODE"

echo "== Top-up: success path"
req POST "/users/$U/top-ups" '{"referenceId":"tu-1","amount":500000}'
check "initiate top-up -> 201" 201 "$CODE"; check "top-up pending" Pending "$(j .status)"
TU1_ID=$(j .id); TU1_PRV=$(j .providerReferenceId)
check "provider reference assigned" true "$([[ $TU1_PRV == chg_* ]] && echo true)"
check "balance unchanged while pending" "0/0" "$(wallet "$U")"
req POST "/users/$U/top-ups" '{"referenceId":"tu-1","amount":500000}'
check "replayed top-up -> 200" 200 "$CODE"; check "replay returns same transaction" "$TU1_ID" "$(j .id)"
req POST "/users/$U/top-ups" '{"referenceId":"tu-1","amount":999}'
check "same ref, different body -> 409" 409 "$CODE"; check "error code" Transaction.DuplicateReference "$(j .title)"
req POST "/dev/provider/$TU1_PRV/complete?outcome=success"
check "dev provider charge success -> 200" 200 "$CODE"; check "top-up succeeded" Succeeded "$(j .status)"
check "balance credited" "500000/500000" "$(wallet "$U")"
req POST "/dev/provider/$TU1_PRV/complete?outcome=success"
check "duplicate success webhook -> 200" 200 "$CODE"
check "no double credit" "500000/500000" "$(wallet "$U")"

echo "== Top-up: failure path and webhook endpoint"
req POST "/users/$U/top-ups" '{"referenceId":"tu-2","amount":1000}'; TU2_PRV=$(j .providerReferenceId)
req POST /webhooks/provider "{\"event\":\"charge.failed\",\"providerReferenceId\":\"$TU2_PRV\",\"amount\":1000}"
check "charge.failed webhook -> 200" 200 "$CODE"; check "top-up failed" Failed "$(j .status)"
check "balance unchanged after failed top-up" "500000/500000" "$(wallet "$U")"
req POST /webhooks/provider "{\"event\":\"charge.success\",\"providerReferenceId\":\"$TU2_PRV\",\"amount\":1000}"
check "success after failure does not credit" "500000/500000" "$(wallet "$U")"
check "late charge.success leaves it Failed" Failed "$(j .status)"
req POST "/users/$U/top-ups" '{"referenceId":"tu-3","amount":2500}'; TU3_PRV=$(j .providerReferenceId)
req POST /webhooks/provider "{\"event\":\"charge.success\",\"providerReferenceId\":\"$TU3_PRV\",\"amount\":2500}"
check "charge.success via public webhook -> 200" 200 "$CODE"
check "balance credited via webhook" "502500/502500" "$(wallet "$U")"
req POST "/users/$U/top-ups" '{"referenceId":"tu-4","amount":4000}'; TU4_PRV=$(j .providerReferenceId)
req POST /webhooks/provider "{\"event\":\"charge.success\",\"providerReferenceId\":\"$TU4_PRV\",\"amount\":1}"
check "amount mismatch marks top-up Failed" Failed "$(j .status)"
check "mismatched amount not credited as 4000/1" "502500/502500" "$(wallet "$U")"
req POST /webhooks/provider '{"event":"charge.success","providerReferenceId":"chg_unknown","amount":1}'
check "webhook for unknown charge -> 404" 404 "$CODE"
req POST /webhooks/provider '{"event":"bogus.event","providerReferenceId":"x","amount":1}'
check "unknown webhook event -> 400" 400 "$CODE"; check "error code" Validation.Event "$(j .title)"

echo "== Top-up validation"
req POST "/users/$U/top-ups" '{"referenceId":"tu-zero","amount":0}'; check "zero amount -> 400" 400 "$CODE"
req POST "/users/$U/top-ups" '{"referenceId":"tu-neg","amount":-5}'; check "negative amount -> 400" 400 "$CODE"
req POST "/users/$U/top-ups" '{"referenceId":"","amount":5}'; check "empty referenceId -> 400" 400 "$CODE"
req POST "/users/$U/top-ups" "{\"referenceId\":\"$(printf 'x%.0s' {1..129})\",\"amount\":5}"; check "129-char referenceId -> 400" 400 "$CODE"
req POST "/users/$U/top-ups" '{"amount":5}'; check "missing referenceId -> 400" 400 "$CODE"
req POST "/users/$U/top-ups" '{"referenceId":"x","amount":"lots"}'; check "non-numeric amount -> 400" 400 "$CODE"
req POST "/users/$NOWALLET/top-ups" '{"referenceId":"tu-x","amount":5}'; check "top-up lazily creates wallet -> 201" 201 "$CODE"
req GET "/users/$NOWALLET/wallet"; check "lazily created wallet exists" 200 "$CODE"
NOWALLET=$(uuidgen)

echo "== Withdrawal: success path (outbox -> RabbitMQ -> payout worker -> webhook)"
WD='{"referenceId":"wd-1","amount":100000,"bankCode":"058","accountNumber":"0123456789","accountName":"Ada Lovelace"}'
req POST "/users/$U/withdrawals" "$WD"
check "request withdrawal -> 202" 202 "$CODE"; check "withdrawal pending" Pending "$(j .status)"; WD1_ID=$(j .id)
check "funds held (balance kept, available reduced)" "502500/402500" "$(wallet "$U")"
req POST "/users/$U/withdrawals" "$WD"; check "replayed withdrawal -> 200" 200 "$CODE"; check "same transaction" "$WD1_ID" "$(j .id)"
check "replay does not hold twice" "502500/402500" "$(wallet "$U")"
prv_of() { psql_q "select coalesce(\"ProviderReferenceId\",'') from \"Transactions\" where \"Id\"='$1'" 2>/dev/null; }
wait_for "[[ \$(prv_of $WD1_ID) == trf_* ]] && echo yes" yes 20
WD1_PRV=$(prv_of "$WD1_ID"); check "payout worker sent payout to provider" true "$([[ $WD1_PRV == trf_* ]] && echo true)"
check "outbox drained" 0 "$(psql_q "select count(*) from \"OutboxMessages\" where \"ProcessedAt\" is null" 2>/dev/null)"
req POST "/dev/provider/$WD1_PRV/complete?outcome=success"
check "transfer success -> Succeeded" Succeeded "$(j .status)"
check "hold settled (balance debited)" "402500/402500" "$(wallet "$U")"
req POST "/dev/provider/$WD1_PRV/complete?outcome=failed"
check "late transfer.failed leaves it Succeeded" Succeeded "$(j .status)"
check "late failure does not refund a settled payout" "402500/402500" "$(wallet "$U")"

echo "== Withdrawal: failure path releases hold"
req POST "/users/$U/withdrawals" '{"referenceId":"wd-2","amount":2500,"bankCode":"058","accountNumber":"0123456789","accountName":"Ada"}'; WD2_ID=$(j .id)
check "held" "402500/400000" "$(wallet "$U")"
wait_for "[[ \$(prv_of $WD2_ID) == trf_* ]] && echo yes" yes 20; WD2_PRV=$(prv_of "$WD2_ID")
req POST /webhooks/provider "{\"event\":\"transfer.failed\",\"providerReferenceId\":\"$WD2_PRV\",\"reference\":\"${WD2_ID//-/}\",\"amount\":2500}"
check "transfer.failed webhook -> Failed" Failed "$(j .status)"
check "hold released" "402500/402500" "$(wallet "$U")"
req POST /webhooks/provider "{\"event\":\"transfer.failed\",\"providerReferenceId\":\"$WD2_PRV\",\"amount\":2500}"
check "duplicate transfer.failed does not double release" "402500/402500" "$(wallet "$U")"

echo "== Withdrawal: webhook matched by reference before provider ref stored"
req POST "/users/$U/withdrawals" '{"referenceId":"wd-3","amount":500,"bankCode":"058","accountNumber":"0123456789","accountName":"Ada"}'; WD3_ID=$(j .id)
req POST /webhooks/provider "{\"event\":\"transfer.success\",\"providerReferenceId\":\"trf_not_yet_stored\",\"reference\":\"${WD3_ID//-/}\",\"amount\":500}"
check "transfer.success by reference -> Succeeded" Succeeded "$(j .status)"
check "settled" "402000/402000" "$(wallet "$U")"

echo "== Withdrawal validation"
w() { req POST "/users/$U/withdrawals" "$1"; }
w '{"referenceId":"wd-big","amount":999999999,"bankCode":"058","accountNumber":"0123456789","accountName":"Ada"}'
check "insufficient funds -> 422" 422 "$CODE"; check "error code" Wallet.InsufficientFunds "$(j .title)"
w '{"referenceId":"wd-v1","amount":0,"bankCode":"058","accountNumber":"0123456789","accountName":"Ada"}'; check "zero amount -> 400" 400 "$CODE"
w '{"referenceId":"wd-v2","amount":10,"bankCode":"058","accountNumber":"123","accountName":"Ada"}'; check "short account number -> 400" 400 "$CODE"
w '{"referenceId":"wd-v3","amount":10,"bankCode":"058","accountNumber":"01234abcde","accountName":"Ada"}'; check "non-digit account number -> 400" 400 "$CODE"
w '{"referenceId":"wd-v4","amount":10,"bankCode":"","accountNumber":"0123456789","accountName":"Ada"}'; check "empty bank code -> 400" 400 "$CODE"
w '{"referenceId":"wd-v5","amount":10,"bankCode":"058","accountNumber":"0123456789","accountName":""}'; check "empty account name -> 400" 400 "$CODE"
w '{"referenceId":"tu-1","amount":10,"bankCode":"058","accountNumber":"0123456789","accountName":"Ada"}'; check "referenceId reused from a top-up -> 409" 409 "$CODE"
req POST "/users/$NOWALLET/withdrawals" '{"referenceId":"wd-x","amount":10,"bankCode":"058","accountNumber":"0123456789","accountName":"Ada"}'; check "withdrawal without wallet -> 404" 404 "$CODE"
check "balance untouched by rejected requests" "402000/402000" "$(wallet "$U")"

echo "== Concurrency: parallel withdrawals cannot overspend"
req POST "/users/$U2/top-ups" '{"referenceId":"c-tu","amount":10000}'; C_PRV=$(j .providerReferenceId)
req POST "/dev/provider/$C_PRV/complete?outcome=success"
codes=$(pids=(); for i in $(seq 10); do curl -s -o /dev/null -w '%{http_code}\n' -X POST "$BASE/users/$U2/withdrawals" -H 'content-type: application/json' \
  -d "{\"referenceId\":\"c-wd-$i\",\"amount\":3000,\"bankCode\":\"058\",\"accountNumber\":\"0123456789\",\"accountName\":\"Ada\"}" & pids+=($!); done; wait "${pids[@]}")
accepted=$(grep -c '^202$' <<<"$codes"); echo "       statuses: $(sort <<<"$codes" | uniq -c | tr '\n' ' ')"
check "exactly 3 of 10 x 3000 accepted from 10000" 3 "$accepted"
check "available never negative" "10000/1000" "$(wallet "$U2")"
codes=$(pids=(); for i in $(seq 5); do curl -s -o /dev/null -w '%{http_code}\n' -X POST "$BASE/users/$U2/top-ups" -H 'content-type: application/json' -d '{"referenceId":"same-ref","amount":77}' & pids+=($!); done; wait "${pids[@]}")
echo "       parallel same-ref top-ups: $(sort <<<"$codes" | uniq -c | tr '\n' ' ')"
check "exactly one transaction for parallel same-ref" 1 "$(psql_q "select count(*) from \"Transactions\" where \"ReferenceId\"='same-ref' and \"AccountId\"='$(req GET /users/$U2/wallet; j .accountId)'" 2>/dev/null)"

echo "== Transactions listing"
req GET "/users/$U/wallet/transactions"; check "list -> 200" 200 "$CODE"
check "all 7 of user's transactions listed" 7 "$(j 'length')"
check "newest first" true "$(j '[.[].createdAt] == ([.[].createdAt] | sort | reverse)')"
check "no cross-user leakage" 0 "$(j '[.[] | select(.referenceId | startswith("c-"))] | length')"

echo "== Dev provider endpoint validation"
req POST "/dev/provider/$TU1_PRV/complete?outcome=maybe"; check "bad outcome -> 400" 400 "$CODE"
req POST "/dev/provider/chg_nope/complete?outcome=success"; check "unknown provider ref -> 404" 404 "$CODE"

echo "== Reconciliation sweeper (interval 5s, threshold 20s)"
SW_ID=$(psql_q 'select "Id" from "Transactions" where "ReferenceId" like '"'c-wd-%'"' and "Status"='"'Pending'"' order by "CreatedAt" limit 1')
echo "       waiting up to 60s for a sweep that sees the stale pending withdrawals..."
wait_for "grep -cE 'Payout sweep: 0 settled, 0 released, 0 re-queued, [1-9][0-9]* still pending' $LOG | grep -qv '^0$' && echo yes" yes 60
check "sweeper ran and kept provider-pending payouts held" yes "$(grep -cE 'Payout sweep: 0 settled, 0 released, 0 re-queued, [1-9][0-9]* still pending' $LOG | grep -qv '^0$' && echo yes)"
check "swept withdrawal remains Pending" Pending "$(psql_q "select \"Status\" from \"Transactions\" where \"Id\"='$SW_ID'")"
check "held funds still held after sweep" "10000/1000" "$(wallet "$U2")"

echo "== Ledger invariants (database)"
check "I1 every posted transaction is a balanced 2-leg entry" 0 "$(psql_q 'select count(*) from (select "TransactionId" from "LedgerEntries" group by "TransactionId" having count(*)<>2 or sum(case when "Direction"='"'Debit'"' then "Amount" else 0 end)<>sum(case when "Direction"='"'Credit'"' then "Amount" else 0 end)) x')"
check "I3 account balances equal entry nets" 0 "$(psql_q 'select count(*) from "Accounts" a where a."Balance" <> coalesce((select sum(case when "Direction"='"'Credit'"' then "Amount" else -"Amount" end) from "LedgerEntries" e where e."AccountId"=a."Id"),0)')"
check "I4 no negative/invalid wallets" 0 "$(psql_q 'select count(*) from "Accounts" where "Type"='"'User'"' and ("Balance"<0 or "AvailableBalance"<0 or "AvailableBalance">"Balance")')"
check "I6 all balances sum to zero" 0 "$(psql_q 'select sum("Balance") from "Accounts"')"
check "no invariant violations logged" 0 "$(grep -c 'invariant .* violated' $LOG)"
check "no unhandled exceptions logged" 0 "$(grep -c 'An unhandled exception' $LOG)"

echo "== RabbitMQ topology"
q=$(docker exec "$RMQ" rabbitmqctl -q list_queues name type messages 2>/dev/null)
check "wallet.payouts is a quorum queue" quorum "$(awk '$1=="wallet.payouts"{print $2}' <<<"$q")"
check "dead-letter queue exists" true "$(grep -q '^wallet.payouts.dead' <<<"$q" && echo true)"
check "no dead-lettered payouts" 0 "$(awk '$1=="wallet.payouts.dead"{print $3}' <<<"$q")"

echo "== Outbox survives a RabbitMQ outage"
OU=$(uuidgen)
req POST "/users/$OU/top-ups" '{"referenceId":"o-tu","amount":9000}'; req POST "/dev/provider/$(j .providerReferenceId)/complete?outcome=success"
docker stop "$RMQ" >/dev/null
req POST "/users/$OU/withdrawals" '{"referenceId":"o-wd","amount":4000,"bankCode":"058","accountNumber":"0123456789","accountName":"Ada"}'
check "withdrawal accepted while broker is down -> 202" 202 "$CODE"; O_ID=$(j .id)
check "funds held while broker is down" "9000/5000" "$(wallet "$OU")"
wait_for "curl -s $BASE/health" Unhealthy 30; check "health reports Unhealthy" Unhealthy "$(curl -s $BASE/health)"
check "payout request waits in the outbox" 1 "$(psql_q "select count(*) from \"OutboxMessages\" where \"ProcessedAt\" is null and \"Payload\"::text like '%$O_ID%'")"
docker start "$RMQ" >/dev/null; wait_rmq
wait_for "[[ \$(prv_of $O_ID) == trf_* ]] && echo yes" yes 90
check "payout delivered after broker recovers" true "$([[ $(prv_of "$O_ID") == trf_* ]] && echo true)"
check "outbox drained after recovery" 0 "$(psql_q 'select count(*) from "OutboxMessages" where "ProcessedAt" is null')"

echo "== Sweeper resubmits payouts the provider lost (API restart wipes the in-memory fake provider)"
stop_api; start_api
PENDING=$(psql_q "select count(*) from \"Transactions\" where \"Type\"='Withdrawal' and \"Status\"='Pending'")
echo "       $PENDING pending withdrawals the restarted provider has never seen; waiting up to 60s..."
wait_for "grep -cE 'Payout sweep: 0 settled, 0 released, [1-9][0-9]* re-queued' $LOG | grep -qv '^0$' && echo yes" yes 60
check "sweeper re-queued the lost payouts" yes "$(grep -cE 'Payout sweep: 0 settled, 0 released, [1-9][0-9]* re-queued' $LOG | grep -qv '^0$' && echo yes)"
wait_for "psql_q \"select count(*) from \\\"Transactions\\\" where \\\"Type\\\"='Withdrawal' and \\\"Status\\\"='Pending' and \\\"ProviderReferenceId\\\" is null\"" 0 30
check "every pending payout was resubmitted to the provider" 0 "$(psql_q "select count(*) from \"Transactions\" where \"Type\"='Withdrawal' and \"Status\"='Pending' and \"ProviderReferenceId\" is null")"
wait_for "[ \$(grep -c 'resubmitting it' $LOG) -ge $PENDING ] && echo yes" yes 30
check "every lost provider reference was logged" true "$([ "$(grep -c 'resubmitting it' $LOG)" -ge "$PENDING" ] && echo true)"
SWEEPS=$(grep -c 'Payout sweep' "$LOG"); wait_for "echo \$((\$(grep -c 'Payout sweep' $LOG) - SWEEPS))" 2 30
OUTBOX=$(psql_q 'select count(*) from "OutboxMessages"'); SWEEPS=$(grep -c 'Payout sweep' "$LOG")
wait_for "echo \$((\$(grep -c 'Payout sweep' $LOG) - SWEEPS))" 2 30
check "no further re-queues once resubmitted (outbox stable over 2 sweeps)" "$OUTBOX" "$(psql_q 'select count(*) from "OutboxMessages"')"
check "held funds still held" "10000/1000" "$(wallet "$U2")"
check "no invariant violations after restart" 0 "$(grep -c 'invariant .* violated' $LOG)"

echo; echo "RESULT: $PASS passed, $FAIL failed"
[ "$FAIL" -eq 0 ]
