// A workbook the preview refuses for its size is offered as a Sheet instead.
import { expect, it } from "vitest"
import { render, screen } from "@testing-library/react"
import { XlsxPreview } from "../xlsx-preview"
import { XLSX_LIMITS } from "../xlsx-limits"

it("offers the workbook as a Sheet beside its refusal", async () => {
  const bytes = new Uint8Array(XLSX_LIMITS.compressedBytes + 1)
  render(<XlsxPreview bytes={bytes} path="big.xlsx" offer={<button type="button">Open as a Sheet</button>} />)
  expect(await screen.findByRole("alert")).toHaveTextContent("exceeds")
  expect(screen.getByRole("button", { name: "Open as a Sheet" })).toBeInTheDocument()
})
