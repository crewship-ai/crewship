"use client"

import { useCallback, useState } from "react"
import { motion, AnimatePresence } from "motion/react"
import { Check, Copy } from "lucide-react"
import { legacyCopy } from "@/lib/clipboard"

/** Copies `text`, preferring the async clipboard API where the page is a
 *  secure context and falling back to execCommand otherwise. */
export function copyText(text: string, onSuccess: () => void) {
  const modernAvailable =
    typeof navigator !== "undefined" &&
    navigator.clipboard &&
    typeof navigator.clipboard.writeText === "function" &&
    typeof window !== "undefined" &&
    window.isSecureContext
  if (modernAvailable) {
    navigator.clipboard.writeText(text).then(onSuccess).catch(() => { if (legacyCopy(text)) onSuccess() })
    return
  }
  if (legacyCopy(text)) onSuccess()
}

interface CommandSnippetProps {
  /** The shell command, without the leading `$`. */
  command: string
  /** Optional caption above the command. */
  caption?: string
  className?: string
}

/**
 * A terminal one-liner with a copy button — the wizard shows the same
 * `claude setup-token` twice (a heads-up on step 1, the token field on step
 * 2) and a person following it should never have to select monospace text
 * by hand with a mouse.
 */
export function CommandSnippet({ command, caption, className = "" }: CommandSnippetProps) {
  const [copied, setCopied] = useState(false)
  const onCopy = useCallback(() => {
    copyText(command, () => {
      setCopied(true)
      setTimeout(() => setCopied(false), 1500)
    })
  }, [command])

  return (
    <div className={`rounded-lg border border-border bg-card/70 px-3 py-2 font-mono text-micro leading-relaxed ${className}`}>
      {caption && <div className="mb-1 font-sans text-micro text-muted-foreground">{caption}</div>}
      <div className="flex items-center justify-between gap-2">
        <code className="select-all text-success">
          <span className="text-muted-foreground">$ </span>
          {command}
        </code>
        <button
          type="button"
          onClick={onCopy}
          aria-label={`Copy "${command}"`}
          className="inline-flex h-7 shrink-0 items-center gap-1 rounded-md border border-border px-2 font-sans text-micro font-medium text-muted-foreground transition-colors hover:bg-muted hover:text-foreground"
        >
          <AnimatePresence mode="wait" initial={false}>
            {copied ? (
              <motion.span
                key="check"
                initial={{ scale: 0.6, opacity: 0 }}
                animate={{ scale: 1, opacity: 1 }}
                exit={{ scale: 0.6, opacity: 0 }}
                transition={{ duration: 0.2 }}
                className="inline-flex items-center gap-1 text-success"
              >
                <Check className="h-3 w-3" /> Copied
              </motion.span>
            ) : (
              <motion.span
                key="copy"
                initial={{ scale: 0.6, opacity: 0 }}
                animate={{ scale: 1, opacity: 1 }}
                exit={{ scale: 0.6, opacity: 0 }}
                transition={{ duration: 0.2 }}
                className="inline-flex items-center gap-1"
              >
                <Copy className="h-3 w-3" /> Copy
              </motion.span>
            )}
          </AnimatePresence>
        </button>
      </div>
    </div>
  )
}
