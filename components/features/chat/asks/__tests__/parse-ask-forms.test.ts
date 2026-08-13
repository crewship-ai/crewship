import { describe, it, expect } from "vitest"

// =============================================================================
// askFormsFromColumn sits between a TEXT column and a conversation.
//
// The parse itself is lib/ask-template.ts's, deliberately tolerant because it
// is also the authoring UI's reader — a half-written definition has to survive
// long enough to be corrected there. A CHAT is the other case: the rail is
// drawn on every mount, and a chip that opens an empty sheet is a dead end the
// user cannot get out of. So this layer drops what it cannot render, and its
// one hard requirement is that nothing it does not understand ever reaches the
// UI as anything other than "no forms" — which is exactly how an agent nobody
// has configured behaves.
// =============================================================================

import { askFormsFromColumn, usableAskForms } from "../types"
import type { AskForm } from "../types"

const valid = {
  id: "receipt",
  label: "Add a receipt",
  template: "Zaúčtuj fakturu od {{supplier}}",
  attachment: "required",
  fields: [{ name: "supplier", label: "Supplier", type: "text", required: true }],
}

const column = (forms: unknown[]) => JSON.stringify(forms)

describe("askFormsFromColumn", () => {
  it("reads the documented shape out of the column", () => {
    const [form] = askFormsFromColumn(column([valid]))
    expect(form.id).toBe("receipt")
    expect(form.label).toBe("Add a receipt")
    expect(form.attachment).toBe("required")
    expect(form.fields).toHaveLength(1)
    expect(form.fields[0].name).toBe("supplier")
  })

  it.each([
    ["undefined", undefined],
    ["null", null],
    ["an empty string", ""],
    ["unparseable JSON", "{not json"],
    ["a JSON object rather than an array", '{"forms":[]}'],
    ["a value that is not a string at all", 7],
  ])("answers no forms for %s", (_name, input) => {
    expect(askFormsFromColumn(input)).toEqual([])
  })

  it("keeps a field type it has never heard of, so the renderer can fall back to text", () => {
    const [form] = askFormsFromColumn(
      column([{ ...valid, fields: [{ name: "future", label: "Future", type: "quantum-flux" }] }]),
    )
    expect(form.fields[0].type).toBe("quantum-flux")
  })
})

describe("usableAskForms", () => {
  it("drops the entries a conversation cannot render and keeps the rest", () => {
    const forms = usableAskForms([
      { ...valid, id: "" },
      { ...valid, id: "no-label", label: "  " },
      // Sends an empty message.
      { ...valid, id: "no-template", template: "" },
      // Opens a sheet with nothing in it (PRD §6).
      { ...valid, id: "no-fields", fields: [] },
      { ...valid, id: "junk-fields", fields: [{ name: "" }] },
      { ...valid, id: "dupe" },
      { ...valid, id: "dupe", label: "Second one with the same id" },
    ] as unknown as AskForm[])

    expect(forms.map((f) => f.id)).toEqual(["dupe"])
    // First definition wins — a duplicate id is a bug, and picking the later
    // one would make which chip you get depend on array order.
    expect(forms[0].label).toBe("Add a receipt")
  })

  it("trims a label so a chip cannot be padded into looking wider than it is", () => {
    const [form] = usableAskForms([{ ...valid, label: "  Add a receipt  " }] as unknown as AskForm[])
    expect(form.label).toBe("Add a receipt")
  })

  it("leaves a well-formed list alone", () => {
    const forms = usableAskForms([valid] as unknown as AskForm[])
    expect(forms).toHaveLength(1)
  })
})
