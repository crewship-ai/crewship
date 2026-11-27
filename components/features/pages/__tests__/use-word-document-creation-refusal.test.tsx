// A workbook the server refused before writing anything (not a workbook, password, too large)
// leaves the Sheet's initialization unattempted, so Try again uploads again into the same empty Page.
import { act, renderHook } from "@testing-library/react"
import { QueryClient, QueryClientProvider } from "@tanstack/react-query"
import type { ReactNode } from "react"
import { expect, it, vi } from "vitest"

const m = vi.hoisted(() => ({ apiFetch: vi.fn() }))
vi.mock("@/hooks/use-documents-availability", () => ({ useDocumentsAvailability: () => ({ gridcraftNative: true }) }))
vi.mock("@/hooks/use-page-creation-access", () => ({ usePageCreationAccess: () => true }))
vi.mock("@/lib/api-fetch", async importOriginal => ({ ...await importOriginal<typeof import("@/lib/api-fetch")>(), apiFetch: m.apiFetch }))

import { useWordDocumentCreation } from "../use-word-document-creation"

const wrapper = ({ children }: { children: ReactNode }) => <QueryClientProvider client={new QueryClient()}>{children}</QueryClientProvider>

it("uploads again after a refusal before anything was written, into the same Page", async () => {
  m.apiFetch.mockImplementation(async () => new Response(JSON.stringify({ id: "pg_9" }), { status: 201 }))
  const initialize = vi.fn()
    .mockRejectedValueOnce(Object.assign(new Error("This file is not a readable .xlsx or .xlsm workbook."), { beforeWrite: true }))
    .mockResolvedValueOnce(new Response("{}", { status: 500 }))
  const { result } = renderHook(() => useWordDocumentCreation("w1"), { wrapper })
  const input = { name: "Budget", slug: "budget-1", format: "sheet" as const, prepareImport: async () => initialize }
  await act(async () => { await expect(result.current.create.mutateAsync(input)).rejects.toThrow("not a readable") })
  await act(async () => { await expect(result.current.create.mutateAsync(input)).rejects.toThrow(/could not be confirmed/) })
  expect(initialize).toHaveBeenCalledTimes(2)
  expect(m.apiFetch.mock.calls.filter(([url]) => String(url).startsWith("/api/v1/pages?")), "one Page only").toHaveLength(1)
})

it("never uploads again after an initialization whose answer was lost", async () => {
  m.apiFetch.mockImplementation(async () => new Response(JSON.stringify({ id: "pg_9" }), { status: 201 }))
  const initialize = vi.fn().mockRejectedValue(new TypeError("Failed to fetch"))
  const { result } = renderHook(() => useWordDocumentCreation("w1"), { wrapper })
  const input = { name: "Budget", slug: "budget-2", format: "sheet" as const, prepareImport: async () => initialize }
  await act(async () => { await expect(result.current.create.mutateAsync(input)).rejects.toThrow("Failed to fetch") })
  await act(async () => { await expect(result.current.create.mutateAsync(input)).rejects.toThrow(/could not be confirmed/) })
  expect(initialize).toHaveBeenCalledTimes(1)
})
