import { describe, it, expect } from "vitest"

import { apiErrorMessage, readApiError } from "@/lib/api-error"

describe("apiErrorMessage", () => {
  it("reads writeProblem's RFC 7807 detail", () => {
    expect(
      apiErrorMessage(
        { type: "about:blank", title: "Bad Request", status: 400, detail: "crew is not linked" },
        "fallback",
      ),
    ).toBe("crew is not linked")
  })

  it("reads replyError's error field", () => {
    expect(apiErrorMessage({ error: "integration not found" }, "fallback")).toBe(
      "integration not found",
    )
  })

  // The reason this helper exists rather than a `??` chain at each site:
  // writeProblem always SETS detail, so an empty one is present-but-blank
  // and `body?.detail ?? body?.error` would return "" — an empty toast.
  it("treats a present-but-blank field as absent", () => {
    expect(apiErrorMessage({ detail: "" }, "fallback")).toBe("fallback")
    expect(apiErrorMessage({ detail: "   " }, "fallback")).toBe("fallback")
    expect(apiErrorMessage({ detail: "", error: "the real reason" }, "fallback")).toBe(
      "the real reason",
    )
  })

  it("falls back for bodies that carry no message", () => {
    for (const body of [null, undefined, {}, [], "a string", 42, { detail: 7 }]) {
      expect(apiErrorMessage(body, "fallback")).toBe("fallback")
    }
  })
})

describe("readApiError", () => {
  it("returns the server's message", async () => {
    const res = new Response(JSON.stringify({ detail: "not allowed" }), { status: 403 })
    expect(await readApiError(res, "fallback")).toBe("not allowed")
  })

  // A 502 from a proxy is HTML, not JSON. Throwing here would replace the
  // server's refusal with a parse error and report the wrong failure.
  it("falls back instead of throwing when the body is not JSON", async () => {
    const res = new Response("<html>502 Bad Gateway</html>", { status: 502 })
    expect(await readApiError(res, "Request failed")).toBe("Request failed")
  })

  it("falls back on an empty body", async () => {
    const res = new Response(null, { status: 500 })
    expect(await readApiError(res, "Request failed")).toBe("Request failed")
  })
})
