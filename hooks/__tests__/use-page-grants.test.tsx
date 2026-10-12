import React, { type ReactNode } from "react"
import { act, cleanup, renderHook, waitFor } from "@testing-library/react"
import { QueryClient, QueryClientProvider } from "@tanstack/react-query"
import { afterEach, beforeEach, expect, it, vi } from "vitest"
const api = vi.hoisted(() => vi.fn())
vi.mock("@/lib/api-fetch", () => ({ apiFetch: api }))
vi.mock("@/hooks/use-realtime", () => ({ useRealtimeEventSafe: vi.fn() }))
import { normalizePageGrants, normalizePageVersions, toPageOwner, pagePanelCount, usePageGrants, usePageVersions, usePageGrantWrite, usePageGrantRevoke, usePageRollback, pageWriteFailureMessage, type WirePageDetail } from "@/hooks/use-page-grants"

const clients: QueryClient[] = []
function setup() {
 const client = new QueryClient({ defaultOptions: { queries: { retry: false, gcTime: 0 }, mutations: { retry: false } } })
 clients.push(client)
 return { client, wrapper: ({ children }: { children: ReactNode }) => <QueryClientProvider client={client}>{children}</QueryClientProvider> }
}
beforeEach(() => { api.mockReset() })
afterEach(() => { cleanup(); clients.splice(0).forEach(client => client.clear()); vi.restoreAllMocks() })

it("normalizes sparse grant rows without inventing active permissions", () => {
 const grants = normalizePageGrants([{ subject_id: " user-1 ", granted_by_user_id: " owner ", panels: [" first ", " ", null], live: "true" }, {}])
 expect(grants[0]).toMatchObject({ subject: "user-1", subjectId: "user-1", grantedBy: "owner", grantedByUserID: "owner", panels: ["first"], live: false, inertReason: null })
 expect(grants[1]).toMatchObject({ subject: "", subjectType: "", level: "", grantedBy: "", panels: [], live: false })
 expect(normalizePageGrants({ grants: [{ live: true, inert_reason: "obsolete" }] })[0].inertReason).toBeNull()
})
it.each([null, 42, {}, { grants: false }])("treats invalid grants envelope %j as empty", body => expect(normalizePageGrants(body)).toEqual([]))
it("normalizes retained version metadata without fabricating measurements", () => {
 const versions = normalizePageVersions([{ seq: Infinity, panel_count: -1, current: "true" }, { seq: 3, panel_count: 2, current: true }, {}])
 expect(versions.map(v => [v.seq, v.panelCount, v.current])).toEqual([[0, 0, false], [3, 2, true], [0, 0, false]])
 expect(normalizePageVersions({ versions: [{ seq: 7 }] })[0].seq).toBe(7)
})
it.each([null, 42, {}, { versions: false }])("treats invalid versions envelope %j as empty", body => expect(normalizePageVersions(body)).toEqual([]))
it.each([
 [null, null],
 [{ owner_crew_name: " Crew " }, { kind: "crew", ref: "Crew", label: "Crew" }],
 [{ owner_crew_slug: "ops" }, { kind: "crew", ref: "ops", label: "ops" }],
 [{ owner: "crew/ops" }, { kind: "crew", ref: "ops", label: "ops" }],
 [{ owner: "user/" }, { kind: "user", ref: "user/", label: "user/" }],
 [{ owner: "crew/" }, { kind: "crew", ref: "crew/", label: "crew/" }],
 [{ owner: "alien/id" }, { kind: "unknown", ref: "alien/id", label: "alien/id" }],
 [{ owner: "legacy" }, { kind: "unknown", ref: "legacy", label: "legacy" }],
])("preserves owner identity for %j", (wire, expected) => expect(toPageOwner(wire as WirePageDetail | null)).toEqual(expected))
it.each([[null, 0], [{ panels: [] }, 0], [{ panel_count: 4 }, 4], [{ panels: 2 }, 2], [{ panels: -1 }, 0], [{ panel_count: -1 }, 0]])("counts panels from %j", (wire, count) => expect(pagePanelCount(wire as WirePageDetail | null)).toBe(count))

it.each([[undefined, "page", true], ["ws", null, true], ["ws", "page", false]])("keeps ACL/history reads idle without selection or enablement", async (ws, slug, enabled) => {
 const { wrapper } = setup()
 const { result } = renderHook(() => ({ grants: usePageGrants(ws, slug, enabled), versions: usePageVersions(ws, slug, enabled) }), { wrapper })
 await act(async () => { await Promise.resolve() })
 expect(api).not.toHaveBeenCalled()
 expect(result.current.grants.loading).toBe(false)
 expect(result.current.versions.loading).toBe(false)
})
it("counts inert grants from the server verdict and preserves version metadata", async () => {
 api.mockImplementation(async (url: string) => new Response(JSON.stringify(url.includes("/grants") ? { grants: [{ live: true }, { live: false, inert_reason: "Issuer left" }] } : { versions: [{ seq: 3, current: true }] })))
 const { wrapper } = setup()
 const { result } = renderHook(() => ({ grants: usePageGrants("ws & one", "page / one"), versions: usePageVersions("ws & one", "page / one") }), { wrapper })
 await waitFor(() => expect(result.current.versions.versions).toHaveLength(1))
 expect(result.current.grants.inertCount).toBe(1)
 expect(result.current.grants.grants[1].inertReason).toBe("Issuer left")
 expect(api.mock.calls.every(([url, init]) => String(url).includes("page%20%2F%20one") && String(url).includes("workspace_id=ws%20%26%20one") && init.signal instanceof AbortSignal)).toBe(true)
})
it.each([[403, { error: "Only owners may read" }], [500, null], [503, "invalid JSON"]])("distinguishes forbidden ACL/history from failure %s", async (status, body) => {
 api.mockImplementation(async () => new Response(body === "invalid JSON" ? "unreadable" : JSON.stringify(body), { status }))
 const { wrapper } = setup()
 const { result } = renderHook(() => ({ grants: usePageGrants("ws", "page"), versions: usePageVersions("ws", "page") }), { wrapper })
 await waitFor(() => expect(result.current.grants.loading || result.current.versions.loading).toBe(false))
 for (const state of [result.current.grants, result.current.versions]) {
  if (status === 403) { expect(state.refusal).toBe("Only owners may read"); expect(state.error).toBeNull() }
  else { expect(state.refusal).toBeNull(); expect(state.error).toContain(String(status)) }
 }
})

it.each([
 { subjectType: "workspace" as const, subject: "ignored", level: "read" as const },
 { subjectType: "user" as const, subject: "ada", level: "produce" as const, panels: ["health"] },
 { subjectType: "crew" as const, subject: "ops", level: "produce" as const, panels: [] },
])("writes only meaningful grant scope: %j", async variables => {
 const { wrapper } = setup(); const onOk = vi.fn()
 api.mockResolvedValue(new Response("{}"))
 const { result } = renderHook(() => usePageGrantWrite("ws", "page", { onOk }), { wrapper })
 await act(async () => { await result.current.mutateAsync(variables) })
 const body = JSON.parse(api.mock.calls[0][1].body)
 expect(body.subject_type).toBe(variables.subjectType)
 expect(body.level).toBe(variables.level)
 if (variables.subjectType === "workspace") expect(body).not.toHaveProperty("subject")
 else expect(body.subject).toBe(variables.subject)
 if (variables.panels?.length) expect(body.panels).toEqual(variables.panels)
 else expect(body).not.toHaveProperty("panels")
 expect(onOk).toHaveBeenCalledWith(variables)
})
it.each([undefined, "read" as const])("revokes the subject with optional level %s in the query", async level => {
 const { wrapper } = setup();const onOk = vi.fn()
 api.mockResolvedValue(new Response(null, { status: 204 }))
 const { result } = renderHook(() => usePageGrantRevoke("ws", "page / one", { onOk }), { wrapper })
 const variables = { subjectType: "user" as const, subject: "ada@example.com", level }
 await act(async () => { await result.current.mutateAsync(variables) })
 const url = new URL(api.mock.calls[0][0], "https://crewship.example")
 expect(url.pathname).toBe("/api/v1/pages/page%20%2F%20one/grants")
 expect(url.searchParams.get("subject")).toBe("ada@example.com")
 expect(url.searchParams.get("level")).toBe(level ?? null)
 expect(api.mock.calls[0][1].method).toBe("DELETE")
 expect(onOk).toHaveBeenCalledWith(variables)
})
it("rolls back to the selected retained version and invalidates history and page data", async () => {
 const { client, wrapper } = setup();const onOk = vi.fn();const invalidate = vi.spyOn(client, "invalidateQueries")
 api.mockResolvedValue(new Response("{}"))
 const { result } = renderHook(() => usePageRollback("ws", "page", { onOk }), { wrapper })
 await act(async () => { await result.current.mutateAsync({ to: 3 }) })
 expect(JSON.parse(api.mock.calls[0][1].body)).toEqual({ to: 3 })
 expect(onOk).toHaveBeenCalledWith({ to: 3 })
 expect(invalidate).toHaveBeenCalledWith({ queryKey: ["page-versions", "ws", { slug: "page" }] })
 expect(invalidate).toHaveBeenCalledWith({ queryKey: ["pages", "ws"] })
})
it("retains a distinct transport failure message", () => {
 expect(pageWriteFailureMessage(new Error("offline"))).toBe("Could not reach the server: offline")
 expect(pageWriteFailureMessage(null)).toBe("Could not reach the server")
})

it.each(["grant", "revoke", "rollback"] as const)("keeps a refused %s retryable and preserves server messages", async kind => {
 const { client, wrapper } = setup();const onOk = vi.fn();const onRefused = vi.fn();const invalidate = vi.spyOn(client, "invalidateQueries")
 api.mockResolvedValueOnce(new Response(JSON.stringify({ error: "Issuer no longer authorized" }), { status: 403 }))
 api.mockResolvedValueOnce(new Response("{}"))
 const { result } = renderHook(() => ({
  grant: usePageGrantWrite("ws", "page", { onOk, onRefused }),
  revoke: usePageGrantRevoke("ws", "page", { onOk, onRefused }),
  rollback: usePageRollback("ws", "page", { onOk, onRefused }),
 }), { wrapper })
 await act(async () => {
  const attempt = kind === "grant" ? result.current.grant.mutateAsync({ subjectType: "user", subject: "ada", level: "read" }) : kind === "revoke" ? result.current.revoke.mutateAsync({ subjectType: "user", subject: "ada" }) : result.current.rollback.mutateAsync({ to: 3 })
  await expect(attempt).rejects.toThrow("Issuer no longer authorized")
 })
 expect(onOk).not.toHaveBeenCalled()
 expect(onRefused).toHaveBeenCalledWith("Issuer no longer authorized")
 expect(invalidate).not.toHaveBeenCalled()
 const original = api.mock.calls[0][1]
 await act(async () => { await result.current[kind].retryAsync() })
 expect(onOk).toHaveBeenCalledTimes(1)
 const retried = api.mock.calls[1][1]
 expect(new Headers(retried.headers).get("Idempotency-Key")).toBe(new Headers(original.headers).get("Idempotency-Key"))
 expect(retried.body).toBe(original.body)
})
it.each(["grant", "revoke", "rollback"] as const)("reports an already-running %s without claiming completion", async kind => {
 const { client, wrapper } = setup();const onOk = vi.fn();const onRefused = vi.fn();const invalidate = vi.spyOn(client, "invalidateQueries")
 api.mockResolvedValue(new Response(JSON.stringify({ error: "Try later" }), { status: 429, headers: { "Retry-After": "5" } }))
 const { result } = renderHook(() => ({
  grant: usePageGrantWrite("ws", "page", { onOk, onRefused }),
  revoke: usePageGrantRevoke("ws", "page", { onOk, onRefused }),
  rollback: usePageRollback("ws", "page", { onOk, onRefused }),
 }), { wrapper })
 await act(async () => {
  const outcome = await (kind === "grant" ? result.current.grant.mutateAsync({ subjectType: "user", subject: "ada", level: "read" }) : kind === "revoke" ? result.current.revoke.mutateAsync({ subjectType: "user", subject: "ada" }) : result.current.rollback.mutateAsync({ to: 3 }))
  expect(outcome).toMatchObject({ kind: "already-running", retryAfterSeconds: 5 })
 })
 expect(onOk).not.toHaveBeenCalled()
 expect(onRefused).toHaveBeenCalledWith("Try later")
 expect(invalidate).not.toHaveBeenCalled()
})
it("allows grant/revoke/rollback callers to omit callbacks", async () => {
 const { wrapper } = setup()
 api.mockImplementation(async () => new Response("{}"))
 const { result } = renderHook(() => ({ grant: usePageGrantWrite("ws", "page"), revoke: usePageGrantRevoke("ws", "page"), rollback: usePageRollback("ws", "page") }), { wrapper })
 await act(async () => {
  await result.current.grant.mutateAsync({ subjectType: "user", subject: "ada", level: "read" })
  await result.current.revoke.mutateAsync({ subjectType: "user", subject: "ada" })
  await result.current.rollback.mutateAsync({ to: 3 })
 })
 expect(api).toHaveBeenCalledTimes(3)
})
