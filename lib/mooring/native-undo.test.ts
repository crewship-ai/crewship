// @vitest-environment node
import { expect, it, vi } from "vitest"
import { registerSheetUndo, requestSheetUndo } from "./native-undo"

it("asks the editor host of that Sheet, and says when none is open", async () => {
  await expect(requestSheetUndo("pg", "op")).resolves.toBe("no_editor")
  const answer = new Response("{}", { status: 201 }), handler = vi.fn(async () => answer)
  const off = registerSheetUndo("pg", handler)
  await expect(requestSheetUndo("pg", "op")).resolves.toBe(answer)
  expect(handler).toHaveBeenCalledWith("op")
  await expect(requestSheetUndo("other", "op")).resolves.toBe("no_editor")
  off()
  await expect(requestSheetUndo("pg", "op")).resolves.toBe("no_editor")
})
it("an earlier host leaving does not remove a newer one", async () => {
  const first = registerSheetUndo("pg", async () => "not_editing"), second = vi.fn(async () => "not_editing" as const)
  const off = registerSheetUndo("pg", second)
  first(); await requestSheetUndo("pg", "op"); expect(second).toHaveBeenCalled(); off()
})
