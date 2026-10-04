import { cleanup, render, screen, within } from "@testing-library/react"
import { afterEach, expect, it } from "vitest"
import { CrewCard } from "@/components/features/crews/crew-card"
import { DockerOverview } from "../docker-overview"

afterEach(cleanup)
const crew = { id: "crew-id", name: "Research", slug: "research", description: null, color: null, icon: null, _count: { agents: 3, members: 2 } }

it("links the crew identity and shows truthful membership and missing description", () => {
  render(<CrewCard crew={crew} />)
  const card = screen.getByRole("link", { name: /Research/ })
  expect(card).toHaveAttribute("href", "/crews/crew-id")
  expect(within(card).getByText("No description")).toBeInTheDocument()
  expect(within(card).getByText("3 agents")).toBeInTheDocument()
  expect(within(card).getByText("2 members")).toBeInTheDocument()
  expect(screen.queryByText(/running|error/)).not.toBeInTheDocument()
})

it.each([
  [2, 0, "2 running", "text-success"],
  [0, 1, "1 error", "text-destructive"],
  [2, 1, "2 running · 1 error", "text-destructive"],
] as const)("presents active and error counts without treating stopped agents as failures", (running, error, label, css) => {
  render(<CrewCard crew={{ ...crew, description: "Investigation crew", icon: "rocket", color: "blue", agent_status_summary: { running, error, idle: 4, stopped: 5 } }} />)
  expect(screen.getByText(label)).toHaveClass(css)
  expect(screen.getByText("Investigation crew")).toBeInTheDocument()
  expect(screen.queryByText("No description")).not.toBeInTheDocument()
})

it("keeps an entirely idle crew free of a misleading health warning", () => {
  render(<CrewCard crew={{ ...crew, agent_status_summary: { running: 0, error: 0, idle: 3, stopped: 1 } }} />)
  expect(screen.queryByText(/running|error/)).not.toBeInTheDocument()
})

it("distinguishes empty infrastructure from known crew container names", () => {
  const { rerender } = render(<DockerOverview crews={[]} />)
  expect(screen.getByText("No crews configured")).toBeInTheDocument()
  rerender(<DockerOverview crews={[{ ...crew, color: "blue" }, { id: "other", slug: "other", name: "Other", icon: null, color: null }]} />)
  expect(screen.getAllByRole("columnheader").map(el => el.textContent)).toEqual(["Container", "Agents"])
  const rows = screen.getAllByRole("row")
  expect(rows).toHaveLength(3)
  expect(rows[1]).toHaveTextContent("crewship-team-research")
  expect(within(rows[1]).getByText("3")).toBeInTheDocument()
  expect(rows[2]).toHaveTextContent("crewship-team-other")
  expect(within(rows[2]).getByText("0")).toBeInTheDocument()
  expect(screen.getByText(/Live container metrics.*not available yet/)).toBeInTheDocument()
  expect(screen.queryByText("Running")).not.toBeInTheDocument()
})
