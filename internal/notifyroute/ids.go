package notifyroute

import (
	"crypto/rand"
	"encoding/hex"
	"time"
)

// generateID mirrors internal/notify's channel-id shape (short random hex
// with a prefix) for this package's own rows (prefs, deliveries).
func generateID(prefix string) string {
	return prefix + "_" + randomHex(8)
}

// randomHex returns n bytes of random entropy, hex-encoded. Falls back to
// a timestamp-derived value in the astronomically unlikely case
// crypto/rand fails, matching internal/notify's generateChannelID
// fallback so an entropy-source outage degrades to "still unique," not
// "crash."
func randomHex(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return hex.EncodeToString([]byte(time.Now().UTC().Format(time.RFC3339Nano)))
	}
	return hex.EncodeToString(b)
}
