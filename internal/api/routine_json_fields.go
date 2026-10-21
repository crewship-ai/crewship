package api

import (
	"encoding/json"
	"strings"
)

// encoding/json uses Unicode folding for struct field matches. Removing only
// exact map keys would leave aliases that take precedence on a later decode.
func stripRoutineJSONFields(fields map[string]json.RawMessage, reserved ...string) {
	for key := range fields {
		for _, name := range reserved {
			if strings.EqualFold(key, name) {
				delete(fields, key)
				break
			}
		}
	}
}
