import { act, cleanup, renderHook, waitFor } from "@testing-library/react"
import { QueryClient, QueryClientProvider } from "@tanstack/react-query"
import type { ReactNode } from "react"
import { afterEach, beforeEach, expect, it, vi } from "vitest"
import * as client from "../client"
import { useConversationLibrary, useWorkCards } from "../use-artifact-context-data"

beforeEach(() => { vi.stubEnv("NEXT_PUBLIC_CHAT_ARTIFACT_CONTEXT", "true") })
afterEach(() => { cleanup(); vi.unstubAllEnvs(); vi.restoreAllMocks() })
const wrap = ({ children }: { children: ReactNode }) => <QueryClientProvider client={new QueryClient()}>{children}</QueryClientProvider>
const scope = { workspaceId: "w", chatId: "c", currentUserId: "u" }
const entry = { title: "Plan", target: null, model: { id: "k" } } as unknown as client.WorkCardEntry
const row = { item: { status: "readable", linkId: "l", kind: "files", name: "n", revisionKey: "r" }, target: null } as client.LibraryRow

it("keeps the last good cards when a refetch fails, and reports the failure", async () => {
  const fetchCards = vi.spyOn(client, "fetchWorkCards").mockResolvedValueOnce({ status: "ok", data: [entry] }).mockResolvedValue({ status: "error" })
  const { result } = renderHook(() => useWorkCards(scope), { wrapper: wrap })
  await waitFor(() => expect(result.current.cards).toHaveLength(1))
  await act(async () => { await result.current.refetch() })
  await waitFor(() => expect(fetchCards).toHaveBeenCalledTimes(2))
  expect(result.current.cards).toHaveLength(1)
  expect(result.current.stale).toBe(true)
})
it("drops the cards when the refetch is denied", async () => {
  vi.spyOn(client, "fetchWorkCards").mockResolvedValueOnce({ status: "ok", data: [entry] }).mockResolvedValue({ status: "denied" })
  const { result } = renderHook(() => useWorkCards(scope), { wrapper: wrap })
  await waitFor(() => expect(result.current.cards).toHaveLength(1))
  await act(async () => { await result.current.refetch() })
  await waitFor(() => expect(result.current.cards).toHaveLength(0))
  expect(result.current.stale).toBe(false)
})
it("keeps the last good library rows when a refetch fails", async () => {
  vi.spyOn(client, "fetchLibrary").mockResolvedValueOnce({ status: "ok", data: [row] }).mockResolvedValue({ status: "error" })
  const { result } = renderHook(() => useConversationLibrary({ ...scope, open: true }), { wrapper: wrap })
  await waitFor(() => expect(result.current.rows).toHaveLength(1))
  await act(async () => { await result.current.refetch() })
  await waitFor(() => expect(result.current.stale).toBe(true))
  expect(result.current.rows).toHaveLength(1)
})
