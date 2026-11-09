import { describe, expect, it } from "vitest"
import { codeTokens, highlightable, MAX_HIGHLIGHT_CHARS } from "../document-code-blocks"

describe("codeTokens", () => {
  it.each([
    ["python", "def f(x):\n    return x + 1  # one\n", ["def", "f", "x", "return", "1", "# one"]],
    ["go", "package main\n\nfunc main() { println(\"hi\") }", ["package", "func", "main", "\"hi\""]],
    ["ts", "const n: number = 42", ["const", "number", "42"]],
    ["sql", "SELECT id FROM runs WHERE n > 3;", ["SELECT", "FROM", "WHERE", "3"]],
    ["yml", "name: crew\ncount: 2", ["name", "crew", "count", "2"]],
    ["sh", "echo \"$HOME\" | wc -l", ["echo", "$HOME"]],
    ["json", "{\"a\": [1, true, null]}", ["\"a\"", "1", "true", "null"]],
  ])("covers %s with in-bounds runs of hljs classes", (language, text, expected) => {
    const tokens = codeTokens(text, language)
    expect(tokens.length).toBeGreaterThan(0)
    let last = 0
    for (const token of tokens) {
      expect(token.from).toBeGreaterThanOrEqual(last)
      expect(token.to).toBeGreaterThan(token.from)
      expect(token.to).toBeLessThanOrEqual(text.length)
      expect(token.className).toMatch(/^hljs-[a-z_-]+( hljs-[a-z_-]+)*$/)
      last = token.to
    }
    const covered = tokens.map(token => text.slice(token.from, token.to))
    for (const word of expected) expect(covered.some(run => run.includes(word))).toBe(true)
  })

  it.each([
    ["no language", "x = 1", null],
    ["an unknown language", "x = 1", "brainfork"],
    ["plain text", "x = 1", "plaintext"],
    ["a language name that is not a string", "x = 1", 7],
    ["empty text", "", "python"],
    ["text over the limit", "x".repeat(MAX_HIGHLIGHT_CHARS + 1), "python"],
  ])("returns no runs for %s", (_name, text, language) => {
    expect(codeTokens(text as string, language)).toEqual([])
  })

  it("knows the aliases a block may store", () => {
    for (const name of ["ts", "js", "py", "sh", "yml", "html", "c++", "golang"]) expect(highlightable(name)).toBe(true)
    expect(highlightable("latex")).toBe(false)
  })
})
