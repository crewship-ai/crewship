import { describe, expect, it } from "vitest"

import fixture from "@/testdata/ask-templates.json"
import {
  MAX_FIELDS_PER_FORM,
  MAX_FORMS,
  MAX_LABEL_RUNES,
  MAX_MESSAGE_RUNES,
  MAX_TEMPLATE_RUNES,
  MAX_VALUE_RUNES,
  parseAskForms,
  renderAskTemplate,
  summarizeAskForms,
  type AskForm,
  type AskValues,
} from "@/lib/ask-template"

/**
 * The TypeScript half of the shared golden fixture.
 *
 * testdata/ask-templates.json is read here and, byte for byte, by
 * internal/askforms/render_test.go. Both renderers produce the message the
 * user actually sends — this one renders the preview and the outgoing text in
 * the composer, the Go one renders the same template for the server and for
 * `crewship agent ask-preview` — so a rule implemented in one and not the
 * other is a message that differs from the preview the user approved.
 *
 * The import goes through the `@/` alias, the same way lib/routine-dsl-schema.ts
 * reads schemas/routine.v1.json. Vitest resolves it via resolve.alias in
 * vitest.config.ts, so the file on disk is the only copy either language sees.
 */

type Directive = { $repeat?: string; $count?: number; $concat?: unknown[] }

/** Resolves the two size directives the fixture uses so a 32 000-rune
 *  expectation does not have to be typed out. Mirrored exactly by
 *  expandFixture in internal/askforms/render_test.go. */
function expand(node: unknown): unknown {
  if (Array.isArray(node)) return node.map(expand)
  if (node && typeof node === "object") {
    const d = node as Directive
    if (typeof d.$repeat === "string") return d.$repeat.repeat(d.$count ?? 0)
    if (Array.isArray(d.$concat)) return d.$concat.map((p) => expand(p) as string).join("")
    const out: Record<string, unknown> = {}
    for (const [k, v] of Object.entries(node as Record<string, unknown>)) out[k] = expand(v)
    return out
  }
  return node
}

interface FixtureCase {
  name: string
  note?: string
  chatId: string
  form: unknown
  values: Record<string, unknown>
  want: unknown
}

const cases = fixture.cases as unknown as FixtureCase[]

describe("renderAskTemplate — the shared golden fixture", () => {
  it("has cases to run", () => {
    // A fixture that silently stopped loading would turn every assertion
    // below into a vacuous pass, which is worse than a red run.
    expect(cases.length).toBeGreaterThan(10)
  })

  it.each(cases.map((c) => [c.name, c] as [string, FixtureCase]))("%s", (_name, c) => {
    const form = expand(c.form) as AskForm
    const values = expand(c.values ?? {}) as AskValues
    const want = expand(c.want) as string

    expect(renderAskTemplate(form, values, c.chatId), c.note).toBe(want)
  })
})

describe("the caps are a cross-language contract", () => {
  // A cap that moves in TypeScript and not in Go is a message that differs
  // between the preview the user approved and the text that was sent.
  it.each([
    ["maxForms", MAX_FORMS],
    ["maxFieldsPerForm", MAX_FIELDS_PER_FORM],
    ["maxLabelRunes", MAX_LABEL_RUNES],
    ["maxTemplateRunes", MAX_TEMPLATE_RUNES],
    ["maxValueRunes", MAX_VALUE_RUNES],
    ["maxMessageRunes", MAX_MESSAGE_RUNES],
  ] as [keyof typeof fixture.limits, number][])("%s matches the fixture", (key, got) => {
    expect(fixture.limits[key]).toBe(got)
  })
})

describe("parseAskForms", () => {
  it("treats unset as no forms, not as an error", () => {
    for (const raw of [null, undefined, "", "   "]) {
      expect(parseAskForms(raw)).toEqual({ forms: [] })
    }
  })

  it("reports malformed JSON instead of throwing", () => {
    const { forms, error } = parseAskForms("[{")
    expect(forms).toEqual([])
    expect(error).toBeTruthy()
  })

  it("refuses anything that is not an array of forms", () => {
    expect(parseAskForms('{"id":"receipt"}').error).toMatch(/array/)
  })

  it("reads a real definition", () => {
    const { forms, error } = parseAskForms(
      '[{"id":"receipt","label":"Add a receipt","template":"{{a}}","fields":[{"name":"a","label":"A","type":"text"}]}]',
    )
    expect(error).toBeUndefined()
    expect(forms).toHaveLength(1)
    expect(forms[0].id).toBe("receipt")
  })
})

describe("summarizeAskForms", () => {
  const form = (id: string, fields: number) =>
    JSON.stringify({
      id,
      label: id,
      template: "{{a}}",
      fields: Array.from({ length: fields }, (_, i) => ({
        name: `f${i}`,
        label: `F${i}`,
        type: "text",
      })),
    })

  it("counts forms and fields for the editor", () => {
    const raw = `[${form("a", 2)},${form("b", 3)}]`
    expect(summarizeAskForms(raw)).toMatchObject({ forms: 2, fields: 5, tooManyForms: false })
  })

  it("marks a list over the form cap", () => {
    const raw = `[${form("a", 1)},${form("b", 1)},${form("c", 1)},${form("d", 1)},${form("e", 1)}]`
    expect(summarizeAskForms(raw).tooManyForms).toBe(true)
  })

  it("names the form that has too many fields", () => {
    expect(summarizeAskForms(`[${form("receipt", MAX_FIELDS_PER_FORM + 1)}]`).overFullForms).toEqual([
      "receipt",
    ])
  })

  it("says nothing is configured when nothing is", () => {
    expect(summarizeAskForms(null)).toMatchObject({ forms: 0, fields: 0 })
  })
})
