import { renderHook } from "@testing-library/react"
import { beforeEach, expect, it, vi } from "vitest"

const m = vi.hoisted(() => ({ docs: {} as Record<string, unknown> }))
vi.mock("@/hooks/use-documents-availability", () => ({ useDocumentsAvailability: () => m.docs }))
vi.mock("@/hooks/use-page-creation-access", () => ({ usePageCreationAccess: () => true }))
vi.mock("../../projects/use-artifact-project-data", () => ({ useArtifactProjectsAvailable: () => false }))

import { useArtifactCapabilities } from "../use-artifact-capabilities"

beforeEach(() => { m.docs = { data: false, wordcraftNative: false, gridcraftNative: false, wordcraftCommands: false } })

it("offers Sheet only when the server advertises gridcraft_native", () => {
  expect(renderHook(() => useArtifactCapabilities("ws1")).result.current.sheets).toBe(false)
  m.docs = { ...m.docs, gridcraftNative: true }
  expect(renderHook(() => useArtifactCapabilities("ws1")).result.current.sheets).toBe(true)
})

// #4897: dev3 serves native Word (wordcraft_native) with the legacy document
// flag off; Word must be offered exactly as Sheet is, and withdrawn otherwise.
it("offers Word when the server advertises wordcraft_native and only then", () => {
  expect(renderHook(() => useArtifactCapabilities("ws1")).result.current.documents).toBe(false)
  m.docs = { ...m.docs, wordcraftNative: true }
  expect(renderHook(() => useArtifactCapabilities("ws1")).result.current.documents).toBe(true)
  m.docs = { ...m.docs, wordcraftNative: undefined }
  expect(renderHook(() => useArtifactCapabilities("ws1")).result.current.documents).toBe(false)
})
