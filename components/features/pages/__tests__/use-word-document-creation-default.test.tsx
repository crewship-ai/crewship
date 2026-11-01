import { renderHook } from "@testing-library/react"
import { QueryClient, QueryClientProvider } from "@tanstack/react-query"
import type { ReactNode } from "react"
import { beforeEach, expect, it, vi } from "vitest"

const m = vi.hoisted(() => ({ availability: {} as Record<string, unknown> }))
vi.mock("@/hooks/use-documents-availability", () => ({ useDocumentsAvailability: () => m.availability }))
vi.mock("@/hooks/use-page-creation-access", () => ({ usePageCreationAccess: () => true }))

import { useWordDocumentCreation } from "../use-word-document-creation"

const wrapper = ({ children }: { children: ReactNode }) => <QueryClientProvider client={new QueryClient()}>{children}</QueryClientProvider>

beforeEach(() => { m.availability = {} })

it("never defaults a Word creation to a Sheet when only GridCraft is native", () => {
  m.availability = { gridcraftNative: true, data: true }
  const { result } = renderHook(() => useWordDocumentCreation("w1"), { wrapper })
  expect(result.current.wordFormat).toBe("doc")
  expect(result.current.wordFormatAvailable).toBe(true)
})

it("still prefers native WordCraft for Word when it is advertised", () => {
  m.availability = { wordcraftNative: true, gridcraftNative: true, data: true }
  const { result } = renderHook(() => useWordDocumentCreation("w1"), { wrapper })
  expect(result.current.wordFormat).toBe("native")
})
