#!/usr/bin/env node
// Regenerates internal/api/testdata/dicebear_avatars.json — the fixtures that
// pin the contract between the JS avatar generator and the Go validator
// (validateAgentAvatarSVG in internal/api/agents_avatar.go).
//
// Why this exists: the validator is an allowlist over the tag/attribute
// vocabulary DiceBear actually emits. If a collection starts using an element
// the allowlist is missing, PutAvatar begins rejecting legitimate avatars —
// and nothing looks broken, because the client silently falls back to
// generating from the seed. Persistence would just stop happening. The Go
// test over these fixtures turns that silent failure into a red build.
//
// Run after any @dicebear/* version change:
//
//   node scripts/gen-avatar-fixtures.mjs
//
// Then run `go test ./internal/api/ -run DiceBear`. A failure means the
// vocabulary moved: widen the allowlist in agents_avatar.go if the new
// elements are inert, and think hard if they are not.
//
// Keep the style list in sync with AVATAR_STYLES in lib/agent-avatar.ts.

import { writeFileSync, mkdirSync } from "node:fs"
import { dirname, join } from "node:path"
import { fileURLToPath } from "node:url"

const repoRoot = join(dirname(fileURLToPath(import.meta.url)), "..")
const OUT = join(repoRoot, "internal", "api", "testdata", "dicebear_avatars.json")

const STYLES = [
  "bottts-neutral",
  "adventurer",
  "fun-emoji",
  "pixel-art",
  "micah",
  "notionists",
  "thumbs",
  "lorelei",
  "big-smile",
  "avataaars",
]

// Same seeds as lib/__tests__/agent-avatar-stability.test.ts, so the two
// tripwires describe the same sample of the generator's output space.
const SEEDS = ["alice", "bob-the-builder", "42"]

const { createAvatar } = await import("@dicebear/core")

const fixtures = {}
for (const style of STYLES) {
  const collection = await import(`@dicebear/${style}`)
  for (const seed of SEEDS) {
    fixtures[`${style}:${seed}`] = createAvatar(collection, { seed, size: 128 }).toString()
  }
}

mkdirSync(dirname(OUT), { recursive: true })
writeFileSync(OUT, JSON.stringify(fixtures, null, 1))
console.log(`wrote ${Object.keys(fixtures).length} fixtures to ${OUT}`)
