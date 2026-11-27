// A workbook reads as Excel everywhere, also as an attachment in the composer.
import { expect, it } from "vitest"
import { render } from "@testing-library/react"
import { AttachmentChip } from "../attachments"

it("marks an attached workbook in the Sheet colour, and other files as before", () => {
  const xlsx = render(<AttachmentChip attachment={{ id: "a", name: "Plan.xlsx", size: 10, type: "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet", status: "ready" }} />)
  expect(xlsx.container.querySelector("svg")!.getAttribute("class")).toMatch(/\btext-sheet\b/)
  xlsx.unmount()
  const txt = render(<AttachmentChip attachment={{ id: "b", name: "notes.txt", size: 10, type: "text/plain", status: "ready" }} />)
  expect(txt.container.querySelector("svg")!.getAttribute("class")).not.toMatch(/\btext-sheet\b/)
})
