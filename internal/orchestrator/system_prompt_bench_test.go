package orchestrator

import (
	"strings"
	"testing"
)

// The RunAgent path assembles the final system prompt from up to 5 pieces:
// the base prompt, a LEAD or PEER crew context, an optional memory block,
// and an optional language directive. Each piece is 1–5 kB in typical use.
//
// These benchmarks compare the previous `+`-concat pattern (O(n²) because
// each step copies the whole accumulated prompt) against a single
// strings.Builder pass (O(n) with pre-grown capacity).

const (
	benchBase = "You are an agent with persistent memory, responsible for..."
	benchLead = "[CREW CONTEXT]\nYour fellow crew members: ..."
	benchMem  = "[AGENT MEMORY]\n--- AGENT.md (long-term memory) ---\n..."
	benchLang = "cs"
)

func benchBasePrompt() string  { return strings.Repeat(benchBase, 80) } // ~4.8 kB
func benchLeadBlock() string   { return strings.Repeat(benchLead, 60) } // ~2.7 kB
func benchMemoryBlock() string { return strings.Repeat(benchMem, 80) }  // ~4.0 kB

// BenchmarkSystemPromptAssembly_Concat replicates the old RunAgent pattern:
// repeated `req.SystemPrompt = req.SystemPrompt + "\n\n" + section` —
// each step reallocates and copies the growing prompt.
func BenchmarkSystemPromptAssembly_Concat(b *testing.B) {
	base := benchBasePrompt()
	lead := benchLeadBlock()
	mem := benchMemoryBlock()

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		out := base
		out = out + "\n\n" + lead
		out = out + "\n\n" + mem
		out = out + "\n\n[LANGUAGE]\nAlways respond and write comments in " +
			benchLang + ". All your output, summaries, and handoff descriptions must be in " +
			benchLang + ".\n[END LANGUAGE]"
		_ = out
	}
}

// BenchmarkSystemPromptAssembly_Builder measures the strings.Builder
// equivalent with a Grow() hint — what RunAgent uses after the fix.
func BenchmarkSystemPromptAssembly_Builder(b *testing.B) {
	base := benchBasePrompt()
	lead := benchLeadBlock()
	mem := benchMemoryBlock()

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		var sb strings.Builder
		sb.Grow(len(base) + len(lead) + len(mem) + 512)
		sb.WriteString(base)
		sb.WriteString("\n\n")
		sb.WriteString(lead)
		sb.WriteString("\n\n")
		sb.WriteString(mem)
		sb.WriteString("\n\n[LANGUAGE]\nAlways respond and write comments in ")
		sb.WriteString(benchLang)
		sb.WriteString(". All your output, summaries, and handoff descriptions must be in ")
		sb.WriteString(benchLang)
		sb.WriteString(".\n[END LANGUAGE]")
		_ = sb.String()
	}
}
