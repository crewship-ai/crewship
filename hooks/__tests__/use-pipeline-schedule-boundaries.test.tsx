import { act, cleanup, renderHook, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { apiFetch } from "@/lib/api-fetch";
import { usePipelineSchedules } from "../use-pipeline-schedules";
const events = vi.hoisted(() => new Map<string, () => Promise<void>>());
vi.mock("@/hooks/use-realtime", () => ({ useRealtimeEvent: (name: string, cb: () => Promise<void>) => events.set(name, cb) }));
vi.mock("@/lib/api-fetch", () => ({ apiFetch: vi.fn() }));
const fetch = vi.mocked(apiFetch);
const reply = (body: unknown, status = 200) => new Response(JSON.stringify(body), { status });
function deferred<T>() { let resolve!: (v: T) => void; let reject!: (e: unknown) => void; const promise = new Promise<T>((yes, no) => { resolve = yes; reject = no; }); return { promise, resolve, reject }; }
const input = { name: "Daily", cron_expr: "0 9 * * *", timezone: "Europe/Prague" };
type Hook = ReturnType<typeof usePipelineSchedules>;
function mutate(method: string, hook: Hook) { return method === "create" ? hook.create(input) : method === "update" ? hook.update("id /", { enabled: false }) : hook.remove("id /"); }
beforeEach(() => { fetch.mockReset(); events.clear(); });
afterEach(() => cleanup());
it("loads all pages and subscribes to schedule lifecycle updates", async () => {
    const first = reply([{ id: "one" }]);
    first.headers.set("X-Next-Offset", "1");
    fetch.mockResolvedValueOnce(first).mockResolvedValueOnce(reply([{ id: "two" }]));
    const { result } = renderHook(() => usePipelineSchedules("ws /"));
    await waitFor(() => expect(result.current.schedules).toEqual([{ id: "one" }, { id: "two" }]));
    expect(String(fetch.mock.calls[1][0])).toBe("/api/v1/workspaces/ws%20%2F/pipeline-schedules?limit=200&offset=1");
    for (const event of ["pipeline.run.started", "pipeline.run.completed", "pipeline.saved"]) {
        fetch.mockResolvedValueOnce(reply([{ id: event }]));
        await act(async () => events.get(event)!());
        expect(result.current.schedules).toEqual([{ id: event }]);
    }
});
it("keeps the last good list on error and clears errors on recovery", async () => {
    fetch.mockResolvedValueOnce(reply([{ id: "existing" }])).mockResolvedValueOnce(reply({}, 503)).mockResolvedValueOnce(reply([]));
    const { result } = renderHook(() => usePipelineSchedules("ws"));
    await waitFor(() => expect(result.current.loading).toBe(false));
    await act(async () => result.current.refresh());
    expect(result.current).toMatchObject({ schedules: [{ id: "existing" }], error: "pipeline schedules: 503", loading: false });
    await act(async () => result.current.refresh());
    expect(result.current).toMatchObject({ schedules: [], error: null });
});
it.each([new Error("offline"), "offline"])("reports transport failure %s", async (error) => {
    fetch.mockRejectedValue(error);
    const { result } = renderHook(() => usePipelineSchedules("ws"));
    await waitFor(() => expect(result.current.error).toBe("offline"));
});
it.each(["response", "error"])("ignores obsolete list %s after clearing scope", async (stage) => {
    const pending = deferred<Response>();
    fetch.mockReturnValue(pending.promise);
    const { result, rerender } = renderHook(({ ws }: {
        ws: string | null;
    }) => usePipelineSchedules(ws), { initialProps: { ws: "ws" as string | null } });
    const signal = fetch.mock.calls[0][1]?.signal;
    rerender({ ws: null });
    expect(signal?.aborted).toBe(true);
    expect(result.current).toMatchObject({ schedules: [], loading: false, error: null });
    await act(async () => { if (stage === "error")
        pending.reject(new Error("old"));
    else
        pending.resolve(reply([{ id: "old" }])); });
    expect(result.current).toMatchObject({ schedules: [], loading: false, error: null });
});
it.each(["create", "update", "remove"])("%s uses the scoped URL and refreshes after success", async (method) => {
    fetch.mockResolvedValueOnce(reply([])).mockResolvedValueOnce(reply({ id: "saved" })).mockResolvedValueOnce(reply([{ id: "saved" }]));
    const { result } = renderHook(() => usePipelineSchedules("ws /"));
    await waitFor(() => expect(result.current.loading).toBe(false));
    await act(async () => { await mutate(method, result.current); });
    expect(fetch.mock.calls[1][0]).toBe(`/api/v1/workspaces/ws%20%2F/pipeline-schedules${method === "create" ? "" : "/id%20%2F"}`);
    expect(fetch.mock.calls[1][1]?.method).toBe(method === "create" ? "POST" : method === "update" ? "PATCH" : "DELETE");
    expect(result.current.schedules).toEqual([{ id: "saved" }]);
});
it.each(["create", "update", "remove"])("%s surfaces server rejection without refreshing", async (method) => {
    fetch.mockResolvedValueOnce(reply([])).mockResolvedValueOnce(new Response("policy denied", { status: 403 }));
    const { result } = renderHook(() => usePipelineSchedules("ws"));
    await waitFor(() => expect(result.current.loading).toBe(false));
    // create/update throw the server's sentence with the status on the error (#2862).
    await expect(mutate(method, result.current)).rejects.toThrow(method === "remove" ? "403" : "policy denied");
    expect(fetch).toHaveBeenCalledTimes(2);
});
it.each(["create", "update", "remove"])("late %s cannot refresh the previous workspace", async (method) => {
    const pending = deferred<Response>();
    fetch.mockResolvedValueOnce(reply([{ id: "old" }])).mockReturnValueOnce(pending.promise).mockResolvedValueOnce(reply([{ id: "new" }]));
    const { result, rerender } = renderHook(({ ws }) => usePipelineSchedules(ws), { initialProps: { ws: "old" } });
    await waitFor(() => expect(result.current.loading).toBe(false));
    let mutation!: ReturnType<typeof mutate>;
    act(() => { mutation = mutate(method, result.current); });
    rerender({ ws: "new" });
    await waitFor(() => expect(result.current.schedules).toEqual([{ id: "new" }]));
    await act(async () => { pending.resolve(reply({ id: "saved" })); await mutation; });
    expect(fetch).toHaveBeenCalledTimes(3);
    expect(result.current.schedules).toEqual([{ id: "new" }]);
});
it("treats a missing deleted schedule as success", async () => {
    fetch.mockResolvedValueOnce(reply([])).mockResolvedValueOnce(reply({}, 404)).mockResolvedValueOnce(reply([]));
    const { result } = renderHook(() => usePipelineSchedules("ws"));
    await waitFor(() => expect(result.current.loading).toBe(false));
    await act(async () => result.current.remove("missing"));
    expect(fetch).toHaveBeenCalledTimes(3);
});
it("previews encoded cron/timezone and count without mutating schedules", async () => {
    fetch.mockResolvedValueOnce(reply([])).mockResolvedValueOnce(reply({ occurrences: ["tomorrow"] })).mockResolvedValueOnce(reply({ occurrences: [] })).mockResolvedValueOnce(new Response(JSON.stringify({ error: "bad cron" }), { status: 400 }));
    const { result } = renderHook(() => usePipelineSchedules("ws"));
    await waitFor(() => expect(result.current.loading).toBe(false));
    expect(await result.current.preview("*/5 * * * *", "Europe/Prague")).toEqual({ occurrences: ["tomorrow"] });
    expect(new URL(String(fetch.mock.calls[1][0]), "http://localhost").searchParams.get("count")).toBe("5");
    await result.current.preview("* * * * *", "UTC", 2);
    expect(String(fetch.mock.calls[2][0])).toContain("count=2");
    await expect(result.current.preview("invalid", "UTC")).rejects.toThrow(/^bad cron$/); // the server's sentence, not the raw JSON body (#2862)
});
it("does not perform scoped operations without a workspace", async () => {
    const { result } = renderHook(() => usePipelineSchedules(null));
    expect(await result.current.create(input)).toBeNull();
    expect(await result.current.update("id", {})).toBeNull();
    await result.current.remove("id");
    await expect(result.current.preview("* * * * *", "UTC")).rejects.toThrow("no workspace");
    expect(fetch).not.toHaveBeenCalled();
});
