package domain

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
)

// RequestHash is a stable fingerprint of a request's meaningful fields, stored with the transaction for idempotency
// checks.
func RequestHash(parts ...any) string {
	texts := make([]string, len(parts))
	for i, part := range parts {
		texts[i] = fmt.Sprint(part)
	}
	sum := sha256.Sum256([]byte(strings.Join(texts, "|")))
	return strings.ToUpper(hex.EncodeToString(sum[:]))
}
