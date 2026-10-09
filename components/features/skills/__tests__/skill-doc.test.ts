import { describe, expect, it } from "vitest"
import {
  frontmatter,
  hasStructure,
  listItems,
  patchFrontmatter,
  readSkillDoc,
  sectionRole,
  skillMarkdown,
  slugifySkill,
  splitSections,
  usageByDay,
} from "@/components/features/skills/skill-doc"
import { skillIcon, skillIconName, skillTags } from "@/components/features/skills/skills-model"

// The bundled community skill File Crafter, as dev4 stores it.
const FILE_CRAFTER = `# File Crafter

## When to Activate
- Task involves creating files, directories, or structured data
- Keywords: "create", "file", "directory", "CSV", "JSON", "tree", "generate"

## Instructions
1. Create parent directories with mkdir -p before writing files
2. Use heredoc (cat << 'EOF') for multi-line file content
3. For CSV files: always include a header row with column names

## Output Format
- Always show the created file tree with: find <dir> -type f | sort
- Report file sizes and line counts: wc -l <file>

## Guardrails
- Write only to /tmp/ — never to system directories
- Don't create files larger than 10MB`

describe("readSkillDoc", () => {
  it("lays a conventional SKILL.md out by role", () => {
    const doc = readSkillDoc(FILE_CRAFTER)
    expect(doc.when).toEqual(["Task involves creating files, directories, or structured data"])
    expect(doc.triggers).toEqual(["create", "file", "directory", "CSV", "JSON", "tree", "generate"])
    expect(doc.steps).toHaveLength(3)
    expect(doc.steps[1]).toBe("Use heredoc (cat << 'EOF') for multi-line file content")
    expect(doc.output).toHaveLength(2)
    expect(doc.guardrails).toEqual(["Write only to /tmp/ — never to system directories", "Don't create files larger than 10MB"])
    expect(doc.other).toEqual([])
    expect(hasStructure(doc)).toBe(true)
  })

  it("keeps sections it cannot place, and says there is no structure", () => {
    const doc = readSkillDoc("# Brand\n\nIntro.\n\n## Colours\nNavy and white.\n\n## Typography\n- Inter")
    expect(hasStructure(doc)).toBe(false)
    expect(doc.other.map((s) => s.heading)).toEqual(["Brand", "Colours", "Typography"])
  })

  it("does not read headings inside code fences", () => {
    const sections = splitSections("## Instructions\n```bash\n# not a heading\n```\n## Guardrails\n- x")
    expect(sections.map((s) => s.heading)).toEqual(["Instructions", "Guardrails"])
  })

  it("gives repeated headings distinct anchors", () => {
    expect(splitSections("## Notes\na\n## Notes\nb").map((s) => s.id)).toEqual(["notes", "notes-2"])
  })

  it("survives an empty or missing body", () => {
    expect(hasStructure(readSkillDoc(null))).toBe(false)
    expect(readSkillDoc("").sections).toEqual([])
  })
})

describe("sectionRole", () => {
  it.each([
    ["When to Use", "when"],
    ["Triggers", "when"],
    ["How it works", "steps"],
    ["Workflow", "steps"],
    ["Output rules", "output"],
    ["Response format", "output"],
    ["Rules", "guardrails"],
    ["Constraints", "guardrails"],
    ["Background", null],
  ])("%s → %s", (heading, role) => {
    expect(sectionRole(heading)).toBe(role)
  })
})

describe("listItems", () => {
  it("joins wrapped continuation lines onto their item", () => {
    expect(listItems("- first\n  continues\n- second\n\nprose")).toEqual(["first continues", "second"])
  })
})

describe("writing SKILL.md", () => {
  it("quotes free text so a colon cannot break the frontmatter", () => {
    const fm = frontmatter({
      name: "invoice-matcher",
      display_name: "Invoice Matcher",
      description: "Match payments: amount, date and reference",
      category: "FINANCE",
      icon: "receipt",
      credential_requirements: ["FAKTUROID_TOKEN"],
    })
    expect(fm).toBe(
      [
        "---",
        "name: invoice-matcher",
        'display_name: "Invoice Matcher"',
        'description: "Match payments: amount, date and reference"',
        "category: FINANCE",
        "icon: receipt",
        "credential_requirements:",
        "  - FAKTUROID_TOKEN",
        "---",
      ].join("\n"),
    )
  })

  it("leaves out empty keys", () => {
    expect(frontmatter({ name: "x", icon: null, description: "  ", tags: [] })).toBe("---\nname: x\n---")
  })

  it("puts the body after the frontmatter", () => {
    expect(skillMarkdown({ name: "x" }, "\n# X\n")).toBe("---\nname: x\n---\n\n# X\n")
  })

  it("slugifies the way the importer does", () => {
    expect(slugifySkill("  Invoice Matcher — v2 ")).toBe("invoice-matcher-v2")
    expect(slugifySkill("Účetní kontrola")).toBe("ucetni-kontrola")
  })
})

describe("patchFrontmatter", () => {
  const generated = "---\nname: Invoice matcher\ndescription: Match payments\ncategory: DATA\ntags:\n  - money\n---\n# Invoice matcher\n"

  it("replaces keys that are there and adds the ones that are not", () => {
    const out = patchFrontmatter(generated, { name: "invoice-matcher", category: "FINANCE", icon: "receipt" })
    expect(out).toBe("---\nname: invoice-matcher\ndescription: Match payments\ncategory: FINANCE\ntags:\n  - money\nicon: receipt\n---\n# Invoice matcher\n")
  })

  it("replaces a list key with its items", () => {
    const out = patchFrontmatter(generated, { tags: "finance" })
    expect(out).toContain("tags: finance\n---")
    expect(out).not.toContain("- money")
  })

  it("wraps a body that has no frontmatter", () => {
    expect(patchFrontmatter("# Body", { name: "x", icon: "box" })).toBe("---\nname: x\nicon: box\n---\n\n# Body\n")
  })
})

describe("usageByDay", () => {
  const now = new Date(2026, 9, 9, 15, 0)
  const at = (d: number, h = 10) => new Date(2026, 9, d, h).toISOString()

  it("counts seven local days ending today, and the failed uses", () => {
    const days = usageByDay(
      [
        { ts: at(9), entry_type: "skill.invoked", payload: { exit_code: 0 } },
        { ts: at(9, 11), entry_type: "skill.invoked", payload: { exit_code: 2 } },
        { ts: at(3), entry_type: "skill.invoked" },
        { ts: at(2), entry_type: "skill.invoked" },
        { ts: at(9), entry_type: "skill.assigned" },
      ],
      now,
    )
    expect(days).toHaveLength(7)
    expect(days[0].date).toBe("2026-10-03")
    expect(days[6]).toMatchObject({ date: "2026-10-09", uses: 2, errors: 1 })
    expect(days[0].uses).toBe(1)
    expect(days.reduce((n, d) => n + d.uses, 0)).toBe(3)
  })
})

describe("skill icons", () => {
  it("draws the skill's own icon, mapping lucide names onto the catalog", () => {
    expect(skillIconName("terminal")).toBe("terminal")
    expect(skillIconName("code-2")).toBe("code")
    expect(skillIconName("swatch-book")).toBe("palette")
  })

  it("falls back to the domain icon when it has none or an unknown one", () => {
    expect(skillIconName(null)).toBeNull()
    expect(skillIconName("not-an-icon")).toBeNull()
    expect(skillIcon({ icon: "not-an-icon", category: "CODING" })).toBe(skillIcon({ icon: null, category: "CODING" }))
    expect(skillIcon({ icon: "terminal", category: "CODING" })).not.toBe(skillIcon({ icon: null, category: "CODING" }))
  })

  it("decodes tags without throwing", () => {
    expect(skillTags({ tags: '["a","b"]' })).toEqual(["a", "b"])
    expect(skillTags({ tags: "nope" })).toEqual([])
    expect(skillTags({})).toEqual([])
  })
})
