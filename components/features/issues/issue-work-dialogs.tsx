"use client"

import * as React from "react"
import { Bot, Play, Send, User } from "lucide-react"

import { Combobox, type ComboboxOption } from "@/components/ui/combobox"
import { Textarea } from "@/components/ui/textarea"
import { Switch } from "@/components/ui/switch"
import { AgentAvatar } from "@/components/ui/agent-avatar"
import {
  CreateSurface,
  CreateSurfaceBody,
  CreateSurfaceChoice,
  CreateSurfaceField,
  CreateSurfaceFooter,
  CreateSurfaceHeader,
  CreateSurfaceRefusal,
  CreateSurfaceToggleRow,
} from "@/components/layout/create-surface"
import { useIssuePeople } from "@/hooks/use-issue-people"
import type { IssueWork } from "@/hooks/use-issue-work"
import type { MentionAgent } from "@/lib/mentions"
import type { Mission } from "@/lib/types/mission"
import { MAX_REVIEW_ROUNDS } from "./issue-session"

/**
 * The work dialogs of the issue page — Hand off, Delegate, Submit result and
 * Request changes — each a CreateSurface (M) instead of the inline forms the
 * Current work card used to unfold. One mounts at a time, chosen by
 * `work.dialog`.
 */
export function IssueWorkDialogs({ issue, agents, work }: { issue: Mission; agents: readonly MentionAgent[]; work: IssueWork }) {
  const d = work.dialog
  const close = (open: boolean) => {
    if (!open && !work.busy) work.closeDialog()
  }
  return (
    <>
      <HandOffDialog
        open={d?.kind === "handoff" || d?.kind === "delegate"}
        onOpenChange={close}
        issue={issue}
        agents={agents}
        work={work}
        delegate={d?.kind === "delegate"}
        retry={d?.kind === "handoff" && Boolean(d.retry)}
        initialTo={d?.kind === "handoff" ? d.to : undefined}
      />
      <SubmitResultDialog open={d?.kind === "submit"} onOpenChange={close} issue={issue} work={work} />
      <RequestChangesDialog open={d?.kind === "request_changes"} onOpenChange={close} issue={issue} work={work} />
    </>
  )
}

function revisionHint(issue: Mission) {
  return `Revision ${issue.work_revision ?? 0} · operation id is generated`
}

interface Candidate {
  id: string
  name: string
  seed?: string
  description?: string
}

function WhoPicker({
  to,
  candidates,
  value,
  onChange,
  error,
}: {
  to: "agent" | "person"
  candidates: Candidate[]
  value: string
  onChange: (id: string) => void
  error?: string
}) {
  const options: ComboboxOption[] = candidates.map((c) => ({
    value: c.id,
    label: c.name,
    description: c.description,
    icon:
      to === "agent" ? (
        <AgentAvatar seed={c.seed ?? c.id} alt="" className="size-5 rounded-full" />
      ) : (
        <span className="grid size-5 place-items-center rounded-full bg-muted text-muted-foreground"><User className="size-3" aria-hidden /></span>
      ),
  }))
  const noun = to === "agent" ? "agents" : "people"
  return (
    <CreateSurfaceField label="Who" htmlFor="work-who" error={error} required>
      <Combobox
        id="work-who"
        modal
        ariaLabel="Who"
        value={value || null}
        onChange={onChange}
        options={options}
        placeholder={to === "agent" ? "Choose an agent" : "Choose a person"}
        searchPlaceholder={`Search ${noun}`}
        emptyText={candidates.length === 0 ? `No ${noun} to hand off to.` : "Nobody matches."}
      />
    </CreateSurfaceField>
  )
}

function HandOffDialog({
  open,
  onOpenChange,
  issue,
  agents,
  work,
  delegate,
  retry,
  initialTo,
}: {
  open: boolean
  onOpenChange: (open: boolean) => void
  issue: Mission
  agents: readonly MentionAgent[]
  work: IssueWork
  delegate: boolean
  retry: boolean
  initialTo?: "agent" | "person"
}) {
  const [to, setTo] = React.useState<"agent" | "person">("agent")
  const [who, setWho] = React.useState("")
  const [note, setNote] = React.useState("")
  const [start, setStart] = React.useState(true)
  const [tried, setTried] = React.useState(false)
  const { people, error: peopleError } = useIssuePeople(issue.workspace_id, open && !delegate)

  React.useEffect(() => {
    if (!open) return
    setTo(delegate ? "agent" : (initialTo ?? (issue.work_mode === "human" ? "agent" : "person")))
    setWho(retry ? (issue.delegate?.id ?? "") : "")
    setNote("")
    setStart(true)
    setTried(false)
  }, [open, delegate, retry, initialTo, issue.work_mode, issue.delegate?.id])

  const candidates: Candidate[] =
    to === "agent"
      ? agents.map((a) => ({ id: a.id, name: a.name, seed: a.avatar_seed ?? a.slug, description: a.role_title ?? undefined }))
      : people.map((p) => ({ id: p.id, name: p.name }))

  // The work endpoint takes no transfer without a note (issue_handler_work.go):
  // it goes into the brief history and the receiver's Inbox item.
  const noteRequired = true
  const ready = Boolean(who) && (!noteRequired || note.trim() !== "")
  const startOffered = to === "agent"
  const go = () => {
    setTried(true)
    if (!ready || work.busy) return
    void work.handOff(to, who, note.trim(), { start: startOffered && start })
  }
  const id = issue.identifier ?? "this issue"
  const title = delegate ? "Delegate" : retry ? `Retry ${id} with a note` : `Hand off ${id}`
  const description = delegate
    ? "You stay the owner. Pick the agent that does the work."
    : "Give the work to an agent or a person. A note is required; it goes into the brief history."

  return (
    <CreateSurface open={open} onOpenChange={onOpenChange} size="md" onSubmit={go} dirty={note.trim() !== "" || Boolean(who)} discardLabel="this handoff">
      <CreateSurfaceHeader concept="issues" icon={delegate ? Bot : undefined} title={title} description={description} onClose={() => onOpenChange(false)} />
      <CreateSurfaceBody>
        {!delegate && (
          <CreateSurfaceChoice
            ariaLabel="Hand off to"
            value={to}
            onChange={(v) => {
              setTo(v)
              setWho("")
            }}
            options={[
              { value: "agent", label: <span className="inline-flex items-center gap-1.5"><Bot className="size-3.5" aria-hidden />To an agent</span>, hint: "Prepares the issue for its run, with your note" },
              { value: "person", label: <span className="inline-flex items-center gap-1.5"><User className="size-3.5" aria-hidden />To a person</span>, hint: "They hold the work until they submit or hand it on" },
            ]}
          />
        )}
        <WhoPicker
          to={to}
          candidates={candidates}
          value={who}
          onChange={setWho}
          error={tried && !who ? `Pick ${to === "agent" ? "an agent" : "a person"}.` : to === "person" && peopleError ? "People could not be loaded. Close and reopen to retry." : undefined}
        />
        <CreateSurfaceField
          label={noteRequired ? "Note (required)" : "Note"}
          htmlFor="work-note"
          required={noteRequired}
          error={tried && noteRequired && !note.trim() ? "Say what is done, what is needed next and which files matter." : undefined}
        >
          <Textarea
            id="work-note"
            rows={4}
            maxLength={20000}
            value={note}
            onChange={(e) => setNote(e.target.value)}
            placeholder="What is done, what is needed next, and the files that matter"
          />
        </CreateSurfaceField>
        {startOffered && (
          <CreateSurfaceToggleRow
            label="Start work now"
            hint="Delegating alone does not start a run."
            control={<Switch checked={start} onCheckedChange={setStart} aria-label="Start work now" />}
          />
        )}
        <CreateSurfaceRefusal message={work.error} />
      </CreateSurfaceBody>
      <CreateSurfaceFooter
        hint={revisionHint(issue)}
        onCancel={() => onOpenChange(false)}
        primaryLabel={work.busy ? "Saving…" : delegate ? (start ? "Delegate and start" : "Delegate") : "Hand off"}
        primaryIcon={startOffered && start ? Play : undefined}
        onPrimary={go}
        primaryDisabled={!ready}
        busy={work.busy}
      />
    </CreateSurface>
  )
}

function SubmitResultDialog({ open, onOpenChange, issue, work }: { open: boolean; onOpenChange: (open: boolean) => void; issue: Mission; work: IssueWork }) {
  const [note, setNote] = React.useState("")
  const [tried, setTried] = React.useState(false)
  React.useEffect(() => {
    if (open) {
      setNote("")
      setTried(false)
    }
  }, [open])
  const go = () => {
    setTried(true)
    if (!note.trim() || work.busy) return
    void work.submit(note.trim())
  }
  return (
    <CreateSurface open={open} onOpenChange={onOpenChange} size="md" onSubmit={go} dirty={note.trim() !== ""} discardLabel="this result">
      <CreateSurfaceHeader
        concept="issues"
        icon={Send}
        title={`Submit result for ${issue.identifier ?? "this issue"}`}
        description="Moves the issue to In review. The owner accepts it or asks for changes."
        onClose={() => onOpenChange(false)}
      />
      <CreateSurfaceBody>
        <CreateSurfaceField label="What you did (required)" htmlFor="work-result" required error={tried && !note.trim() ? "Describe the result and how you checked it." : undefined}>
          <Textarea id="work-result" rows={5} maxLength={20000} value={note} onChange={(e) => setNote(e.target.value)} placeholder="The result, and how you verified it" />
        </CreateSurfaceField>
        <CreateSurfaceRefusal message={work.error} />
      </CreateSurfaceBody>
      <CreateSurfaceFooter hint={revisionHint(issue)} onCancel={() => onOpenChange(false)} primaryLabel={work.busy ? "Saving…" : "Submit result"} onPrimary={go} primaryDisabled={!note.trim()} busy={work.busy} />
    </CreateSurface>
  )
}

function RequestChangesDialog({ open, onOpenChange, issue, work }: { open: boolean; onOpenChange: (open: boolean) => void; issue: Mission; work: IssueWork }) {
  const [note, setNote] = React.useState("")
  const [tried, setTried] = React.useState(false)
  React.useEffect(() => {
    if (open) {
      setNote("")
      setTried(false)
    }
  }, [open])
  const round = Math.min((issue.execution?.attempt ?? 0) + 1, MAX_REVIEW_ROUNDS)
  const go = () => {
    setTried(true)
    if (!note.trim() || work.busy) return
    void work.run("request_changes", note.trim())
  }
  return (
    <CreateSurface open={open} onOpenChange={onOpenChange} size="md" onSubmit={go} dirty={note.trim() !== ""} discardLabel="this request">
      <CreateSurfaceHeader
        concept="issues"
        icon={Bot}
        title="Request changes"
        description={`The agent revises and the Lead reviews again. This counts toward the ${MAX_REVIEW_ROUNDS} correction rounds.`}
        onClose={() => onOpenChange(false)}
      />
      <CreateSurfaceBody>
        <CreateSurfaceField label="What should change (required)" htmlFor="work-changes" required error={tried && !note.trim() ? "Say what should change." : undefined}>
          <Textarea id="work-changes" rows={4} maxLength={20000} value={note} onChange={(e) => setNote(e.target.value)} placeholder="What needs to change…" />
        </CreateSurfaceField>
        <CreateSurfaceRefusal message={work.error} />
      </CreateSurfaceBody>
      <CreateSurfaceFooter
        hint={issue.execution ? `Round ${round} of ${MAX_REVIEW_ROUNDS}` : undefined}
        onCancel={() => onOpenChange(false)}
        primaryLabel={work.busy ? "Sending…" : "Send back"}
        onPrimary={go}
        primaryDisabled={!note.trim()}
        busy={work.busy}
      />
    </CreateSurface>
  )
}
