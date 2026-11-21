"use client"

import * as React from "react"
import { Play } from "lucide-react"
import { toast } from "@/lib/toast"

import { apiFetch } from "@/lib/api-fetch"
import { Textarea } from "@/components/ui/textarea"
import { Note } from "@/components/ui/note"
import { useCredentials } from "@/components/features/mcp/hooks/use-credentials"
import { nativeSelect } from "@/components/features/settings/shared"
import {
  CreateSurface, CreateSurfaceBody, CreateSurfaceField, CreateSurfaceFooter, CreateSurfaceHeader,
} from "@/components/layout/create-surface"
import { DecisionChip } from "./decision-chip"
import { useDecideAccess } from "./decide-access"

interface Crew { id: string; name: string }
interface Agent { id: string; name: string; crew_id?: string | null }

/**
 * "Try a request": POST /admin/keeper/ask puts a credential request to the
 * real judge as an operator and writes a real decision to the log. The server
 * takes the workspace from the session and ignores any other, so the dialog
 * names that workspace and is only offered while it is known. The route is
 * `roleManage`: only an OWNER or ADMIN of that workspace may ask, instance
 * administrator or not (useDecideAccess).
 */
export function AskDialog({ open, onOpenChange, workspaceId, workspaceName, onAsked }: {
  open: boolean
  onOpenChange: (o: boolean) => void
  workspaceId: string
  workspaceName: string
  onAsked: () => void
}) {
  const { allowed: canAct } = useDecideAccess(workspaceId, workspaceName)
  const [crews, setCrews] = React.useState<Crew[]>([])
  const [agents, setAgents] = React.useState<Agent[]>([])
  const { credentials } = useCredentials(open ? workspaceId : undefined)
  const [crew, setCrew] = React.useState("")
  const [agent, setAgent] = React.useState("")
  const [credential, setCredential] = React.useState("")
  const [intent, setIntent] = React.useState("")
  const [busy, setBusy] = React.useState(false)
  const [answer, setAnswer] = React.useState<{ decision: string; risk?: number; reason?: string } | null>(null)
  const [error, setError] = React.useState<string | null>(null)

  React.useEffect(() => {
    if (!open) return
    setAnswer(null); setError(null)
    const ws = encodeURIComponent(workspaceId)
    let live = true
    void (async () => {
      try {
        const [c, a] = await Promise.all([apiFetch(`/api/v1/crews?workspace_id=${ws}&limit=500`), apiFetch(`/api/v1/agents?workspace_id=${ws}&limit=500`)])
        if (!live) return
        const cj = c.ok ? await c.json() : [], aj = a.ok ? await a.json() : []
        if (live) { setCrews(Array.isArray(cj) ? cj : []); setAgents(Array.isArray(aj) ? aj : []) }
      } catch { /* the pickers stay empty and the form says so */ }
    })()
    return () => { live = false }
  }, [open, workspaceId])

  const crewAgents = agents.filter((a) => !crew || a.crew_id === crew)
  const chosenCredential = credentials.find((c) => c.id === credential)
  const valid = !!crew && !!agent && !!credential && intent.trim().length > 0

  async function ask() {
    if (!valid || busy || !canAct) return
    setBusy(true); setError(null); setAnswer(null)
    try {
      const r = await apiFetch(`/api/v1/admin/keeper/ask?workspace_id=${encodeURIComponent(workspaceId)}`, {
        method: "POST", headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ requesting_agent_id: agent, requesting_crew_id: crew, credential_id: credential, credential_name: chosenCredential?.name ?? "", intent: intent.trim() }),
      })
      const body = (await r.json().catch(() => ({}))) as { decision?: string; risk_score?: number; risk?: number; reason?: string; error?: string; detail?: string }
      if (!r.ok) { setError(body.detail ?? body.error ?? `The judge could not be asked (HTTP ${r.status}).`); return }
      setAnswer({ decision: body.decision ?? "", risk: body.risk_score ?? body.risk, reason: body.reason })
      toast.success(`${body.decision ?? "Answered"}${body.risk_score != null ? ` · risk ${body.risk_score}` : ""}`)
      onAsked()
    } catch (e) {
      setError(e instanceof Error ? e.message : "The judge could not be asked.")
    } finally {
      setBusy(false)
    }
  }

  return (
    <CreateSurface open={open} onOpenChange={onOpenChange} size="md">
      <CreateSurfaceHeader icon={Play} title="Try a request" description={`Asks the real judge in ${workspaceName}.`} onClose={() => onOpenChange(false)} />
      <CreateSurfaceBody className="grid gap-3">
        <Note tone="warn" data-testid="ask-warning">
          Runs the real judge and writes a real decision to the log of <b className="font-medium">{workspaceName}</b>. It can also reach the Inbox.
        </Note>
        <div className="grid gap-3 sm:grid-cols-2">
          <CreateSurfaceField label="Crew" htmlFor="ask-crew">
            <select id="ask-crew" className={nativeSelect} value={crew} onChange={(e) => { setCrew(e.target.value); setAgent("") }}>
              <option value="">Choose a crew</option>
              {crews.map((c) => <option key={c.id} value={c.id}>{c.name}</option>)}
            </select>
          </CreateSurfaceField>
          <CreateSurfaceField label="Agent" htmlFor="ask-agent">
            <select id="ask-agent" className={nativeSelect} value={agent} onChange={(e) => setAgent(e.target.value)} disabled={!crew}>
              <option value="">Choose an agent</option>
              {crewAgents.map((a) => <option key={a.id} value={a.id}>{a.name}</option>)}
            </select>
          </CreateSurfaceField>
        </div>
        <CreateSurfaceField label="Credential" htmlFor="ask-credential">
          <select id="ask-credential" className={nativeSelect} value={credential} onChange={(e) => setCredential(e.target.value)}>
            <option value="">Choose a credential</option>
            {credentials.map((c) => <option key={c.id} value={c.id}>{c.name}</option>)}
          </select>
        </CreateSurfaceField>
        <CreateSurfaceField label="What the agent says it needs it for" htmlFor="ask-intent">
          <Textarea id="ask-intent" rows={3} value={intent} onChange={(e) => setIntent(e.target.value)} placeholder="Look up three competitor pricing pages for the weekly scan" />
        </CreateSurfaceField>
        {!canAct && <p data-testid="ask-not-allowed" className="text-label text-muted-foreground">Only an Owner or Admin of {workspaceName} can try a request.</p>}
        {error && <p role="alert" className="text-label text-destructive">{error}</p>}
        {answer && (
          <div role="status" data-testid="ask-answer" className="flex flex-wrap items-center gap-2 rounded-lg border border-border p-3 text-control">
            <DecisionChip decision={answer.decision} />
            {answer.risk != null && <span className="font-mono text-label">risk {answer.risk}/10</span>}
            {answer.reason && <span className="text-label text-muted-foreground">{answer.reason}</span>}
          </div>
        )}
      </CreateSurfaceBody>
      <CreateSurfaceFooter hint={null} onCancel={() => onOpenChange(false)} cancelLabel={answer ? "Close" : "Cancel"}
        primaryLabel="Ask the judge" onPrimary={() => void ask()} primaryDisabled={!valid || busy || !canAct} busy={busy} />
    </CreateSurface>
  )
}
