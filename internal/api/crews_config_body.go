package api

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
)

// A native mise lock is JSON embedded inside the JSON request. Give its
// bounded, twice-escaped envelope room without increasing every API's budget.
func readCrewConfigJSON(r *http.Request, value any) error {
	const limit = 8 << 20
	body, err := io.ReadAll(io.LimitReader(r.Body, limit+1))
	if err != nil {
		return err
	}
	if len(body) > limit {
		return fmt.Errorf("crew configuration body exceeds 8 MiB")
	}
	return json.Unmarshal(body, value)
}
