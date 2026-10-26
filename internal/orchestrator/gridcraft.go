package orchestrator

// Enabling tools changes sidecar reuse identity, but must never turn an absent
// keyed credential fingerprint into a nonempty, unkeyed route binding.
func gridcraftSidecarFingerprint(key string, credentials []Credential, enabled bool) string {
	fingerprint := sidecarConfigFingerprint(key, credentials)
	if enabled && fingerprint != "" {
		fingerprint += ":gridcraft-v1"
	}
	return fingerprint
}
