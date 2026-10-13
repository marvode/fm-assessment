package api

import (
	"encoding/json"
	"net/http"

	"wallet/internal/ledger/domain"
)

const maxBodyBytes = 1 << 20

var errBadBody = domain.Validation("Body", "The request body is not valid JSON for this endpoint.")

// decode reads exactly one JSON value of at most 1 MiB into dst.
func decode(w http.ResponseWriter, r *http.Request, dst any) error {
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBodyBytes))
	if err := decoder.Decode(dst); err != nil {
		return errBadBody
	}
	if decoder.More() {
		return errBadBody
	}
	return nil
}

// required unwraps a JSON field that must be present and non-null, like a .NET `required` property.
func required[T any](value *T, field string) (T, error) {
	if value == nil {
		var zero T
		return zero, domain.Validation(field, field+" is required.")
	}
	return *value, nil
}
