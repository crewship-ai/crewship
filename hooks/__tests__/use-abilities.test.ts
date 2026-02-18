import { describe, it, expect, vi, beforeEach } from "vitest"
import { renderHook } from "@testing-library/react"

const mockWorkspace = {
  role: null as string | null,
  loading: false,
}

vi.mock("@/hooks/use-workspace", () => ({
  useWorkspace: () => mockWorkspace,
}))

import { useAbilities } from "@/hooks/use-abilities"

describe("useAbilities", () => {
  beforeEach(() => {
    mockWorkspace.role = null
    mockWorkspace.loading = false
  })

  it("returns VIEWER abilities when role is null", () => {
    const { result } = renderHook(() => useAbilities())
    expect(result.current.role).toBeNull()
    expect(result.current.abilities).toBeDefined()
    // VIEWER can read but not create
    expect(result.current.abilities.can("read", "Agent")).toBe(true)
    expect(result.current.abilities.can("create", "Agent")).toBe(false)
    expect(result.current.abilities.can("manage", "all")).toBe(false)
  })

  it("returns OWNER abilities when role is OWNER", () => {
    mockWorkspace.role = "OWNER"
    const { result } = renderHook(() => useAbilities())
    expect(result.current.role).toBe("OWNER")
    expect(result.current.abilities.can("manage", "all")).toBe(true)
  })

  it("returns ADMIN abilities when role is ADMIN", () => {
    mockWorkspace.role = "ADMIN"
    const { result } = renderHook(() => useAbilities())
    expect(result.current.abilities.can("manage", "all")).toBe(true)
  })

  it("returns MEMBER abilities with limited permissions", () => {
    mockWorkspace.role = "MEMBER"
    const { result } = renderHook(() => useAbilities())
    expect(result.current.abilities.can("read", "Agent")).toBe(true)
    expect(result.current.abilities.can("create", "Agent")).toBe(false)
  })

  it("returns VIEWER abilities with read-only permissions", () => {
    mockWorkspace.role = "VIEWER"
    const { result } = renderHook(() => useAbilities())
    expect(result.current.abilities.can("read", "Agent")).toBe(true)
    expect(result.current.abilities.can("create", "Agent")).toBe(false)
    expect(result.current.abilities.can("update", "Agent")).toBe(false)
  })

  it("passes loading state from useWorkspace", () => {
    mockWorkspace.loading = true
    const { result } = renderHook(() => useAbilities())
    expect(result.current.loading).toBe(true)
  })

  it("MANAGER can create agents but cannot manage members", () => {
    mockWorkspace.role = "MANAGER"
    const { result } = renderHook(() => useAbilities())
    expect(result.current.abilities.can("create", "Crew")).toBe(true)
    expect(result.current.abilities.can("create", "Agent")).toBe(true)
    expect(result.current.abilities.can("read", "Agent")).toBe(true)
    // MANAGER cannot manage members or delete workspace
    expect(result.current.abilities.can("create", "Member")).toBe(false)
    expect(result.current.abilities.can("delete", "Workspace")).toBe(false)
  })
})
