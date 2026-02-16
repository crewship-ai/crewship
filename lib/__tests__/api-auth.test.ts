import { describe, it, expect, vi, beforeEach } from "vitest"
import { NextResponse } from "next/server"

// Mock auth and prisma
vi.mock("@/auth", () => ({
  auth: vi.fn(),
}))

vi.mock("@/lib/db", () => ({
  prisma: {
    organizationMember: {
      findUnique: vi.fn(),
    },
  },
}))

describe("api-auth", () => {
  beforeEach(() => {
    vi.resetModules()
  })

  async function loadModules() {
    const { auth } = await import("@/auth")
    const { prisma } = await import("@/lib/db")
    const { requireAuth, isAuthError } = await import("@/lib/api-auth")
    return { auth: vi.mocked(auth), prisma, requireAuth, isAuthError }
  }

  it("returns 401 when no session", async () => {
    const { auth, requireAuth, isAuthError } = await loadModules()
    auth.mockResolvedValue(null)

    const result = await requireAuth("org-1")
    expect(isAuthError(result)).toBe(true)
    expect((result as NextResponse).status).toBe(401)
  })

  it("returns 400 when orgId is null", async () => {
    const { auth, requireAuth, isAuthError } = await loadModules()
    auth.mockResolvedValue({ user: { id: "user-1" }, expires: "" } as never)

    const result = await requireAuth(null)
    expect(isAuthError(result)).toBe(true)
    expect((result as NextResponse).status).toBe(400)
  })

  it("returns 403 when user not a member", async () => {
    const { auth, prisma, requireAuth, isAuthError } = await loadModules()
    auth.mockResolvedValue({ user: { id: "user-1" }, expires: "" } as never)
    vi.mocked(prisma.organizationMember.findUnique).mockResolvedValue(null)

    const result = await requireAuth("org-1")
    expect(isAuthError(result)).toBe(true)
    expect((result as NextResponse).status).toBe(403)
  })

  it("returns AuthResult on success", async () => {
    const { auth, prisma, requireAuth, isAuthError } = await loadModules()
    auth.mockResolvedValue({ user: { id: "user-1" }, expires: "" } as never)
    vi.mocked(prisma.organizationMember.findUnique).mockResolvedValue({
      role: "ADMIN",
    } as never)

    const result = await requireAuth("org-1")
    expect(isAuthError(result)).toBe(false)
    if (!isAuthError(result)) {
      expect(result.userId).toBe("user-1")
      expect(result.orgId).toBe("org-1")
      expect(result.role).toBe("ADMIN")
    }
  })

  it("isAuthError distinguishes NextResponse from AuthResult", async () => {
    const { isAuthError } = await loadModules()
    expect(isAuthError(NextResponse.json({}, { status: 401 }))).toBe(true)
    expect(isAuthError({ userId: "u1", orgId: "o1", role: "ADMIN" })).toBe(false)
  })
})
