import { describe, it, expect, vi, beforeEach } from "vitest"
import { render, screen, fireEvent, cleanup, act, waitFor } from "@testing-library/react"
import { useState } from "react"

const push = vi.fn()
vi.mock("next/navigation", () => ({ useRouter: () => ({ push }) }))
const toastError = vi.fn()
vi.mock("sonner", () => ({ toast: { error: (...a: unknown[]) => toastError(...a) } }))

import { PageSaveBar, PageSaveProvider, usePageSave, usePageSaveGuard } from "../page-save-bar"
import { SaveFooter } from "../save-footer"
import { SettingsCard, SettingsSaveBar } from "@/components/features/settings/shared"
import type { SaveStatus } from "@/hooks/use-dirty-form"

function Page({ children }: { children: React.ReactNode }) {
  return (
    <PageSaveProvider>
      {children}
      <PageSaveBar />
    </PageSaveProvider>
  )
}

/** A card whose save the test controls. */
function Card({ label, count, save, discard }: { label: string; count: number; save: () => void | Promise<unknown>; discard?: () => void }) {
  usePageSave({ label, count, save, discard: discard ?? (() => {}) })
  return <div>{label}</div>
}

const bar = () => screen.queryByRole("region", { name: "Unsaved changes" })

describe("PageSaveBar", () => {
  beforeEach(() => {
    cleanup()
    push.mockReset()
    toastError.mockReset()
  })

  it("draws nothing while no card has edits", () => {
    render(<Page><Card label="Workspace" count={0} save={vi.fn()} /></Page>)
    expect(bar()).toBeNull()
  })

  it("adds up every card's edits in one bar", () => {
    render(<Page><Card label="Workspace" count={2} save={vi.fn()} /><Card label="Containers" count={1} save={vi.fn()} /></Page>)
    expect(bar()).toHaveTextContent("3 unsaved changes")
  })

  it("Save commits every card with edits, and only those", async () => {
    const a = vi.fn(), b = vi.fn(), c = vi.fn()
    render(<Page><Card label="A" count={1} save={a} /><Card label="B" count={2} save={b} /><Card label="C" count={0} save={c} /></Page>)
    fireEvent.click(screen.getByRole("button", { name: "Save" }))
    await waitFor(() => expect(a).toHaveBeenCalledTimes(1))
    expect(b).toHaveBeenCalledTimes(1)
    expect(c).not.toHaveBeenCalled()
  })

  it("Discard throws every card's draft away", () => {
    const a = vi.fn(), b = vi.fn()
    render(<Page><Card label="A" count={1} save={vi.fn()} discard={a} /><Card label="B" count={1} save={vi.fn()} discard={b} /></Page>)
    fireEvent.click(screen.getByRole("button", { name: "Discard" }))
    expect(a).toHaveBeenCalledTimes(1)
    expect(b).toHaveBeenCalledTimes(1)
  })

  it("a rejected save is a corner toast naming the card, not a message in the page", async () => {
    render(<Page><Card label="Workspace" count={1} save={() => Promise.reject(new Error("Slug is taken."))} /></Page>)
    fireEvent.click(screen.getByRole("button", { name: "Save" }))
    await waitFor(() => expect(toastError).toHaveBeenCalledTimes(1))
    expect(toastError.mock.calls[0][0]).toBe("Couldn’t save Workspace")
    expect(toastError.mock.calls[0][1]).toMatchObject({ description: "Slug is taken. Your edits are kept.", duration: Infinity })
    // The edits are still pending, so the bar stays.
    expect(bar()).toHaveTextContent("1 unsaved change")
  })

  it("⌘S / Ctrl+S saves", async () => {
    const a = vi.fn()
    render(<Page><Card label="A" count={1} save={a} /></Page>)
    fireEvent.keyDown(window, { key: "s", ctrlKey: true })
    await waitFor(() => expect(a).toHaveBeenCalledTimes(1))
  })

  it("asks before a section switch drops pending edits", () => {
    const go = vi.fn()
    const discard = vi.fn()
    function Nav() {
      const guard = usePageSaveGuard()
      return <button type="button" onClick={() => guard(go)}>Members</button>
    }
    render(<Page><Card label="A" count={1} save={vi.fn()} discard={discard} /><Nav /></Page>)
    fireEvent.click(screen.getByRole("button", { name: "Members" }))
    expect(go).not.toHaveBeenCalled()
    expect(screen.getByRole("alertdialog")).toHaveTextContent("Leave with 1 unsaved change?")

    fireEvent.click(screen.getByRole("button", { name: "Stay" }))
    expect(go).not.toHaveBeenCalled()

    fireEvent.click(screen.getByRole("button", { name: "Members" }))
    fireEvent.click(screen.getByRole("button", { name: "Discard" , hidden: false }))
    expect(discard).toHaveBeenCalled()
    expect(go).toHaveBeenCalledTimes(1)
  })

  it("lets a clean page navigate without asking", () => {
    const go = vi.fn()
    function Nav() {
      const guard = usePageSaveGuard()
      return <button type="button" onClick={() => guard(go)}>Members</button>
    }
    render(<Page><Card label="A" count={0} save={vi.fn()} /><Nav /></Page>)
    fireEvent.click(screen.getByRole("button", { name: "Members" }))
    expect(go).toHaveBeenCalledTimes(1)
  })

  it("a link out of the page asks too", () => {
    render(<Page><Card label="A" count={1} save={vi.fn()} /><a href="/crews">Crews</a></Page>)
    fireEvent.click(screen.getByRole("link", { name: "Crews" }))
    expect(screen.getByRole("alertdialog")).toBeInTheDocument()
    fireEvent.click(screen.getByRole("button", { name: "Discard" }))
    expect(push).toHaveBeenCalledWith("/crews")
  })
})

describe("SaveFooter inside a page", () => {
  beforeEach(() => {
    cleanup()
    toastError.mockReset()
  })

  function FooterCard({ title, initial = "idle", error = null, onSave = vi.fn() }: { title: string; initial?: SaveStatus; error?: string | null; onSave?: () => void }) {
    const [status, setStatus] = useState<SaveStatus>(initial)
    return (
      <SettingsCard title={title}>
        <button type="button" onClick={() => setStatus("error")}>fail</button>
        <SaveFooter dirty count={2} status={status} error={error} onSave={onSave} onCancel={vi.fn()} />
      </SettingsCard>
    )
  }

  it("gives way to the page bar: no Save of its own in the card", () => {
    render(<Page><FooterCard title="Workspace" /></Page>)
    expect(screen.getAllByRole("button", { name: "Save" })).toHaveLength(1)
    expect(bar()).toHaveTextContent("2 unsaved changes")
  })

  it("a failed save toasts with the card's title", () => {
    render(<Page><FooterCard title="Workspace" error="Slug is taken." /></Page>)
    act(() => { fireEvent.click(screen.getByRole("button", { name: "fail" })) })
    expect(toastError).toHaveBeenCalledTimes(1)
    expect(toastError.mock.calls[0][0]).toBe("Couldn’t save Workspace")
  })

  it("keeps its own strip when no page bar exists", () => {
    render(<SaveFooter dirty status="idle" onSave={vi.fn()} onCancel={vi.fn()} />)
    expect(screen.getByRole("button", { name: /cancel/i })).toBeInTheDocument()
  })
})

describe("SettingsSaveBar inside a page", () => {
  beforeEach(() => cleanup())

  it("hands its count to the page bar and draws nothing itself", () => {
    const onSave = vi.fn()
    render(<Page><SettingsSaveBar label="Limits" count={2} onSave={onSave} onDiscard={vi.fn()} /></Page>)
    expect(screen.getAllByRole("region", { name: "Unsaved changes" })).toHaveLength(1)
    expect(bar()).toHaveTextContent("2 unsaved changes")
  })
})

describe("PageSaveBar after a failed save", () => {
  beforeEach(() => cleanup())

  it("does not claim Saved when the edits are later undone by hand", async () => {
    function Flaky() {
      const [count, setCount] = useState(1)
      usePageSave({ label: "A", count, save: () => Promise.reject(new Error("no")), discard: () => {} })
      return <button type="button" onClick={() => setCount(0)}>undo</button>
    }
    render(<Page><Flaky /></Page>)
    fireEvent.click(screen.getByRole("button", { name: "Save" }))
    await waitFor(() => expect(toastError).toHaveBeenCalled())
    fireEvent.click(screen.getByRole("button", { name: "undo" }))
    await waitFor(() => expect(bar()).toBeNull())
    expect(screen.queryByText("Saved")).toBeNull()
  })

  it("says Saved once a Save leaves nothing pending", async () => {
    function Ok() {
      const [count, setCount] = useState(1)
      usePageSave({ label: "A", count, save: async () => { setCount(0) }, discard: () => {} })
      return null
    }
    render(<Page><Ok /></Page>)
    fireEvent.click(screen.getByRole("button", { name: "Save" }))
    expect(await screen.findByText("Saved")).toBeInTheDocument()
  })
})
