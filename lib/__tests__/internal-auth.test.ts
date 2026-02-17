import { describe, it, expect } from "vitest"
import { NextRequest } from "next/server"
import { requireInternal } from "@/lib/internal-auth"

describe("requireInternal", () => {
  it("returns null for valid internal token", () => {
    const req = new NextRequest("http://localhost:3000/api/v1/internal/chats", {
      headers: { "x-internal-token": "crewshipd" },
    })
    expect(requireInternal(req)).toBeNull()
  })

  it("returns 403 for missing token", () => {
    const req = new NextRequest("http://localhost:3000/api/v1/internal/chats")
    const result = requireInternal(req)
    expect(result).not.toBeNull()
    expect(result!.status).toBe(403)
  })

  it("returns 403 for invalid token", () => {
    const req = new NextRequest("http://localhost:3000/api/v1/internal/chats", {
      headers: { "x-internal-token": "wrong-token" },
    })
    const result = requireInternal(req)
    expect(result).not.toBeNull()
    expect(result!.status).toBe(403)
  })
})
