import { describe, it, expect, vi, beforeEach } from "vitest"
import { render, screen, fireEvent, cleanup } from "@testing-library/react"

vi.mock("next/navigation", () => ({ useRouter: () => ({ push: vi.fn(), replace: vi.fn() }) }))

import { PageSaveProvider, usePageSave } from "@/components/ui/page-save-bar"
import { CanvasTabs } from "../canvas-base"

const TABS = [
  { id: "overview", label: "Overview" },
  { id: "persona", label: "Instructions" },
] as const

function Draft({ count, discard }: { count: number; discard: () => void }) {
  usePageSave({ label: "Instructions", count, save: () => {}, discard })
  return null
}

function Page({ count, onChange, discard = () => {} }: { count: number; onChange: (id: string) => void; discard?: () => void }) {
  return (
    <PageSaveProvider>
      <Draft count={count} discard={discard} />
      <CanvasTabs tabs={[...TABS]} active="persona" onChange={onChange} idPrefix="t" label="Agent sections" />
    </PageSaveProvider>
  )
}

describe("CanvasTabs with unsaved inline edits", () => {
  beforeEach(() => cleanup())

  it("switches at once when nothing is pending", () => {
    const onChange = vi.fn()
    render(<Page count={0} onChange={onChange} />)
    fireEvent.click(screen.getByRole("tab", { name: "Overview" }))
    expect(onChange).toHaveBeenCalledWith("overview")
    expect(screen.queryByRole("alertdialog")).not.toBeInTheDocument()
  })

  it("asks first; Stay keeps the tab and the edits", () => {
    const onChange = vi.fn()
    const discard = vi.fn()
    render(<Page count={1} onChange={onChange} discard={discard} />)
    fireEvent.click(screen.getByRole("tab", { name: "Overview" }))
    expect(onChange).not.toHaveBeenCalled()
    expect(screen.getByText("Leave with 1 unsaved change?")).toBeInTheDocument()
    fireEvent.click(screen.getByRole("button", { name: "Stay" }))
    expect(onChange).not.toHaveBeenCalled()
    expect(discard).not.toHaveBeenCalled()
  })

  it("Discard drops the edits and then switches", () => {
    const onChange = vi.fn()
    const discard = vi.fn()
    render(<Page count={1} onChange={onChange} discard={discard} />)
    fireEvent.click(screen.getByRole("tab", { name: "Overview" }))
    fireEvent.click(screen.getByRole("button", { name: "Discard" }))
    expect(discard).toHaveBeenCalledTimes(1)
    expect(onChange).toHaveBeenCalledWith("overview")
  })

  it("clicking the open tab never asks", () => {
    const onChange = vi.fn()
    render(<Page count={1} onChange={onChange} />)
    fireEvent.click(screen.getByRole("tab", { name: "Instructions" }))
    expect(screen.queryByText(/unsaved change/)).not.toBeInTheDocument()
  })
})
