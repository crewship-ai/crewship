"use client"

import { useEffect, useMemo, useRef, useState } from "react"
import { useRouter } from "next/navigation"
import {
  FlaskConical,
  Save,
  AlertTriangle,
  Sparkles,
  GitFork,
  Wrench,
  Search,
  Check,
  ChevronsUpDown,
  ChevronRight,
  Users,
} from "lucide-react"
import { Input } from "@/components/ui/input"
import { toast } from "sonner"
import { cn } from "@/lib/utils"
import {
  CREATE_SURFACE_INPUT,
  CreateSurface,
  CreateSurfaceBody,
  CreateSurfaceField,
  CreateSurfaceFooter,
  CreateSurfaceHeader,
  CreateSurfaceLoading,
  CreateSurfaceRefusal,
  CreateSurfaceSecondaryAction,
  CreateSurfaceTile,
} from "@/components/layout/create-surface"
import { Popover, PopoverContent, PopoverTrigger } from "@/components/ui/popover"
import {
  Command,
  CommandEmpty,
  CommandGroup,
  CommandInput,
  CommandItem,
  CommandList,
} from "@/components/ui/command"
import { apiFetch } from "@/lib/api-fetch"
import { useAbilities } from "@/hooks/use-abilities"
import { AgentAvatar } from "@/components/ui/agent-avatar"
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
//
// The shell is CreateSurface (components/layout/create-surface.tsx), at a
// single `lg` width for all four modes — it used to be 576px at the door,
// 672px in the fork list and 768px × 90vh in the editor, so the footer moved
// out from under the cursor every time you picked a mode. The four modes are
// screens you go BACK from; the header's arrow is the shell's.

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
  // The identity the crews API already returns and the roster already draws.
  // A crew's colour is a hex on most rows and a palette id on the ones the
  // wizard wrote — CrewIcon knows about both, which is why neither is
  // normalised here.
  icon?: string | null
  color?: string | null
  // The default avatar style for agents in the crew that have none of their
  // own. Same fallback chain the roster uses, so a Lead does not get one face
  // here and another two clicks away.
  avatar_style?: string | null
}

interface AgentRec {
  id: string
  name: string
  slug: string
  agent_role: string
  crew_id: string | null
  role_title?: string | null
  avatar_seed?: string | null
  avatar_style?: string | null
  avatar_url?: string | null
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
  // What the server said when it refused the save. It was a toast only, which
  // is the one moment of this surface's life you cannot afford to miss and the
  // only one that used to fade on its own. The toast stays; this is the band
  // between the body and the footer that does not scroll away.
  const [saveError, setSaveError] = useState<string | null>(null)

  // The buffer as it was handed to the editor. Anything else means there is
  // input to lose, which is what the shell's discard guard asks about.
  const pristineText = useRef(liveText)

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

  // The chosen crew's own row — its icon and colour draw the picker's trigger,
  // and its avatar_style is the fallback for a Lead that has none.
  const describeCrew = useMemo<Crew | null>(
    () => crews.find((c) => c.id === authorCrewId) ?? null,
    [crews, authorCrewId],
  )

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
    setSaveError(null)
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
      const msg = e instanceof Error ? e.message : String(e)
      setSaveError(msg)
      toast.error("Save failed", { description: msg })
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

  // The shell's discard guard — Esc and an overlay click ask before throwing
  // input away, and the header's × asks too. The starter template the editor
  // opens with is not input, so an untouched buffer is not dirty.
  const dirty =
    mode === "describe"
      ? goal.trim() !== ""
      : mode === "advanced"
        ? name.trim() !== "" || description.trim() !== "" || liveText !== pristineText.current
        : false

  // ⌘↵ / Ctrl↵, wired once by the shell. It does whatever the mode's primary
  // does, and nothing on the two modes whose actions are their list rows.
  const handleKeyboardSubmit = () => {
    if (busy !== "none") return
    if (mode === "describe") {
      handleDescribe()
    } else if (mode === "advanced") {
      if (skipTestGate) void handleSave()
      else void handleTestAndSave()
    }
  }

  const advancedPrimaryLabel = skipTestGate
    ? busy === "saving"
      ? "Saving…"
      : "Save (skip test)"
    : busy === "testing"
      ? "Testing…"
      : busy === "saving"
        ? "Saving…"
        : "Test & Save"

  return (
    <CreateSurface
      open={open}
      onOpenChange={(next) => {
        if (!next) onClose()
      }}
      size="lg"
      dirty={dirty}
      discardLabel="this routine"
      onSubmit={handleKeyboardSubmit}
      // Width is fixed at lg for every mode; the EDITOR is the one mode that
      // also needs a definite height, because a code pane and a step graph
      // sized by their content give you a 400px dialog with a 200px editor in
      // it. The shell fixes widths and leaves height to the content, so this
      // is local — and it is the same cap the shell already applies.
      className={mode === "advanced" ? "sm:h-[min(85vh,720px)]" : undefined}
    >
      <CreateSurfaceHeader
        concept="routines"
        title={headerTitle}
        description={headerSub}
        onBack={mode === "entry" ? undefined : () => setMode("entry")}
        onClose={onClose}
      />

      {/* ── ENTRY — three cards ───────────────────────────────────────── */}
      {mode === "entry" && (
        <CreateSurfaceBody className="flex flex-col gap-2.5">
          <CreateSurfaceTile
            icon={Sparkles}
            accent="blue"
            title="Describe it"
            description="Tell a Lead agent your goal in plain words. It drafts the routine with you in chat, asks a couple of questions, and shows a readable preview before anything is saved."
            meta={<Sparkles className="h-3 w-3 text-primary" aria-label="recommended" />}
            className="border-primary/40 bg-primary/[0.06] hover:border-primary/60 hover:bg-primary/10"
            onClick={() => setMode("describe")}
          />
          <CreateSurfaceTile
            icon={GitFork}
            accent="slate"
            title="Fork an existing routine"
            description="Start from one of your workspace's own routines and tweak it. No curated catalog — the library grows from what you and your agents actually build."
            onClick={() => setMode("fork")}
          />
          <CreateSurfaceTile
            icon={Wrench}
            accent="slate"
            title="Write it yourself"
            description="Author the DSL in the editor — YAML or JSON, with schema completion and the step graph beside it. Test-run and save without leaving the dialog."
            onClick={() => setMode("advanced")}
          />
        </CreateSurfaceBody>
      )}

      {/* ── DESCRIBE ──────────────────────────────────────────────────── */}
      {mode === "describe" && (
        <>
          <CreateSurfaceBody className="flex flex-col gap-4">
            <div className="flex items-end gap-3">
              <CreateSurfaceField label="Owner (crew)" htmlFor="describe-crew" className="flex-1">
                <CrewPicker
                  id="describe-crew"
                  ariaLabel="Select crew"
                  crews={crews}
                  value={authorCrewId}
                  onChange={setAuthorCrewId}
                  placeholder="Choose a crew…"
                />
              </CreateSurfaceField>
              <div className="pb-1.5 text-[11px] text-muted-foreground">
                {authorCrewId ? (
                  describeLead ? (
                    <span className="inline-flex items-center gap-1.5">
                      {/* The Lead's own face, from the same (seed, style)
                          fallback chain the roster uses — this was a purple
                          gradient disc that stood for nobody, next to the name
                          of somebody. */}
                      <AgentAvatar
                        seed={describeLead.avatar_seed || describeLead.name}
                        style={describeLead.avatar_style || describeCrew?.avatar_style}
                        agentId={describeLead.id}
                        avatarUrl={describeLead.avatar_url}
                        alt=""
                        className="h-4 w-4 shrink-0"
                      />
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

            <CreateSurfaceField label="What should the routine do?" htmlFor="describe-goal">
              <textarea
                id="describe-goal"
                value={goal}
                onChange={(e) => setGoal(e.target.value)}
                rows={4}
                placeholder="Describe it in your own words. e.g. Every weekday morning, fetch the top 5 Hacker News stories, summarize each in one sentence, and post the digest to Slack #standup."
                className="w-full resize-y rounded-md border border-hairline bg-background p-2.5 text-[13px] leading-relaxed text-foreground outline-none focus:border-primary"
              />
            </CreateSurfaceField>

            <p className="text-[11px] leading-relaxed text-muted-foreground">
              {describeLead?.name ?? "The Lead"} will draft it and ask a couple of questions, then show a
              readable preview — nothing is saved without you. It grounds the draft in your crew's connected
              integrations, your existing routines, and the routine schema.
            </p>

            <div className="flex gap-3 text-[11px] text-muted-foreground">
              <button type="button" className="hover:text-foreground" onClick={() => setMode("fork")}>
                fork a routine
              </button>
              <button type="button" className="hover:text-foreground" onClick={() => setMode("advanced")}>
                JSON editor
              </button>
            </div>
          </CreateSurfaceBody>

          <CreateSurfaceFooter
            onCancel={onClose}
            primaryLabel={`Draft with ${describeLead?.name ?? "a Lead"}`}
            primaryIcon={Sparkles}
            primaryDisabled={!describeLead || !goal.trim()}
            onPrimary={handleDescribe}
          />
        </>
      )}

      {/* ── FORK ──────────────────────────────────────────────────────── */}
      {mode === "fork" && (
        <CreateSurfaceBody className="flex flex-col gap-3">
          <div className="relative">
            <Search className="pointer-events-none absolute left-2.5 top-1/2 h-3.5 w-3.5 -translate-y-1/2 text-muted-foreground" />
            <Input
              value={forkSearch}
              onChange={(e) => setForkSearch(e.target.value)}
              placeholder="Search your routines…"
              className="h-8 pl-8 text-xs max-sm:h-12 max-sm:text-sm"
            />
          </div>
          {routinesLoading ? (
            <CreateSurfaceLoading rows={3} />
          ) : filteredRoutines.length === 0 ? (
            <div className="rounded-md border border-dashed border-border/60 px-3 py-6 text-center text-xs text-muted-foreground">
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
                  className="group flex w-full items-center gap-3 rounded-md border border-hairline bg-foreground/[0.02] px-3 py-2 text-left transition-colors hover:border-border hover:bg-foreground/[0.05] disabled:opacity-50"
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
          <p className="rounded-md border border-dashed border-border/60 px-3 py-2 text-[11px] leading-relaxed text-muted-foreground">
            Forking copies a routine&apos;s definition into the editor so you can adapt it — the original is
            untouched. Save creates a new routine.
          </p>
        </CreateSurfaceBody>
      )}

      {/* ── ADVANCED (the DSL editor) ─────────────────────────────────── */}
      {mode === "advanced" && (
        <>
          {/* The body is the shell's scrollport everywhere else; here it is a
              two-pane frame, because a code editor that scrolls the dialog
              instead of itself is not a code editor. */}
          <CreateSurfaceBody className="flex overflow-y-hidden p-0 sm:p-0">
            <aside className="w-56 shrink-0 overflow-y-auto border-r border-hairline p-3">
              <div className="mb-3">
                <label htmlFor="routine-name" className="text-[10px] uppercase tracking-wider text-muted-foreground">
                  Name
                </label>
                <Input
                  id="routine-name"
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
                <label htmlFor="routine-description" className="text-[10px] uppercase tracking-wider text-muted-foreground">
                  Description
                </label>
                <textarea
                  id="routine-description"
                  value={description}
                  onChange={(e) => setDescription(e.target.value)}
                  rows={3}
                  placeholder="One-line summary"
                  className="w-full resize-none rounded-md border border-hairline bg-background p-1.5 text-xs text-foreground outline-none focus:border-primary"
                />
              </div>
              <div className="mb-3">
                <label htmlFor="routine-author-crew" className="text-[10px] uppercase tracking-wider text-muted-foreground">
                  Author crew
                </label>
                <CrewPicker
                  id="routine-author-crew"
                  ariaLabel="Select author crew"
                  crews={crews}
                  value={authorCrewId}
                  onChange={setAuthorCrewId}
                  placeholder="— choose at runtime —"
                  clearLabel="— choose at runtime —"
                  className="mt-1"
                />
                <p className="mt-1 text-[10px] text-muted-foreground">
                  Crew whose agents + credentials run this routine.
                </p>
              </div>

              <div className="my-3 border-t border-hairline" />

              <div>
                <label className="text-[10px] uppercase tracking-wider text-muted-foreground">
                  Starter templates
                </label>
                <div className="mt-1 space-y-1">
                  {STARTER_TEMPLATES.map((t) => (
                    <button
                      key={t.id}
                      type="button"
                      onClick={() => applyTemplate(t.id)}
                      className="w-full rounded-md border border-hairline bg-foreground/[0.02] px-2 py-1.5 text-left text-xs transition-colors hover:border-border hover:bg-foreground/[0.05]"
                    >
                      <div className="font-medium">{t.label}</div>
                      <p className="mt-0.5 text-[10px] text-muted-foreground line-clamp-2">{t.description}</p>
                    </button>
                  ))}
                </div>
              </div>
            </aside>

            <div className="flex min-w-0 flex-1 flex-col overflow-hidden">
              {/* The bar the routine editor has, because this is the
                  same job: format toggle, the slug the DSL will save
                  under, and the parse error WITH its line — the old
                  strip said "invalid JSON" and left you to find it. */}
              <div className="flex shrink-0 items-center justify-between gap-2 border-b border-hairline px-3 py-1.5">
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
                <div className="relative min-h-[160px] w-full min-w-0 flex-1 border-b border-hairline md:min-w-[240px] md:border-b-0 md:border-r">
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
            </div>
          </CreateSurfaceBody>

          {/* Both verdicts sit outside the scrollport, next to the button that
              produced them. */}
          {testResult && (
            <div
              className={cn(
                "shrink-0 border-t px-4 py-2 text-xs sm:px-5",
                testResult.passed
                  ? "border-success/30 bg-success/5 text-success"
                  : "border-destructive/30 bg-destructive/5 text-destructive",
              )}
            >
              <div className="flex items-center gap-1.5 font-medium">
                {testResult.passed ? "Test passed" : "Test failed"}
              </div>
              <p className="mt-0.5 font-mono text-[10px] opacity-80">{testResult.details}</p>
            </div>
          )}

          <CreateSurfaceRefusal
            message={saveError == null ? null : `Save failed — ${saveError}`}
            onDismiss={() => setSaveError(null)}
          />

          <CreateSurfaceFooter
            /* Shown only to the roles the server will accept it from. It was
               always visible, so a MANAGER could tick an escape hatch and get
               a 403 for their trouble — an affordance that cannot work is
               worse than an absent one, because it reads as a bug in the
               product rather than a limit on the person. */
            aside={
              canSkipGate ? (
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
              ) : undefined
            }
            onCancel={onClose}
            secondary={
              <CreateSurfaceSecondaryAction
                icon={FlaskConical}
                onClick={handleTestRun}
                disabled={busy !== "none"}
              >
                {busy === "testing" ? "Testing…" : "Test only"}
              </CreateSurfaceSecondaryAction>
            }
            primaryLabel={advancedPrimaryLabel}
            primaryIcon={Save}
            onPrimary={skipTestGate ? () => handleSave() : handleTestAndSave}
            busy={busy !== "none"}
          />
        </>
      )}
    </CreateSurface>
  )
}

/**
 * The crew picker — a crew, drawn the way the rest of the product draws it.
 *
 * This was a native <select> in both places it appears, so the one screen that
 * asks "whose crew is this?" answered with a column of names while the roster
 * two clicks away shows Engineering as a blue terminal and Ops as a red
 * server. The data was never missing: `icon` and `color` come back on the same
 * `/api/v1/crews` response the <select> was already reading.
 *
 * The shape is the New-issue crew popover's — a Command list in a Popover —
 * rather than a tile per crew, because this sits inline beside another field
 * and a workspace with twenty crews would push the goal box off the screen.
 * The one thing added to that idiom is the <CrewIcon>, which handles the
 * hex/palette-id split on its own.
 */
function CrewPicker({
  id,
  crews,
  value,
  onChange,
  placeholder,
  clearLabel,
  ariaLabel,
  className,
}: {
  id: string
  crews: Crew[]
  value: string
  onChange: (id: string) => void
  /** Shown when nothing is chosen — the old select's first <option>. */
  placeholder: string
  /** Present where "no crew" is a real answer, absent where it is not. */
  clearLabel?: string
  ariaLabel: string
  className?: string
}) {
  const [open, setOpen] = useState(false)
  const selected = crews.find((c) => c.id === value) ?? null

  return (
    <Popover open={open} onOpenChange={setOpen}>
      <PopoverTrigger asChild>
        <button
          id={id}
          type="button"
          role="combobox"
          aria-expanded={open}
          aria-label={ariaLabel}
          className={cn(
            "flex w-full items-center gap-2 rounded-md border border-hairline bg-background px-2 text-left text-foreground outline-none transition-colors hover:border-border focus:border-primary",
            CREATE_SURFACE_INPUT,
            className,
          )}
        >
          {selected ? (
            <CrewIcon
              icon={selected.icon || "users"}
              color={selected.color}
              size="sm"
              className="!h-5 !w-5 !rounded"
            />
          ) : (
            <Users className="h-3.5 w-3.5 shrink-0 text-muted-foreground-soft" aria-hidden />
          )}
          <span className={cn("min-w-0 flex-1 truncate", !selected && "text-muted-foreground")}>
            {selected?.name ?? placeholder}
          </span>
          <ChevronsUpDown className="h-3.5 w-3.5 shrink-0 text-muted-foreground" aria-hidden />
        </button>
      </PopoverTrigger>
      <PopoverContent className="w-[240px] p-0" align="start">
        <Command>
          <CommandInput placeholder="Search crews…" className="h-8 text-xs" />
          <CommandList>
            <CommandEmpty>No crews found.</CommandEmpty>
            <CommandGroup>
              {clearLabel && (
                <CommandItem
                  value={clearLabel}
                  onSelect={() => {
                    onChange("")
                    setOpen(false)
                  }}
                >
                  <Users className="h-3.5 w-3.5 shrink-0 text-muted-foreground-soft" aria-hidden />
                  <span className="truncate text-xs text-muted-foreground">{clearLabel}</span>
                  {value === "" && <Check className="ml-auto h-3.5 w-3.5" />}
                </CommandItem>
              )}
              {crews.map((crew) => (
                <CommandItem
                  key={crew.id}
                  // The id rides along so two crews sharing a name still filter
                  // and select independently; it is not rendered, so the row's
                  // accessible name is still just the crew's.
                  value={`${crew.name} ${crew.id}`}
                  onSelect={() => {
                    onChange(crew.id)
                    setOpen(false)
                  }}
                >
                  <CrewIcon
                    icon={crew.icon || "users"}
                    color={crew.color}
                    size="sm"
                    className="!h-5 !w-5 !rounded"
                  />
                  <span className="truncate text-xs">{crew.name}</span>
                  {value === crew.id && <Check className="ml-auto h-3.5 w-3.5" />}
                </CommandItem>
              ))}
            </CommandGroup>
          </CommandList>
        </Command>
      </PopoverContent>
    </Popover>
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
