import { readFileSync } from "node:fs"
import path from "node:path"

import { describe, it, expect, vi, afterEach } from "vitest"
import { render, screen, cleanup, fireEvent, waitFor, within } from "@testing-library/react"

import { CrewCommandLine } from "@/components/features/crews/crew-yaml"

const FX = path.join(__dirname, "fixtures", "crew-export")
const api = JSON.parse(readFileSync(path.join(FX, "api.json"), "utf8")) as Record<string, unknown>
const opsYaml = readFileSync(path.join(FX, "ops.yaml"), "utf8")

afterEach(() => { cleanup(); vi.restoreAllMocks() })

function serve(failAll = false) {
  vi.spyOn(global, "fetch").mockImplementation(async (input) => {
    const u = new URL(String(input), "http://x")
    u.searchParams.delete("workspace_id")
    if (failAll) return new Response("{}", { status: 500 })
    const body = api[u.pathname + (u.search ? u.search : "")]
    return body === undefined ? new Response("{}", { status: 404 }) : new Response(JSON.stringify(body))
  })
}
const view = () => render(<CrewCommandLine workspaceId="ws1" crew={{ slug: "ops", name: "Operations" }} />)

describe("Settings › Command line and crewship.yaml", () => {
  it("keeps the export command and offers the crew as YAML", () => {
    serve()
    view()
    const card = screen.getByRole("region", { name: /Command line/ })
    expect(within(card).getByText("crewship export crew ops > ops.yaml")).toBeInTheDocument()
    expect(within(card).getByRole("button", { name: "View as YAML" })).toBeInTheDocument()
    expect(within(card).getByRole("button", { name: "Download" })).toBeInTheDocument()
  })

  it("says what the file does not carry", () => {
    serve()
    view()
    expect(screen.getByText(/does not carry autonomy, budget, persona layers, people or links/i)).toBeInTheDocument()
  })

  it("View as YAML shows exactly what `crewship export crew` prints", async () => {
    serve()
    view()
    fireEvent.click(screen.getByRole("button", { name: "View as YAML" }))
    const dialog = await screen.findByRole("dialog", { name: /ops\.yaml/ })
    await waitFor(() => expect(within(dialog).getByTestId("crew-yaml").textContent).toBe(opsYaml))
  })

  it("Download saves ops.yaml with the same text", async () => {
    serve()
    const blobs: Blob[] = []
    const create = vi.fn((b: Blob) => { blobs.push(b); return "blob:x" })
    Object.assign(URL, { createObjectURL: create, revokeObjectURL: vi.fn() })
    const clicked: string[] = []
    vi.spyOn(HTMLAnchorElement.prototype, "click").mockImplementation(function (this: HTMLAnchorElement) { clicked.push(this.download) })
    view()
    fireEvent.click(screen.getByRole("button", { name: "Download" }))
    await waitFor(() => expect(clicked).toEqual(["ops.yaml"]))
    expect(await blobs[0].text()).toBe(opsYaml)
  })

  it("a failed export says so and offers nothing to save", async () => {
    serve(true)
    view()
    fireEvent.click(screen.getByRole("button", { name: "View as YAML" }))
    expect(await screen.findByRole("alert")).toHaveTextContent(/could not be exported/i)
  })
})
