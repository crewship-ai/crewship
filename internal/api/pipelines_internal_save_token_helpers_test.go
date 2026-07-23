package api

import "time"

// internalSaveTestSecret is the fixed HMAC secret test handlers wire via
// SetSaveTokenSecret so InternalSave's save_token gate (mirroring the user
// path, #1371) can be satisfied deterministically. 32 bytes, arbitrary.
var internalSaveTestSecret = []byte("internal-save-test-secret-32byte!")

// internalSaveTokenFor mints a save_token exactly as InternalTestRun would for
// the unattended path: bound to (workspace, definition bytes, crew principal).
// Tests that drive InternalSave directly (without the two-step sidecar flow)
// use this to prove a dry-run happened for THIS definition.
func internalSaveTokenFor(wsID, crewID, def string) string {
	return signSaveToken(internalSaveTestSecret, wsID, definitionHashHex([]byte(def)), internalSavePrincipal(crewID), time.Now())
}
