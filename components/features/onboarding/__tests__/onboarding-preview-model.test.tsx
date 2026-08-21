import { describe, it, expect } from "vitest"
import { render, screen } from "@testing-library/react"
import { readFileSync } from "node:fs"
import { join } from "node:path"

import { OnboardingPreview, TEMPLATES, type CrewTemplateSlug } from "../onboarding-preview"
import { getModelLabel } from "@/lib/cli-adapters"

// =============================================================================
// The live preview's crew card advertises "N agents · <model>", and the model
// half of that string was five hand-typed copies of "Claude Sonnet 4.6" — a
// model that appeared in neither of the two places that decide what actually
// happens:
//
//   * the wizard's own picker (ANTHROPIC_MODELS in lib/cli-adapters.ts) offers
//     claude-sonnet-5;
//   * agents deployed from a builtin template get whatever
//     internal/database/builtin/crew-templates/*.yaml pins, also
//     claude-sonnet-5.
//
// So the preview promised a third model that nothing would ever run. The
// property worth pinning is not "the label says Sonnet 5" — that string will
// move — but "this component does not spell a model name itself": whatever it
// shows has to come back through getModelLabel from a model id.
// =============================================================================

const SOURCE = readFileSync(
  join(process.cwd(), "components", "features", "onboarding", "onboarding-preview.tsx"),
  "utf8",
)

// Everything the preview renders lands under one template. Blank is the odd
// one (a single agent) and is worth exercising for the singular/plural branch
// beside the model label.
const SLUGS = Object.keys(TEMPLATES) as CrewTemplateSlug[]

function renderPreview(slug: CrewTemplateSlug) {
  return render(
    <OnboardingPreview workspaceName="Acme" crewSlug={slug} mode={null} adapterKey="CLAUDE_CODE" />,
  )
}

describe("onboarding preview — the model name it shows", () => {
  it.each(SLUGS)("%s shows the label getModelLabel resolves, not a typed-in string", (slug) => {
    const { unmount } = renderPreview(slug)
    const count = TEMPLATES[slug].agents.length
    const noun = count === 1 ? "agent" : "agents"
    // The label is derived here the same way the component derives it: if the
    // id the templates pin ever changes, this test follows it, and it is the
    // *shape* (a resolved label, not a literal) that stays pinned.
    const expected = `${count} ${noun} · ${getModelLabel("claude-sonnet-5")}`
    expect(screen.getByText(expected)).toBeTruthy()
    unmount()
  })

  it("never renders the stale 'Claude Sonnet 4.6' the card used to claim", () => {
    const { container, unmount } = renderPreview("software-development")
    expect(container.textContent).not.toMatch(/Sonnet 4\.6/)
    unmount()
  })

  // Source-level, deliberately: a render test can only catch the templates
  // that exist today, and the failure mode here is someone adding a sixth
  // template with the model name typed in beside the agent list again. The
  // property is about the file, so it is checked against the file.
  it("carries no hardcoded Anthropic model display name", () => {
    // Comments stripped first: the constant's own doc comment names the stale
    // string on purpose, to say why it is gone. A check that cannot tell code
    // from prose would forbid explaining the bug it guards against.
    const code = SOURCE.replace(/\/\*[\s\S]*?\*\//g, "").replace(/\/\/[^\n]*/g, "")
    const literals = code.match(/"Claude (?:Sonnet|Opus|Haiku|Fable)[^"]*"/g) ?? []
    expect(
      literals,
      `hardcoded model display name(s) in onboarding-preview.tsx: ${literals.join(", ")} — ` +
        `render getModelLabel(<model id>) instead`,
    ).toEqual([])
  })

  it("derives its label from a model id the picker and the templates agree on", () => {
    // The id, not the label, is what the component is allowed to name. It has
    // to be one getModelLabel actually resolves — an unknown id falls through
    // to the raw string and the card would read "claude-sonnet-5".
    const id = SOURCE.match(/const TEMPLATE_MODEL_ID = "([^"]+)"/)?.[1]
    expect(id, "TEMPLATE_MODEL_ID went missing or was renamed").toBeTruthy()
    expect(getModelLabel(id!)).not.toBe(id)
    expect(SOURCE).toMatch(/getModelLabel\(TEMPLATE_MODEL_ID\)/)
  })
})
