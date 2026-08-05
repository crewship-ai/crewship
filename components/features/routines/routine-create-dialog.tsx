"use client"

import { useEffect, useMemo, useRef, useState, type ComponentType } from "react"
import { useRouter } from "next/navigation"
import { motion } from "motion/react"
import {
  X,
  FlaskConical,
  Save,
  AlertTriangle,
  ArrowLeft,
  Sparkles,
  GitFork,
  Wrench,
  Search,
  ChevronRight,
} from "lucide-react"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { Spinner } from "@/components/ui/spinner"
import { toast } from "sonner"
import { cn } from "@/lib/utils"
import { apiFetch } from "@/lib/api-fetch"
import { useAbilities } from "@/hooks/use-abilities"
import { CrewIcon } from "@/components/ui/crew-icon"
import { resolveRoutineIcon, resolveRoutineColor } from "@/lib/routine-identity"
import { FileEditor } from "@/components/features/files/file-editor"
import { RoutineDefinitionCanvas } from "./routine-definition-canvas"
import { parseRoutineBuffer } from "@/lib/routine-buffer"
import { routineDslExtensions } from "@/lib/routine-dsl-editor-extensions"
import { convertDsl, toYaml, type DslFormat } from "@/lib/routine-dsl-format"

// RoutineCreateDialog — describe-first authoring entry for new routines.
//
// The dialog is a small router over four modes:
//   • entry    — three cards: Describe it (★) / Fork an existing routine /
//                write it yourself (advanced editor).
//   • describe — pick crew → its Lead agent → a goal, then hand off into a
//                chat with that Lead which auto-sends an authoring prompt.
//                The backend Routine-Author skill drafts from there.
//   • fork     — list the workspace's OWN routines; pick one to load its DSL
//                into the advanced editor (not a curated template catalog).
//   • advanced — the original JSON DSL editor + Test & Save gate, kept as the
//                power-user fallback. Unchanged behaviour.
//
// The save endpoint (POST .../pipelines/save) requires a fresh passing
// test_run; the advanced mode runs /test_run inline before /save so the
// user sees explicit pass/fail. OWNER/ADMIN can toggle "skip test gate".

interface Props {
  workspaceId: string
  open: boolean
  onClose: () => void
  onCreated: (slug: string) => void
}

type Mode = "entry" | "describe" | "fork" | "advanced"

export const STARTER_TEMPLATES = [
  {
    id: "empty",
    label: "Empty",
    description: "Start from scratch — slug + one agent_run step.",
    json: {
      dsl_version: "1.0",
      name: "my-routine",
      description: "Describe what this routine does.",
      inputs: [],
      outputs: [],
      steps: [
        {
          id: "step1",
          type: "agent_run",
          agent_slug: "your-agent-slug",
          complexity: "fast",
          prompt: "Replace with the prompt your agent should run.",
        },
      ],
    },
  },
  {
    id: "summarize",
    label: "Summarize text",
    description: "One-step agent_run that takes 'text' input and returns a summary.",
    json: {
      dsl_version: "1.0",
      name: "summarize-text",
      description: "Summarize input text in 3 bullet points.",
      inputs: [{ name: "text", type: "string", required: true, description: "Text to summarize" }],
      outputs: [{ name: "summary", type: "string" }],
      steps: [
        {
          id: "summarize",
          type: "agent_run",
          agent_slug: "your-agent-slug",
          complexity: "fast",
          prompt: "Summarize the following text in 3 concise bullet points:\n\n{{ inputs.text }}",
          validation: {
            min_length: 10,
            must_not_contain: ["API_KEY=", "Bearer "],
          },
        },
      ],
    },
  },
  {
    id: "two-step",
    label: "Two-step pipeline",
    description: "Fetch → summarize chain. Demonstrates step output templating.",
    json: {
      dsl_version: "1.0",
      name: "fetch-and-summarize",
      description: "Fetch content from a URL, then summarize it.",
      inputs: [{ name: "url", type: "string", required: true }],
      outputs: [{ name: "summary", type: "string" }],
      steps: [
        {
          id: "fetch",
          type: "http",
          http: {
            method: "GET",
            url: "{{ inputs.url }}",
            max_response_bytes: 200000,
          },
        },
        {
          id: "summarize",
          type: "agent_run",
          agent_slug: "your-agent-slug",
          complexity: "fast",
          prompt: "Summarize the following content in 3 bullets:\n\n{{ steps.fetch.output }}",
          needs: ["fetch"],
        },
      ],
    },
  },
]

interface Crew {
  id: string
  name: string
}

interface AgentRec {
  id: string
  name: string
  slug: string
  agent_role: string
  crew_id: string | null
  role_title?: string | null
}

interface RoutineListItem {
  id: string
  slug: string
  name: string
  description?: string
  invocation_count: number
  ephemeral?: boolean
  // Same identity the sidebar and the detail header derive, so a
  // routine looks like itself wherever it is listed. You are choosing
  // something to copy; a column of slugs is not a thing you can
  // recognise.
  icon?: string
  color?: string
  last_invocation_status?: string
}

export function RoutineCreateDialog({ workspaceId, open, onClose, onCreated }: Props) {
  const router = useRouter()
  const { role } = useAbilities()
  // The server gates skip_test_gate on roleManage. Mirroring that here
  // is the difference between an option and a trap.
  const canSkipGate = role === "OWNER" || role === "ADMIN"
  const [mode, setMode] = useState<Mode>("entry")

  // ── Shared meta ────────────────────────────────────────────────────
  const [name, setName] = useState("")
  const [description, setDescription] = useState("")
  const [authorCrewId, setAuthorCrewId] = useState("")
  const [crews, setCrews] = useState<Crew[]>([])
  const [agents, setAgents] = useState<AgentRec[]>([])

  // ── Describe mode ──────────────────────────────────────────────────
  const [goal, setGoal] = useState("")

  // ── Fork mode ──────────────────────────────────────────────────────
  const [routines, setRoutines] = useState<RoutineListItem[]>([])
  const [routinesLoading, setRoutinesLoading] = useState(false)
  const [forkSearch, setForkSearch] = useState("")
  const [forking, setForking] = useState(false)

  // ── Advanced (JSON DSL) mode ───────────────────────────────────────
  // The buffer, in whichever format the author is writing. YAML by
  // default because that is what the routine editor, the manifest kind
  // and `crewship apply -f` all speak — creating in JSON and then
  // editing in YAML made one job two surfaces.
  //
  // `dslText` is what the editor is CONSTRUCTED from and changes only
  // when the buffer is replaced wholesale (template, format switch,
  // fork). `liveText` mirrors the live document for everything derived
  // from it. Feeding typing back into construction rebuilds CodeMirror
  // and puts the caret at position 0 — it shredded a routine in about
  // four seconds when the detail editor did it.
  const [dslFormat, setDslFormat] = useState<DslFormat>("yaml")
  const [dslText, setDslText] = useState(() => toYaml(STARTER_TEMPLATES[0].json))
  const [liveText, setLiveText] = useState(() => toYaml(STARTER_TEMPLATES[0].json))
  const bufferRef = useRef(toYaml(STARTER_TEMPLATES[0].json))
  const [editorKey, setEditorKey] = useState(0)
  const [parseError, setParseError] = useState<string | null>(null)
  const [busy, setBusy] = useState<"none" | "testing" | "saving">("none")
  const [testResult, setTestResult] = useState<{ passed: boolean; details: string } | null>(null)
  const [skipTestGate, setSkipTestGate] = useState(false)
  // saveToken captured from the most recent successful /test_run.
  // Used by the subsequent /save call so the server can verify via
  // HMAC instead of trusting body's last_test_run_at — closes the
  // test-gate body-trust loophole. Cleared on edit + on save success.
  const [saveToken, setSaveToken] = useState<string | null>(null)

  // Reset to the entry screen each time the dialog opens. (Field state is
  // otherwise preserved across a close/reopen within the same session.)
  useEffect(() => {
    if (open) setMode("entry")
  }, [open])

  // Lazy-load crews + agents on first open. Side effects live in useEffect
  // (not the render body) so React's render pipeline isn't disturbed.
  useEffect(() => {
    if (!open) return
    let cancelled = false
    if (crews.length === 0) {
      apiFetch(`/api/v1/crews?workspace_id=${workspaceId}`)
        .then((r) => (r.ok ? r.json() : []))
        .then((data: Crew[]) => {
          if (!cancelled) setCrews(Array.isArray(data) ? data : [])
        })
        .catch(() => {})
    }
    if (agents.length === 0) {
      apiFetch(`/api/v1/agents?workspace_id=${workspaceId}`)
        .then((r) => (r.ok ? r.json() : []))
        .then((data: AgentRec[]) => {
          if (!cancelled) setAgents(Array.isArray(data) ? data : [])
        })
        .catch(() => {})
    }
    return () => {
      cancelled = true
    }
  }, [open, workspaceId, crews.length, agents.length])

  // Load the workspace's own routines when entering Fork mode.
  useEffect(() => {
    if (!open || mode !== "fork" || routines.length > 0) return
    let cancelled = false
    setRoutinesLoading(true)
    apiFetch(`/api/v1/workspaces/${workspaceId}/pipelines`)
      .then((r) => (r.ok ? r.json() : []))
      .then((data: RoutineListItem[]) => {
        if (!cancelled) setRoutines(Array.isArray(data) ? data : [])
      })
      .catch(() => {})
      .finally(() => {
        if (!cancelled) setRoutinesLoading(false)
      })
    return () => {
      cancelled = true
    }
  }, [open, mode, workspaceId, routines.length])

  // The Lead agent for the chosen describe crew (LEAD role, same crew).
  // Falls back to any agent in the crew so a crew without an explicit Lead
  // can still be used as the authoring host.
  const describeLead = useMemo<AgentRec | null>(() => {
    if (!authorCrewId) return null
    const inCrew = agents.filter((a) => a.crew_id === authorCrewId)
    return inCrew.find((a) => a.agent_role === "LEAD") ?? inCrew[0] ?? null
  }, [agents, authorCrewId])

  // Parse the DSL JSON for slug-preview without touching state in render.
  // This useMemo must execute on EVERY render regardless of `open`/`mode`
  // so React's hooks contract holds — the `if (!open) return null` below
  // sits AFTER all hook declarations for that reason.
  const parsedDSL = useMemo<Record<string, unknown> | null>(() => {
    const r = parseRoutineBuffer(liveText, dslFormat)
    return r.ok ? r.parsed : null
  }, [liveText, dslFormat])

  // Schema completion + inline diagnostics — the same extensions the
  // routine editor uses, so a document written here and a document
  // edited there are held to one standard.
  const dslExtensions = useMemo(() => routineDslExtensions(dslFormat), [dslFormat])

  if (!open) return null

  const slug = (parsedDSL?.["name"] as string) || "my-routine"

  const applyTemplate = (templateId: string) => {
    const tpl = STARTER_TEMPLATES.find((t) => t.id === templateId)
    if (!tpl) return
    const j = { ...tpl.json, name: name || tpl.json.name, description: description || tpl.json.description }
    replaceBuffer(dslFormat === "yaml" ? toYaml(j) : JSON.stringify(j, null, 2))
    setParseError(null)
    setTestResult(null)
    setSaveToken(null) // template change → DSL change → bound token invalid
  }

  // Helper for handlers — re-parses with explicit error capture for
  // the inline UI feedback. Distinct from parsedDSL so the render
  // path stays side-effect-free.
  const parseDSLWithError = (): Record<string, unknown> | null => {
    const r = parseRoutineBuffer(bufferRef.current, dslFormat)
    if (!r.ok) {
      setParseError(r.message)
      return null
    }
    setParseError(null)
    return r.parsed
  }

  /** Replace the buffer wholesale — template, format switch, fork. */
  const replaceBuffer = (next: string) => {
    setDslText(next)
    setLiveText(next)
    bufferRef.current = next
    setEditorKey((k) => k + 1)
    setTestResult(null)
    setSaveToken(null)
  }

  /** Every keystroke. Mirrors, never reconstructs. */
  const handleDocChange = (next: string) => {
    bufferRef.current = next
    setLiveText(next)
    setParseError(null)
    setTestResult(null)
    setSaveToken(null)
  }

  const switchDslFormat = (next: DslFormat) => {
    if (next === dslFormat) return
    const converted = convertDsl(bufferRef.current, dslFormat, next)
    if (!converted.ok) {
      toast.error(`Fix the ${dslFormat.toUpperCase()} error before switching`)
      return
    }
    setDslFormat(next)
    replaceBuffer(converted.text)
  }

  // Returns both the pass verdict AND the freshly-minted save_token so a
  // caller chaining straight into save (handleTestAndSave) can pass the token
  // explicitly — React state (setSaveToken) is async and wouldn't be visible
  // to a save invoked in the same tick.
  const handleTestRun = async (): Promise<{ passed: boolean; token: string | null }> => {
    const parsed = parseDSLWithError()
    if (!parsed) {
      toast.error("Definition is not valid JSON")
      return { passed: false, token: null }
    }
    setBusy("testing")
    setTestResult(null)
    setSaveToken(null)
    try {
      const testBody: Record<string, unknown> = { definition: parsed, sample_inputs: {} }
      if (authorCrewId) testBody.author_crew_id = authorCrewId
      const res = await apiFetch(`/api/v1/workspaces/${workspaceId}/pipelines/test_run`, {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify(testBody),
      })
      const data = (await res.json().catch(() => ({}))) as {
        passed?: boolean
        status?: string
        error?: string
        output?: string
        save_token?: string
      }
      if (!res.ok) {
        const msg = data.error ?? `HTTP ${res.status}`
        setTestResult({ passed: false, details: msg })
        toast.error("Test run failed", { description: msg })
        return { passed: false, token: null }
      }
      // DRY_RUN_OK is the dry-run validation's pass status; COMPLETED is
      // tolerated for forward-compat; passed!=false is the legacy fallback
      // for older servers that surface no status field.
      const passed =
        data.status === "DRY_RUN_OK" ||
        data.status === "COMPLETED" ||
        (data.status === undefined && data.passed !== false)
      setTestResult({
        passed,
        details: passed ? `Passed${data.output ? ` (output: ${truncate(String(data.output), 120)})` : ""}` : data.error ?? "test_run reported failure",
      })
      const token = passed && data.save_token ? data.save_token : null
      if (token) {
        setSaveToken(token)
      }
      if (passed) {
        toast.success("Test run passed")
      } else {
        toast.error("Test run failed", { description: data.error ?? "see details below" })
      }
      return { passed, token }
    } catch (e) {
      const msg = e instanceof Error ? e.message : String(e)
      setTestResult({ passed: false, details: msg })
      toast.error("Test run errored", { description: msg })
      return { passed: false, token: null }
    } finally {
      setBusy("none")
    }
  }

  const handleSave = async (tokenOverride?: string | null) => {
    const parsed = parseDSLWithError()
    if (!parsed) {
      toast.error("Definition is not valid JSON")
      return
    }
    if (!parsed["name"]) {
      toast.error("DSL must include a 'name' (used as slug)")
      return
    }
    setBusy("saving")
    try {
      const body: Record<string, unknown> = {
        slug: parsed["name"],
        name: name || (parsed["name"] as string),
        description: description || (parsed["description"] as string | undefined) || "",
        definition: parsed,
        skip_test_gate: skipTestGate,
      }
      // The server clears the save test-gate ONLY via the HMAC save_token
      // (minted by /test_run) or the OWNER/ADMIN skip — it no longer trusts a
      // body "it passed" claim. Prefer an explicitly-threaded token (the
      // test-then-save chain) over the async state copy.
      const effectiveToken = tokenOverride ?? saveToken
      if (effectiveToken) {
        body.save_token = effectiveToken
      }
      // skip_test_gate is already on the body; nothing else to add.
      if (authorCrewId) body.author_crew_id = authorCrewId

      const res = await apiFetch(`/api/v1/workspaces/${workspaceId}/pipelines/save`, {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify(body),
      })
      if (!res.ok) {
        const t = await res.text().catch(() => "")
        throw new Error(`${res.status}: ${t || res.statusText}`)
      }
      const saved = (await res.json()) as { slug: string }
      toast.success(`Routine "${saved.slug}" saved`)
      onCreated(saved.slug)
      onClose()
    } catch (e) {
      toast.error("Save failed", { description: e instanceof Error ? e.message : String(e) })
    } finally {
      setBusy("none")
    }
  }

  const handleTestAndSave = async () => {
    const { passed, token } = await handleTestRun()
    if (passed) {
      await handleSave(token)
    }
  }

  // Describe handoff: navigate into the Lead's chat with the authoring
  // prompt pre-sent. The chat page reads ?prompt= , opens a fresh session
  // and auto-sends once connected; the Routine-Author skill takes over.
  const handleDescribe = () => {
    const text = goal.trim()
    if (!describeLead || !text) return
    const prompt = `Author a routine for me: ${text}`
    router.push(
      `/chat/${encodeURIComponent(describeLead.slug)}?prompt=${encodeURIComponent(prompt)}`,
    )
    onClose()
  }

  // Fork: load an existing routine's DSL into the advanced editor.
  const handleForkPick = async (item: RoutineListItem) => {
    setForking(true)
    try {
      const res = await apiFetch(`/api/v1/workspaces/${workspaceId}/pipelines/${item.slug}`)
      if (!res.ok) throw new Error(`HTTP ${res.status}`)
      const detail = (await res.json()) as { definition?: Record<string, unknown>; name?: string; description?: string }
      const def = detail.definition ?? {}
      // Rename the fork so it doesn't collide with the source slug on save.
      const forkName = `${item.slug}-copy`
      const nextDef = { ...def, name: forkName }
      replaceBuffer(dslFormat === "yaml" ? toYaml(nextDef) : JSON.stringify(nextDef, null, 2))
      setName("")
      setDescription(item.description ?? detail.description ?? "")
      setParseError(null)
      setTestResult(null)
      setSaveToken(null)
      setMode("advanced")
    } catch (e) {
      toast.error("Could not load routine", { description: e instanceof Error ? e.message : String(e) })
    } finally {
      setForking(false)
    }
  }

  const filteredRoutines = routines.filter((r) => {
    if (r.ephemeral) return false
    if (!forkSearch.trim()) return true
    const q = forkSearch.toLowerCase()
    return `${r.slug} ${r.name} ${r.description ?? ""}`.toLowerCase().includes(q)
  })

  const headerTitle =
    mode === "describe"
      ? "Describe your routine"
      : mode === "fork"
        ? "Start from an existing routine"
        : mode === "advanced"
          ? "Write it yourself"
          : "New routine"
  const headerSub =
    mode === "describe"
      ? "a Lead drafts it with you in chat"
      : mode === "fork"
        ? "fork one of your own routines"
        : mode === "advanced"
          ? "Editor — test-run, then save"
          : undefined

  return (
    <motion.div
      initial={{ opacity: 0 }}
      animate={{ opacity: 1 }}
      transition={{ duration: 0.15 }}
      className="fixed inset-0 z-50 flex items-center justify-center bg-black/50 p-4"
      onClick={onClose}
    >
      {/* Dim, do not blur. The shared DialogOverlay every other modal
          in the product uses is a plain bg-black/50 — a blur here made
          this one dialog look like a different application. */}
      <motion.div
        initial={{ opacity: 0, scale: 0.96, y: 8 }}
        animate={{ opacity: 1, scale: 1, y: 0 }}
        transition={{ duration: 0.18, ease: [0.22, 1, 0.36, 1] }}
        className={cn(
          "flex w-full flex-col rounded-lg border border-white/10 bg-card shadow-2xl",
          mode === "advanced"
            ? "h-[90vh] max-w-3xl"
            : mode === "fork"
              ? "max-h-[85vh] max-w-2xl"
              : "max-w-xl",
        )}
        onClick={(e) => e.stopPropagation()}
      >
        {/* Header */}
        <div className="flex items-center gap-2 border-b border-white/[0.06] px-4 py-3 shrink-0">
          {mode !== "entry" && (
            <Button
              size="sm"
              variant="ghost"
              className="h-7 w-7 p-0"
              onClick={() => setMode("entry")}
              aria-label="Back"
            >
              <ArrowLeft className="h-3.5 w-3.5" />
            </Button>
          )}
          <div className="min-w-0">
            <h3 className="text-sm font-medium leading-tight">{headerTitle}</h3>
            {headerSub && <p className="text-[11px] text-muted-foreground">{headerSub}</p>}
          </div>
          <Button size="sm" variant="ghost" className="ml-auto h-7 w-7 p-0" onClick={onClose} aria-label="Close">
            <X className="h-3 w-3" />
          </Button>
        </div>

        {/* ── ENTRY — three cards ─────────────────────────────────────── */}
        {mode === "entry" && (
          <div className="p-4 space-y-2.5">
            <EntryCard
              icon={Sparkles}
              tone="primary"
              star
              title="Describe it"
              description="Tell a Lead agent your goal in plain words. It drafts the routine with you in chat, asks a couple of questions, and shows a readable preview before anything is saved."
              onClick={() => setMode("describe")}
            />
            <EntryCard
              icon={GitFork}
              title="Fork an existing routine"
              description="Start from one of your workspace's own routines and tweak it. No curated catalog — the library grows from what you and your agents actually build."
              onClick={() => setMode("fork")}
            />
            <EntryCard
              icon={Wrench}
              title="Write it yourself"
              description="Author the DSL in the editor — YAML or JSON, with schema completion and the step graph beside it. Test-run and save without leaving the dialog."
              onClick={() => setMode("advanced")}
            />
          </div>
        )}

        {/* ── DESCRIBE ────────────────────────────────────────────────── */}
        {mode === "describe" && (
          <div className="p-4 space-y-4">
            <div className="flex items-end gap-3">
              <div className="flex-1">
                <label htmlFor="describe-crew" className="mb-1 block text-[11px] uppercase tracking-wider text-muted-foreground">
                  Owner (crew)
                </label>
                <select
                  id="describe-crew"
                  value={authorCrewId}
                  onChange={(e) => setAuthorCrewId(e.target.value)}
                  className="h-8 w-full rounded-md border border-white/10 bg-background px-2 text-xs"
                >
                  <option value="">Choose a crew…</option>
                  {crews.map((c) => (
                    <option key={c.id} value={c.id}>
                      {c.name}
                    </option>
                  ))}
                </select>
              </div>
              <div className="pb-1.5 text-[11px] text-muted-foreground">
                {authorCrewId ? (
                  describeLead ? (
                    <span className="inline-flex items-center gap-1.5">
                      <span className="inline-block h-4 w-4 rounded-full bg-gradient-to-br from-purple to-notice" />
                      Lead: <b className="text-foreground">{describeLead.name}</b>
                    </span>
                  ) : (
                    <span className="text-warn">No Lead in this crew</span>
                  )
                ) : (
                  <span className="text-muted-foreground-soft">pick a crew →</span>
                )}
              </div>
            </div>

            <div>
              <label htmlFor="describe-goal" className="mb-1.5 block text-sm font-medium">
                What should the routine do?
              </label>
              <textarea
                id="describe-goal"
                value={goal}
                onChange={(e) => setGoal(e.target.value)}
                rows={4}
                placeholder="Describe it in your own words. e.g. Every weekday morning, fetch the top 5 Hacker News stories, summarize each in one sentence, and post the digest to Slack #standup."
                className="w-full resize-y rounded-md border border-white/10 bg-background p-2.5 text-[13px] leading-relaxed"
              />
            </div>

            <p className="text-[11px] leading-relaxed text-muted-foreground">
              {describeLead?.name ?? "The Lead"} will draft it and ask a couple of questions, then show a
              readable preview — nothing is saved without you. It grounds the draft in your crew's connected
              integrations, your existing routines, and the routine schema.
            </p>

            <div className="flex items-center justify-between gap-2 pt-1">
              <div className="flex gap-3 text-[11px] text-muted-foreground">
                <button type="button" className="hover:text-foreground" onClick={() => setMode("fork")}>
                  fork a routine
                </button>
                <button type="button" className="hover:text-foreground" onClick={() => setMode("advanced")}>
                  JSON editor
                </button>
              </div>
              <Button size="sm" className="gap-1.5" onClick={handleDescribe} disabled={!describeLead || !goal.trim()}>
                <Sparkles className="h-3.5 w-3.5" />
                Draft with {describeLead?.name ?? "a Lead"}
              </Button>
            </div>
          </div>
        )}

        {/* ── FORK ────────────────────────────────────────────────────── */}
        {mode === "fork" && (
          <div className="flex min-h-0 flex-1 flex-col p-4">
            <div className="relative mb-3 shrink-0">
              <Search className="pointer-events-none absolute left-2.5 top-1/2 h-3.5 w-3.5 -translate-y-1/2 text-muted-foreground" />
              <Input
                value={forkSearch}
                onChange={(e) => setForkSearch(e.target.value)}
                placeholder="Search your routines…"
                className="h-8 pl-8 text-xs"
              />
            </div>
            <div className="min-h-0 flex-1 overflow-y-auto">
              {routinesLoading ? (
                <div className="flex items-center justify-center py-10 text-xs text-muted-foreground">
                  <Spinner className="mr-2 h-3.5 w-3.5" /> Loading routines…
                </div>
              ) : filteredRoutines.length === 0 ? (
                <div className="rounded-md border border-dashed border-white/10 px-3 py-6 text-center text-xs text-muted-foreground">
                  {routines.length === 0
                    ? "No routines yet. Describe one, or write the first yourself."
                    : "No routines match your search."}
                </div>
              ) : (
                <div className="space-y-1">
                  {filteredRoutines.map((r) => (
                    <button
                      key={r.id}
                      type="button"
                      disabled={forking}
                      onClick={() => handleForkPick(r)}
                      className="group flex w-full items-center gap-3 rounded-md border border-white/[0.06] bg-card/40 px-3 py-2 text-left transition-colors hover:border-white/15 hover:bg-card disabled:opacity-50"
                    >
                      <span className="relative shrink-0">
                        <CrewIcon
                          icon={resolveRoutineIcon(r)}
                          color={resolveRoutineColor(r)}
                          size="sm"
                          className="!h-6 !w-6 !rounded-md"
                        />
                        <span
                          aria-hidden
                          title={r.last_invocation_status ?? "never invoked"}
                          className={cn(
                            "absolute -bottom-0.5 -right-0.5 h-2 w-2 rounded-full ring-2 ring-card",
                            forkStatusDot(r),
                          )}
                        />
                      </span>
                      <div className="min-w-0 flex-1">
                        <div className="truncate text-xs font-medium">{r.name || r.slug}</div>
                        <div className="truncate font-mono text-[10px] text-muted-foreground-soft">
                          {r.slug}
                        </div>
                        {r.description && (
                          <p className="mt-0.5 line-clamp-1 text-[11px] text-muted-foreground">{r.description}</p>
                        )}
                      </div>
                      <span className="shrink-0 text-[10px] text-muted-foreground-soft">
                        {r.invocation_count > 0 ? `ran ${r.invocation_count}×` : "never run"}
                      </span>
                      <ChevronRight className="h-3.5 w-3.5 shrink-0 text-muted-foreground opacity-0 transition-opacity group-hover:opacity-100" />
                    </button>
                  ))}
                </div>
              )}
            </div>
            <p className="mt-3 shrink-0 rounded-md border border-dashed border-white/10 px-3 py-2 text-[11px] leading-relaxed text-muted-foreground">
              Forking copies a routine&apos;s definition into the editor so you can adapt it — the original is
              untouched. Save creates a new routine.
            </p>
          </div>
        )}

        {/* ── ADVANCED (JSON DSL) ─────────────────────────────────────── */}
        {mode === "advanced" && (
          <>
            <div className="flex flex-1 overflow-hidden">
              <aside className="w-56 shrink-0 border-r border-white/[0.06] p-3 overflow-y-auto">
                <div className="mb-3">
                  <label className="text-[10px] uppercase tracking-wider text-muted-foreground">Name</label>
                  <Input
                    value={name}
                    onChange={(e) => setName(e.target.value)}
                    placeholder="Friendly name"
                    className="h-7 text-xs"
                  />
                  <p className="mt-1 text-[10px] text-muted-foreground">
                    Slug is derived from the DSL <code className="font-mono">name</code> field.
                  </p>
                </div>
                <div className="mb-3">
                  <label className="text-[10px] uppercase tracking-wider text-muted-foreground">
                    Description
                  </label>
                  <textarea
                    value={description}
                    onChange={(e) => setDescription(e.target.value)}
                    rows={3}
                    placeholder="One-line summary"
                    className="w-full resize-none rounded-md border border-white/10 bg-background p-1.5 text-xs"
                  />
                </div>
                <div className="mb-3">
                  <label htmlFor="routine-author-crew" className="text-[10px] uppercase tracking-wider text-muted-foreground">
                    Author crew
                  </label>
                  <select
                    id="routine-author-crew"
                    value={authorCrewId}
                    onChange={(e) => setAuthorCrewId(e.target.value)}
                    className="h-7 w-full rounded-md border border-white/10 bg-background px-1.5 text-xs"
                  >
                    <option value="">— choose at runtime —</option>
                    {crews.map((c) => (
                      <option key={c.id} value={c.id}>
                        {c.name}
                      </option>
                    ))}
                  </select>
                  <p className="mt-1 text-[10px] text-muted-foreground">
                    Crew whose agents + credentials run this routine.
                  </p>
                </div>

                <div className="my-3 border-t border-white/[0.06]" />

                <div>
                  <label className="text-[10px] uppercase tracking-wider text-muted-foreground">
                    Starter templates
                  </label>
                  <div className="mt-1 space-y-1">
                    {STARTER_TEMPLATES.map((t) => (
                      <button
                        key={t.id}
                        onClick={() => applyTemplate(t.id)}
                        className="w-full rounded-md border border-white/[0.06] bg-card/40 px-2 py-1.5 text-left text-xs hover:border-white/15 hover:bg-card transition-colors"
                      >
                        <div className="font-medium">{t.label}</div>
                        <p className="mt-0.5 text-[10px] text-muted-foreground line-clamp-2">{t.description}</p>
                      </button>
                    ))}
                  </div>
                </div>
              </aside>

              <div className="flex flex-1 flex-col overflow-hidden">
                {/* The bar the routine editor has, because this is the
                    same job: format toggle, the slug the DSL will save
                    under, and the parse error WITH its line — the old
                    strip said "invalid JSON" and left you to find it. */}
                <div className="flex items-center justify-between gap-2 border-b border-white/[0.06] px-3 py-1.5 shrink-0">
                  <div className="flex items-center gap-2 text-[11px] text-muted-foreground">
                    <div className="flex items-center gap-0.5 rounded-md border border-border/60 p-0.5">
                      {(["yaml", "json"] as const).map((f) => (
                        <button
                          key={f}
                          type="button"
                          onClick={() => switchDslFormat(f)}
                          aria-pressed={dslFormat === f}
                          className={cn(
                            "rounded px-1.5 py-0.5 font-mono text-[10px] uppercase transition-colors",
                            dslFormat === f
                              ? "bg-primary/15 text-primary"
                              : "text-muted-foreground hover:text-foreground",
                          )}
                        >
                          {f}
                        </button>
                      ))}
                    </div>
                    <span className="font-mono">slug: {slug}</span>
                  </div>
                  {parseError ? (
                    <span className="truncate text-[10px] text-destructive" title={parseError}>
                      {parseError}
                    </span>
                  ) : (
                    <span className="text-[10px] text-success">syntax ok</span>
                  )}
                </div>
                <div className="flex min-h-0 flex-1 flex-col md:flex-row">
                  {/* The graph, beside the code. You could not see what
                      you were building until after it was saved. */}
                  <div className="relative min-h-[160px] w-full min-w-0 flex-1 border-b border-white/[0.06] md:min-w-[240px] md:border-b-0 md:border-r">
                    {parsedDSL ? (
                      <RoutineDefinitionCanvas
                        definition={parsedDSL}
                        slug={slug}
                        name={name || slug}
                      />
                    ) : (
                      <div className="flex h-full items-center justify-center px-4 text-center text-[11px] text-muted-foreground-soft">
                        The graph appears once the definition parses.
                      </div>
                    )}
                  </div>
                  <div className="min-h-[200px] w-full min-w-0 flex-1 overflow-hidden md:w-[52%] md:flex-none">
                    <FileEditor
                      key={editorKey}
                      code={dslText}
                      language={dslFormat}
                      onDocChange={handleDocChange}
                      extraExtensions={dslExtensions}
                      onSave={(next) => {
                        bufferRef.current = next
                        setLiveText(next)
                      }}
                    />
                  </div>
                </div>
                {testResult && (
                  <div
                    className={cn(
                      "border-t px-3 py-2 text-xs",
                      testResult.passed ? "border-success/30 bg-success/5 text-success" : "border-destructive/30 bg-destructive/5 text-destructive",
                    )}
                  >
                    <div className="flex items-center gap-1.5 font-medium">
                      {testResult.passed ? "Test passed" : "Test failed"}
                    </div>
                    <p className="mt-0.5 font-mono text-[10px] opacity-80">{testResult.details}</p>
                  </div>
                )}
              </div>
            </div>

            {/* Footer */}
            <div className="flex items-center justify-between gap-2 border-t border-white/[0.06] px-3 py-2 shrink-0">
              {/* Shown only to the roles the server will accept it
                  from. It was always visible, so a MANAGER could tick
                  an escape hatch and get a 403 for their trouble — an
                  affordance that cannot work is worse than an absent
                  one, because it reads as a bug in the product rather
                  than a limit on the person. */}
              {canSkipGate ? (
                <label className="flex cursor-pointer items-center gap-1.5 text-[11px] text-muted-foreground">
                  <input
                    type="checkbox"
                    checked={skipTestGate}
                    onChange={(e) => setSkipTestGate(e.target.checked)}
                    className="h-3 w-3 cursor-pointer"
                  />
                  Skip test-run gate
                  <AlertTriangle className="h-2.5 w-2.5" />
                </label>
              ) : (
                <span />
              )}
              <div className="flex gap-2">
                <Button size="sm" variant="ghost" onClick={onClose} disabled={busy !== "none"}>
                  Cancel
                </Button>
                <Button
                  size="sm"
                  variant="outline"
                  onClick={handleTestRun}
                  disabled={busy !== "none"}
                  className="gap-1.5"
                >
                  <FlaskConical className="h-3 w-3" />
                  {busy === "testing" ? "Testing…" : "Test only"}
                </Button>
                {skipTestGate ? (
                  <Button
                    size="sm"
                    onClick={() => handleSave()}
                    disabled={busy !== "none"}
                    className="gap-1.5"
                  >
                    <Save className="h-3 w-3" />
                    {busy === "saving" ? "Saving…" : "Save (skip test)"}
                  </Button>
                ) : (
                  <Button size="sm" onClick={handleTestAndSave} disabled={busy !== "none"} className="gap-1.5">
                    <Save className="h-3 w-3" />
                    {busy === "testing" ? "Testing…" : busy === "saving" ? "Saving…" : "Test & Save"}
                  </Button>
                )}
              </div>
            </div>
          </>
        )}
      </motion.div>
    </motion.div>
  )
}

function EntryCard({
  icon: Icon,
  title,
  description,
  onClick,
  tone = "default",
  star = false,
}: {
  icon: ComponentType<{ className?: string }>
  title: string
  description: string
  onClick: () => void
  tone?: "default" | "primary"
  star?: boolean
}) {
  return (
    <button
      type="button"
      onClick={onClick}
      className={cn(
        "flex w-full items-start gap-3 rounded-lg border p-3 text-left transition-colors",
        tone === "primary"
          ? "border-primary/40 bg-primary/[0.06] hover:border-primary/60 hover:bg-primary/10"
          : "border-white/[0.08] bg-card/40 hover:border-white/20 hover:bg-card",
      )}
    >
      <span
        className={cn(
          "flex h-8 w-8 shrink-0 items-center justify-center rounded-lg",
          tone === "primary" ? "bg-primary/20 text-primary" : "bg-white/[0.06] text-muted-foreground",
        )}
      >
        <Icon className="h-4 w-4" />
      </span>
      <div className="min-w-0 flex-1">
        <div className="flex items-center gap-1.5 text-sm font-medium">
          {title}
          {star && <Sparkles className="h-3 w-3 text-primary" aria-label="recommended" />}
        </div>
        <p className="mt-0.5 text-[12px] leading-relaxed text-muted-foreground">{description}</p>
      </div>
      <ChevronRight className="mt-0.5 h-4 w-4 shrink-0 text-muted-foreground" />
    </button>
  )
}

/** Status dot for a fork candidate — same vocabulary as the sidebar. */
function forkStatusDot(r: RoutineListItem): string {
  const s = r.last_invocation_status?.toLowerCase()
  if (s === "completed" || s === "succeeded") return "bg-success"
  if (s === "failed" || s === "error") return "bg-destructive"
  if (r.invocation_count === 0) return "bg-muted-foreground/30"
  return "bg-primary"
}

function truncate(s: string, n: number): string {
  if (s.length <= n) return s
  return s.slice(0, n - 1) + "…"
}
