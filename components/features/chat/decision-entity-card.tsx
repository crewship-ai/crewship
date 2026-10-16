"use client"

import { useId, useRef, useState } from "react"
import { Button } from "@/components/ui/button"
import { E2CommandSchemas, type DecisionRef } from "@/lib/chat-e2-cards"
import { EntityCardFrame } from "./issue-entity-card"

type Answer = ReturnType<typeof E2CommandSchemas.decision_answer.parse>
export type DecisionSubmitResult = "saved" | "conflict" | "unknown" | "refused" | "invalid"
type Props = {
  reference: DecisionRef
  rechecking?: boolean
  onAnswer: (answer: Answer, operationKey: string) => Promise<DecisionSubmitResult>
  onRecheck: () => void
  recovery?: { answer: Answer; key: string } | null
  onReviewConflict?: () => void
  feedback?: string
}

/** Source-authorized actions only; transport owns fresh access and durable replay. */
export function DecisionEntityCard(props: Props) {
  const source = props.reference.decision.source
  return <DecisionCardForm key={JSON.stringify([props.reference.entity.workspace_id, source.type, source.id])} {...props} />
}

function DecisionCardForm({ reference, rechecking = false, onAnswer, onRecheck, recovery, onReviewConflict, feedback }: Props) {
  const card = reference.decision
  const prefix = useId()
  const [values, setValues] = useState<Record<string, string>>(() => Object.fromEntries(Object.entries(recovery?.answer.values ?? {}).map(([key, value]) => [key, String(value)])))
  const [reviewedVersion, setReviewedVersion] = useState(recovery?.answer.expected_version ?? card.source.version)
  const sourceChanged = reviewedVersion !== card.source.version
  const [error, setError] = useState("")
  const [busy, setBusy] = useState(false)
  const [result, setResult] = useState<DecisionSubmitResult | null>(recovery ? "unknown" : null)
  const operation = useRef<{ answer: Answer; key: string } | null>(recovery ?? null)
  const sending = useRef(false)
  const terminal = card.status !== "pending"
  const blocked = sourceChanged || rechecking || busy || result === "unknown" || result === "conflict" || result === "saved" || result === "refused"
  const send = async (op: { answer: Answer; key: string }) => {
    if (sending.current || rechecking || terminal) return
    sending.current = true
    setBusy(true)
    setError("")
    try {
      const outcome = await onAnswer(op.answer, op.key)
      setResult(outcome)
      if (outcome === "saved" || outcome === "refused") onRecheck()
    } catch {
      setResult("unknown")
    } finally {
      sending.current = false
      setBusy(false)
    }
  }
  const decide = (action: Answer["action"], actionId?: string) => {
    if (blocked || terminal) return
    const data: Answer["values"] = {}
    try {
      for (const field of card.fields) {
        const raw = values[field.id] ?? ""
        if (raw === "") {
          if (field.required && action !== "reject" && !card.workflow_actions?.some(item => item.id === actionId && !item.approved)) throw new Error(`${field.label}: Required`)
          continue
        }
        if (field.type === "number") {
          const value = Number(raw)
          if (!Number.isFinite(value)) throw new Error(`${field.label}: Enter a finite number`)
          data[field.id] = value
        } else if (field.type === "boolean") data[field.id] = raw === "true"
        else if (field.type === "datetime") {
          const value = new Date(raw)
          if (!Number.isFinite(value.getTime())) throw new Error(`${field.label}: Enter a date and time`)
          data[field.id] = value.toISOString()
        } else if (field.type === "choice") {
          if (!field.options.some(option => option.value === raw)) throw new Error(`${field.label}: Select an available option`)
          data[field.id] = raw
        } else data[field.id] = raw
      }
      const answer = E2CommandSchemas.decision_answer.parse({ source_id: card.source.id, expected_version: card.source.version, action, ...(actionId ? { action_id: actionId } : {}), values: data })
      operation.current = { answer, key: crypto.randomUUID() }
      void send(operation.current)
    } catch (e) { setError(e instanceof Error ? e.message : "Check the fields") }
  }
  return <EntityCardFrame>
    <h3 className="font-medium [overflow-wrap:anywhere]">{card.title}</h3>
    <p className="text-xs text-muted-foreground">{card.decision_type === "information" ? "Information request" : card.decision_type === "workflow" ? "Workflow decision — applies to this step" : "Whole-run approval — applies to this run"}</p>
    {card.expires_at && <p className="text-xs">Deadline: <time dateTime={card.expires_at}>{card.expires_at}</time></p>}
    {terminal ? <div role="status" className="space-y-1">
      <p>{card.status}</p>
      {card.resolution && <><p>By {card.resolution.answered_by} at <time dateTime={card.resolution.answered_at}>{card.resolution.answered_at}</time></p>
        {card.resolution.action_id && <p>{card.workflow_actions?.find(action => action.id === card.resolution?.action_id)?.label}</p>}
        <dl>{card.fields.filter(field => Object.hasOwn(card.resolution!.values, field.id)).map(field => <div key={field.id}><dt>{field.label}</dt><dd className="whitespace-pre-wrap [overflow-wrap:anywhere]">{String(card.resolution!.values[field.id])}</dd></div>)}</dl></>}
    </div> : <fieldset disabled={blocked} className="min-w-0 space-y-3">
      <legend className="sr-only">Decision response</legend>
      {card.fields.map(field => {
        const id = `${prefix}-${field.id}`
        const shared = { id, value: values[field.id] ?? "", onChange: (event: React.ChangeEvent<HTMLInputElement | HTMLTextAreaElement | HTMLSelectElement>) => { setValues(current => ({ ...current, [field.id]: event.target.value })); setError("") }, className: "w-full min-w-0 rounded-md border bg-background p-2 coarse:min-h-12", "aria-required": field.required }
        return <div key={field.id} className="space-y-1"><label htmlFor={id}>{field.label}{field.required ? " (required for approval or answer)" : ""}</label>
          {field.type === "textarea" ? <textarea {...shared} maxLength={10000} /> : field.type === "choice" || field.type === "boolean" ? <select {...shared}><option value="">Select…</option>{field.type === "boolean" ? <><option value="true">Yes</option><option value="false">No</option></> : field.type === "choice" ? field.options.map(option => <option key={option.value} value={option.value}>{option.label}</option>) : null}</select> : <input {...shared} type={field.type === "datetime" ? "datetime-local" : field.type === "number" ? "number" : "text"} step={field.type === "number" ? "any" : undefined} maxLength={10000} />}
        </div>
      })}
      <div className="flex flex-wrap gap-2">{card.workflow_actions ? card.workflow_actions.filter(action => (card.allowed_actions.includes("answer") || card.allowed_actions.includes(action.approved ? "approve" : "reject"))).map(action => <Button type="button" key={action.id} className="coarse:min-h-12" variant={action.approved ? "default" : "outline"} onClick={() => decide(card.allowed_actions.includes("answer") ? "answer" : action.approved ? "approve" : "reject", action.id)}>{action.label}</Button>) : card.allowed_actions.filter(action => action !== "view").map(action => <Button type="button" key={action} className="coarse:min-h-12" onClick={() => decide(action as Answer["action"])}>{action === "answer" ? "Send answer" : action === "approve" ? "Approve run" : "Reject run"}</Button>)}</div>
    </fieldset>}
    {result === "invalid" && <p role="alert">{feedback || "Response was not accepted. Review the fields and try again."}</p>}
    {error && <p role="alert">{error}</p>}
    {busy && <p role="status">Sending…</p>}
    {result === "unknown" && !terminal && <div role="alert"><p>The outcome is unknown. Your response is kept.</p><Button type="button" disabled={busy || rechecking} className="coarse:min-h-12" onClick={() => operation.current && void send(operation.current)}>Retry the same response</Button></div>}
    {(result === "conflict" || sourceChanged) && !terminal && <div role="alert"><p>This decision changed. Review its current version before answering again.</p><Button type="button" className="coarse:min-h-12" onClick={onRecheck}>Review current decision</Button>{reviewedVersion !== card.source.version && <Button type="button" className="coarse:min-h-12" onClick={() => { onReviewConflict?.(); operation.current = null; setReviewedVersion(card.source.version); setResult(null) }}>Use reviewed version; keep my response</Button>}</div>}
    {result === "refused" && <p role="alert">This response is no longer permitted. Checking current access…</p>}
  </EntityCardFrame>
}
