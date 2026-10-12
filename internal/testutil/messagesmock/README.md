# Offline Messages fixture

`New(Config)` returns an `http.RoundTripper` with no network delegate. It accepts
only authenticated POSTs to the exact Anthropic Messages URL, with the configured
synthetic key/model and `anthropic-version: 2023-06-01`. Token counting is
supported only when `/v1/messages/count_tokens` is an explicit script step.

Each role has its own ordered script and stable marker in user-message text.
Concurrent roles do not consume each other's steps. Tool names must exactly match
the real advertised name, including MCP prefixes. The next request must contain
the issued assistant tool use with its original ID/name/arguments and the matching
successful, nonempty user tool result. `RequireResult` checks an optional nonce in
that result, so a marker copied into the prompt cannot stand in for memory/tool
output. The integration scenario must separately assert the actual application's
state and isolation: a syntactically correct supplied result is not independent
proof of its application provenance.

Scripts are copied, including nested arguments. Provider keys cannot be returned
in scripted text/tool arguments. Unexpected requests fail immediately and remain
sticky. `Complete()` requires every configured role's steps and tool results to
finish, with no rejected requests. `Evidence()` returns a copy of at most 1024
sanitized metadata events; it records no headers, credentials, prompts or tool
output. The fixture writes no logs and installs no production endpoint override.
The owner of a test-only sidecar bootstrap controls evidence output and must
evaluate completion after graceful shutdown.

The response uses either the full Messages envelope or the documented SSE
sequence: message start, content block start/delta/stop, message delta and message
stop. Tool arguments use `input_json_delta`; final text uses `text_delta`.
Protocol reference: [Anthropic streaming documentation](https://platform.claude.com/docs/en/build-with-claude/streaming).
