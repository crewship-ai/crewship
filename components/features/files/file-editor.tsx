"use client"

import { useRef, useEffect, useCallback } from "react"
import { EditorView, keymap, lineNumbers, highlightActiveLine, highlightActiveLineGutter } from "@codemirror/view"
import { EditorState } from "@codemirror/state"
import { defaultKeymap, indentWithTab, history, historyKeymap } from "@codemirror/commands"
import { bracketMatching, indentOnInput, syntaxHighlighting, defaultHighlightStyle } from "@codemirror/language"
import { oneDark } from "@codemirror/theme-one-dark"
import { python } from "@codemirror/lang-python"
import { javascript } from "@codemirror/lang-javascript"
import { json } from "@codemirror/lang-json"
import { yaml } from "@codemirror/lang-yaml"
import { html } from "@codemirror/lang-html"
import { css } from "@codemirror/lang-css"
import { markdown } from "@codemirror/lang-markdown"

function getLanguageExtension(lang: string) {
  switch (lang) {
    case "python": return python()
    case "javascript": case "jsx": return javascript({ jsx: true })
    case "typescript": case "tsx": return javascript({ jsx: true, typescript: true })
    case "json": return json()
    case "yaml": return yaml()
    case "html": case "xml": case "svg": return html()
    case "css": case "scss": case "less": return css()
    case "markdown": return markdown()
    default: return []
  }
}

interface FileEditorProps {
  code: string
  language: string
  onSave: (content: string) => void
  onDirtyChange?: (dirty: boolean) => void
  saveRef?: React.MutableRefObject<(() => void) | null>
}

export function FileEditor({ code, language, onSave, onDirtyChange, saveRef }: FileEditorProps) {
  const containerRef = useRef<HTMLDivElement>(null)
  const viewRef = useRef<EditorView | null>(null)
  const onSaveRef = useRef(onSave)
  const onDirtyChangeRef = useRef(onDirtyChange)
  const initialCodeRef = useRef(code)

  onSaveRef.current = onSave
  onDirtyChangeRef.current = onDirtyChange

  const handleSave = useCallback(() => {
    if (viewRef.current) {
      onSaveRef.current(viewRef.current.state.doc.toString())
    }
  }, [])

  useEffect(() => {
    if (saveRef) saveRef.current = handleSave
    return () => { if (saveRef) saveRef.current = null }
  }, [saveRef, handleSave])

  useEffect(() => {
    if (!containerRef.current) return

    const saveKeymap = keymap.of([{
      key: "Mod-s",
      run: () => { handleSave(); return true },
    }])

    const updateListener = EditorView.updateListener.of((update) => {
      if (update.docChanged && onDirtyChangeRef.current) {
        const current = update.state.doc.toString()
        onDirtyChangeRef.current(current !== initialCodeRef.current)
      }
    })

    const state = EditorState.create({
      doc: code,
      extensions: [
        lineNumbers(),
        highlightActiveLine(),
        highlightActiveLineGutter(),
        history(),
        bracketMatching(),
        indentOnInput(),
        syntaxHighlighting(defaultHighlightStyle, { fallback: true }),
        oneDark,
        getLanguageExtension(language),
        keymap.of([...defaultKeymap, ...historyKeymap, indentWithTab]),
        saveKeymap,
        updateListener,
        EditorView.theme({
          "&": { height: "100%", fontSize: "13px" },
          ".cm-scroller": { overflow: "auto", fontFamily: "var(--font-mono, monospace)" },
          ".cm-content": { padding: "8px 0" },
        }),
      ],
    })

    const view = new EditorView({ state, parent: containerRef.current })
    viewRef.current = view
    view.focus()

    return () => { view.destroy(); viewRef.current = null }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [language])

  return <div ref={containerRef} className="h-full w-full overflow-hidden" />
}
