"use client"

import * as React from "react"
import { RefreshCw } from "lucide-react"
import { Button } from "@/components/ui/button"
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs"
import { apiFetch } from "@/lib/api-fetch"
import { cn } from "@/lib/utils"
import { cellKey, columnName, parseWorkbookSnapshot, renderExtent, type WorkbookSheet, type WorkbookView } from "@/lib/workbooks/snapshot"

/** Smallest grid drawn, so a one-cell workbook still reads as a sheet. */
const MINIMUM = { rows: 10, cols: 5 }
/** Largest grid drawn (A1:AZ500); the rest of a bigger sheet is named, not rendered. */
export const WORKBOOK_RENDER_LIMIT = { rows: 500, cols: 52 }

type Load =
  | { status: "loading" }
  | { status: "ready"; rev: number; view: WorkbookView }
  | { status: "missing" }
  | { status: "error"; message: string }

const object = (value: unknown): value is Record<string, unknown> => Boolean(value) && typeof value === "object" && !Array.isArray(value)
const count = (value: unknown): value is number => Number.isSafeInteger(value) && (value as number) >= 0

function readResponse(body: unknown): { rev: number; view: WorkbookView } | null {
  if (!object(body) || !count(body.schema_version) || !count(body.rev)) return null
  const view = parseWorkbookSnapshot(body.snapshot, WORKBOOK_RENDER_LIMIT)
  return view ? { rev: body.rev, view } : null
}

/**
 * The workbook is private Page content: it is held in this component's state only, never in
 * the shared query cache, read with `no-store`, and dropped on access loss or session expiry.
 * A focus or refresh re-reads it so a person sees what an agent changed meanwhile.
 */
function useWorkbook(workspaceId: string, pageId: string) {
  const url = `/api/v1/pages/${encodeURIComponent("@" + pageId)}/workbook?${new URLSearchParams({ workspace_id: workspaceId })}`
  const [state, setState] = React.useState<Load>({ status: "loading" })
  const [refreshing, setRefreshing] = React.useState(false)
  const [refreshError, setRefreshError] = React.useState<string>()
  const current = React.useRef(state); current.current = state
  const inFlight = React.useRef<AbortController | null>(null)

  const load = React.useCallback(async () => {
    if (inFlight.current) return
    const abort = new AbortController()
    inFlight.current = abort
    const background = current.current.status === "ready"
    if (background) setRefreshing(true)
    else setState({ status: "loading" })
    const fail = (message: string) => {
      if (background) setRefreshError(message)
      else setState({ status: "error", message })
    }
    try {
      const response = await apiFetch(url, { cache: "no-store", signal: abort.signal })
      if (abort.signal.aborted) return
      // Not found and no access read the same, and either one drops what was on screen.
      if ([401, 403, 404].includes(response.status)) { setRefreshError(undefined); setState({ status: "missing" }); return }
      if (!response.ok) { fail(response.status === 503 ? "The workbook service is temporarily unavailable." : "The workbook could not be loaded."); return }
      const parsed = readResponse(await response.json().catch(() => null))
      if (abort.signal.aborted) return
      if (!parsed) { fail("This workbook could not be read."); return }
      setRefreshError(undefined)
      setState({ status: "ready", ...parsed })
    } catch {
      if (!abort.signal.aborted) fail("The workbook could not be loaded.")
    } finally {
      if (inFlight.current === abort) inFlight.current = null
      if (!abort.signal.aborted) setRefreshing(false)
    }
  }, [url])

  React.useEffect(() => {
    void load()
    const onFocus = () => { if (document.visibilityState !== "hidden") void load() }
    const expire = () => { inFlight.current?.abort(); inFlight.current = null; setState({ status: "missing" }) }
    window.addEventListener("focus", onFocus)
    document.addEventListener("visibilitychange", onFocus)
    window.addEventListener("auth:session-expired", expire)
    return () => {
      window.removeEventListener("focus", onFocus)
      document.removeEventListener("visibilitychange", onFocus)
      window.removeEventListener("auth:session-expired", expire)
      inFlight.current?.abort()
      inFlight.current = null
    }
  }, [load])

  return { state, refreshing, refreshError, reload: load }
}

export function PageWorkbook({ workspaceId, pageId, name }: { workspaceId: string; pageId: string; name?: string }) {
  const { state, refreshing, refreshError, reload } = useWorkbook(workspaceId, pageId)
  const [selected, setSelected] = React.useState<number>()
  const view = state.status === "ready" ? state.view : undefined
  const sheetIndex = view ? (selected !== undefined && selected < view.sheets.length ? selected : view.activeSheet) : 0

  return <section aria-label="Workbook page" className="flex min-h-0 flex-1 flex-col overflow-hidden">
    <div className="flex flex-wrap items-center gap-x-3 gap-y-1 border-b p-3">
      <h1 className="mr-auto font-semibold">{name ?? "Workbook"}</h1>
      {state.status === "ready" && <span className="text-sm tabular-nums text-muted-foreground">Revision {state.rev}</span>}
      {(state.status === "ready" || state.status === "error") && <Button type="button" size="sm" variant="outline" disabled={refreshing} onClick={() => void reload()}>
        <RefreshCw className={cn("h-3.5 w-3.5", refreshing && "animate-spin")} aria-hidden />
        {state.status === "error" ? "Retry" : refreshing ? "Refreshing…" : "Refresh"}
      </Button>}
      <p className="basis-full text-xs text-muted-foreground">Read-only preview. Edit by importing a file or through an admitted agent.</p>
    </div>
    {state.status === "loading" && <p role="status" className="p-6 text-sm text-muted-foreground">Loading workbook…</p>}
    {state.status === "missing" && <p role="alert" className="p-6">Workbook unavailable</p>}
    {state.status === "error" && <p role="alert" className="p-6 text-sm text-destructive">{state.message}</p>}
    {refreshError && <p role="alert" className="px-3 pt-2 text-sm text-destructive">{refreshError} Showing the last loaded revision.</p>}
    {view && (view.sheets.length === 0
      ? <p role="status" className="p-6 text-sm text-muted-foreground">This workbook has no sheets.</p>
      : <Tabs value={String(sheetIndex)} onValueChange={value => setSelected(Number(value))} className="min-h-0 flex-1 gap-0">
        {view.sheets.length > 1 && <TabsList aria-label="Sheets" className="m-2 max-w-full justify-start overflow-x-auto">
          {view.sheets.map((sheet, index) => <TabsTrigger key={index} value={String(index)} className="flex-none coarse:min-h-[44px]">{sheet.name}</TabsTrigger>)}
        </TabsList>}
        {view.sheets.map((sheet, index) => <TabsContent key={index} value={String(index)} className="flex min-h-0 flex-col">
          <WorkbookGrid sheet={sheet} />
        </TabsContent>)}
      </Tabs>)}
  </section>
}

const WorkbookGrid = React.memo(function WorkbookGrid({ sheet }: { sheet: WorkbookSheet }) {
  const extent = renderExtent(sheet, MINIMUM, WORKBOOK_RENDER_LIMIT)
  const columns = Array.from({ length: extent.cols }, (_, col) => columnName(col))
  return <>
    {sheet.count === 0 && <p role="status" className="px-3 py-2 text-sm text-muted-foreground">This sheet is empty.</p>}
    {extent.truncated && <p role="status" className="px-3 py-2 text-sm text-muted-foreground">
      Showing A1:{columns[columns.length - 1]}{extent.rows} of a sheet with {sheet.rows} rows and {sheet.cols} columns.
    </p>}
    <div className="min-h-0 flex-1 overflow-auto">
      <table aria-label={`Sheet ${sheet.name}`} className="border-separate border-spacing-0 text-xs">
        <thead>
          <tr>
            <td className="sticky left-0 top-0 z-20 border-b border-r bg-muted" />
            {columns.map(column => <th key={column} scope="col" className="sticky top-0 z-10 min-w-20 border-b border-r bg-muted px-2 py-1 text-center font-medium text-muted-foreground">{column}</th>)}
          </tr>
        </thead>
        <tbody>
          {Array.from({ length: extent.rows }, (_, row) => <tr key={row}>
            <th scope="row" className="sticky left-0 z-10 border-b border-r bg-muted px-2 py-1 text-right font-medium tabular-nums text-muted-foreground">{row + 1}</th>
            {columns.map((column, col) => {
              const cell = sheet.cells.get(cellKey(row, col))
              return <td key={column}
                title={cell?.formula !== undefined ? `=${cell.formula}` : undefined}
                data-formula={cell?.formula !== undefined ? "" : undefined}
                className={cn(
                  "h-6 max-w-72 truncate whitespace-pre border-b border-r border-border/60 bg-background px-2 py-1",
                  cell?.kind === "number" && "text-right tabular-nums",
                  cell?.kind === "bool" && "text-center",
                  cell?.kind === "error" && "text-center text-destructive",
                  cell?.formula !== undefined && "underline decoration-dotted decoration-muted-foreground/60 underline-offset-2",
                )}>{cell?.text}</td>
            })}
          </tr>)}
        </tbody>
      </table>
    </div>
  </>
})
