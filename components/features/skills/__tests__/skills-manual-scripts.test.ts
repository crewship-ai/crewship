import { readFileSync } from "node:fs"
import { describe, expect, it } from "vitest"

// The two manual Playwright scripts for /skills (#3488 SKL-01) are not run in
// CI, so nothing else notices when they still drive controls the Harbor 2
// Skills page removed. This keeps them on the controls the page has.

const read = (name: string) => readFileSync(`e2e/${name}`, "utf8")

// Controls of the pre-Harbor 2 page: the Create Skill button and its form, the
// card grid, the Install to agent detail panel and the rail/detail panels.
const RETIRED = [
  "Create Skill",
  "input#skill-slug",
  'name: "Generate"',
  'has-text("Done")',
  "Install to agent",
  'aria-label*="anthropic/"',
  "skills-rail",
  "skills-detail",
  "?vendor=workspace",
]

describe("manual skills scripts", () => {
  it.each(["skills-create-flow.mjs", "skills-eval.mjs"])("%s no longer drives retired controls", (name) => {
    const src = read(name)
    for (const old of RETIRED) expect(src, `${name} still uses ${old}`).not.toContain(old)
  })

  it("skills-create-flow.mjs goes Add skill › Describe it › Write skill", () => {
    const src = read("skills-create-flow.mjs")
    for (const control of ['"Add skill"', "Describe it", "#skill-name", "#skill-prompt", '"Write skill"', "workspace_id="]) expect(src).toContain(control)
  })

  it("skills-eval.mjs checks the panel, the Views keys, the library rows and the skill page", () => {
    const src = read("skills-eval.mjs")
    for (const control of ['"Add skill"', "Add skill steps", "[data-panel-id=skills]", "aria-keyshortcuts", "[data-slot=skill-row]", "Give to agents", "scrollWidth"]) expect(src).toContain(control)
  })
})
