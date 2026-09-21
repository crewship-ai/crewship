import { expect, it } from "vitest"
import { approvalSteps } from "../routine-approval-steps"

it("preserves exact paths through nested loops and hooks, excluding non-approval waits", () => {
  const wait = { id: "decision", type: "wait", wait: { kind: "approval" } }
  const loop = { type: "foreach", foreach: { steps: [wait] } }
  const def = { steps: [null, { type: "wait", wait: { kind: "event" } }, { type: "foreach", foreach: { steps: [loop] } }], hooks: { on_failure: wait } }
  expect(approvalSteps(def).map((s) => s.path)).toEqual([
    ["steps", 2, "foreach", "steps", 0, "foreach", "steps", 0], ["hooks", "on_failure"],
  ])
})
