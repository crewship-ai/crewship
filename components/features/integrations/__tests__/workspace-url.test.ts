import { renderHook } from "@testing-library/react"
import { expect, it } from "vitest"
import { useWorkspaceURLReady } from "../use-workspace-url"

it("preserves cold deep links and clears entity selection only across workspaces", () => {
  window.history.replaceState({}, "", "/integrations?tab=incoming&section=agent&target=a1&server=s1")
  const { result, rerender } = renderHook(({ workspaceId, loading }) => useWorkspaceURLReady(workspaceId, loading), { initialProps: { workspaceId: null as string | null, loading: true } })
  rerender({ workspaceId: "first", loading: false })
  expect(result.current).toBe(true)
  expect(new URLSearchParams(window.location.search).get("target")).toBe("a1")
  rerender({ workspaceId: null, loading: true })
  rerender({ workspaceId: "second", loading: false })
  expect(result.current).toBe(true)
  expect(window.location.search).toBe("?tab=incoming")
})
