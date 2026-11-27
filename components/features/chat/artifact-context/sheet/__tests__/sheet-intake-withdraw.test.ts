// A workbook opened as a Sheet showed twice in the composer (its attachment chip and the
// Sheet's chip) and the message told the agent about the file as well. Once it is a Sheet, the attachment goes.
import { beforeEach, expect, it } from "vitest"
import { useComposerStore } from "@/stores/composer-store"
import { withdrawSheetAttachment } from "../sheet-intake"

const KEY = "scope:chat-1"
const chip = (id: string, name: string, size: number) => ({ id, name, size, type: "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet", status: "ready" as const, path: `attachments/chat-1/${name}` })
beforeEach(() => { useComposerStore.setState({ attachments: { [KEY]: [chip("a", "Plan.xlsx", 10), chip("b", "notes.txt", 3), chip("c", "Plan.xlsx", 99)] } } as never) })

it("removes the attachment of the workbook that became a Sheet, and nothing else", () => {
  expect(withdrawSheetAttachment(KEY, new File([new Uint8Array(10)], "Plan.xlsx"))).toBe(true)
  expect(useComposerStore.getState().attachments[KEY]!.map(a => a.id)).toEqual(["b", "c"])
})
it("leaves the attachments alone when none is that file", () => {
  expect(withdrawSheetAttachment(KEY, new File([new Uint8Array(5)], "Other.xlsx"))).toBe(false)
  expect(useComposerStore.getState().attachments[KEY]!.map(a => a.id)).toEqual(["a", "b", "c"])
})

// The agent may lose nothing. Only a Sheet created in a chat is referenced by it and kept in
// agent_files; one created before the chat existed is not, so its attachment stays.
it("replaces the attachment only when the Sheet was created in this chat", async () => {
  const { sheetReplacesAttachment } = await import("../sheet-intake")
  expect(sheetReplacesAttachment({ attached: true }, "chat-1")).toBe(true)
  expect(sheetReplacesAttachment({ attached: true }, undefined)).toBe(false)
  expect(sheetReplacesAttachment({ attached: false }, "chat-1"), "nothing to replace").toBe(false)
})
