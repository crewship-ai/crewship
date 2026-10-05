import { act, cleanup, renderHook, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { useNotificationProviders } from "../use-notification-providers";
import { useChannelAgents } from "../use-channel-agents";
import { apiFetch } from "@/lib/api-fetch";
vi.mock("@/lib/api-fetch", () => ({ apiFetch: vi.fn() }));
const fetch = vi.mocked(apiFetch);
const reply = (body: unknown, status = 200) => new Response(JSON.stringify(body), { status });
function deferred<T>() { let resolve!: (v: T) => void; let reject!: (v: unknown) => void; const promise = new Promise<T>((yes, no) => { resolve = yes; reject = no; }); return { promise, resolve, reject }; }
function useProviders(ws: string | null, _channel: string | null) { const state = useNotificationProviders(ws); return { ...state, items: state.providers }; }
function useAgents(ws: string | null, channel: string | null) { const state = useChannelAgents(ws, channel); return { ...state, items: state.agents }; }
beforeEach(() => { fetch.mockReset(); });
afterEach(() => { cleanup(); });
for (const kind of ["providers", "agents"] as const) {
    describe(`notification ${kind} registry`, () => {
        const useSubject = kind === "providers" ? useProviders : useAgents;
        const body = (id: string) => ({ [kind]: [{ id }] });
        it("encodes identifiers and exposes successful records", async () => {
            fetch.mockResolvedValue(reply(body("one")));
            const { result } = renderHook(() => useSubject("ws /&", "channel /&"));
            await waitFor(() => expect(result.current.items).toEqual([{ id: "one" }]));
            expect(String(fetch.mock.calls[0][0])).toBe(kind === "providers" ? "/api/v1/notification-providers?workspace_id=ws%20%2F%26" : "/api/v1/notification-channels/channel%20%2F%26/agents?workspace_id=ws%20%2F%26");
            expect(result.current).toMatchObject({ loading: false, error: null });
        });
        it.each([{}, null, { [kind]: {} }])("treats malformed collections as empty (%j)", async (data) => {
            fetch.mockResolvedValue(reply(data));
            const { result } = renderHook(() => useSubject("ws", "channel"));
            await waitFor(() => expect(result.current.loading).toBe(false));
            expect(result.current.items).toEqual([]);
        });
        it.each(["response", "body", "error"])("ignores obsolete %s when the workspace changes", async (stage) => {
            const pending = deferred<Response>();
            const decoding = deferred<unknown>();
            const json = vi.fn(() => decoding.promise);
            fetch.mockReturnValueOnce(stage === "body" ? Promise.resolve({ ok: true, json } as unknown as Response) : pending.promise).mockResolvedValueOnce(reply(body("current")));
            const { result, rerender } = renderHook(({ ws }) => useSubject(ws, "channel"), { initialProps: { ws: "old" } });
            if (stage === "body")
                await waitFor(() => expect(json).toHaveBeenCalledOnce());
            const signal = fetch.mock.calls[0][1]?.signal;
            rerender({ ws: "current" });
            await waitFor(() => expect(result.current.items).toEqual([{ id: "current" }]));
            expect(signal?.aborted).toBe(true);
            await act(async () => { if (stage === "error")
                pending.reject(new Error("obsolete"));
            else {
                pending.resolve(reply(body("obsolete")));
                decoding.resolve(body("obsolete"));
            } });
            expect(result.current).toMatchObject({ items: [{ id: "current" }], loading: false, error: null });
        });
        it("settles loading and prevents stale data after clearing scope", async () => {
            const pending = deferred<Response>();
            fetch.mockReturnValue(pending.promise);
            const { result, rerender } = renderHook(({ ws }: {
                ws: string | null;
            }) => useSubject(ws, "channel"), { initialProps: { ws: "old" as string | null } });
            expect(result.current.loading).toBe(true);
            rerender({ ws: null });
            expect(result.current).toMatchObject({ loading: false, error: null, items: [] });
            await act(async () => pending.resolve(reply(body("old"))));
            expect(result.current.items).toEqual([]);
            expect(fetch).toHaveBeenCalledOnce();
        });
        it.each([new Error("offline"), "opaque"])("surfaces transport failure and clears it after refresh (%s)", async (error) => {
            fetch.mockRejectedValueOnce(error).mockResolvedValueOnce(reply(body("recovered")));
            const { result } = renderHook(() => useSubject("ws", "channel"));
            await waitFor(() => expect(result.current.error).not.toBeNull());
            expect(result.current.error).toBe(error instanceof Error ? "offline" : kind === "providers" ? "failed to load providers" : "failed to load");
            await act(async () => { await result.current.refresh(); });
            expect(result.current).toMatchObject({ error: null, items: [{ id: "recovered" }] });
        });
        it("clears HTTP errors when the workspace disappears", async () => {
            fetch.mockResolvedValue(reply({}, 403));
            const { result, rerender } = renderHook(({ ws }: {
                ws: string | null;
            }) => useSubject(ws, "channel"), { initialProps: { ws: "ws" as string | null } });
            await waitFor(() => expect(result.current.error).toContain("403"));
            rerender({ ws: null });
            expect(result.current).toMatchObject({ error: null, loading: false, items: [] });
        });
        it("aborts requests on unmount", async () => {
            const pending = deferred<Response>();
            fetch.mockReturnValue(pending.promise);
            const view = renderHook(() => useSubject("ws", "channel"));
            const signal = fetch.mock.calls[0][1]?.signal;
            view.unmount();
            expect(signal?.aborted).toBe(true);
            await act(async () => pending.resolve(reply(body("late"))));
        });
    });
}
it("restores fallback categories when changing workspace or clearing scope", async () => {
    fetch.mockResolvedValueOnce(reply({ providers: [], categories: [{ key: "custom", label: "Custom" }] })).mockResolvedValueOnce(reply({ providers: [], categories: [] }));
    const { result, rerender } = renderHook(({ ws }: {
        ws: string | null;
    }) => useNotificationProviders(ws), { initialProps: { ws: "old" as string | null } });
    await waitFor(() => expect(result.current.categories).toEqual([{ key: "custom", label: "Custom" }]));
    rerender({ ws: "new" });
    await waitFor(() => expect(result.current.loading).toBe(false));
    expect(result.current.categories.map(c => c.key)).toEqual(["chat", "push", "incident"]);
    rerender({ ws: null });
    expect(result.current.categories.map(c => c.key)).toEqual(["chat", "push", "incident"]);
});
it("does not fetch absent channels and invalidates a changed channel", async () => {
    const pending = deferred<Response>();
    fetch.mockReturnValueOnce(pending.promise).mockResolvedValueOnce(reply({ agents: [{ id: "new" }] }));
    const { result, rerender } = renderHook(({ channel }: {
        channel: string | null;
    }) => useChannelAgents("ws", channel), { initialProps: { channel: null as string | null } });
    expect(fetch).not.toHaveBeenCalled();
    rerender({ channel: "old" });
    rerender({ channel: "new" });
    await waitFor(() => expect(result.current.agents).toEqual([{ id: "new" }]));
    await act(async () => pending.resolve(reply({ agents: [{ id: "old" }] })));
    expect(result.current.agents).toEqual([{ id: "new" }]);
    rerender({ channel: null });
    expect(result.current).toMatchObject({ agents: [], loading: false, error: null });
});
