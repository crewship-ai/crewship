package orchestrator

// parseCodexStreamJSON parses one stdout line from `codex --quiet`.
//
// Stub: codexAdapter.UseStreamJSON() returns false today, so this function is
// not invoked by streamOutput in production. The codex Rust port's event
// schema (post-Apr-2025 rewrite) is JSONL but the discriminator field set has
// not yet been pinned in our fixtures — see parser_codex_test.go (to be added
// alongside the first captured fixture).
func parseCodexStreamJSON(line []byte, handler EventHandler) {
	// Intentionally empty until the schema is pinned.
}
