package pipeline

// Canonical registry of `type: code` step runtimes.
//
// A step whose runtime has no wired CodeRunner saves-but-fails: it
// passes the DB write, then errors at every invocation (potentially a
// 03:00 cron). To kill that class of silent failure we reject unwired
// runtimes at AUTHOR time — the DSL validator (dsl_validate_egress.go)
// and the manifest plan (internal/manifest/routine_warnings.go) both
// consult this single source of truth.
//
// wiredCodeRuntimes — has a CodeRunner wired in this build:
//   - expr : single boolean comparison (ExprCodeRunner), token-zero.
//   - cel  : Google Common Expression Language (CelCodeRunner),
//     token-zero, non-Turing-complete — bool logic, arithmetic,
//     strings, lists, maps. The general agentless-logic primitive.
//
// knownCodeRuntimes — syntactically legal runtime names (superset of
// wired). python | go | bash stay RESERVED but UNWIRED: no sandbox
// runner exists. Naming an unknown runtime is a hard schema error;
// naming a known-but-unwired one is a "no wired runner" error with the
// convert-to-agent_run guidance. python/go/bash become wired only when
// a WASM/Starlark sandbox lands (PRD-ROUTINES-MAX-2026 Wave 1.2).
var wiredCodeRuntimes = map[string]bool{
	"expr": true,
	"cel":  true,
}

var knownCodeRuntimes = map[string]bool{
	"expr":   true,
	"cel":    true,
	"python": true,
	"go":     true,
	"bash":   true,
}

// IsWiredCodeRuntime reports whether rt has a CodeRunner wired in this
// build. Exported so internal/manifest shares the single source of
// truth instead of re-declaring the list.
func IsWiredCodeRuntime(rt string) bool {
	return wiredCodeRuntimes[rt]
}

// IsKnownCodeRuntime reports whether rt is a syntactically legal code
// runtime name (wired or reserved-unwired).
func IsKnownCodeRuntime(rt string) bool {
	return knownCodeRuntimes[rt]
}
