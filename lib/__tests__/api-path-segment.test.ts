import { describe, expect, it } from "vitest"
import { encodeApiPathSegment } from "../api-path-segment"

describe("encodeApiPathSegment", () => {
  it.each(["", ".", ".."]) ("rejects structural segment %s", (value) => {
    expect(encodeApiPathSegment(value)).toBeNull()
  })
  it.each(["../auth", "a/b", "a?b", "a#b", "%2e%2e", "normal-id"]) ("encodes identifier %s as data", (value) => {
    expect(encodeApiPathSegment(value)).toBe(encodeURIComponent(value))
  })
})
