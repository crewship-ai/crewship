import { describe, it, expect } from "vitest"
import { readFileSync } from "node:fs"
import { join } from "node:path"

// =============================================================================
// The field-type verdict and the answer constraints, read from the SAME
// fixture internal/askforms/fieldtypes_test.go reads.
//
// Not belt-and-braces: one rule, enforced at two different moments. Go decides
// what may be SAVED, this decides what a user may SUBMIT, and a disagreement
// between them is precisely the defect P0.7 describes — a definition the
// server accepted and the sheet then mishandled. The fixture is the only
// statement of the rule; both suites go red when it moves.
// =============================================================================

import {
  classifyAskFieldType,
  KNOWN_ASK_FIELD_TYPES,
  MAX_ASK_TYPE_RUNES,
  ASK_TYPE_SHAPE,
  validateAskAnswers,
  isSafeAskFieldType,
} from "@/lib/ask-validate"
import type { AskForm, AskFormField, AskValues } from "@/lib/ask-template"

interface TypeCase {
  type: string
  verdict: string
  reason?: string
}
interface AnswerCase {
  name: string
  field: AskFormField
  value: AskValues[string]
  errors: { code: string; message: string }[]
}

const fixture = JSON.parse(
  readFileSync(join(process.cwd(), "testdata", "ask-field-types.json"), "utf8"),
) as {
  type_shape: string
  max_type_runes: number
  types: TypeCase[]
  answers: AnswerCase[]
}

function formOf(field: AskFormField): AskForm {
  return {
    id: "fixture",
    label: "Fixture",
    template: `{{${field.name}}}`,
    fields: [field],
  }
}

describe("ask field types", () => {
  it("states the same shape and cap the Go validator enforces", () => {
    expect(ASK_TYPE_SHAPE.source).toBe(fixture.type_shape)
    expect(MAX_ASK_TYPE_RUNES).toBe(fixture.max_type_runes)
  })

  it.each(fixture.types)("classifies $type as $verdict", (tc) => {
    const got = classifyAskFieldType(tc.type)
    expect(got.verdict).toBe(tc.verdict)
    expect(got.reason).toBe(tc.reason ?? "")
  })

  // The open list is the property being PRESERVED, not the one being removed:
  // a type this release never heard of still renders, which is what lets the
  // server ship a field type without a coordinated frontend release.
  it("keeps the list open for a type it has never heard of", () => {
    expect(classifyAskFieldType("quantum-flux").verdict).toBe("open")
    expect(isSafeAskFieldType("quantum-flux")).toBe(true)
  })

  it("fails closed on anything that names a secret", () => {
    for (const type of ["secret", "password", "api_key", "client_secret", "otp"]) {
      expect(isSafeAskFieldType(type)).toBe(false)
    }
  })

  it("lists exactly the types the sheet has a control for", () => {
    const known = fixture.types.filter((t) => t.verdict === "known").map((t) => t.type)
    expect([...KNOWN_ASK_FIELD_TYPES].sort()).toEqual(known.sort())
  })
})

describe("ask answer constraints", () => {
  it.each(fixture.answers)("$name", (tc) => {
    const errors = validateAskAnswers(formOf(tc.field), { [tc.field.name]: tc.value })

    expect(errors.map((e) => ({ code: e.code, message: e.message }))).toEqual(tc.errors)
    for (const e of errors) {
      // A message that does not name the field is a message the user cannot
      // act on — six inputs on screen and "must be at least 3 characters"
      // names none of them.
      expect(e.message).toContain(tc.field.label)
      expect(e.field).toBe(tc.field.name)
    }
  })

  it("reports one problem per field, and every field that has one", () => {
    const form: AskForm = {
      id: "f",
      label: "F",
      template: "{{a}} {{b}} {{c}}",
      fields: [
        { name: "a", label: "A", type: "text", required: true },
        { name: "b", label: "B", type: "number", min: 10 },
        { name: "c", label: "C", type: "text" },
      ],
    }
    const errors = validateAskAnswers(form, { a: "", b: "2", c: "fine" })
    expect(errors.map((e) => e.field)).toEqual(["a", "b"])
  })

  it("says nothing about a field the form does not have", () => {
    const form = formOf({ name: "a", label: "A", type: "text" })
    expect(validateAskAnswers(form, { a: "ok", stray: "ignored" })).toEqual([])
  })
})
