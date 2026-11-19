package internaltoken

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
)

// AgentInboxGrantHeader carries an independent host-issued proof. It never
// changes the canonical run bearer that other tools may bind by exact digest.
const AgentInboxGrantHeader = "X-Crewship-Inbox-Grant"

// DeriveAgentInboxGrant signs the exact run bearer with the crew's run key.
func DeriveAgentInboxGrant(runKey, token string) string {
	if runKey == "" || token == "" {
		return ""
	}
	mac := hmac.New(sha256.New, []byte(runKey))
	mac.Write([]byte("crewship agent human inbox discovery v1\x00"))
	mac.Write([]byte(token))
	return hex.EncodeToString(mac.Sum(nil))
}

// ValidateAgentInboxGrant verifies a discovery grant. The caller must also
// check that its run bearer belongs to a live run in the current crew.
func ValidateAgentInboxGrant(runKey, token, grant string) bool {
	expected := DeriveAgentInboxGrant(runKey, token)
	return expected != "" && hmac.Equal([]byte(expected), []byte(grant))
}
