import { expect, it } from "vitest"
import { DocumentAPIError, documentFailure, UnconfirmedReceipt } from "../use-page-document"
import { haltsQueue } from "@/lib/editor-lifecycle/write-queue"

// What the poll loop blocks on, and so what must halt the shared write queue (N4).
it.each([
  ["400", new DocumentAPIError("bad", 400), "rejected", true],
  ["401", new DocumentAPIError("signed out", 401), "denied", true],
  ["403", new DocumentAPIError("forbidden", 403), "denied", true],
  ["404", new DocumentAPIError("missing", 404), "denied", true],
  ["409 epoch", new DocumentAPIError("stale", 409, "document_epoch"), "conflict", true],
  ["409 generation", new DocumentAPIError("changed", 409, "document_generation"), "conflict", true],
  ["413", new DocumentAPIError("too large", 413), "rejected", true],
  ["422", new DocumentAPIError("invalid", 422), "rejected", true],
  ["429 document_quota", new DocumentAPIError("quota", 429, "document_quota"), "rejected", true],
  ["429 document_rate_limit", new DocumentAPIError("wait", 429, "document_rate_limit"), "rate-limited", false],
  ["429 without a code", new DocumentAPIError("Too many requests", 429), "rate-limited", false],
  ["503", new DocumentAPIError("not acknowledged", 503), "transient", false],
  ["a network error", new TypeError("Failed to fetch"), "offline", false],
  ["an unverifiable receipt", new UnconfirmedReceipt("no receipt"), "unconfirmed", true],
] as const)("%s → %s, halts %s", (_name, cause, kind, halts) => {
  const failure = documentFailure(cause)
  expect(failure.kind).toBe(kind)
  expect(haltsQueue(failure.kind)).toBe(halts)
})
