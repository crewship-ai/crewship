import { act, cleanup, renderHook, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { apiFetch } from "@/lib/api-fetch";
import { isActiveRunStatus, usePipelineRunRecords } from "../use-pipeline-run-records";
const events = vi.hoisted(() => new Map<string, () => Promise<void>>());
vi.mock("@/hooks/use-realtime", () => ({ useRealtimeEvent: (name: string, cb: () => Promise<void>) => events.set(name, cb) }));
vi.mock("@/lib/api-fetch", () => ({ apiFetch: vi.fn() }));
const fetch = vi.mocked(apiFetch);
const reply = (body: unknown, status = 200) => new Response(JSON.stringify(body), { status });
function deferred<T>() { let resolve!: (v: T) => void; let reject!: (v: unknown) => void; const promise = new Promise<T>((yes, no) => { resolve = yes; reject = no; }); return { promise, resolve, reject }; }
beforeEach(() => { fetch.mockReset(); events.clear(); });
afterEach(() => cleanup());
it("encodes scope and paging filters and refreshes on lifecycle events", async () => {
    fetch.mockResolvedValueOnce(reply([{ id: "first" }]));
    const { result } = renderHook(() => usePipelineRunRecords("ws /", "slug /", "waiting", "cursor +"));
    await waitFor(() => expect(result.current.records).toEqual([{ id: "first" }]));
    expect(fetch.mock.calls[0][0]).toBe("/api/v1/workspaces/ws%20%2F/pipelines/slug%20%2F/run-records?limit=50&before=cursor%20%2B&status=waiting");
    for (const event of ["pipeline.run.started", "pipeline.run.completed", "pipeline.run.failed"]) {
        fetch.mockResolvedValueOnce(reply([{ id: event }]));
        await act(async () => events.get(event)!());
        expect(result.current.records).toEqual([{ id: event }]);
    }
});
it("falls back on 503 and returns to records after recovery", async () => {
    fetch.mockResolvedValueOnce(reply({}, 503)).mockResolvedValueOnce(reply([{ id: "recovered" }]));
    const { result } = renderHook(() => usePipelineRunRecords("ws", "slug"));
    await waitFor(() => expect(result.current.legacy).toBe(true));
    expect(result.current).toMatchObject({ error: null, records: [], loading: false });
    await act(async () => result.current.refresh());
    expect(result.current).toMatchObject({ legacy: false, error: null, records: [{ id: "recovered" }] });
});
it.each([{}, null])("tolerates malformed collection %j", async (body) => {
    fetch.mockResolvedValue(reply(body));
    const { result } = renderHook(() => usePipelineRunRecords("ws", "slug"));
    await waitFor(() => expect(result.current.loading).toBe(false));
    expect(result.current.records).toEqual([]);
});
it("retains known records on HTTP errors and clears legacy mode", async () => {
    fetch.mockResolvedValueOnce(reply([{ id: "known" }])).mockResolvedValueOnce(reply({}, 403));
    const { result } = renderHook(() => usePipelineRunRecords("ws", "slug"));
    await waitFor(() => expect(result.current.loading).toBe(false));
    await act(async () => result.current.refresh());
    expect(result.current).toMatchObject({ records: [{ id: "known" }], legacy: false, error: "run-records: 403" });
});
it.each([new Error("offline"), "offline"])("reports transport error %s", async (error) => {
    fetch.mockRejectedValue(error);
    const { result } = renderHook(() => usePipelineRunRecords("ws", "slug"));
    await waitFor(() => expect(result.current.error).toBe("offline"));
    expect(result.current.legacy).toBe(false);
});
it.each(["response", "body", "error"])("ignores obsolete %s when changing routine", async (stage) => {
    const pending = deferred<Response>();
    const decoding = deferred<unknown>();
    const json = vi.fn(() => decoding.promise);
    fetch.mockReturnValueOnce(stage === "body" ? Promise.resolve({ ok: true, status: 200, json } as unknown as Response) : pending.promise).mockResolvedValueOnce(reply([{ id: "new" }]));
    const { result, rerender } = renderHook(({ slug }) => usePipelineRunRecords("ws", slug), { initialProps: { slug: "old" } });
    if (stage === "body")
        await waitFor(() => expect(json).toHaveBeenCalledOnce());
    const signal = fetch.mock.calls[0][1]?.signal;
    rerender({ slug: "new" });
    await waitFor(() => expect(result.current.records).toEqual([{ id: "new" }]));
    expect(signal?.aborted).toBe(true);
    await act(async () => { if (stage === "error")
        pending.reject(new Error("old"));
    else {
        pending.resolve(reply({}, 503));
        decoding.resolve([{ id: "old" }]);
    } });
    expect(result.current).toMatchObject({ records: [{ id: "new" }], error: null, legacy: false, loading: false });
});
it.each(["workspace", "slug"])("clears loading and data after removing %s", async (which) => {
    const pending = deferred<Response>();
    fetch.mockReturnValue(pending.promise);
    const { result, rerender } = renderHook(({ ws, slug }: {
        ws: string | null;
        slug: string | null;
    }) => usePipelineRunRecords(ws, slug), { initialProps: { ws: "ws" as string | null, slug: "slug" as string | null } });
    expect(result.current.loading).toBe(true);
    rerender({ ws: which === "workspace" ? null : "ws", slug: which === "slug" ? null : "slug" });
    expect(result.current).toMatchObject({ loading: false, error: null, legacy: false, records: [] });
    await act(async () => pending.resolve(reply([{ id: "old" }])));
    expect(result.current.records).toEqual([]);
});
it.each(["queued", "running", "waiting"])("recognizes cancellable status %s", status => expect(isActiveRunStatus(status)).toBe(true));
it.each(["completed", "failed", "cancelled", "dry_run", "interrupted", "unknown"])("rejects terminal or unknown status %s", status => expect(isActiveRunStatus(status)).toBe(false));
