// Package dockerfilesources guards the Dockerfile's backend stage against a
// root-level Go package it forgot to copy (#886 schemas/, #2328 config/).
//
// The stage copies source directories one by one instead of the whole
// checkout, so a new top-level package compiles everywhere except inside
// the image, where `go build` fails with "no required module provides
// package". Regular CI never notices because it builds from a full
// checkout; this test does, on every PR.
//
// The frontend contract preserves every tracked TypeScript input admitted by
// the production tsconfig, imported shared JSON contracts, Prisma/config/public
// inputs and build/legal helpers while keeping backend source out of its cache.
// Actual image builds remain required by CI; static source checks complement
// rather than replace in-image compilation. VERSION remains an export input,
// so new commit identities still rebuild the UI instead of reusing stale stamps.
package dockerfilesources
