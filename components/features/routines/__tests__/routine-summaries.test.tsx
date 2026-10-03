import { cleanup, render, screen, within } from "@testing-library/react"
import { afterEach, describe, expect, it } from "vitest"
import { RoutineTouches } from "../routine-touches"
import { RoutineMiniTrace } from "../routine-mini-trace"
import type { MiniTraceNode } from "@/lib/routine-mini-trace"
import type { RoutineManifest } from "@/lib/routine-flow"

afterEach(cleanup)
const manifest: RoutineManifest = { integrations: ["github", "custom-app"],
  datastores: [{ type: "postgres", name: "Main" }, { type: "custom-kv" }],
  tools: [{ type: "bash", name: "deploy.sh" }], agents: ["scout"], routines: ["child"],
  egress: ["api.example.test"], credentials: [{ type: "TOKEN", scope: "read" }, { type: "" }],
  has_http: true, has_code: true }
const node: MiniTraceNode = { id: "step", kind: "agent", label: "Inspect", iconKey: "agent", status: "pending", calls: [] }

describe("routine capability summary", () => {
  it.each([undefined, null])("explains an absent manifest", (value) => {
    render(<RoutineTouches manifest={value} />)
    expect(screen.getByText("This routine declares no external resources.")).toBeInTheDocument()
    expect(screen.queryByText("Credentials")).not.toBeInTheDocument()
  })
  it("shows every resource group in declared order, including risky tools and credentials", () => {
    const { container } = render(<RoutineTouches manifest={manifest} />)
    const groupLabels = ["Integrations", "Datastores", "Tools / scripts", "Agents", "Sub-routines", "Egress", "Credentials"]
    const groupNodes = groupLabels.map(label => screen.getByText(label))
    groupNodes.slice(1).forEach((element, index) => {
      expect(groupNodes[index].compareDocumentPosition(element) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy()
    })
    for (const label of ["GitHub", "Custom App", "postgres · Main", "custom-kv", "bash · deploy.sh", "@scout", "child", "api.example.test", "TOKEN · read"]) {
      expect(screen.getByText(label)).toBeInTheDocument()
    }
    expect(screen.getByText("TOKEN · read")).toHaveClass("text-warn")
    expect(screen.getByText("bash · deploy.sh")).toHaveClass("text-warn")
    expect(container.querySelectorAll("svg").length).toBeGreaterThan(9)
  })
})

describe("routine miniature trace", () => {
  it("explains missing captured actions for an empty run", () => {
    render(<RoutineMiniTrace nodes={[]} />)
    expect(screen.getByText("No captured actions for this run.")).toBeInTheDocument()
    expect(screen.queryAllByRole("listitem")).toHaveLength(0)
  })
  it("renders step status and details without giving pending steps a successful outcome", () => {
    render(<RoutineMiniTrace nodes={[
      { ...node, id: "trigger", kind: "trigger", iconKey: "trigger", label: "Schedule", detail: "internal trigger", status: "success" },
      { ...node, id: "running", label: "Analyze", detail: "private-agent", model: "fast-model", status: "running" },
      { ...node, id: "error", label: "Store", kind: "store", iconKey: "store-postgres", brandIconKey: "postgresql", status: "failed" },
      { ...node, id: "pending", label: "Publish" },
      { ...node, id: "future", label: "New kind", kind: "future" as MiniTraceNode["kind"], iconKey: "future" as MiniTraceNode["iconKey"], status: "none" },
    ]} />)
    expect(screen.getByLabelText("succeeded")).toBeInTheDocument()
    expect(screen.getByLabelText("running")).toBeInTheDocument()
    expect(screen.getByLabelText("failed")).toBeInTheDocument()
    expect(screen.getByTitle("private-agent")).toBeInTheDocument()
    expect(screen.queryByText("internal trigger")).not.toBeInTheDocument()
    expect(screen.getByText("fast-model")).toBeInTheDocument()
    expect(within(screen.getByText("Publish").closest("li")!).queryByLabelText("succeeded")).not.toBeInTheDocument()
  })
  it("shows tool outcomes as accessible text, duration and useful full paths", () => {
    render(<RoutineMiniTrace nodes={[{ ...node, calls: [
      { kind: "write", name: "Write report", artifactPath: "/work/report.md", host: "ignored", tool: "bash", status: "ok", durationMs: 1200 },
      { kind: "http", name: "Request", host: "api.example.test", status: "error", durationMs: 0 },
      { kind: "tool", name: "Pending call", status: "running" },
      { kind: "tool", name: "Future call", status: "future" as never, durationMs: -1 },
    ]}]} />)
    expect(screen.queryByText("No captured actions for this run.")).not.toBeInTheDocument()
    expect(screen.getByTitle("/work/report.md")).toHaveTextContent("Write report")
    expect(screen.getByTitle("api.example.test")).toHaveTextContent("Request")
    expect(screen.getByTitle("Pending call")).toBeInTheDocument()
    const write = within(screen.getByText("Write report").closest("li")!)
    expect(write.getByText("succeeded")).toHaveClass("sr-only")
    expect(write.getByText("1.2s")).toBeInTheDocument()
    expect(write.getByText("bash")).toBeInTheDocument()
    expect(within(screen.getByText("Request").closest("li")!).getByText("failed")).toHaveClass("sr-only")
    expect(within(screen.getByText("Pending call").closest("li")!).getByText("running")).toHaveClass("sr-only")
  })
})
