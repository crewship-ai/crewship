import { describe, it, expect } from "vitest"

import { ApiError, apiErrorCodeAndField, apiErrorMessage, readApiError, toApiError } from "@/lib/api-error"
import { ApiMutationError } from "@/hooks/use-api-mutation"

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

// #2862: both envelopes may carry a machine code and the field it concerns.
describe("apiErrorCodeAndField / ApiError / toApiError", () => {
  it("reads code and field from replyErrorCode's envelope", () => {
    expect(apiErrorCodeAndField({ error: "Agent slug already taken", code: "agent_slug_taken", field: "slug" }))
      .toEqual({ code: "agent_slug_taken", field: "slug" })
  })
  it("reads code and field from writeProblemCode's RFC 7807 envelope", () => {
    expect(apiErrorCodeAndField({ detail: "taken", status: 409, code: "agent_slug_taken", field: "slug" }))
      .toEqual({ code: "agent_slug_taken", field: "slug" })
  })
  it("omits absent, blank or non-string values", () => {
    expect(apiErrorCodeAndField({ error: "x" })).toEqual({})
    expect(apiErrorCodeAndField({ error: "x", code: "", field: 3 })).toEqual({})
    expect(apiErrorCodeAndField(null)).toEqual({})
  })
  it("builds one error with status, body, code and field", async () => {
    const res = new Response(JSON.stringify({ error: "Crew already has a lead agent", code: "crew_lead_exists", field: "agent_role" }), { status: 409 })
    const err = await toApiError(res, "fallback")
    expect(err).toBeInstanceOf(ApiError)
    expect(err).toMatchObject({ message: "Crew already has a lead agent", status: 409, code: "crew_lead_exists", field: "agent_role" })
  })
  it("keeps a short plain-text refusal readable and drops an HTML page", async () => {
    expect((await toApiError(new Response("slug taken", { status: 409 }), "fallback")).message).toBe("slug taken")
    expect((await toApiError(new Response("<html>502 Bad Gateway</html>", { status: 502 }), "fallback")).message).toBe("fallback")
    expect((await toApiError(new Response("", { status: 500 }), "fallback")).message).toBe("fallback")
  })
  it("ApiMutationError is an ApiError, so one handler serves both", () => {
    const err = new ApiMutationError("taken", 409, { error: "taken", code: "agent_slug_taken", field: "slug" })
    expect(err).toBeInstanceOf(ApiError)
    expect(err.name).toBe("ApiMutationError")
    expect(err.code).toBe("agent_slug_taken")
    expect(err.field).toBe("slug")
  })
})
