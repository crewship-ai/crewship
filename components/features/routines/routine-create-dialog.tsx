"use client"

import { useEffect, useState } from "react"
import { X, FlaskConical, Save, AlertTriangle } from "lucide-react"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { Badge } from "@/components/ui/badge"
import { toast } from "sonner"
import { cn } from "@/lib/utils"

// RoutineCreateDialog — UI authoring flow for new routines. Two-step
// pattern mirrors what Trigger.dev does for tasks (write code, then
// test) but compressed into one dialog: enter DSL → Test & Save.
//
// The save endpoint (POST /api/v1/workspaces/{ws}/pipelines/save)
// requires a fresh passing test_run. We run /test_run locally inside
// this dialog before /save so the user sees explicit pass/fail before
// the routine lands. OWNER + ADMIN roles can additionally toggle
// "skip test gate" — useful when the DSL is hand-crafted from a
// known-good template.
//
// Starter templates seed the editor with valid DSL skeletons so a
// non-engineering author has something to riff on without reading
// the spec first.

interface Props {
  workspaceId: string
  open: boolean
  onClose: () => void
  onCreated: (slug: string) => void
}

const STARTER_TEMPLATES = [
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

export function RoutineCreateDialog({ workspaceId, open, onClose, onCreated }: Props) {
  const [name, setName] = useState("")
  const [description, setDescription] = useState("")
  const [authorCrewId, setAuthorCrewId] = useState("")
  const [crews, setCrews] = useState<Crew[]>([])
  const [dslJson, setDslJson] = useState(() => JSON.stringify(STARTER_TEMPLATES[0].json, null, 2))
  const [parseError, setParseError] = useState<string | null>(null)
  const [busy, setBusy] = useState<"none" | "testing" | "saving">("none")
  const [testResult, setTestResult] = useState<{ passed: boolean; details: string } | null>(null)
  const [skipTestGate, setSkipTestGate] = useState(false)

  // Lazy-load crews on first open. Side effect lives in useEffect
  // (not in render body) so React's render pipeline isn't disturbed
  // — putting fetch + setState directly in the component body causes
  // re-render loops and, in some hydration scenarios, blocks the
  // dialog from mounting at all.
  useEffect(() => {
    if (!open || crews.length > 0) return
    let cancelled = false
    fetch(`/api/v1/crews?workspace_id=${workspaceId}`)
      .then((r) => (r.ok ? r.json() : []))
      .then((data: Crew[]) => {
        if (!cancelled) setCrews(Array.isArray(data) ? data : [])
      })
      .catch(() => {})
    return () => {
      cancelled = true
    }
  }, [open, workspaceId, crews.length])

  if (!open) return null

  const applyTemplate = (templateId: string) => {
    const tpl = STARTER_TEMPLATES.find((t) => t.id === templateId)
    if (!tpl) return
    const j = { ...tpl.json, name: name || tpl.json.name, description: description || tpl.json.description }
    setDslJson(JSON.stringify(j, null, 2))
    setParseError(null)
    setTestResult(null)
  }

  const parseDSL = (): Record<string, unknown> | null => {
    try {
      const parsed = JSON.parse(dslJson) as Record<string, unknown>
      setParseError(null)
      return parsed
    } catch (e) {
      setParseError(e instanceof Error ? e.message : "invalid JSON")
      return null
    }
  }

  const slug = (parseDSL()?.["name"] as string) || "my-routine"

  const handleTestRun = async (): Promise<boolean> => {
    const parsed = parseDSL()
    if (!parsed) {
      toast.error("Definition is not valid JSON")
      return false
    }
    setBusy("testing")
    setTestResult(null)
    try {
      const res = await fetch(`/api/v1/workspaces/${workspaceId}/pipelines/test_run`, {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ definition: parsed, sample_inputs: {} }),
      })
      const data = (await res.json().catch(() => ({}))) as { passed?: boolean; error?: string; output?: string }
      if (!res.ok) {
        const msg = data.error ?? `HTTP ${res.status}`
        setTestResult({ passed: false, details: msg })
        toast.error("Test run failed", { description: msg })
        return false
      }
      const passed = data.passed !== false
      setTestResult({
        passed,
        details: passed ? `Passed${data.output ? ` (output: ${truncate(String(data.output), 120)})` : ""}` : data.error ?? "test_run reported failure",
      })
      if (passed) {
        toast.success("Test run passed")
      } else {
        toast.error("Test run failed", { description: data.error ?? "see details below" })
      }
      return passed
    } catch (e) {
      const msg = e instanceof Error ? e.message : String(e)
      setTestResult({ passed: false, details: msg })
      toast.error("Test run errored", { description: msg })
      return false
    } finally {
      setBusy("none")
    }
  }

  const handleSave = async (assumeTestPassed: boolean) => {
    const parsed = parseDSL()
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
        last_test_run_passed: assumeTestPassed || skipTestGate,
        skip_test_gate: skipTestGate,
      }
      if (assumeTestPassed && !skipTestGate) {
        body.last_test_run_at = new Date().toISOString()
      }
      if (authorCrewId) body.author_crew_id = authorCrewId

      const res = await fetch(`/api/v1/workspaces/${workspaceId}/pipelines/save`, {
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
    const passed = await handleTestRun()
    if (passed) {
      await handleSave(true)
    }
  }

  return (
    <div className="fixed inset-0 z-50 flex items-center justify-center bg-black/50 backdrop-blur-sm p-4" onClick={onClose}>
      <div
        className="flex h-[90vh] w-full max-w-3xl flex-col rounded-lg border border-white/10 bg-card shadow-2xl"
        onClick={(e) => e.stopPropagation()}
      >
        {/* Header */}
        <div className="flex items-center justify-between border-b border-white/[0.06] px-4 py-3 shrink-0">
          <h3 className="text-sm font-medium">New routine</h3>
          <Button size="sm" variant="ghost" className="h-7 w-7 p-0" onClick={onClose}>
            <X className="h-3 w-3" />
          </Button>
        </div>

        {/* Body — split: left meta + templates, right DSL editor */}
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
              <label className="text-[10px] uppercase tracking-wider text-muted-foreground">
                Author crew
              </label>
              <select
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
            <div className="flex items-center justify-between border-b border-white/[0.06] px-3 py-1.5 shrink-0">
              <div className="flex items-center gap-2 text-[11px] text-muted-foreground">
                <span>JSON DSL</span>
                <span>·</span>
                <span className="font-mono">slug: {slug}</span>
              </div>
              {parseError && (
                <Badge variant="outline" className="text-[10px] border-red-500/30 text-red-400">
                  invalid JSON
                </Badge>
              )}
            </div>
            <textarea
              value={dslJson}
              onChange={(e) => {
                setDslJson(e.target.value)
                setParseError(null)
                setTestResult(null)
              }}
              spellCheck={false}
              className="flex-1 resize-none bg-background p-3 font-mono text-[11px] leading-relaxed outline-none"
            />
            {testResult && (
              <div
                className={cn(
                  "border-t px-3 py-2 text-xs",
                  testResult.passed ? "border-emerald-500/30 bg-emerald-500/5 text-emerald-300" : "border-red-500/30 bg-red-500/5 text-red-400",
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
          <label className="flex cursor-pointer items-center gap-1.5 text-[11px] text-muted-foreground">
            <input
              type="checkbox"
              checked={skipTestGate}
              onChange={(e) => setSkipTestGate(e.target.checked)}
              className="h-3 w-3 cursor-pointer accent-blue-500"
            />
            Skip test-run gate
            <AlertTriangle className="h-2.5 w-2.5" />
            <span className="text-[10px]">(OWNER / ADMIN only)</span>
          </label>
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
                onClick={() => handleSave(false)}
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
      </div>
    </div>
  )
}

function truncate(s: string, n: number): string {
  if (s.length <= n) return s
  return s.slice(0, n - 1) + "…"
}
