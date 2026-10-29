import { describe, expect, it, vi } from "vitest"
import { readFileSync } from "node:fs"
import { join } from "node:path"
import { validateAskAnswers } from "@/lib/ask-validate"
import type { AskForm } from "@/lib/ask-template"

// Capture the browser engine before installing the trap. Throw BEFORE it can
// compile an author's source, so even a long near-match cannot hang a red run.
function trapNativePattern(pattern: string) {
  const Native = globalThis.RegExp
  const calls: string[] = []
  const inspect = (source: string) => {
    if (source === pattern || source === `^(?:${pattern})$` || source === `^${pattern}$` || (pattern.length > 3 && source.includes(pattern))) {
      calls.push(source)
      throw new Error("author pattern reached native RegExp")
    }
  }
  // No dynamic native constructor is needed while validating answers. Trap
  // all constructor calls, including transformed or escaped author sources.
  const inspectConstruction = (source: string) => {
    calls.push(source)
    throw new Error("author pattern reached native RegExp")
  }
  const originalExec = Native.prototype.exec
  const exec = vi.spyOn(Native.prototype, "exec").mockImplementation(function (this: RegExp, value: string) {
    inspect(this.source)
    return originalExec.call(this, value)
  })
  globalThis.RegExp = new Proxy(Native, {
    construct(target, args) {
      inspectConstruction(typeof args[0] === "string" ? args[0] : args[0]?.source ?? "")
      return Reflect.construct(target, args, target)
    },
    apply(target, thisArg, args) {
      inspectConstruction(typeof args[0] === "string" ? args[0] : args[0]?.source ?? "")
      return Reflect.apply(target, thisArg, args)
    },
  })
  return { calls, restore: () => { globalThis.RegExp = Native; exec.mockRestore() } }
}

const sharedCases = JSON.parse(readFileSync(join(process.cwd(), "testdata", "ask-patterns.json"), "utf8")) as {
  name: string
  pattern: string
  value: string
  definition_valid: boolean
  matches: boolean
}[]

const cases = [
  { name: "ordinary whole-value pattern", pattern: "CZ[0-9]{8}", yes: "CZ25788001", no: "xxCZ25788001xx" },
  { name: "nested quantifiers", pattern: "(a+)+$", yes: "a".repeat(4096), no: "a".repeat(4096) + "!" },
  { name: "ambiguous alternatives", pattern: "(a|aa)+$", yes: "a".repeat(4096), no: "a".repeat(4096) + "!" },
  { name: "Go inline flags", pattern: "(?i)crewship", yes: "CREWSHIP", no: "other" },
  { name: "Go Unicode script", pattern: "\\p{Greek}+", yes: "Ελλάδα", no: "Crewship" },
  { name: "Go POSIX class", pattern: "[[:alpha:]]+", yes: "Crewship", no: "1234" },
  { name: "astral runes", pattern: "🚢+", yes: "🚢🚢", no: "🚢x" },
] as const

function formOf(pattern: string, type = "text"): AskForm {
  return { id: "safe-pattern", label: "Pattern form", template: "{{answer}}", fields: [
    { name: "answer", label: "Answer", type, pattern },
  ] }
}

describe("Ask author patterns stay outside the native JavaScript regex engine", () => {
  it("the guard stops constructor calls and execution of an already compiled pattern", () => {
    const pattern = "(guard_a+)+$"
    const compiledBeforeGuard = new RegExp(`^(?:${pattern})$`)
    const trap = trapNativePattern(pattern)
    try {
      expect(() => new RegExp(pattern)).toThrow("author pattern reached native RegExp")
      expect(() => compiledBeforeGuard.test("guard_a")).toThrow("author pattern reached native RegExp")
    } finally { trap.restore() }
    expect(trap.calls).toHaveLength(2)
  })

  it.each(sharedCases)("defers shared Go corpus pattern to server: $name", ({ pattern, value }) => {
    const trap = trapNativePattern(pattern)
    let errors: ReturnType<typeof validateAskAnswers>
    try { errors = validateAskAnswers(formOf(pattern), { answer: value }) }
    finally { trap.restore() }
    expect(trap.calls, "author regex source crossed the native engine boundary").toEqual([])
    expect(errors!).toEqual([])
  })

  it.each(cases)("$name defers matching and nonmatching values without native compilation or execution", ({ pattern, yes, no }) => {
    const trap = trapNativePattern(pattern)
    let accepted: ReturnType<typeof validateAskAnswers>
    let refused: ReturnType<typeof validateAskAnswers>
    try {
      accepted = validateAskAnswers(formOf(pattern), { answer: yes })
      refused = validateAskAnswers(formOf(pattern), { answer: no })
    } finally { trap.restore() }
    expect(trap.calls, "author regex source crossed the native engine boundary").toEqual([])
    expect(accepted!).toEqual([])
    expect(refused!).toEqual([])
  })

  it.each(["textarea", "quantum-flux"])("keeps the same boundary for %s fields", (type) => {
    const pattern = "(a+)+$"
    const trap = trapNativePattern(pattern)
    let errors: ReturnType<typeof validateAskAnswers>
    try { errors = validateAskAnswers(formOf(pattern, type), { answer: "a".repeat(4096) + "!" }) }
    finally { trap.restore() }
    expect(trap.calls).toEqual([])
    expect(errors!).toEqual([])
  })

  it("keeps an uncompilable legacy pattern answerable without native fallback", () => {
    const pattern = "legacy["
    const trap = trapNativePattern(pattern)
    let errors: ReturnType<typeof validateAskAnswers>
    try { errors = validateAskAnswers(formOf(pattern), { answer: "legacy answer" }) }
    finally { trap.restore() }
    expect(trap.calls).toEqual([])
    expect(errors!).toEqual([])
  })

  it("keeps empty optional answers optional", () => {
    expect(validateAskAnswers(formOf("(a+)+$"), { answer: "" })).toEqual([])
  })
})
