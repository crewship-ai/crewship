// @vitest-environment node
import { expect, it } from "vitest"
import { MAX_DRAFT_STORE_BYTES } from "../document-queue"

it("lets the draft store keep a sealed 64 MiB workbook journal row (128 MiB)", () => {
  // A 64 MiB workbook is about 85.4 MiB as base64 in the journal row and about 114 MiB once sealed.
  const sealed = Math.ceil(Math.ceil(64 * 1024 * 1024 * 4 / 3) * 4 / 3)
  expect(MAX_DRAFT_STORE_BYTES).toBe(128 * 1024 * 1024)
  expect(sealed).toBeLessThan(MAX_DRAFT_STORE_BYTES)
})
