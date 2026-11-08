import { useState } from "react"
import { act, cleanup, fireEvent, render, screen, within } from "@testing-library/react"
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"
import type { NativeCraftHostStatus } from "@/components/features/pages/native-craft-editor-host"
import type { LibraryItem } from "../../../library-items"

// The host is the real lease, save and admission owner; here it only shows
// where it was asked to put its controls, and reports the status it is given.
const state = vi.hoisted(() => ({ status: null as NativeCraftHostStatus | null, availability: { enabled: true, can_edit: true } as unknown, history: true }))
vi.mock("@/components/features/pages/native-craft-editor-host", async () => {
  const { useEffect } = await import("react")
  return { NativeCraftEditorHost: (props: { placeLease?: (n: React.ReactNode) => React.ReactNode; placeHistory?: (n: React.ReactNode) => React.ReactNode; onStatus?: (s: NativeCraftHostStatus) => void; editorHidden?: boolean }) => {
    useEffect(() => { if (state.status) props.onStatus?.(state.status) }, [props])
    const lease = <div role="toolbar" aria-label="Document editing"><span>Jana is editing · since 10:20</span><button type="button">Request edit</button></div>
    return <section data-testid="native-host" data-editor-hidden={String(Boolean(props.editorHidden))}>
      {props.placeLease ? props.placeLease(lease) : lease}
      {state.history && (props.placeHistory ? props.placeHistory(<ol aria-label="Saved versions"><li>Saved by Jana</li></ol>) : null)}
      <div data-testid="editor-surface" hidden={props.editorHidden} />
    </section>
  } }
})
vi.mock("@/components/features/pages/native-craft-preview", () => ({ NativeCraftPreview: () => <div data-testid="native-preview" /> }))
vi.mock("@/components/features/pages/native-craft-reader-history", () => ({ NativeCraftReaderHistory: () => <ol aria-label="Saved versions" /> }))
vi.mock("@/lib/mooring/native-read-loss", () => ({ verifyNativeReadLoss: () => Promise.resolve() }))
vi.mock("@/lib/api-fetch", () => ({ apiFetch: () => Promise.resolve({ ok: true, json: () => Promise.resolve(state.availability) }) }))
vi.mock("@/hooks/use-auth", () => ({ useSessionSafe: () => ({ status: "authenticated", data: { user: { id: "u1" } } }) }))
vi.mock("@/hooks/use-document-viewer-read", () => ({
  useDocumentViewerRead: () => ({ scopeKey: "k", refresh: vi.fn(), read: { status: "ready", title: "Brief", content: { schema: 3, rev: 2, content: { type: "doc" } } } }),
}))
vi.mock("@/components/features/pages/document-readonly/document-readonly", () => ({ DocumentReadonly: ({ accessibleLabel }: { accessibleLabel: string }) => <article aria-label={accessibleLabel}>reflowed text</article> }))

import { ArtifactFrame, craftFrameStatus, NO_FRAME_CAPABILITIES, type FrameLayout } from "../../../frame"
import { CRAFT_LOCK_RULE, CraftBody, useCraftLeaseTarget, versionLabel } from "../craft-body"

const item = (kind: "word" | "sheet"): LibraryItem => ({ slug: "brief", name: "Partner brief", kind, native: true, pageId: "page-1" } as unknown as LibraryItem)
const host = (over: Partial<NativeCraftHostStatus> = {}): NativeCraftHostStatus => ({ ready: true, dirty: false, canEdit: true, editing: false, holder: { kind: "human", name: "Jana" }, holderIsMe: false, openProposals: 0, recoveredDrafts: 0, version: "5", ...over })

function Harness({ kind, layout }: { kind: "word" | "sheet"; layout: FrameLayout }) {
  const lease = useCraftLeaseTarget()
  const [craft, setCraft] = useState<NativeCraftHostStatus | null>(null)
  return <ArtifactFrame name="Partner brief" kind={{ kind }} caps={{ ...NO_FRAME_CAPABILITIES, status: craft ? craftFrameStatus(craft) : null }} onHome={vi.fn()} layout={layout}
    statusActions={lease.statusActions} statusFoot={CRAFT_LOCK_RULE}>
    <CraftBody workspaceId="ws1" item={item(kind)} craft={craft} onStatus={setCraft} onAvailability={vi.fn()} placeLease={lease.place} fallback={null} />
  </ArtifactFrame>
}

const mount = async (kind: "word" | "sheet" = "word", layout: FrameLayout = "desktop") => {
  render(<Harness kind={kind} layout={layout} />)
  await screen.findByTestId("native-host")
  await act(async () => {})
}

beforeEach(() => { state.status = host(); state.availability = { enabled: true, can_edit: true }; state.history = true })
afterEach(cleanup)

describe("Craft body in the ArtifactFrame", () => {
  it("keeps the lock out of the stage: the controls render only inside the status popover", async () => {
    await mount()
    expect(screen.queryByRole("toolbar", { name: "Document editing" })).toBeNull()
    const chip = screen.getByRole("button", { name: "Status: Jana is editing" })
    fireEvent.click(chip)
    const popover = await screen.findByRole("dialog")
    const bar = within(popover).getByRole("toolbar", { name: "Document editing" })
    expect(bar).toHaveTextContent("Jana is editing · since 10:20")
    expect(within(bar).getByRole("button", { name: "Request edit" })).toBeInTheDocument()
    expect(popover).toHaveTextContent(CRAFT_LOCK_RULE)
  })

  it.each([
    [{ holder: null, editing: false }, "free", "Free to edit"],
    [{ holder: { kind: "human" as const, name: "Pavel" }, holderIsMe: true, editing: true }, "editing", "You are editing"],
    [{ holder: { kind: "agent" as const, name: "Scout" } }, "agent", "Scout is editing"],
    [{ openProposals: 1 }, "proposal", "Proposal waiting"],
    [{ recoveredDrafts: 1 }, "draft", "Recovered draft"],
    [{ holder: null, canEdit: false }, "readonly", "Read-only"],
  ])("maps the host state %o to the %s chip", async (over, key, label) => {
    state.status = host(over)
    await mount()
    const chip = screen.getByRole("button", { name: `Status: ${label}` })
    expect(chip.dataset.status).toBe(key)
  })

  it("names the artifact and its version in the view slot, never a storage file name, and opens History from it", async () => {
    await mount("sheet")
    const view = screen.getByRole("toolbar", { name: "Sheet view" })
    expect(view).toHaveTextContent("Partner brief")
    expect(view).toHaveTextContent("v5")
    expect(view).not.toHaveTextContent(/document\.json|\.json/)
    fireEvent.click(within(view).getByRole("button", { name: /Version history/ }))
    expect(screen.getByRole("complementary", { name: "History" })).not.toHaveAttribute("hidden")
    expect(screen.getByRole("list", { name: "Saved versions" })).toBeInTheDocument()
  })

  it("shows the version as a static label when no History tab exists", async () => {
    state.history = false
    await mount()
    const view = screen.getByRole("toolbar", { name: "Word view" })
    expect(within(view).queryByRole("button")).toBeNull()
    expect(view).toHaveTextContent("Partner brief")
  })

  it("puts no Export in the output slot: the parent has no export path, the editor's own ribbon keeps it", async () => {
    await mount()
    expect(document.querySelector('[data-artifact-slot="output"]')).toBeNull()
    expect(document.querySelector('[data-artifact-slot="nav"]')).toBeNull()
  })

  it("reads a Word reflowed on a phone and hides only the editor surface until this person edits", async () => {
    await mount("word", "phone")
    expect(screen.getByRole("article", { name: "Partner brief, saved version" })).toBeInTheDocument()
    expect(screen.getByTestId("native-host").dataset.editorHidden).toBe("true")
  })

  it("shows the editor on a phone once this person holds the lock", async () => {
    state.status = host({ holder: { kind: "human", name: "Pavel" }, holderIsMe: true, editing: true })
    await mount("word", "phone")
    expect(screen.queryByRole("article", { name: "Partner brief, saved version" })).toBeNull()
    expect(screen.getByTestId("native-host").dataset.editorHidden).toBe("false")
  })

  it("never swaps a Sheet for a reader on a phone: the grid scrolls inside the editor", async () => {
    await mount("sheet", "phone")
    expect(screen.queryByRole("article")).toBeNull()
    expect(screen.getByTestId("native-host").dataset.editorHidden).toBe("false")
  })

  it("formats only a server version number", () => {
    expect(versionLabel("12")).toBe("v12")
    expect(versionLabel(undefined)).toBeNull()
    expect(versionLabel("document.json")).toBeNull()
  })
})
