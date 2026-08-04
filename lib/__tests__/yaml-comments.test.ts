import { describe, it, expect } from "vitest"

import { hasYamlComments } from "@/lib/routine-dsl-format"

// The editor warned, permanently, that YAML comments do not survive a
// save. True — canonical JSON is what gets stored — but a warning that
// is always on screen is chrome, and chrome is what people stop
// reading. It matters exactly when someone has written a comment, so
// that is when it appears.
//
// Which makes the detection the interesting part: a `#` inside a prompt
// is not a comment, and warning about one would train the reader to
// dismiss the warning that counts.

describe("hasYamlComments", () => {
  it("finds a whole-line comment", () => {
    expect(hasYamlComments("# why this exists\nname: demo\n")).toBe(true)
  })

  it("finds an indented comment", () => {
    expect(hasYamlComments("steps:\n  # the slow one\n  - id: a\n")).toBe(true)
  })

  it("finds a trailing comment after a value", () => {
    expect(hasYamlComments("name: demo # temporary\n")).toBe(true)
  })

  it("does not flag a # inside a quoted string", () => {
    expect(hasYamlComments('prompt: "reply with #done when finished"\n')).toBe(false)
  })

  it("does not flag a # inside a block scalar", () => {
    // The case that matters most: agent prompts are full of markdown
    // headings, and every one of them starts with #.
    expect(hasYamlComments("prompt: |\n  ## Change plan\n  Restart the pods.\n")).toBe(false)
  })

  it("is false for a document with none", () => {
    expect(hasYamlComments("name: demo\nsteps:\n  - id: a\n    type: http\n")).toBe(false)
  })

  it("never throws on garbage", () => {
    expect(() => hasYamlComments("")).not.toThrow()
    expect(() => hasYamlComments("\t\t{{{")).not.toThrow()
  })
})
