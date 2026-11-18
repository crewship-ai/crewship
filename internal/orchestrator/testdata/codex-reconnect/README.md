# Codex exec reconnect captures

These files retain only the top-level error and turn completion lines, in their
original order. The 0.169.0 captures come from two recovered Codex CLI runs.
The 0.159.0 capture comes from the same release binary pinned by the restricted
native worker. A loopback Responses server closed the first SSE response before
completion, then returned a successful response on the second request. The
real CLI exited zero in all three captures. The extracts contain no prompts,
thread IDs, host paths or credentials.
