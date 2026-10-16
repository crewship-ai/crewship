"use client"
import { useEffect, useRef, useState } from 'react'
import { Button } from '@/components/ui/button'
import type { BoardRequestDetail } from '@/hooks/use-board-requests'

/** A private read, never a renderer or an insertion proposal. A changed Board
 * invalidates the drift assessment; the human explicitly fetches it again. */
export function BoardDiagramSource({ workspaceId, slug, requestId, boardSeq, status, detail }: {
  workspaceId: string; slug: string; requestId: string; boardSeq: number; status: string
  detail: (id: string, signal?: AbortSignal) => Promise<BoardRequestDetail>
}) {
  const context = JSON.stringify([workspaceId, slug, requestId, boardSeq, status])
  const activeContext = useRef(context); activeContext.current = context
  const pending = useRef<AbortController | null>(null)
  const [state, setState] = useState<{ context: string; loading?: boolean; source?: BoardRequestDetail; error?: string } | null>(null)
  useEffect(() => { setState(null); return () => pending.current?.abort() }, [context])
  const current = state?.context === context ? state : null
  const load = async () => {
    pending.current?.abort()
    const controller = new AbortController(); pending.current = controller
    setState({ context, loading: true })
    try {
      const source = await detail(requestId, controller.signal)
      if (controller.signal.aborted || activeContext.current !== context) return
      if (source.id !== requestId || source.kind !== 'diagram' || typeof source.mermaid_source !== 'string' || new TextEncoder().encode(source.mermaid_source).length > 32768 || typeof source.edited_since_insert !== 'boolean') throw new Error('Diagram source response is unavailable or exceeds 32 KiB.')
      setState({ context, source })
    } catch (failure) {
      if (!controller.signal.aborted && activeContext.current === context) setState({ context, error: failure instanceof Error ? failure.message : 'Could not read diagram source.' })
    }
  }
  return <div className="space-y-2">
    <Button variant="ghost" size="sm" className="coarse:h-12" disabled={current?.loading} onClick={() => void load()}>{current?.loading ? 'Reading Mermaid source…' : current?.source ? 'Refresh Mermaid source' : 'View Mermaid source'}</Button>
    {current && <Button variant="ghost" size="sm" className="coarse:h-12" onClick={() => { pending.current?.abort(); setState(null) }}>Close source</Button>}
    {current?.error && <p role="alert" className="text-sm text-destructive">{current.error}</p>}
    {current?.source && <section aria-label="Mermaid source review" className="space-y-2 rounded-md border p-3">
      <p className="text-xs text-muted-foreground">Original agent Mermaid source. The inserted drawing is a human-confirmed conversion and is not proof of an exact server conversion.</p>
      {current.source.result_seq != null && <p role="status" className={`text-sm ${current.source.edited_since_insert ? 'text-warn' : 'text-muted-foreground'}`}>{current.source.edited_since_insert ? 'The inserted drawing changed after insertion. This saved source does not describe all current drawing edits.' : 'The inserted drawing matches its recorded insertion baseline.'}</p>}
      <pre aria-label="Original Mermaid source" className="max-h-80 overflow-auto whitespace-pre-wrap break-words text-xs">{current.source.mermaid_source}</pre>
    </section>}
  </div>
}
