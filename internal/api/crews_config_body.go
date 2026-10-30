package api

import (
	"net/http"
)

// A native mise lock is JSON embedded inside the JSON request. Give its
// bounded, twice-escaped envelope room without increasing every API's budget.
// An oversized body is errBodyTooLarge (413 via replyJSONBodyError).
func readCrewConfigJSON(r *http.Request, value any) error {
	const limit = 8 << 20
	return readJSONBounded(r, value, limit)
}
