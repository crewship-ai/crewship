package sidecar

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
)

// sidecarMaxBodyBytes caps every JSON request body the sidecar routes decode.
// The sidecar runs as UID 1002 and is the egress/credential boundary for the
// container; a compromised agent that POSTs a multi-GB body to an uncapped
// json.NewDecoder(r.Body).Decode would buffer it and OOM the sidecar (#1046,
// #1058). 1 MiB is far above any legitimate control-plane payload.
const sidecarMaxBodyBytes = 1 << 20

// Field-length ceilings for the agent-controlled keeper fields that previously
// had no bound. credential_name is matched against a DB row; env_var becomes a
// container environment-variable name. Length + NUL parity with the existing
// intent/command guards (#1058).
const (
	maxCredentialNameLength = 256
	maxEnvVarLength         = 256
)

// decodeCappedJSON caps r.Body at sidecarMaxBodyBytes and decodes the JSON into
// dst. It replaces the bare json.NewDecoder(r.Body).Decode pattern across the
// sidecar routes. Returns true on success; on an oversized body it responds 413
// and returns false; on any other decode error it responds 400 ("invalid JSON
// body", matching the prior message) and returns false.
func decodeCappedJSON(w http.ResponseWriter, r *http.Request, dst any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, sidecarMaxBodyBytes)
	if err := json.NewDecoder(r.Body).Decode(dst); err != nil {
		var mbErr *http.MaxBytesError
		if errors.As(err, &mbErr) {
			writeJSONResponse(w, http.StatusRequestEntityTooLarge, map[string]string{"error": "request body too large"})
			return false
		}
		writeJSONResponse(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON body"})
		return false
	}
	return true
}

// rejectInvalidField writes a 400 and returns true when value exceeds maxLen or
// contains a NUL byte — the length + NUL guard the intent/command fields
// already use, extended to the equally agent-controlled credential_name and
// env_var fields (#1058). Empty values pass (presence is validated separately).
func rejectInvalidField(w http.ResponseWriter, value, fieldName string, maxLen int) bool {
	if len(value) > maxLen {
		writeJSONResponse(w, http.StatusBadRequest, map[string]string{"error": fieldName + " exceeds maximum allowed length"})
		return true
	}
	if strings.ContainsRune(value, 0) {
		writeJSONResponse(w, http.StatusBadRequest, map[string]string{"error": fieldName + " contains invalid characters"})
		return true
	}
	return false
}
