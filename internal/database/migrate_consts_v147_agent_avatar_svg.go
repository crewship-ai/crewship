package database

// migrationAgentAvatarSVG (v147) persists an agent's rendered avatar so its
// face stops being a function of the installed DiceBear version (#1297).
//
// Until now an agent avatar was re-derived on every render from
// (avatar_seed, avatar_style) by the client-side generator, which means a
// dependency bump repaints every existing agent — the 9→10 spike recorded in
// lib/__tests__/agent-avatar-stability.test.ts produced zero identical
// outputs across 10 styles × 5 seeds. Storing the render freezes the face at
// the version that drew it.
//
//   - avatar_svg holds the generator output verbatim. Written once (see
//     PutAvatar's write-once rule) and served back byte-for-byte, so the
//     stored face is exactly what the agent looked like when it was drawn.
//     Validated against a tag/attribute allowlist on write.
//   - avatar_svg_hash is a content hash of avatar_svg, used as the ?v= cache
//     buster in the serve URL. It exists as its own column so list queries
//     can build the URL without pulling multi-KB blobs — at ~2.6 KB per
//     default-style avatar, SELECTing avatar_svg for a 200-agent roster
//     would move ~500 KB per request for data the response never carries.
//
// Both nullable: a NULL pair means "not backfilled yet", and the client
// falls back to generating from the seed exactly as it does today. That is
// what makes the rollout lazy rather than requiring a JS-side migration
// (DiceBear is JS-only, so no Go migration can generate these).
const migrationAgentAvatarSVG = `
ALTER TABLE agents ADD COLUMN avatar_svg TEXT;
ALTER TABLE agents ADD COLUMN avatar_svg_hash TEXT;
`
