package orchestrator

// parseGeminiStreamJSON parses one stdout line from `gemini -p --output-format
// stream-json`.
//
// Stub: geminiAdapter.UseStreamJSON() returns false today, so this function is
// not invoked by streamOutput in production. The schema introduced by
// google-gemini/gemini-cli PR #10883 emits init/message/tool/result events;
// concrete struct shape will be filled in once a fixture is captured from a
// real run.
func parseGeminiStreamJSON(line []byte, handler EventHandler) {
	// Intentionally empty until the schema is pinned.
}
