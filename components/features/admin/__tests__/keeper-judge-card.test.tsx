import { describe, it, expect, vi, beforeEach } from "vitest"
import { render, screen, fireEvent, waitFor } from "@testing-library/react"
import { KeeperJudgeCard } from "../keeper-judge-card"

// The card exists because this page could previously only DIAGNOSE a dead
// credential-access judge. What it must get right, in order of how badly each
// one bites: the engine + endpoint + model commit as ONE write (Keeper is
// fail-closed, so enabling without a judge cannot be a separate step), a value's
// provenance is visible (otherwise "disabled" and "somebody turned this off
// here" look the same), and a refused write keeps the draft.

const apiFetch = vi.fn()
vi.mock("@/lib/api-fetch", () => ({
  apiFetch: (...args: unknown[]) => apiFetch(...args),
}))

let canManage = true
vi.mock("@/hooks/use-abilities", () => ({
  useAbilities: () => ({
    abilities: { can: () => canManage },
    role: canManage ? "OWNER" : "MEMBER",
    loading: false,
  }),
}))

const toastSuccess = vi.fn()
const toastError = vi.fn()
vi.mock("sonner", () => ({
  toast: {
    success: (...args: unknown[]) => toastSuccess(...args),
    error: (...args: unknown[]) => toastError(...args),
    warning: vi.fn(),
  },
}))

function jsonResponse(body: unknown, status = 200) {
  return { ok: status < 400, status, json: async () => body }
}

type Source = "instance" | "env" | "default"

function config(over: {
  enabled?: [boolean, Source]
  endpoint?: [string, Source]
  model?: [string, Source]
  overridden?: boolean
} = {}) {
  const [enabled, enabledSrc] = over.enabled ?? [false, "env" as Source]
  const [endpoint, endpointSrc] = over.endpoint ?? ["", "default" as Source]
  const [model, modelSrc] = over.model ?? ["", "default" as Source]
  return {
    enabled: { value: enabled, source: enabledSrc, editable: true },
    judge_provider: { value: "ollama", source: "default", editable: false },
    judge_endpoint_url: { value: endpoint, source: endpointSrc, editable: true },
    judge_wire: { value: "ollama", source: "default", editable: false },
    judge_model: { value: model, source: modelSrc, editable: true },
    overridden: over.overridden ?? false,
    judge_configured: endpoint !== "" && model !== "",
  }
}

/**
 * Models the endpoints: a PUT applies its fields and returns the whole config,
 * and the judge check/discovery answer from `judge` (overridable per test).
 */
function mockRoutes(
  initial: ReturnType<typeof config>,
  judge: { models?: string[]; test?: unknown; modelsError?: string } = {},
) {
  let current = initial
  apiFetch.mockImplementation(async (url: string, init?: RequestInit) => {
    if (url.includes("/admin/keeper/judge/models")) {
      return jsonResponse({ endpoint: "http://x", models: judge.models ?? [], error: judge.modelsError })
    }
    if (url.includes("/admin/keeper/judge/test")) {
      return jsonResponse(judge.test ?? { ok: false, endpoint: "http://x", stages: [] })
    }
    if (!url.includes("/admin/keeper/config")) throw new Error(`unexpected fetch: ${url}`)
    if (init?.method === "PUT") {
      const body = JSON.parse(String(init.body)) as {
        enabled: boolean
        judge_endpoint_url: string
        judge_model: string
      }
      current = config({
        enabled: [body.enabled, "instance"],
        endpoint: [body.judge_endpoint_url, body.judge_endpoint_url ? "instance" : "default"],
        model: [body.judge_model, body.judge_model ? "instance" : "default"],
        overridden: true,
      })
      return jsonResponse(current)
    }
    if (init?.method === "DELETE") {
      current = initial
      return jsonResponse(current)
    }
    return jsonResponse(current)
  })
}

function putBodies(): Record<string, unknown>[] {
  return apiFetch.mock.calls
    .filter(([, init]) => (init as RequestInit)?.method === "PUT")
    .map(([, init]) => JSON.parse(String((init as RequestInit).body)) as Record<string, unknown>)
}

describe("KeeperJudgeCard", () => {
  beforeEach(() => {
    apiFetch.mockReset()
    toastSuccess.mockReset()
    toastError.mockReset()
    canManage = true
  })

  it("hydrates the effective values and says where each came from", async () => {
    mockRoutes(config({
      enabled: [true, "env"],
      endpoint: ["http://127.0.0.1:11434", "env"],
      model: ["qwen2.5:7b", "instance"],
    }))
    render(<KeeperJudgeCard workspaceId="ws1" />)

    expect(await screen.findByTestId("keeper-judge-endpoint")).toHaveValue("http://127.0.0.1:11434")
    expect(screen.getByTestId("keeper-judge-model")).toHaveValue("qwen2.5:7b")
    expect(screen.getByTestId("keeper-judge-enabled")).toHaveAttribute("aria-checked", "true")
    // Provenance, not decoration: two "from server config" (engine, endpoint)
    // and one "instance override" (model).
    expect(screen.getAllByText(/from server config/i)).toHaveLength(2)
    expect(screen.getByText(/instance override/i)).toBeInTheDocument()
    // Nothing edited → no Save on offer.
    expect(screen.queryByTestId("keeper-judge-save")).not.toBeInTheDocument()
  })

  it("turns Keeper on and names the judge in a single write", async () => {
    // A fresh instance: nothing configured, engine off. This is the flow the
    // whole slice exists for.
    mockRoutes(config())
    render(<KeeperJudgeCard workspaceId="ws1" />)

    fireEvent.change(await screen.findByTestId("keeper-judge-endpoint"), {
      target: { value: "  http://192.168.1.40:11434 " },
    })
    fireEvent.change(screen.getByTestId("keeper-judge-model"), { target: { value: "qwen2.5:7b" } })
    fireEvent.click(screen.getByTestId("keeper-judge-enabled"))

    const save = screen.getByRole("button", { name: /^save$/i })
    expect(save).toBeEnabled()
    fireEvent.click(save)

    await waitFor(() => expect(putBodies()).toHaveLength(1))
    expect(putBodies()[0]).toEqual({
      enabled: true,
      // Trimmed — a pasted URL routinely carries whitespace.
      judge_endpoint_url: "http://192.168.1.40:11434",
      judge_model: "qwen2.5:7b",
    })
    expect(await screen.findByText(/^saved$/i)).toBeInTheDocument()
  })

  it("refuses to submit the engine on with no judge, and says why", async () => {
    mockRoutes(config())
    render(<KeeperJudgeCard workspaceId="ws1" />)

    fireEvent.click(await screen.findByTestId("keeper-judge-enabled"))

    expect(screen.getByText(/fail-closed/i)).toBeInTheDocument()
    expect(screen.getByRole("button", { name: /^save$/i })).toBeDisabled()
    expect(putBodies()).toHaveLength(0)
  })

  it("keeps the draft and shows the server's message when the write is refused", async () => {
    apiFetch.mockImplementation(async (url: string, init?: RequestInit) => {
      if (!url.includes("/admin/keeper/config")) throw new Error(`unexpected fetch: ${url}`)
      if (init?.method === "PUT") {
        return jsonResponse({ error: "judge endpoint needs an http:// or https:// scheme — try http://host:11434" }, 400)
      }
      return jsonResponse(config({ enabled: [false, "env"], model: ["qwen2.5:7b", "env"] }))
    })
    render(<KeeperJudgeCard workspaceId="ws1" />)

    fireEvent.change(await screen.findByTestId("keeper-judge-endpoint"), {
      target: { value: "192.168.1.40:11434" },
    })
    fireEvent.click(screen.getByRole("button", { name: /^save$/i }))

    expect(await screen.findByText(/needs an http:\/\/ or https:\/\/ scheme/i)).toBeInTheDocument()
    // The value someone typed survives a refusal — retyping a URL to read the
    // error again is how people give up on a form.
    expect(screen.getByTestId("keeper-judge-endpoint")).toHaveValue("192.168.1.40:11434")
  })

  it("offers Reset only when something is overridden, and clears it", async () => {
    mockRoutes(config({
      enabled: [true, "instance"],
      endpoint: ["http://10.0.0.5:11434", "instance"],
      model: ["qwen3:4b", "instance"],
      overridden: true,
    }))
    render(<KeeperJudgeCard workspaceId="ws1" />)

    fireEvent.click(await screen.findByTestId("keeper-judge-reset"))

    await waitFor(() =>
      expect(apiFetch.mock.calls.some(([, init]) => (init as RequestInit)?.method === "DELETE")).toBe(true),
    )
    await waitFor(() => expect(toastSuccess).toHaveBeenCalled())
  })

  it("hides Reset when nothing is overridden", async () => {
    mockRoutes(config({ endpoint: ["http://127.0.0.1:11434", "env"], model: ["qwen2.5:7b", "env"] }))
    render(<KeeperJudgeCard workspaceId="ws1" />)

    await screen.findByTestId("keeper-judge-endpoint")
    expect(screen.queryByTestId("keeper-judge-reset")).not.toBeInTheDocument()
  })

  it("is read-only for a non-manager", async () => {
    canManage = false
    mockRoutes(config({
      enabled: [true, "instance"],
      endpoint: ["http://127.0.0.1:11434", "instance"],
      model: ["qwen2.5:7b", "instance"],
      overridden: true,
    }))
    render(<KeeperJudgeCard workspaceId="ws1" />)

    expect(await screen.findByTestId("keeper-judge-endpoint")).toBeDisabled()
    expect(screen.getByTestId("keeper-judge-model")).toBeDisabled()
    expect(screen.getByTestId("keeper-judge-enabled")).toBeDisabled()
    // No Reset either: it is a write.
    expect(screen.queryByTestId("keeper-judge-reset")).not.toBeInTheDocument()
  })

  it("reports a failed read instead of rendering an empty form", async () => {
    // An empty, editable form on a 503 would invite an edit whose save vanishes.
    apiFetch.mockImplementation(async () => jsonResponse({ error: "Keeper configuration is not available" }, 503))
    render(<KeeperJudgeCard workspaceId="ws1" />)

    expect(await screen.findByText(/keeper configuration is not available/i)).toBeInTheDocument()
    expect(screen.getByRole("button", { name: /retry/i })).toBeInTheDocument()
    expect(screen.queryByTestId("keeper-judge-endpoint")).not.toBeInTheDocument()
  })

  // The flow the user asked for, end to end: paste an address, press Test, be
  // told in words whether it works.
  it("runs the three-stage check and reports each stage", async () => {
    mockRoutes(config({ endpoint: ["http://127.0.0.1:11434", "env"], model: ["qwen2.5:7b", "env"] }), {
      test: {
        ok: true,
        endpoint: "http://127.0.0.1:11434",
        model: "qwen2.5:7b",
        decision: "ALLOW",
        models: ["qwen2.5:7b", "llama3:8b"],
        stages: [
          { name: "reach", label: "Reach the endpoint", ok: true, detail: "answering · 2 models available", latency_ms: 12 },
          { name: "model", label: "Model is available", ok: true, detail: "qwen2.5:7b is pulled and ready" },
          { name: "verdict", label: "Returns a verdict", ok: true, detail: "verdict: ALLOW", latency_ms: 900 },
        ],
      },
    })
    render(<KeeperJudgeCard workspaceId="ws1" />)

    fireEvent.click(await screen.findByTestId("keeper-judge-test"))

    const result = await screen.findByTestId("keeper-judge-test-result")
    expect(result).toHaveTextContent(/this judge works/i)
    expect(result).toHaveTextContent(/answering · 2 models available/i)
    expect(result).toHaveTextContent(/is pulled and ready/i)
    expect(result).toHaveTextContent(/verdict: ALLOW/i)
    await waitFor(() => expect(toastSuccess).toHaveBeenCalled())
  })

  // A failed check must say WHICH stage failed and leave the skipped ones
  // visibly skipped — "no verdict" reads as a broken model until you notice the
  // endpoint never answered.
  it("distinguishes a failed stage from the stages it skipped", async () => {
    mockRoutes(config({ endpoint: ["http://127.0.0.1:1", "instance"], model: ["qwen2.5:7b", "instance"], overridden: true }), {
      test: {
        ok: false,
        endpoint: "http://127.0.0.1:1",
        model: "qwen2.5:7b",
        stages: [
          { name: "reach", label: "Reach the endpoint", ok: false, detail: "nothing is listening there" },
          { name: "model", label: "Model is available", ok: false, skipped: true, detail: "not checked — the endpoint did not answer" },
          { name: "verdict", label: "Returns a verdict", ok: false, skipped: true, detail: "not checked — the endpoint did not answer" },
        ],
      },
    })
    render(<KeeperJudgeCard workspaceId="ws1" />)

    fireEvent.click(await screen.findByTestId("keeper-judge-test"))

    const result = await screen.findByTestId("keeper-judge-test-result")
    expect(result).toHaveTextContent(/not usable yet/i)
    expect(result).toHaveTextContent(/nothing is listening there/i)
    expect(result).toHaveTextContent(/not checked/i)
    // Not a success, so no green toast.
    expect(toastSuccess).not.toHaveBeenCalled()
  })

  // The models the endpoint actually serves, one click to use — the thing that
  // removes "type the model name from memory" from the setup.
  it("offers the endpoint's own models and fills the field on click", async () => {
    mockRoutes(
      config({ endpoint: ["http://127.0.0.1:11434", "env"] }),
      { models: ["qwen2.5:7b", "llama3:8b"] },
    )
    render(<KeeperJudgeCard workspaceId="ws1" />)
    await screen.findByTestId("keeper-judge-endpoint")

    // Discovery is debounced against the endpoint draft.
    const chips = await screen.findByTestId("keeper-judge-models", {}, { timeout: 3000 })
    expect(chips).toHaveTextContent("qwen2.5:7b")
    expect(chips).toHaveTextContent("llama3:8b")

    fireEvent.click(screen.getByRole("button", { name: "llama3:8b" }))
    expect(screen.getByTestId("keeper-judge-model")).toHaveValue("llama3:8b")
    // Picking a model is an edit, so the card offers to save it.
    expect(screen.getByRole("button", { name: /^save$/i })).toBeInTheDocument()
  })

  it("offers no Test button to a non-manager", async () => {
    canManage = false
    mockRoutes(config({ endpoint: ["http://127.0.0.1:11434", "env"], model: ["qwen2.5:7b", "env"] }))
    render(<KeeperJudgeCard workspaceId="ws1" />)

    await screen.findByTestId("keeper-judge-endpoint")
    expect(screen.queryByTestId("keeper-judge-test")).not.toBeInTheDocument()
  })

  it("renders nothing without a workspace", () => {
    const { container } = render(<KeeperJudgeCard workspaceId={null} />)
    expect(container).toBeEmptyDOMElement()
    expect(apiFetch).not.toHaveBeenCalled()
  })
})
