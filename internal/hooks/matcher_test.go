package hooks

import "testing"

// TestMatches covers the matcher contract end-to-end. The function
// composes four independent constraint dimensions (tools, agent IDs,
// crew IDs, severities) and the contract is "every populated slice
// gates; empty slices are 'don't care'." A handful of regressions in
// past PRs have toggled the wrong polarity on one of these axes —
// pinning each case down by subtest keeps that from happening
// silently.
func TestMatches(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		m    Matcher
		ctx  EventContext
		want bool
	}{
		// ── Zero-value matcher: match everything ────────────────
		{
			name: "zero_matcher_matches_zero_ctx",
			m:    Matcher{},
			ctx:  EventContext{},
			want: true,
		},
		{
			name: "zero_matcher_matches_populated_ctx",
			m:    Matcher{},
			ctx: EventContext{
				ToolName: "Bash", AgentID: "a1", CrewID: "c1", Severity: "error",
			},
			want: true,
		},

		// ── Tools (regex) ──────────────────────────────────────
		{
			name: "tools_exact_string_matches",
			m:    Matcher{Tools: []string{"Bash"}},
			ctx:  EventContext{ToolName: "Bash"},
			want: true,
		},
		{
			name: "tools_regex_alternation_matches",
			m:    Matcher{Tools: []string{"Bash|Read|Write"}},
			ctx:  EventContext{ToolName: "Read"},
			want: true,
		},
		{
			name: "tools_anchored_pattern_does_not_match_substring",
			// The matcher uses unanchored Match — but a caller who wants
			// strictness can pass anchors. Verify ^...$ behaves.
			m:    Matcher{Tools: []string{"^Read$"}},
			ctx:  EventContext{ToolName: "Reader"},
			want: false,
		},
		{
			name: "tools_unanchored_matches_substring",
			m:    Matcher{Tools: []string{"Read"}},
			ctx:  EventContext{ToolName: "Reader"},
			want: true,
		},
		{
			name: "tools_populated_but_ctx_tool_empty_does_not_match",
			// Event without ToolName should fail a Tools constraint
			// rather than silently matching every event. Otherwise a
			// matcher meant for tool-call events would also fire on
			// PreAgentStart etc.
			m:    Matcher{Tools: []string{".*"}},
			ctx:  EventContext{ToolName: ""},
			want: false,
		},
		{
			name: "tools_invalid_regex_is_no_match_not_panic",
			// An invalid pattern (e.g. unclosed bracket) caches as nil
			// in regexCache and the loop continues. The matcher returns
			// false rather than panicking — bad-pattern hooks become
			// "silent no-op" instead of "crash the dispatcher."
			m:    Matcher{Tools: []string{"[unclosed"}},
			ctx:  EventContext{ToolName: "Bash"},
			want: false,
		},
		{
			name: "tools_invalid_regex_alongside_valid_uses_the_valid_one",
			m:    Matcher{Tools: []string{"[invalid", "^Bash$"}},
			ctx:  EventContext{ToolName: "Bash"},
			want: true,
		},

		// ── AgentIDs (exact match) ─────────────────────────────
		{
			name: "agent_ids_exact_match",
			m:    Matcher{AgentIDs: []string{"a1", "a2"}},
			ctx:  EventContext{AgentID: "a2"},
			want: true,
		},
		{
			name: "agent_ids_no_match",
			m:    Matcher{AgentIDs: []string{"a1"}},
			ctx:  EventContext{AgentID: "a99"},
			want: false,
		},
		{
			name: "agent_ids_empty_ctx_agent_does_not_match",
			m:    Matcher{AgentIDs: []string{"a1"}},
			ctx:  EventContext{AgentID: ""},
			want: false,
		},

		// ── CrewIDs (exact match) ──────────────────────────────
		{
			name: "crew_ids_exact_match",
			m:    Matcher{CrewIDs: []string{"crew-1"}},
			ctx:  EventContext{CrewID: "crew-1"},
			want: true,
		},
		{
			name: "crew_ids_no_match",
			m:    Matcher{CrewIDs: []string{"crew-1"}},
			ctx:  EventContext{CrewID: "crew-2"},
			want: false,
		},

		// ── Severities (exact match) ───────────────────────────
		{
			name: "severities_exact_match",
			m:    Matcher{Severities: []string{"warning", "error"}},
			ctx:  EventContext{Severity: "warning"},
			want: true,
		},
		{
			name: "severities_case_sensitive_no_match",
			// contains() is byte-exact; callers must normalize casing
			// upstream. This pins that contract — a regression that
			// added strings.EqualFold would silently widen every hook.
			m:    Matcher{Severities: []string{"warning"}},
			ctx:  EventContext{Severity: "WARNING"},
			want: false,
		},

		// ── Composition (AND across populated axes) ────────────
		{
			name: "all_axes_must_match",
			m: Matcher{
				Tools:      []string{"Bash"},
				AgentIDs:   []string{"a1"},
				CrewIDs:    []string{"c1"},
				Severities: []string{"high"},
			},
			ctx: EventContext{
				ToolName: "Bash", AgentID: "a1", CrewID: "c1", Severity: "high",
			},
			want: true,
		},
		{
			name: "any_axis_failure_fails_the_match",
			m: Matcher{
				Tools:    []string{"Bash"},
				AgentIDs: []string{"a1"},
			},
			ctx:  EventContext{ToolName: "Bash", AgentID: "different"},
			want: false,
		},

		// ── Payload is ignored by the matcher ──────────────────
		{
			name: "payload_ignored",
			m:    Matcher{},
			ctx: EventContext{
				Payload: map[string]any{"anything": true},
			},
			want: true,
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := Matches(tc.m, tc.ctx); got != tc.want {
				t.Errorf("Matches() = %v, want %v\n  matcher: %+v\n  ctx:     %+v",
					got, tc.want, tc.m, tc.ctx)
			}
		})
	}
}

// TestRegexCache_CachesNilForInvalidPattern locks in the negative-cache
// behaviour: compiling a bad pattern once stores a nil sentinel so the
// second call returns immediately. Without this the dispatcher's hot
// path would re-parse the same broken pattern on every event when a
// user shipped a malformed hook config.
func TestRegexCache_CachesNilForInvalidPattern(t *testing.T) {
	// Not t.Parallel — regexCache is package-level and shared, so
	// asserting on cache state would race with sibling tests.
	pattern := "[bad-regex-for-cache-test"

	if got := compileRegex(pattern); got != nil {
		t.Fatalf("first call: want nil compile result, got %v", got)
	}
	if _, ok := regexCache.Load(pattern); !ok {
		t.Fatal("nil sentinel should have been stored in cache after first call")
	}
	if got := compileRegex(pattern); got != nil {
		t.Fatalf("second call should return cached nil, got %v", got)
	}
}

// TestRegexCache_CachesCompiledPattern ensures the positive cache
// path also works — a valid pattern compiles once and subsequent calls
// hand back the same *Regexp pointer. Pointer equality is the cheapest
// proof the cache actually hit; a regression that always recompiled
// would slip through with deep-equal but fresh pointers.
func TestRegexCache_CachesCompiledPattern(t *testing.T) {
	pattern := "^valid-cache-test-[a-z]+$"

	first := compileRegex(pattern)
	if first == nil {
		t.Fatal("valid pattern should compile")
	}
	second := compileRegex(pattern)
	if first != second {
		t.Errorf("cache miss on second call: pointers should be identical")
	}
}
