import { readFileSync } from "node:fs"
import { expect, it } from "vitest"
import { terminalRoutineRunStatuses } from "../routine-run-status"

it("matches the server's accepted terminal run states", () => {
  const source = readFileSync("internal/pipeline/runs.go", "utf8")
  const terminalCase = source.split("func (s *RunStore) MarkTerminal")[1].match(/case ([^:]+):/)![1]
  const values = terminalCase.split(",").map((name) => {
    const declaration = new RegExp(`${name.trim()}\\s+RunStatus = "([^"]+)"`)
    return source.match(declaration)![1]
  })
  expect([...terminalRoutineRunStatuses].sort()).toEqual(values.sort())
})
