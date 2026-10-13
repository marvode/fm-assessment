// Package domain is the wallet's ledger model. It does no I/O: handlers load entities through store.Tx, call these
// methods, and store.Run persists the result.
package domain

import (
	"strings"
	"unicode/utf8"

	"github.com/google/uuid"
)

// Currency is the only currency the wallet handles. Amounts are integer kobo.
const Currency = "NGN"

type AccountType string

const (
	AccountUser   AccountType = "User"
	AccountSystem AccountType = "System"
)

type TransactionType string

const (
	TopUp      TransactionType = "TopUp"
	Withdrawal TransactionType = "Withdrawal"
)

type TransactionStatus string

const (
	Pending   TransactionStatus = "Pending"
	Succeeded TransactionStatus = "Succeeded"
	Failed    TransactionStatus = "Failed"
)

// Direction: Credit raises an account's balance, Debit lowers it, for every account type.
type Direction string

const (
	Debit  Direction = "Debit"
	Credit Direction = "Credit"
)

// Column sizes, shared by validation and the schema so oversized input is a 400, never a database error.
const (
	ReferenceIDMaxLength = 128
	BankCodeMaxLength    = 16
	AccountNumberLength  = 10
	AccountNameMaxLength = 128
)

// ProviderSettlementID is the seeded system account that mirrors money held at the payment provider.
var ProviderSettlementID = uuid.MustParse("00000000-0000-0000-0000-000000000001")

const ProviderSettlementName = "ProviderSettlement"

// BlankOrLonger reports whether s is empty, whitespace only, or longer than max characters.
func BlankOrLonger(s string, max int) bool {
	return strings.TrimSpace(s) == "" || utf8.RuneCountInString(s) > max
}

func newID() uuid.UUID { return uuid.Must(uuid.NewV7()) }
