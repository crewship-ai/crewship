package orchestrator

import (
	"encoding/json"
	"sync"
)

// loopGuardThreshold is the number of *consecutive identical* tool calls
// (same tool name + byte-identical input) that trips the guard and aborts
// the run. A confused agent re-issuing the exact same failing action is the
// most expensive failure mode we see: it burns to the --max-turns ceiling
// re-sending the full context every turn for zero progress. Nobody in the
// ecosystem (LangGraph, CrewAI, OpenAI/Claude Agent SDK) ships this — the
// numeric turn cap is their only stop — so it's a cheap, high-leverage guard.
//
// 5 (not 3) is deliberate: a legitimate poll-until-ready loop ("check status"
// with unchanged args) can repeat a few times before state flips. 5 identical
// calls in a row is well past any healthy poll and squarely in stuck-loop
// territory, while still cutting a 50-turn runaway by ~90%. Only *consecutive*
// repeats count — any differing call in between resets the streak, so varied
// work never trips it.
const loopGuardThreshold = 5

// loopGuard detects a stuck agent that repeats the identical tool call with no
// variation. It is adapter-agnostic: it observes the normalized tool_call
// event stream, so it covers every CLI adapter (Claude, Codex, Gemini, …) that
// emits tool calls through the shared parser. Safe for concurrent use — the
// stream handler and the abort check run on different goroutines.
type loopGuard struct {
	mu      sync.Mutex
	lastSig string
	count   int
	tripped bool
}

// observe records one tool call and returns true exactly once — the moment the
// consecutive-identical streak first reaches loopGuardThreshold. Calls after a
// trip return false so the caller's abort fires a single time.
func (g *loopGuard) observe(toolName string, input any) bool {
	sig := toolCallSignature(toolName, input)
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.tripped {
		return false
	}
	if sig == g.lastSig && g.count > 0 {
		g.count++
	} else {
		g.lastSig = sig
		g.count = 1
	}
	if g.count >= loopGuardThreshold {
		g.tripped = true
		return true
	}
	return false
}

// Tripped reports whether the guard has fired. Read after the stream ends to
// decide whether the run terminated on a loop rather than completing.
func (g *loopGuard) Tripped() bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.tripped
}

// toolCallSignature is the identity key for loop detection: tool name plus a
// canonical JSON encoding of the input. A NUL separator keeps a crafted tool
// name from colliding with the input boundary. Unmarshalable input collapses
// to a sentinel — two such calls still compare equal, which is the correct
// conservative behaviour (identical unknown inputs look identical).
func toolCallSignature(toolName string, input any) string {
	b, err := json.Marshal(input)
	if err != nil {
		b = []byte("\x00unencodable")
	}
	return toolName + "\x00" + string(b)
}
