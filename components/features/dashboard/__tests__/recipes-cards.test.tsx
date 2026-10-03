import type { ComponentProps } from "react"
import { act, cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react"
import { afterEach, beforeEach, expect, it, vi } from "vitest"
import { RecipesEmptyState } from "../recipes-cards"
import type { RecipeInstallSheet } from "@/components/features/recipes/recipe-install-sheet"
import { apiFetch } from "@/lib/api-fetch"
vi.mock("@/lib/api-fetch", () => ({ apiFetch: vi.fn() }))
vi.mock("@/components/features/recipes/recipe-install-sheet", () => ({
  RecipeInstallSheet: ({ workspaceId, recipeSlug, open, onOpenChange, onInstalled }: ComponentProps<typeof RecipeInstallSheet>) => open ? <section aria-label="Recipe installation">
    <p>{workspaceId}: {recipeSlug}</p>
    <button onClick={() => onOpenChange(false)}>Cancel install</button>
    <button onClick={() => { onInstalled?.(); onOpenChange(false) }}>Complete install</button>
  </section> : null,
}))
const fetchMock = vi.mocked(apiFetch)
beforeEach(() => { fetchMock.mockReset() })
afterEach(cleanup)
const recipe = { slug: "research", name: "Research crew", description: "Investigates a topic", icon: "search", color: "blue" }
it("opens the selected recipe in the active workspace and forwards installation completion", async () => {
  const onInstalled = vi.fn()
  fetchMock.mockResolvedValue(new Response(JSON.stringify([recipe, { ...recipe, slug: "review", name: "Review crew" }])))
  render(<RecipesEmptyState workspaceId="workspace-one" onInstalled={onInstalled} />)
  fireEvent.click(await screen.findByRole("button", { name: /Research crew/ }))
  expect(screen.getByRole("region", { name: "Recipe installation" })).toHaveTextContent("workspace-one: research")
  fireEvent.click(screen.getByRole("button", { name: "Cancel install" }))
  expect(screen.queryByRole("region", { name: "Recipe installation" })).not.toBeInTheDocument()
  expect(onInstalled).not.toHaveBeenCalled()
  fireEvent.click(screen.getByRole("button", { name: /Review crew/ }))
  expect(screen.getByRole("region", { name: "Recipe installation" })).toHaveTextContent("workspace-one: review")
  fireEvent.click(screen.getByRole("button", { name: "Complete install" }))
  expect(onInstalled).toHaveBeenCalledTimes(1)
  expect(fetchMock).toHaveBeenCalledWith("/api/v1/recipes")
})
it.each(["empty", "non-array", "refused", "transport", "malformed"])("does not show a broken recommendation when catalog is %s", async state => {
  if (state === "transport") fetchMock.mockRejectedValue(new Error("offline"))
  else fetchMock.mockResolvedValue(new Response(state === "empty" ? "[]" : state === "malformed" ? "invalid" : "{}", { status: state === "refused" ? 403 : 200 }))
  const { container } = render(<RecipesEmptyState workspaceId="w" />)
  await act(async () => { await Promise.resolve() })
  await waitFor(() => expect(fetchMock).toHaveBeenCalledTimes(1))
  await waitFor(() => expect(container).toBeEmptyDOMElement())
  expect(screen.queryByRole("button")).not.toBeInTheDocument()
})
