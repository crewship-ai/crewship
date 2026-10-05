import React from "react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, cleanup, renderHook, waitFor } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { fetchPageBundle, usePageDelete, usePageImport, usePagePublicLinks, usePagePublish, usePageUnpublish, usePageWebhookCreate, usePageWebhookRevoke, usePageWebhooks, type WirePageBundle, } from "@/hooks/use-page-sharing";
const fetchMock = vi.fn();
let client: QueryClient;
function Wrapper({ children }: {
    children: React.ReactNode;
}) {
    return <QueryClientProvider client={client}>{children}</QueryClientProvider>;
}
function response(body: unknown, status = 200) {
    return new Response(JSON.stringify(body), { status, headers: { "Content-Type": "application/json" } });
}
beforeEach(() => {
    fetchMock.mockReset();
    vi.stubGlobal("fetch", fetchMock);
    client = new QueryClient({ defaultOptions: { queries: { retry: false, gcTime: 0 }, mutations: { retry: false } } });
});
afterEach(() => { cleanup(); client.clear(); vi.unstubAllGlobals(); });
describe.each([
    { name: "public links", useRead: usePagePublicLinks, field: "tokens", path: "public" },
    { name: "webhooks", useRead: usePageWebhooks, field: "webhooks", path: "webhooks" },
])("$name reads", ({ useRead, field, path }) => {
    it.each([["", "page"], ["ws", null]])("does not request incomplete scope %s/%s", (workspace, slug) => {
        const { result } = renderHook(() => useRead(workspace!, slug), { wrapper: Wrapper });
        expect(result.current.loading).toBe(false);
        expect(fetchMock).not.toHaveBeenCalled();
    });
    it("encodes scope and retains revoked entries for audit", async () => {
        const rows = [{ id: "live", live: true }, { id: "revoked", live: false, revoked_at: "2026-01-01" }];
        fetchMock.mockResolvedValue(response({ [field]: rows }));
        const { result } = renderHook(() => useRead("ws & one", "page/one"), { wrapper: Wrapper });
        await waitFor(() => expect(result.current.loading).toBe(false));
        expect(fetchMock.mock.calls[0][0]).toContain(`/page%2Fone/${path}?`);
        expect(new URL(fetchMock.mock.calls[0][0], "http://localhost").searchParams.get("workspace_id")).toBe("ws & one");
        expect("links" in result.current ? result.current.links : result.current.webhooks).toEqual(rows);
    });
    it.each([
        { status: 403, body: { error: "Sharing forbidden" }, message: "Sharing forbidden" },
        { status: 500, body: null, message: "Request failed (500)" },
        { status: 502, body: { error: 17 }, message: "Request failed (502)" },
    ])("reports refusal or failure for $status", async ({ status, body, message }) => {
        fetchMock.mockResolvedValue(response(body, status));
        const { result } = renderHook(() => useRead("ws", "page"), { wrapper: Wrapper });
        await waitFor(() => expect(result.current.loading).toBe(false));
        expect(status === 403 ? result.current.refusal : result.current.error).toBe(message);
    });
    it("uses status when the error response is not JSON", async () => {
        fetchMock.mockResolvedValue(new Response("gateway unavailable", { status: 503 }));
        const { result } = renderHook(() => useRead("ws", "page"), { wrapper: Wrapper });
        await waitFor(() => expect(result.current.error).toBe("Request failed (503)"));
    });
});
const bundle: WirePageBundle = { format: "crewship-page-bundle/v1", page: { name: "Report", slug: "report", panels: [] }, references: [], metadata: { exported_at: "now", panel_count: 0 } };
const writes = [
    { name: "publish", path: "/page%2Fone/public", method: "POST", key: "page-public-links", body: {}, useWrite: (cb?: {
            onOk: (value: unknown) => void;
            onRefused: (value: unknown) => void;
        }) => { const m = usePagePublish("ws", "page/one", cb); return { ...m, submit: () => m.mutateAsync({}) }; } },
    { name: "unpublish", path: "/page%2Fone/public/token%2Fone", method: "DELETE", key: "page-public-links", body: undefined, useWrite: (cb?: {
            onOk: (value: unknown) => void;
            onRefused: (value: unknown) => void;
        }) => { const m = usePageUnpublish("ws", "page/one", cb); return { ...m, submit: () => m.mutateAsync({ id: "token/one" }) }; } },
    { name: "webhook create", path: "/page%2Fone/webhooks", method: "POST", key: "page-webhooks", body: { panel: "sales" }, useWrite: (cb?: {
            onOk: (value: unknown) => void;
            onRefused: (value: unknown) => void;
        }) => { const m = usePageWebhookCreate("ws", "page/one", cb); return { ...m, submit: () => m.mutateAsync({ panel: "sales" }) }; } },
    { name: "webhook revoke", path: "/page%2Fone/webhooks/token%2Fone", method: "DELETE", key: "page-webhooks", body: undefined, useWrite: (cb?: {
            onOk: (value: unknown) => void;
            onRefused: (value: unknown) => void;
        }) => { const m = usePageWebhookRevoke("ws", "page/one", cb); return { ...m, submit: () => m.mutateAsync({ id: "token/one" }) }; } },
    { name: "delete", path: "/page%2Fone", method: "DELETE", key: "pages", body: undefined, useWrite: (cb?: {
            onOk: (value: unknown) => void;
            onRefused: (value: unknown) => void;
        }) => { const m = usePageDelete("ws", "page/one", cb); return { ...m, submit: () => m.mutateAsync(undefined) }; } },
    { name: "import", path: "/import", method: "POST", key: "pages", body: { format: bundle.format, page: bundle.page, references: [] }, useWrite: (cb?: {
            onOk: (value: unknown) => void;
            onRefused: (value: unknown) => void;
        }) => { const m = usePageImport("ws", cb); return { ...m, submit: () => m.mutateAsync({ bundle, bind: {} }) }; } },
];
describe.each(writes)("$name", ({ name, path, method, key, body, useWrite }) => {
    it.each([true, false])("sends the scoped request and invalidates after success (callbacks=%s)", async (callbacks) => {
        const payload = { id: "created", token: "one-time-test-value", url: "/public/example", slug: "installed" };
        fetchMock.mockResolvedValue(response(payload));
        const cb = { onOk: vi.fn(), onRefused: vi.fn() };
        const invalidate = vi.spyOn(client, "invalidateQueries");
        const { result } = renderHook(() => useWrite(callbacks ? cb : undefined), { wrapper: Wrapper });
        await act(async () => { await result.current.submit(); });
        const [url, init] = fetchMock.mock.calls[0];
        expect(url).toContain(`/api/v1/pages${path}?`);
        expect(init.method).toBe(method);
        expect(init.body ? JSON.parse(init.body) : undefined).toEqual(body);
        expect(invalidate).toHaveBeenCalledWith(expect.objectContaining({ queryKey: expect.arrayContaining([key, "ws"]) }));
        if (callbacks) {
            const expected = name === "publish" || name === "webhook create" ? payload : name === "delete" ? undefined : name === "import" ? { slug: "installed" } : { id: "token/one" };
            expect(cb.onOk).toHaveBeenCalledWith(expected);
            expect(cb.onRefused).not.toHaveBeenCalled();
        }
    });
    it.each([403, 429])("reports $status without invalidation", async (status) => {
        fetchMock.mockResolvedValue(response({ error: "Denied by server" }, status));
        const cb = { onOk: vi.fn(), onRefused: vi.fn() };
        const invalidate = vi.spyOn(client, "invalidateQueries");
        const { result } = renderHook(() => useWrite(cb), { wrapper: Wrapper });
        await act(async () => {
            if (status === 429)
                await result.current.submit();
            else
                await expect(result.current.submit()).rejects.toThrow("Denied by server");
        });
        expect(cb.onRefused).toHaveBeenCalledWith("Denied by server");
        expect(cb.onOk).not.toHaveBeenCalled();
        expect(invalidate).not.toHaveBeenCalled();
    });
});
it.each([null, {}, { error: "Export denied" }, "not-json"])("reports export refusal with body %j", async (body) => {
    fetchMock.mockResolvedValue(body === "not-json" ? new Response("not-json", { status: 403 }) : response(body, 403));
    await expect(fetchPageBundle("ws", "page/one")).rejects.toThrow(body && typeof body === "object" && "error" in body ? body.error as string : "Export failed (403)");
});
it("exports a fresh bundle for every explicit request", async () => {
    fetchMock.mockImplementation(async () => response(bundle));
    expect(await fetchPageBundle("ws", "page")).toEqual(bundle);
    expect(await fetchPageBundle("ws", "page")).toEqual(bundle);
    expect(fetchMock).toHaveBeenCalledTimes(2);
});
it("preserves explicit publish options including false provenance", async () => {
    fetchMock.mockResolvedValue(response({ id: "link" }));
    const { result } = renderHook(() => usePagePublish("ws", "page"), { wrapper: Wrapper });
    await act(async () => { await result.current.mutateAsync({ expiresInDays: 7, password: "synthetic passphrase", showProvenance: false }); });
    expect(JSON.parse(fetchMock.mock.calls[0][1].body)).toEqual({ expires_in_days: 7, password: "synthetic passphrase", show_provenance: false });
});
it("sends an author's webhook name", async () => {
    fetchMock.mockResolvedValue(response({ id: "webhook" }));
    const { result } = renderHook(() => usePageWebhookCreate("ws", "page"), { wrapper: Wrapper });
    await act(async () => { await result.current.mutateAsync({ panel: "sales", name: "Nightly report" }); });
    expect(JSON.parse(fetchMock.mock.calls[0][1].body)).toEqual({ panel: "sales", name: "Nightly report" });
});
it("imports a v2 project intact with an explicit destination and bindings", async () => {
    const v2 = { ...bundle, format: "crewship-page-bundle/v2", project: { format: "project/v1", runtime: "html", files: [{ path: "index.html", encoding: "utf8", content: "<p>Report</p>" }] } };
    fetchMock.mockResolvedValue(response({}));
    const onOk = vi.fn();
    const { result } = renderHook(() => usePageImport("ws", { onOk }), { wrapper: Wrapper });
    await act(async () => { await result.current.mutateAsync({ bundle: v2, slug: "copy", bind: { source: "local" } }); });
    expect(JSON.parse(fetchMock.mock.calls[0][1].body)).toEqual({ ...v2, slug: "copy", bind: { source: "local" } });
    expect(onOk).toHaveBeenCalledWith({ slug: "" });
});
it("returns unresolved references so the author can repair the import", async () => {
    const refs = [{ ref: "sales", kind: "query", used_by: ["report"], reason: "missing" }];
    fetchMock.mockResolvedValue(response({ error: "Map missing references", unresolved: refs }, 422));
    const onUnresolved = vi.fn(), onRefused = vi.fn();
    const { result } = renderHook(() => usePageImport("ws", { onUnresolved, onRefused }), { wrapper: Wrapper });
    await act(async () => { await expect(result.current.mutateAsync({ bundle, bind: {} })).rejects.toThrow("Map missing references"); });
    expect(onUnresolved).toHaveBeenCalledWith(refs, "Map missing references");
    expect(onRefused).not.toHaveBeenCalled();
});
