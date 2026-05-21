"use client"

// PR-F2 — Reusable CodeMirror markdown editor.
//
// Follows the same construction pattern as components/features/files/file-editor.tsx
// (raw @codemirror/* imports, no @uiw/react-codemirror wrapper) so the
// bundle stays small and we get consistent themes/keymaps across the
// app. Designed specifically for the memory tab — short, char-capped
// markdown content where the operator wants real syntax highlighting
// but doesn't need lineNumbers/gutter clutter.
//
// Differences from FileEditor:
//   - Optional lineNumbers (default off — memory tier files are tiny).
//   - Controlled value via `value`; updates are pushed through
//     `onChange` on every doc change. Char counting happens at the
//     call site so it can show byte counts (UTF-8) against an
//     enforced cap.
//   - readOnly toggles EditorState.readOnly + an "editable=false"
//     facet so the agent-written tiers (AGENT.md / CREW.md) cannot be
//     accidentally edited.
//   - autoFocus optional — defaults to true when not read-only.

import { useEffect, useMemo, useRef } from "react"
import { EditorView, keymap, highlightActiveLine } from "@codemirror/view"
import { EditorState, Compartment } from "@codemirror/state"
import {
  defaultKeymap, indentWithTab, history, historyKeymap,
} from "@codemirror/commands"
import {
  bracketMatching, indentOnInput, syntaxHighlighting, defaultHighlightStyle,
} from "@codemirror/language"
import { oneDark } from "@codemirror/theme-one-dark"
import { markdown } from "@codemirror/lang-markdown"

export interface MarkdownEditorProps {
  value: string
  onChange: (next: string) => void
  readOnly?: boolean
  autoFocus?: boolean
  minHeight?: string
  className?: string
  ariaLabel?: string
}

export function MarkdownEditor({
  value,
  onChange,
  readOnly = false,
  autoFocus,
  minHeight = "10rem",
  className,
  ariaLabel,
}: MarkdownEditorProps) {
  const containerRef = useRef<HTMLDivElement>(null)
  const viewRef = useRef<EditorView | null>(null)
  // Mutable refs let the update listener stay stable across renders
  // while always invoking the latest onChange. Mirrors the pattern in
  // file-editor.tsx.
  const onChangeRef = useRef(onChange)
  onChangeRef.current = onChange

  // Compartments so we can toggle readOnly without rebuilding the
  // editor (rebuild would lose focus + scroll position).
  const readOnlyCompartment = useMemo(() => new Compartment(), [])

  useEffect(() => {
    if (!containerRef.current) return

    const updateListener = EditorView.updateListener.of((update) => {
      if (update.docChanged) {
        onChangeRef.current(update.state.doc.toString())
      }
    })

    const state = EditorState.create({
      doc: value,
      extensions: [
        history(),
        highlightActiveLine(),
        bracketMatching(),
        indentOnInput(),
        syntaxHighlighting(defaultHighlightStyle, { fallback: true }),
        markdown(),
        oneDark,
        keymap.of([...defaultKeymap, ...historyKeymap, indentWithTab]),
        updateListener,
        readOnlyCompartment.of([
          EditorState.readOnly.of(readOnly),
          EditorView.editable.of(!readOnly),
        ]),
        EditorView.theme({
          "&": {
            fontSize: "13px",
            minHeight,
            borderRadius: "0.375rem",
            border: "1px solid hsl(var(--border) / 0.6)",
            backgroundColor: "rgb(24 24 27 / 0.6)", // matches zinc-900/60 used elsewhere
          },
          "&.cm-focused": {
            outline: "2px solid rgb(16 185 129 / 0.4)", // emerald-500/40 ring
            outlineOffset: "0",
          },
          ".cm-scroller": {
            overflow: "auto",
            fontFamily: "var(--font-mono, ui-monospace, SFMono-Regular, monospace)",
          },
          ".cm-content": { padding: "8px 12px" },
        }),
      ],
    })

    const view = new EditorView({ state, parent: containerRef.current })
    viewRef.current = view
    if (autoFocus ?? !readOnly) view.focus()

    return () => {
      view.destroy()
      viewRef.current = null
    }
    // We deliberately only rebuild when the initial `value` reference
    // changes (e.g. parent reset). Subsequent user edits flow through
    // the EditorState; rebuilding on every change would wipe selection
    // and focus.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [])

  // Sync readOnly without rebuilding.
  useEffect(() => {
    const view = viewRef.current
    if (!view) return
    view.dispatch({
      effects: readOnlyCompartment.reconfigure([
        EditorState.readOnly.of(readOnly),
        EditorView.editable.of(!readOnly),
      ]),
    })
  }, [readOnly, readOnlyCompartment])

  // External value resets (e.g. cancel-edit reverts) replace the doc.
  useEffect(() => {
    const view = viewRef.current
    if (!view) return
    const current = view.state.doc.toString()
    if (current !== value) {
      view.dispatch({
        changes: { from: 0, to: current.length, insert: value },
      })
    }
  }, [value])

  return (
    <div
      ref={containerRef}
      className={className}
      role="textbox"
      aria-multiline="true"
      aria-readonly={readOnly}
      aria-label={ariaLabel}
    />
  )
}
