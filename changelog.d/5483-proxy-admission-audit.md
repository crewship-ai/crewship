Credential proxy admission now records a separate `PROXY_ADMITTED` event for
authenticated active runs, correlated to the agent and crew. Audit failures
refuse admission. This records an allowed attempt, not upstream authentication,
and leaves `USE` and `last_used_at` unchanged.
