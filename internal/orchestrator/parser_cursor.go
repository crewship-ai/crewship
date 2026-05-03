package orchestrator

// parseCursorStreamJSON parses one stdout line from `cursor-agent -p
// --output-format stream-json`. The Cursor NDJSON shape (system / delta /
// tool_call / result) is close enough to Claude Code's stream-json that the
// parser will likely share most of its struct definitions once wired.
//
// Stub: cursorAdapter.UseStreamJSON() returns false today, so this function is
// not invoked by streamOutput in production.
func parseCursorStreamJSON(line []byte, handler EventHandler) {
	// Intentionally empty until the schema is pinned.
}
