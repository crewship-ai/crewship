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
// Keep the style list in sync with AVATAR_STYLES in lib/agent-avatar.ts (the
// 9.4 styles here; the DiceBear 10 voxel styles below).

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

// #3789: DiceBear 10's voxel styles, still and animated. These are NOT
// storable — the Go test over them pins that the allowlist refuses them (an
// XML comment, <use href>, <style>), which is why lib/agent-avatar-persist.ts
// never sends them. If DiceBear 10 ever emits only allowlisted markup, that
// test goes red and the decision can be revisited.
const VOXEL_OUT = join(repoRoot, "internal", "api", "testdata", "dicebear10_voxel_avatars.json")
const v10 = await import("@dicebear/core-v10")
const voxel = {}
for (const style of ["voxel-bot", "voxel-art"]) {
  const { default: definition } = await import(`@dicebear/styles-v10/${style}.json`, { with: { type: "json" } })
  const s = new v10.Style(definition)
  for (const seed of SEEDS) {
    voxel[`${style}:${seed}`] = new v10.Avatar(s, { seed, size: 128, animationVariant: "none" }).toString()
    voxel[`${style}:${seed}:animated`] = new v10.Avatar(s, { seed, size: 128, animationVariant: "slow" }).toString()
  }
}
writeFileSync(VOXEL_OUT, JSON.stringify(voxel, null, 1))
console.log(`wrote ${Object.keys(voxel).length} fixtures to ${VOXEL_OUT}`)
