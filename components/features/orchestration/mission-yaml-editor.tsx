"use client"

import { useRef, useEffect, useCallback } from "react"
import { Save, FileCode2 } from "lucide-react"
import { EditorView, keymap, lineNumbers, highlightActiveLine, highlightActiveLineGutter } from "@codemirror/view"
import { EditorState } from "@codemirror/state"
import { defaultKeymap, indentWithTab, history, historyKeymap } from "@codemirror/commands"
import { bracketMatching, indentOnInput, syntaxHighlighting, defaultHighlightStyle } from "@codemirror/language"
import { oneDark } from "@codemirror/theme-one-dark"
import { yaml } from "@codemirror/lang-yaml"
import { Button } from "@/components/ui/button"
import type { Mission } from "@/lib/types/mission"

export interface MissionYamlEditorProps {
  mission: Mission | null
  readOnly?: boolean
  onSave?: (yaml: string) => void
}

function escapeYaml(val: string): string {
  if (/[:#\[\]{}&*!|>'",%@`]/.test(val) || val.trim() !== val) {
    return `"${val.replace(/\\/g, "\\\\").replace(/"/g, '\\"')}"`
  }
  return `"${val}"`
}

// Exported for unit testing. Defensive against partially-loaded missions:
// the /issues list endpoint omits `tasks` (no include_tasks), so a mission
// in the drawer can arrive with tasks === undefined. Iterating that threw
// "undefined is not iterable" and crashed the Spec tab's error boundary.
export function missionToYaml(mission: Mission): string {
  const lines: string[] = [
    "mission:",
    `  title: ${escapeYaml(mission.title ?? "")}`,
    // Route nullish scalars through escapeYaml so a missing field serializes
    // as "" rather than a bare `status:` (which YAML reads as null) — a
    // partially-loaded issue mustn't silently change shape on save.
    `  status: ${escapeYaml(mission.status ?? "")}`,
    `  lead_agent: ${escapeYaml(mission.lead_agent_slug ?? "")}`,
  ]

  if (mission.pattern) lines.push(`  pattern: ${mission.pattern}`)
  if (mission.complexity) lines.push(`  complexity: ${mission.complexity}`)
  if (mission.total_token_budget != null) lines.push(`  token_budget: ${mission.total_token_budget}`)
  if (mission.description) lines.push(`  description: ${escapeYaml(mission.description)}`)

  lines.push("  tasks:")

  for (const task of mission.tasks ?? []) {
    lines.push(`    - id: ${task.id}`)
    lines.push(`      title: ${escapeYaml(task.title)}`)
    lines.push(`      status: ${task.status}`)
    if (task.agent_slug) lines.push(`      agent: ${task.agent_slug}`)
    if (task.complexity) lines.push(`      complexity: ${task.complexity}`)
    if (task.token_budget != null) lines.push(`      token_budget: ${task.token_budget}`)
    if (task.depends_on && task.depends_on !== "[]") {
      try {
        const deps = JSON.parse(task.depends_on) as string[]
        if (deps.length > 0) {
          lines.push(`      depends_on: [${deps.map(d => `"${d}"`).join(", ")}]`)
        }
      } catch { /* skip malformed */ }
    }
    if (task.description) lines.push(`      description: ${escapeYaml(task.description)}`)
  }

  return lines.join("\n") + "\n"
}

export function MissionYamlEditor({ mission, readOnly = false, onSave }: MissionYamlEditorProps) {
  const containerRef = useRef<HTMLDivElement>(null)
  const viewRef = useRef<EditorView | null>(null)
  const onSaveRef = useRef(onSave)
  onSaveRef.current = onSave

  const handleSave = useCallback(() => {
    if (viewRef.current && onSaveRef.current) {
      onSaveRef.current(viewRef.current.state.doc.toString())
    }
  }, [])

  const yamlContent = mission ? missionToYaml(mission) : ""

  useEffect(() => {
    if (!containerRef.current) return

    const extensions = [
      lineNumbers(),
      highlightActiveLine(),
      highlightActiveLineGutter(),
      history(),
      bracketMatching(),
      indentOnInput(),
      syntaxHighlighting(defaultHighlightStyle, { fallback: true }),
      oneDark,
      yaml(),
      keymap.of([...defaultKeymap, ...historyKeymap, indentWithTab]),
      EditorView.theme({
        "&": { height: "100%", fontSize: "13px" },
        ".cm-scroller": { overflow: "auto", fontFamily: "var(--font-mono, monospace)" },
        ".cm-content": { padding: "8px 0" },
      }),
    ]

    if (readOnly) {
      extensions.push(EditorState.readOnly.of(true))
    } else {
      extensions.push(keymap.of([{ key: "Mod-s", run: () => { handleSave(); return true } }]))
    }

    const state = EditorState.create({ doc: yamlContent, extensions })
    const view = new EditorView({ state, parent: containerRef.current })
    viewRef.current = view

    return () => { view.destroy(); viewRef.current = null }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [yamlContent, readOnly])

  if (!mission) {
    return (
      <div className="flex flex-col items-center justify-center h-full py-8 text-muted-foreground/70">
        <FileCode2 className="size-6 mb-2" />
        <p className="text-xs">Select a mission</p>
      </div>
    )
  }

  return (
    <div className="relative h-full flex flex-col">
      {!readOnly && onSave && (
        <div className="absolute top-2 right-3 z-10">
          <Button variant="outline" size="xs" onClick={handleSave}>
            <Save className="size-3" /> Save
          </Button>
        </div>
      )}
      <div ref={containerRef} className="flex-1 min-h-0 overflow-hidden" />
    </div>
  )
}
