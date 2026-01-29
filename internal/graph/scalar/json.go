// Package scalar implements the custom GraphQL scalars.
package scalar

import (
	"encoding/json"
	"io"

	"github.com/99designs/gqlgen/graphql"
)

// MarshalJSON writes raw JSON straight into the response. Empty input is null.
func MarshalJSON(v json.RawMessage) graphql.Marshaler {
	if len(v) == 0 {
		return graphql.Null
	}
	return graphql.WriterFunc(func(w io.Writer) {
		w.Write(v)
	})
}

// UnmarshalJSON accepts any JSON value from a variable or an inline literal
// and keeps it as raw JSON.
func UnmarshalJSON(v any) (json.RawMessage, error) {
	if v == nil {
		return nil, nil
	}
	return json.Marshal(v)
}
