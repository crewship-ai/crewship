package orchestrator

// parseOpenCodeStreamJSON parses one stdout line from `opencode run
// --output-format json` (or stream variant once exposed).
//
// Stub: opencodeAdapter.UseStreamJSON() returns false today, so this function
// is not invoked by streamOutput in production. OpenCode's typed OutputFormat
// is documented but the JSONL discriminator set has not yet been pinned in our
// fixtures.
func parseOpenCodeStreamJSON(line []byte, handler EventHandler) {
	// Intentionally empty until the schema is pinned.
}
