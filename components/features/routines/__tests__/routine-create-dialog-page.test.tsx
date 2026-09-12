vi.mock("../routine-schedules-tab", () => ({ RoutineSchedulesTab: () => <div /> }))
vi.mock("../routine-webhooks-tab", () => ({ RoutineWebhooksTab: () => <div /> }))
import { beforeEach, describe, expect, it, vi } from "vitest"
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react"
import type { RoutineDetail } from "../routines-detail-panel"

// =============================================================================
// The routine editor as a PAGE (#2519).
//
// Editing an existing recipe happens in the routines content area, not in a
// modal: the layout owns the breadcrumb, the header says "Edit recipe", and
// the document reads top to bottom in the order a person asks about a
// routine. The save-token flow, the publish confirmation and the discard
// guard are the dialog's — routine-edit-builder.test.tsx covers those and
// they are not repeated here.
// =============================================================================

vi.mock("sonner", () => ({ toast: { success: vi.fn(), error: vi.fn() } }))
vi.mock("next/navigation", () => ({ useRouter: () => ({ push: vi.fn() }) }))
vi.mock("../routine-definition-canvas", () => ({
  RoutineDefinitionCanvas: () => <div data-testid="graph" />,
}))
vi.mock("@/components/features/files/file-editor", () => ({
  FileEditor: () => <div data-testid="editor" />,
}))
vi.mock("@/components/crew-icon-popover", () => ({
  CrewIconPopover: (p: { icon: string }) => <button type="button">Icon: {p.icon}</button>,
}))
vi.mock("@/components/ui/agent-avatar", () => ({ AgentAvatar: () => <span /> }))
vi.mock("@/components/features/crews/crew-picker", () => ({
  CrewPicker: (p: { value: string; id: string }) => (
    <div data-testid="crew" id={p.id}>
      {p.value}
    </div>
  ),
}))
vi.mock("@/lib/api-fetch", () => ({
  apiFetch: vi.fn(async (url: string) => {
    if (url.endsWith("/draft"))
      return {
        ok: true,
        json: async () => ({
          id: "",
          slug: url.split("/").at(-2),
          revision: 0,
          base_pipeline_id: "",
          base_revision: 0,
          document: {},
        }),
      }
    if (url.startsWith("/api/v1/crews"))
      return { ok: true, json: async () => [{ id: "crew1", name: "Ops" }] }
    if (url.startsWith("/api/v1/agents"))
      return {
        ok: true,
        json: async () => [
          { id: "a1", slug: "worker", name: "Worker", crew_id: "crew1", agent_role: "AGENT" },
        ],
      }
    return { ok: true, json: async () => [] }
  }),
}))

import { RoutineCreateDialog } from "../routine-create-dialog"

const routine = {
  id: "r1",
  slug: "existing",
  name: "Existing recipe",
  description: "Original description",
  icon: "clock",
  color: "blue",
  head_version: 3,
  author_crew_id: "crew1",
  author_agent_id: "a1",
  definition: {
    dsl_version: "1.0",
    name: "existing",
    description: "Original description",
    inputs: [{ name: "region", type: "string", required: true }],
    outputs: [{ name: "report", type: "string" }],
    steps: [{ id: "work", type: "agent_run", agent_slug: "worker", prompt: "Do the work" }],
  },
} as unknown as RoutineDetail

const props = {
  workspaceId: "ws1",
  open: true,
  routine,
  onCreated: vi.fn(),
  onClose: vi.fn(),
}

const settled = () =>
  waitFor(() => expect(screen.queryByText("Loading saved draft…")).not.toBeInTheDocument())

const sections = (root: HTMLElement) =>
  Array.from(root.querySelectorAll("[data-doc-section]")).map((el) =>
    el.getAttribute("data-doc-section"),
  )

describe("routine editor as a page", () => {
  beforeEach(() => {
    cleanup()
    vi.clearAllMocks()
  })

  it("renders the editor without a dialog, headed 'Edit recipe'", async () => {
    render(<RoutineCreateDialog {...props} presentation="page" />)
    await settled()

    expect(screen.queryByRole("dialog")).not.toBeInTheDocument()
    expect(document.querySelector('[data-slot="dialog-content"]')).toBeNull()

    const h1 = screen.getByRole("heading", { level: 1 })
    expect(h1.textContent?.trim()).toBe("Edit recipe")
    expect(screen.queryByText("Edit your routine")).not.toBeInTheDocument()
    expect(
      screen.getByText("Nothing here is live until you publish and confirm."),
    ).toBeInTheDocument()
    // The layout's breadcrumb already says "Routines › name".
    expect(h1.textContent).not.toMatch(/Routines/)

    // The editor itself, not a stripped-down copy: identity, the Recipe | Code
    // strip, the draft strip and the footer.
    expect(screen.getByLabelText("Name")).toHaveValue("Existing recipe")
    expect(screen.getByRole("button", { name: "Code", exact: true })).toBeInTheDocument()
    expect(screen.getByRole("button", { name: "Save draft", exact: true })).toBeInTheDocument()
    expect(screen.getByRole("button", { name: "Publish", exact: true })).toBeInTheDocument()
  })

  it("keeps the dialog presentation by default", async () => {
    render(<RoutineCreateDialog {...props} />)
    await settled()
    expect(screen.getByRole("dialog")).toBeInTheDocument()
    expect(screen.getByRole("heading", { level: 2 })).toHaveTextContent("Edit your routine")
    expect(screen.queryByRole("heading", { level: 1 })).not.toBeInTheDocument()
  })

  it("orders the document: identity, description, inputs, steps, then the folded rest", async () => {
    const { container } = render(<RoutineCreateDialog {...props} presentation="page" />)
    await settled()

    expect(sections(container)).toEqual([
      "identity",
      "description",
      "inputs",
      "steps",
      "team",
      "access",
      "publication",
    ])

    // The headings a person reads, in DOM order.
    const headings = Array.from(
      container.querySelectorAll("[data-doc-section] > :is(div, summary) h3, [data-doc-section] > summary"),
    ).map((el) => el.textContent?.replace(/\s+/g, " ").trim())
    expect(headings.slice(0, 4).map((h) => h?.split(" — ")[0])).toEqual([
      "Identity",
      "Purpose",
      "Inputs",
      "Recipe steps",
    ])
    expect(headings[4]).toMatch(/^Team, results and technical identity/)
    expect(headings[5]).toBe("Access, budget and technical details")
    expect(headings[6]).toBe("Publication changes")

    // The three rarely-touched blocks are folded and closed by default.
    for (const id of ["team", "access", "publication"]) {
      const details = container.querySelector(`details[data-doc-section="${id}"]`)!
      expect(details).not.toBeNull()
      expect(details.hasAttribute("open")).toBe(false)
    }

    // What the fold holds: Team, then Results, then the identifier.
    const team = container.querySelector('[data-doc-section="team"]')!
    const inner = Array.from(team.querySelectorAll("label, h4")).map((el) =>
      el.textContent?.trim(),
    )
    expect(inner.indexOf("Team")).toBeLessThan(inner.indexOf("Results"))
    expect(inner.indexOf("Results")).toBeLessThan(inner.indexOf("Technical identity"))
    expect(screen.getByLabelText("Routine identifier")).toHaveValue("existing")
    expect(screen.getByText("report")).toBeInTheDocument()

    // Description carries the list-facing label from the prototype.
    expect(screen.getByLabelText("What this routine does")).toHaveValue(
      "Original description",
    )
  })

  it("keeps the starter template and start fields for a NEW routine, in the same frame", async () => {
    const { container } = render(
      <RoutineCreateDialog {...props} routine={undefined} presentation="page" />,
    )
    fireEvent.click(screen.getByText("Write it yourself"))
    await settled()
    expect(sections(container)).toEqual([
      "identity",
      "description",
      "inputs",
      "template",
      "steps",
      "start",
      "team",
      "publication",
    ])
  })
})
