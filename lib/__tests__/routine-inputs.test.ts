import { describe, expect, it } from "vitest"

import {
  RoutineInputError,
  SLASH_ROUTINE_ID_PREFIX,
  coerceRoutineInput,
  formatInputDefault,
  isMissingRequired,
  routineInputSpecs,
  routineInputsFromValues,
  routineSlugFromSlashId,
  slashCommandName,
  slashFieldsFromRoutineInputs,
  widgetForInputType,
} from "@/lib/routine-inputs"

// The pairs in this file are deliberately the same pairs
// internal/api/slash_routine_catalog_test.go asserts. The Go side
// translates a routine's inputs into a form schema and this side
// translates the filled form back; if the two drift, a routine runs with
// values it did not declare. Two test files, one table.

describe("routineSlugFromSlashId", () => {
  it("reads the slug out of a per-routine catalog id", () => {
    expect(routineSlugFromSlashId("routine.run:msn-etn-podklady")).toBe("msn-etn-podklady")
  })

  it("does not mistake the platform /routine command for a run entry", () => {
    // `/routine` SCHEDULES a routine; `routine.run:<slug>` RUNS one.
    // They differ by a character a startsWith("routine") would have run
    // together, and the two POST completely different endpoints.
    expect(routineSlugFromSlashId("routine")).toBeNull()
  })

  it("rejects a prefix with no slug behind it", () => {
    expect(routineSlugFromSlashId(SLASH_ROUTINE_ID_PREFIX)).toBeNull()
  })

  it.each(["issue", "skill", "credential"])("leaves the platform id %s alone", (id) => {
    expect(routineSlugFromSlashId(id)).toBeNull()
  })
})

describe("slashCommandName", () => {
  it("offers a routine under its bare slug", () => {
    expect(slashCommandName("routine.run:msn-etn-podklady")).toBe("msn-etn-podklady")
  })

  it("leaves platform commands typed as they are named", () => {
    expect(slashCommandName("issue")).toBe("issue")
    expect(slashCommandName("routine")).toBe("routine")
  })
})

describe("widgetForInputType", () => {
  it.each([
    ["string", "text"],
    ["integer", "number"],
    ["number", "number"],
    ["boolean", "boolean"],
    ["array", "textarea"],
    ["object", "textarea"],
  ])("draws a %s input as %s", (type, widget) => {
    expect(widgetForInputType(type)).toBe(widget)
  })

  it("falls back to text for an undeclared or unknown type", () => {
    expect(widgetForInputType(undefined)).toBe("text")
    expect(widgetForInputType("")).toBe("text")
    // A type from a DSL newer than this build still draws something the
    // server can validate.
    expect(widgetForInputType("geopoint")).toBe("text")
  })
})

describe("formatInputDefault", () => {
  it.each([
    [42, "42"], // not "42.0"
    [0.5, "0.5"],
    [1000000, "1000000"], // not "1e+06"
    [0, "0"],
    [true, "true"], // not "True"
    [false, "false"],
    ["hello", "hello"],
    [null, ""],
    [undefined, ""],
  ])("renders %p as %p", (input, want) => {
    expect(formatInputDefault(input)).toBe(want)
  })

  it("renders arrays and objects as compact JSON, which is what the textarea parses back", () => {
    expect(formatInputDefault(["a", "b"])).toBe('["a","b"]')
    expect(formatInputDefault({ k: "v" })).toBe('{"k":"v"}')
  })
})

describe("slashFieldsFromRoutineInputs", () => {
  it("translates msn-etn-podklady's three inputs the way the server does", () => {
    // The same expectation as TestSlashCatalog_RoutineEntryShape.
    expect(
      slashFieldsFromRoutineInputs([
        { name: "obdobi", type: "string" },
        { name: "ucetnictvi_root", type: "string", default: "Unify - Účetnictví" },
        { name: "vypis_odesilatel", type: "string", default: "info@rb.cz" },
      ]),
    ).toEqual([
      { name: "obdobi", type: "text", required: false, default: "", value_type: "string", help: undefined },
      {
        name: "ucetnictvi_root",
        type: "text",
        required: false,
        default: "Unify - Účetnictví",
        value_type: "string",
        help: undefined,
      },
      {
        name: "vypis_odesilatel",
        type: "text",
        required: false,
        default: "info@rb.cz",
        value_type: "string",
        help: undefined,
      },
    ])
  })

  it("carries a declared description through as field help", () => {
    const [field] = slashFieldsFromRoutineInputs([
      { name: "obdobi", type: "string", description: "YYYY-MM; empty means last month" },
    ])
    expect(field.help).toBe("YYYY-MM; empty means last month")
  })

  it("drops an input with no name, which no value could be keyed under", () => {
    expect(slashFieldsFromRoutineInputs([{ name: "" }, { name: "kept" }])).toHaveLength(1)
    expect(slashFieldsFromRoutineInputs(undefined)).toEqual([])
    expect(slashFieldsFromRoutineInputs([])).toEqual([])
  })
})

describe("routineInputSpecs", () => {
  it("reads inputs out of a definition", () => {
    expect(routineInputSpecs({ inputs: [{ name: "obdobi", type: "string" }] })).toHaveLength(1)
  })

  it("survives a definition that carries no usable inputs", () => {
    // The definition is a Record<string, unknown> on this side and can be
    // anything; a run button must not throw over it.
    expect(routineInputSpecs(undefined)).toEqual([])
    expect(routineInputSpecs(null)).toEqual([])
    expect(routineInputSpecs({})).toEqual([])
    expect(routineInputSpecs({ inputs: "nope" })).toEqual([])
    expect(routineInputSpecs({ inputs: [null, 42, { noName: true }] })).toEqual([])
  })
})

describe("coerceRoutineInput", () => {
  it("restores each declared type from the string a form holds", () => {
    expect(coerceRoutineInput("string", "hello")).toBe("hello")
    expect(coerceRoutineInput("integer", "42")).toBe(42)
    expect(coerceRoutineInput("number", "0.5")).toBe(0.5)
    expect(coerceRoutineInput("boolean", "true")).toBe(true)
    expect(coerceRoutineInput("boolean", "")).toBe(false)
    expect(coerceRoutineInput("array", '["a","b"]')).toEqual(["a", "b"])
    expect(coerceRoutineInput("object", '{"k":"v"}')).toEqual({ k: "v" })
  })

  it("returns an integer as a number, never as the string that was typed", () => {
    // The whole reason this module exists: a `code` step sees inputs
    // with their original types, so `inputs.limit > 20` fails the run
    // when 42 arrives as the string "42".
    expect(typeof coerceRoutineInput("integer", "42")).toBe("number")
  })

  it("refuses an integer too large to send exactly", () => {
    // Number("9007199254740993") is 9007199254740992. The routine would
    // run on a value nobody typed and nothing downstream would say so —
    // and an integer input holding an account id or an invoice number is
    // exactly where that lands. The repl agrees: strconv.ParseInt errors
    // on overflow rather than rounding.
    expect(() => coerceRoutineInput("integer", "9007199254740993", "invoice")).toThrow(
      RoutineInputError,
    )
    expect(() => coerceRoutineInput("integer", "9007199254740993", "invoice")).toThrow(
      /too large to send exactly/,
    )
    // The boundary itself still goes through.
    expect(coerceRoutineInput("integer", String(Number.MAX_SAFE_INTEGER))).toBe(
      Number.MAX_SAFE_INTEGER,
    )
    expect(coerceRoutineInput("integer", String(-Number.MAX_SAFE_INTEGER))).toBe(
      -Number.MAX_SAFE_INTEGER,
    )
  })

  it("treats an unknown or absent value_type as a string", () => {
    // Every field in the static slash catalog, and every field from a
    // server older than this build.
    expect(coerceRoutineInput(undefined, "7")).toBe("7")
    expect(coerceRoutineInput("", "7")).toBe("7")
    expect(coerceRoutineInput("geopoint", "7")).toBe("7")
  })

  it("accepts the checkbox encoding for a boolean", () => {
    // FormField emits "true"/"" — the empty string is what an unticked
    // box sends, and it has to mean false rather than throw.
    expect(coerceRoutineInput("boolean", "true")).toBe(true)
    expect(coerceRoutineInput("boolean", "false")).toBe(false)
  })

  // The repl and this file must accept the same words. A user is told one
  // command; `/pack dry=yes` cannot work here and error in the shell. The
  // mirror of this table is
  // TestCoerceRoutineInput_BooleanVocabularyMatchesTheBrowser.
  it.each(["true", "TRUE", "True", "1", "yes", "YES", "on", " true "])(
    "reads %s as true, exactly as the repl does",
    (raw) => {
      expect(coerceRoutineInput("boolean", raw)).toBe(true)
    },
  )
  it.each(["false", "0", "no", "off", "", "  "])(
    "reads %s as false, exactly as the repl does",
    (raw) => {
      expect(coerceRoutineInput("boolean", raw)).toBe(false)
    },
  )
  it.each(["t", "f", "maybe"])("rejects %s in both clients", (raw) => {
    // strconv.ParseBool accepts "t"/"T". The repl no longer does, because
    // this side never did — rejecting in both beats accepting in one.
    expect(() => coerceRoutineInput("boolean", raw)).toThrow(RoutineInputError)
  })

  it.each([
    ["integer", "banana", "not a whole number"],
    ["integer", "4.5", "not a whole number"],
    ["number", "lots", "not a number"],
    ["boolean", "maybe", "not true or false"],
    ["array", '["a",', "not valid JSON"],
    ["object", "{", "not valid JSON"],
    // Valid JSON of the wrong shape. JSON.parse accepts these, so
    // without the shape check they would sail through to the server and
    // fail somewhere less legible.
    ["array", '{"k":"v"}', "not an array"],
    ["object", "42", "not an object"],
    ["object", '["a"]', "not an object"],
  ])("refuses a %s field holding %s", (type, raw, message) => {
    expect(() => coerceRoutineInput(type, raw, "obdobi")).toThrow(RoutineInputError)
    // The message names the field — a form with six inputs and an error
    // that says only "not a number" is not usable.
    expect(() => coerceRoutineInput(type, raw, "obdobi")).toThrow(/obdobi/)
    expect(() => coerceRoutineInput(type, raw, "obdobi")).toThrow(new RegExp(message))
  })
})

describe("isMissingRequired", () => {
  it("treats an empty required text field as missing", () => {
    const [field] = slashFieldsFromRoutineInputs([{ name: "obdobi", type: "string", required: true }])
    expect(isMissingRequired(field, "")).toBe(true)
    expect(isMissingRequired(field, "   ")).toBe(true)
    expect(isMissingRequired(field, "2026-07")).toBe(false)
  })

  it("never treats a required boolean as missing", () => {
    // A checkbox emits "" when unticked. A blank-string check would
    // report "required" until it was TICKED, leaving no way at all to
    // submit the answer `false` — half of what a boolean is for.
    const [field] = slashFieldsFromRoutineInputs([{ name: "confirm", type: "boolean", required: true }])
    expect(isMissingRequired(field, "")).toBe(false)
    expect(isMissingRequired(field, "true")).toBe(false)
  })

  it("ignores optional fields", () => {
    const [field] = slashFieldsFromRoutineInputs([{ name: "obdobi", type: "string" }])
    expect(isMissingRequired(field, "")).toBe(false)
  })
})

describe("routineInputsFromValues", () => {
  const msnFields = slashFieldsFromRoutineInputs([
    { name: "obdobi", type: "string" },
    { name: "ucetnictvi_root", type: "string", default: "Unify - Účetnictví" },
    { name: "vypis_odesilatel", type: "string", default: "info@rb.cz" },
  ])

  it("builds the inputs map for the headline case", () => {
    expect(
      routineInputsFromValues(msnFields, {
        obdobi: "2026-07",
        ucetnictvi_root: "Unify - Účetnictví",
        vypis_odesilatel: "info@rb.cz",
      }),
    ).toEqual({
      obdobi: "2026-07",
      ucetnictvi_root: "Unify - Účetnictví",
      vypis_odesilatel: "info@rb.cz",
    })
  })

  it("omits an empty value rather than sending an empty string", () => {
    // The routine's own default then applies server-side, which is the
    // only place that knows what it is: an empty `obdobi` means "the
    // previous month", and sending "" would replace that with a blank.
    const got = routineInputsFromValues(msnFields, {
      obdobi: "",
      ucetnictvi_root: "Unify - Účetnictví",
      vypis_odesilatel: "",
    })
    expect(got).toEqual({ ucetnictvi_root: "Unify - Účetnictví" })
    expect("obdobi" in got).toBe(false)
  })

  it("round-trips every type onto the wire as itself", () => {
    const fields = slashFieldsFromRoutineInputs([
      { name: "count", type: "integer" },
      { name: "rate", type: "number" },
      { name: "flag", type: "boolean" },
      { name: "tags", type: "array" },
      { name: "opts", type: "object" },
    ])
    const inputs = routineInputsFromValues(fields, {
      count: "42",
      rate: "0.5",
      flag: "true",
      tags: '["a","b"]',
      opts: '{"k":"v"}',
    })
    expect(inputs).toEqual({
      count: 42,
      rate: 0.5,
      flag: true,
      tags: ["a", "b"],
      opts: { k: "v" },
    })
    // And it has to survive JSON.stringify, not just the object: 42 goes
    // out as 42, never as "42".
    expect(JSON.stringify({ inputs })).toBe(
      '{"inputs":{"count":42,"rate":0.5,"flag":true,"tags":["a","b"],"opts":{"k":"v"}}}',
    )
  })

  it("passes an undeclared value through as a string", () => {
    // The user supplied it deliberately, and the server rejecting an
    // input the routine doesn't declare says so better than a silent
    // client-side drop.
    expect(routineInputsFromValues(msnFields, { typo_field: "x" })).toEqual({ typo_field: "x" })
  })

  it("throws with the offending field named, so the form can point at it", () => {
    const fields = slashFieldsFromRoutineInputs([{ name: "limit", type: "integer" }])
    expect(() => routineInputsFromValues(fields, { limit: "banana" })).toThrow(RoutineInputError)
    try {
      routineInputsFromValues(fields, { limit: "banana" })
    } catch (e) {
      expect((e as RoutineInputError).field).toBe("limit")
    }
  })

  it("survives a schema it was given none of", () => {
    expect(routineInputsFromValues(undefined, { a: "1" })).toEqual({ a: "1" })
  })
})
