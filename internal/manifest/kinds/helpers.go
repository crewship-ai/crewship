package kinds

// Shared helpers used by multiple kind implementations in this package.
// Per-kind agents prefix their private helpers with the kind name to
// avoid collisions, but a small set of utilities is genuinely shared —
// they live here so we have one definition instead of fourteen.

import (
	"fmt"
	"io"

	"github.com/crewship-ai/crewship/internal/manifest/internalapi"
)

// checkStatus returns nil when the response is a 2xx; otherwise it
// reads up to 4 KiB of the body (to keep error messages bounded) and
// wraps it in an error prefixed with the caller-supplied context
// string. The context string lets reads like "list projects" and
// "deploy crew_template X" produce diagnostically useful errors
// without each call site re-spelling the operation in fmt.Errorf.
func checkStatus(resp *internalapi.Response, contextMsg string) error {
	if resp == nil {
		return fmt.Errorf("%s: response is nil", contextMsg)
	}
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return nil
	}
	var snippet string
	if resp.Body != nil {
		limited := io.LimitReader(resp.Body, 4096)
		if data, err := io.ReadAll(limited); err == nil && len(data) > 0 {
			snippet = ": " + string(data)
		}
	}
	return fmt.Errorf("%s: unexpected status %d%s", contextMsg, resp.StatusCode, snippet)
}

// readAll consumes an internalapi.Response body and returns the bytes.
// Used by kinds that need the full body for JSON decode. Returns
// (nil, nil) for a nil body so callers can no-op gracefully.
func readAll(r io.Reader) ([]byte, error) {
	if r == nil {
		return nil, nil
	}
	return io.ReadAll(r)
}

// deref returns the pointee or "" for nil. Used heavily in the
// update-patch paths where remote-side fields are pointers (so we
// can distinguish "no value" from "empty value") but the diff only
// cares about the string form. Shared across kinds — the agents,
// issues and milestones REST APIs all return sql.NullString-style
// pointers for nullable text columns, and the manifest treats absent
// and "" as equivalent for diffing.
func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
