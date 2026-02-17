import { describe, it, expect, vi, beforeEach } from "vitest"
import http from "node:http"
import { EventEmitter } from "node:events"

// Mock http.request before importing the module
vi.mock("node:http", async () => {
  const actual = await vi.importActual<typeof import("node:http")>("node:http")
  return {
    ...actual,
    default: {
      ...actual,
      request: vi.fn(),
    },
  }
})

describe("crewshipd-client", () => {
  beforeEach(() => {
    vi.resetModules()
    vi.stubEnv("CREWSHIPD_URL", "http://localhost:8080")
  })

  async function loadClient() {
    return await import("@/lib/crewshipd-client")
  }

  function mockHttpRequest(statusCode: number, body: unknown) {
    const mockReq = new EventEmitter() as EventEmitter & {
      write: ReturnType<typeof vi.fn>
      end: ReturnType<typeof vi.fn>
      destroy: ReturnType<typeof vi.fn>
    }
    mockReq.write = vi.fn()
    mockReq.end = vi.fn()
    mockReq.destroy = vi.fn()

    const mockRes = new EventEmitter() as EventEmitter & {
      statusCode: number
    }
    mockRes.statusCode = statusCode

    vi.mocked(http.request).mockImplementation((_opts, callback) => {
      if (callback) {
        (callback as (res: typeof mockRes) => void)(mockRes)
        process.nextTick(() => {
          mockRes.emit("data", Buffer.from(JSON.stringify(body)))
          mockRes.emit("end")
        })
      }
      return mockReq as unknown as http.ClientRequest
    })

    return { mockReq, mockRes }
  }

  it("healthCheck calls /health", async () => {
    mockHttpRequest(200, { status: "ok" })
    const { healthCheck } = await loadClient()
    const result = await healthCheck()
    expect(result.ok).toBe(true)
    if (result.ok) {
      expect(result.data.status).toBe("ok")
    }
  })

  it("getAgentStatus calls correct path", async () => {
    mockHttpRequest(200, { agent_id: "a1", status: "running" })
    const { getAgentStatus } = await loadClient()
    const result = await getAgentStatus("a1")
    expect(result.ok).toBe(true)
  })

  it("handles error response", async () => {
    mockHttpRequest(500, { error: "internal error" })
    const { healthCheck } = await loadClient()
    const result = await healthCheck()
    expect(result.ok).toBe(false)
    expect(result.status).toBe(500)
  })

  it("startAgent sends POST", async () => {
    mockHttpRequest(202, { agent_id: "a1", status: "starting" })
    const { startAgent } = await loadClient()
    const result = await startAgent("a1", { session_id: "s1" })
    expect(result.ok).toBe(true)
  })

  it("stopAgent sends POST", async () => {
    mockHttpRequest(200, { agent_id: "a1", status: "stopped" })
    const { stopAgent } = await loadClient()
    const result = await stopAgent("a1")
    expect(result.ok).toBe(true)
  })

  it("createChat sends POST with body", async () => {
    mockHttpRequest(201, { id: "s1", status: "created" })
    const { createChat } = await loadClient()
    const result = await createChat({
      session_id: "s1",
      agent_id: "a1",
      workspace_id: "o1",
    })
    expect(result.ok).toBe(true)
    if (result.ok) {
      expect(result.data.status).toBe("created")
    }
  })

  it("getContainerStatus calls correct path", async () => {
    mockHttpRequest(200, { crew_id: "t1", status: "running" })
    const { getContainerStatus } = await loadClient()
    const result = await getContainerStatus("t1")
    expect(result.ok).toBe(true)
  })

  it("getChatMessages calls correct path", async () => {
    mockHttpRequest(200, { session_id: "s1", messages: [] })
    const { getChatMessages } = await loadClient()
    const result = await getChatMessages("s1", 0, 50)
    expect(result.ok).toBe(true)
  })
})
