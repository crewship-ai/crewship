import { describe, expect, it } from "vitest"
import { readFileSync } from "node:fs"
import { join } from "node:path"

import { getModelLabel } from "@/lib/cli-adapters"
import { TEAM_TEMPLATE_SLUGS, TEMPLATES, TEMPLATE_MODEL_ID, TEMPLATE_MODEL_LABEL } from "../crew-templates"

// The setup wizard's template tiles must describe the crew a person actually
// gets. Each field below is the YAML's own (internal/database/builtin/
// crew-templates/<slug>.yaml), so the tile and the deployed crew cannot drift.
function yamlField(slug: string, key: string): string {
  const yaml = readFileSync(join(process.cwd(), "internal", "database", "builtin", "crew-templates", `${slug}.yaml`), "utf8")
  const line = yaml.split("\n").find((l) => l.startsWith(`${key}:`))
  expect(line, `${slug}.yaml has no ${key}`).toBeDefined()
  return line!.slice(key.length + 1).trim().replace(/^'(.*)'$/, "$1")
}

describe("setup crew templates", () => {
  it.each(TEAM_TEMPLATE_SLUGS)("%s mirrors its template YAML", (slug) => {
    const tpl = TEMPLATES[slug]
    expect(tpl.name).toBe(yamlField(slug, "name"))
    expect(tpl.description).toBe(yamlField(slug, "description"))
    expect(tpl.icon).toBe(yamlField(slug, "icon"))
    expect(tpl.color).toBe(yamlField(slug, "color"))
  })

  it("names the pinned model through getModelLabel, never a typed-in string", () => {
    // The retired preview once advertised a model nothing ran.
    expect(TEMPLATE_MODEL_LABEL).toBe(getModelLabel(TEMPLATE_MODEL_ID))
    const yaml = readFileSync(join(process.cwd(), "internal", "database", "builtin", "crew-templates", "software-development.yaml"), "utf8")
    expect(yaml).toContain(`llm_model: ${TEMPLATE_MODEL_ID}`)
  })

  it("offers exactly four team templates plus Start blank", () => {
    expect(TEAM_TEMPLATE_SLUGS).toHaveLength(4)
    expect(Object.keys(TEMPLATES).sort()).toEqual([...TEAM_TEMPLATE_SLUGS, "blank"].sort())
  })
})
