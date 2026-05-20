// Package memory owns Crewship's agent-memory engine: file-first storage
// for AGENT.md / CREW.md / pins.md / daily/YYYY-MM-DD.md / lessons.md and
// (PR-E) PERSONA.md + peers/{user}.md, plus FTS5 search across the
// indexed corpus.
//
// The package is organized as:
//
//   - engine.go      — read / write / search dispatcher
//   - chunk.go       — markdown chunker for indexing
//   - index.go       — FTS5 index management
//   - versions.go    — write-once snapshots used by the memory_versions table
//   - verifier.go    — stale-citation + (future) contradiction detection
//   - retention.go   — daily/journal pruning policies
//   - watcher.go     — filesystem watch for human-edited memory files
//   - safety.go      — path-traversal guard reused across CLI/HTTP entry points
//
// # Tool naming (Crewship-native, not Anthropic memory_20250818)
//
// PR-A (F1) exposes memory access to agents as native function-calling
// tools: memory.read / memory.write / memory.search / memory.append_daily.
// These names follow the existing internal/memory/ Go package convention
// and the established CLI surface (crewship memory ...). They differ
// from Anthropic's memory_20250818 schema (view/create/str_replace/
// insert/delete/rename) by design — see PRD §5 Z.8 and §6 F1
// "Anthropic compliance" for rationale.
//
// No memory_20250818 compatibility shim is built in MVP. If a customer
// later requires Anthropic-canonical tool names (e.g. for a managed-
// agents migration), wire a thin translation layer in this package
// rather than duplicating storage primitives. Do NOT pre-emptively
// scaffold the shim — dead code attracts drift.
package memory
