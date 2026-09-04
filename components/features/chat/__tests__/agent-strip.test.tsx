import { render, screen } from "@testing-library/react"
import { describe, expect, it, vi } from "vitest"

import { entityHref } from "@/lib/entity-links"

import { AgentStrip, agentStatusPill } from "../agent-strip"

vi.mock("@/components/ui/agent-avatar", () => ({
  AgentAvatar: ({ seed }: { seed: string }) => <span data-testid="avatar">{seed}</span>,
}))

const riley = {
  id: "ag-1",
  name: "Riley",
  slug: "riley",
  status: "RUNNING",
  role_title: "Platform Engineer",
  llm_model: "claude-sonnet-4-5",
  crew: { name: "Ops", slug: "ops", color: "emerald" },
  _count: { skills: 3, credentials: 1 },
}

describe("AgentStrip", () => {
  it("names the agent, its role, its crew, its model and its counts — and links them", () => {
    render(<AgentStrip agent={riley} />)
    expect(screen.getByText("Riley")).toBeInTheDocument()
    expect(screen.getByText("Platform Engineer")).toBeInTheDocument()
    expect(screen.getByRole("link", { name: /Ops/ }).getAttribute("href")).toBe("/crews?crew=ops")
    expect(screen.getByRole("link", { name: "3 skills" }).getAttribute("href")).toBe("/crews?agent=riley&tab=skills")
    expect(screen.getByRole("link", { name: "1 credential" }).getAttribute("href")).toBe("/crews?agent=riley&tab=credentials")
    // Through entityHref, so the assertion follows the journal's query key
    // rather than pinning one spelling of it.
    expect(screen.getByRole("link", { name: "runs" }).getAttribute("href")).toBe(entityHref({ kind: "journal", agentSlug: "riley" }))
    // The model is a label, never the id.
    expect(screen.queryByText("claude-sonnet-4-5")).not.toBeInTheDocument()
  })

  it("shows the status as a word, not a colour alone", () => {
    render(<AgentStrip agent={riley} />)
    expect(screen.getByText("Running")).toBeInTheDocument()
  })

  it("does not invent counts the roster did not carry", () => {
    render(<AgentStrip agent={{ ...riley, _count: null, llm_model: null, crew: null }} />)
    expect(screen.queryByText(/skills?$/)).not.toBeInTheDocument()
    expect(screen.queryByText(/credentials?$/)).not.toBeInTheDocument()
  })
})

describe("agentStatusPill", () => {
  it("maps the roster's status to a word and a tone", () => {
    expect(agentStatusPill("RUNNING")).toMatchObject({ label: "Running", tone: "blue", live: true })
    expect(agentStatusPill("IDLE")).toMatchObject({ label: "Idle", tone: "success", live: false })
    expect(agentStatusPill("ERROR")).toMatchObject({ label: "Error", tone: "danger" })
    expect(agentStatusPill("PENDING_REVIEW")).toMatchObject({ label: "Pending review", tone: "warn", live: false })
    expect(agentStatusPill("PAUSED")).toMatchObject({ label: "Paused", tone: "warn" })
    expect(agentStatusPill(undefined)).toMatchObject({ label: "Idle" })
  })
})
