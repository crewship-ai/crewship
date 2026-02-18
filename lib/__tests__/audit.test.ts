import { describe, it, expect, vi, beforeEach } from "vitest"

vi.mock("@/lib/db", () => ({
  prisma: {
    auditLog: {
      create: vi.fn(),
    },
  },
}))

describe("createAuditLog", () => {
  beforeEach(() => {
    vi.resetModules()
  })

  async function loadModules() {
    const { prisma } = await import("@/lib/db")
    const { createAuditLog } = await import("@/lib/audit")
    return { prisma, createAuditLog }
  }

  it("creates audit log with required fields", async () => {
    const { prisma, createAuditLog } = await loadModules()
    const mockCreate = vi.mocked(prisma.auditLog.create)
    mockCreate.mockResolvedValue({} as never)

    await createAuditLog({
      workspaceId: "ws-1",
      action: "CREATE",
      entityType: "Agent",
    })

    expect(mockCreate).toHaveBeenCalledWith({
      data: expect.objectContaining({
        workspace_id: "ws-1",
        action: "CREATE",
        entity_type: "Agent",
      }),
    })
  })

  it("passes optional fields when provided", async () => {
    const { prisma, createAuditLog } = await loadModules()
    const mockCreate = vi.mocked(prisma.auditLog.create)
    mockCreate.mockResolvedValue({} as never)

    await createAuditLog({
      workspaceId: "ws-1",
      userId: "user-1",
      action: "UPDATE",
      entityType: "Agent",
      entityId: "agent-1",
      metadata: { changed: "name" },
      ipAddress: "127.0.0.1",
      userAgent: "Mozilla/5.0",
    })

    expect(mockCreate).toHaveBeenCalledWith({
      data: {
        workspace_id: "ws-1",
        user_id: "user-1",
        action: "UPDATE",
        entity_type: "Agent",
        entity_id: "agent-1",
        metadata: { changed: "name" },
        ip_address: "127.0.0.1",
        user_agent: "Mozilla/5.0",
      },
    })
  })

  it("sets optional fields to undefined when not provided", async () => {
    const { prisma, createAuditLog } = await loadModules()
    const mockCreate = vi.mocked(prisma.auditLog.create)
    mockCreate.mockResolvedValue({} as never)

    await createAuditLog({
      workspaceId: "ws-1",
      action: "DELETE",
      entityType: "Credential",
    })

    expect(mockCreate).toHaveBeenCalledWith({
      data: expect.objectContaining({
        user_id: undefined,
        entity_id: undefined,
        ip_address: undefined,
        user_agent: undefined,
      }),
    })
  })

  it("handles null metadata by passing undefined", async () => {
    const { prisma, createAuditLog } = await loadModules()
    const mockCreate = vi.mocked(prisma.auditLog.create)
    mockCreate.mockResolvedValue({} as never)

    await createAuditLog({
      workspaceId: "ws-1",
      action: "CREATE",
      entityType: "Crew",
      metadata: null,
    })

    expect(mockCreate).toHaveBeenCalledWith({
      data: expect.objectContaining({
        metadata: undefined,
      }),
    })
  })

  it("propagates database errors", async () => {
    const { prisma, createAuditLog } = await loadModules()
    const mockCreate = vi.mocked(prisma.auditLog.create)
    mockCreate.mockRejectedValue(new Error("DB connection failed"))

    await expect(
      createAuditLog({
        workspaceId: "ws-1",
        action: "CREATE",
        entityType: "Agent",
      }),
    ).rejects.toThrow("DB connection failed")
  })
})
