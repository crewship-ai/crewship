import { describe, expect, it } from "vitest"
import { encodeApiPathSegment, requireApiPathSegment } from "../api-path-segment"

describe("encodeApiPathSegment", () => {
  it.each(["", ".", ".."]) ("rejects structural segment %s", (value) => {
    expect(encodeApiPathSegment(value)).toBeNull()
  })
  it.each(["../auth", "a/b", "a?b", "a#b", "%2e%2e", "normal-id"]) ("encodes identifier %s as data", (value) => {
    expect(encodeApiPathSegment(value)).toBe(encodeURIComponent(value))
  })
})

it("rejects invalid identifiers before constructing command URLs", () => {
  for (const value of ["", ".", ".."]) expect(() => requireApiPathSegment(value)).toThrow(TypeError)
  expect(requireApiPathSegment("one/two")).toBe("one%2Ftwo")
})
