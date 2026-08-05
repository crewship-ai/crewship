import { describe, it, expect } from "vitest"

import { htmlToMarkdown, markdownToHtml } from "../tiptap-editor-markdown"

// ENG-3 on dev2 arrived with its seeded description stored FIVE times over,
// while the seed file holds it once and no other issue is affected. The
// description editor saves on blur, comparing the draft against the prop:
//
//   if (draft.current !== description) onSave(draft.current)
//
// so the loop only closes if the editor hands back something different from
// what it was given. These tests ask the round-trip that question directly:
// is `htmlToMarkdown(markdownToHtml(x))` equal to `x`, and does repeating it
// converge or grow? A round-trip that grows is a save-on-every-blur that
// makes the text longer each time, which is the observed shape.
//
// The seeded text matters here, not a tidy fixture: it uses two-space hard
// line breaks, the construct most likely to survive markdown→HTML→markdown
// unevenly.

const SEEDED = [
  "Create the following directory structure under /tmp/demo-project/:  ",
  "/tmp/demo-project/  ",
  "README.md (with project name and date)  ",
  "src/  ",
  "main.py (simple hello world)  ",
  "utils.py (a helper function)  ",
  "tests/  ",
  "test_main.py (a basic test)  ",
  "data/  ",
  "config.json (sample config with 3 keys)",
  "",
  "Verify all files exist and list the tree.",
].join("\n")

/** One pass of what the editor does between mount and blur. */
function roundTrip(md: string): string {
  return htmlToMarkdown(markdownToHtml(md))
}

describe("markdown round-trip", () => {
  it("does not grow the seeded description", () => {
    const once = roundTrip(SEEDED)
    // Growth is the failure that matters: it is what turns a blur into an
    // append, and five blurs into five copies.
    expect(once.length).toBeLessThanOrEqual(SEEDED.length * 1.2)
    expect(once).not.toContain("Verify all files exist and list the tree.\nCreate the following")
  })

  it("converges — a second pass changes nothing a third would change again", () => {
    // Idempotence from the second pass on is the property the editor needs.
    // Without it every blur writes, and the issue is edited by simply being
    // looked at.
    const a = roundTrip(SEEDED)
    const b = roundTrip(a)
    const c = roundTrip(b)
    expect(c).toBe(b)
  })

  it("keeps the paragraph count stable", () => {
    const before = SEEDED.split(/\n\s*\n/).length
    const after = roundTrip(SEEDED).split(/\n\s*\n/).length
    expect(after).toBe(before)
  })

  it("survives plain prose unchanged", () => {
    const plain = "One line.\n\nAnother paragraph."
    expect(roundTrip(roundTrip(plain))).toBe(roundTrip(plain))
  })
})
