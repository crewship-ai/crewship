// Package memtest provides reusable scaffolds for tests that touch the
// memory subsystem. The goal is to make it cheap for a future engineer
// to spin up a deterministic memory test without re-implementing the
// same mock provider, fixture builder, and edge-case enumeration that
// every test file currently inlines.
//
// # When to reach for memtest
//
// Use this package from any _test.go that:
//
//   - exercises a caller of [memory.Provider] (orchestrator, sidecar,
//     CLI) and needs to stub the backing tier deterministically;
//   - constructs RetainRequest / RecallRequest / RecallSnippet
//     fixtures and wants sensible defaults instead of hand-rolling
//     them every time (drift between test files is otherwise certain);
//   - wants to verify edge-case handling against the canonical
//     catalog in [EdgeCases] so coverage gaps surface explicitly
//     ("we don't have a test for the empty-tier case").
//
// Do NOT use memtest from production code paths. The package imports
// only the testing-relevant subset of the memory API and panics where
// production code would return an error — the panic is the deliberate
// signal that a caller has wandered out of the test boundary.
//
// # Edge case catalog
//
// [EdgeCases] enumerates the recurring memory failure modes that
// real-world reports keep surfacing. Each entry is a short slug, a
// human description, and a snippet showing how to configure a
// [MockProvider] to reproduce it. Adding a new edge case here is the
// preferred pattern for documenting a class of bugs the test suite
// should keep regression-proof; the slug becomes searchable
// ("grep edge_case_oversize_retain") across the codebase.
//
// # Concurrency
//
// All exported types are safe for concurrent use unless noted
// otherwise. [MockProvider] uses a sync.Mutex around its counters
// and configured behaviours so a test that exercises a worker pool
// can still assert "Recall was called exactly N times" without
// races flagging.
//
// # Stability
//
// This is INTERNAL test tooling. The exported surface MAY change
// without a deprecation cycle if a real test file wants something
// different. Treat memtest as you would a snippet library, not a
// public API.
package memtest
