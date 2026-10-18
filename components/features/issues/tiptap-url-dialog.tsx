"use client"

import * as React from "react"
import { ImagePlus, Link2 } from "lucide-react"

import {
  CREATE_SURFACE_INPUT,
  CreateSurface,
  CreateSurfaceBody,
  CreateSurfaceField,
  CreateSurfaceFooter,
  CreateSurfaceHeader,
} from "@/components/layout/create-surface"
import { Input } from "@/components/ui/input"

/**
 * The issue editor's URL input: a link to set or edit, or an image to insert.
 *
 * Replaces the browser's native prompt (#3343), which was unstyled, blocked
 * the page and accepted anything — including `javascript:` hrefs. Built on
 * CreateSurface at its one-question width, so overlay, Esc and focus trap are
 * the kit's and not this file's.
 */

export type UrlDialogKind = "link" | "image"

export interface UrlDialogRequest {
  /** Bumped per opening so the field starts fresh every time. */
  id: number
  kind: UrlDialogKind
  /** The link being edited, or "" for a new one. */
  initialUrl: string
}

export type UrlValidation = { ok: true; url: string } | { ok: false; error: string }

const SCHEME_ERROR = "Enter a full URL starting with http:// or https://."

/**
 * Only absolute http(s) URLs pass. For a link, an empty value is valid and
 * means "remove the link"; an image has nothing to remove, so it is refused.
 */
export function validateEditorUrl(raw: string, kind: UrlDialogKind): UrlValidation {
  const value = raw.trim()
  if (value === "") {
    return kind === "link" ? { ok: true, url: "" } : { ok: false, error: "Enter an image URL." }
  }
  let parsed: URL
  try {
    parsed = new URL(value)
  } catch {
    return { ok: false, error: SCHEME_ERROR }
  }
  if (parsed.protocol !== "http:" && parsed.protocol !== "https:") {
    return { ok: false, error: SCHEME_ERROR }
  }
  return { ok: true, url: value }
}

export function TiptapUrlDialog({
  request,
  onSubmit,
  onCancel,
}: {
  /** null = closed. */
  request: UrlDialogRequest | null
  /** Called with a validated URL ("" only for removing a link). */
  onSubmit: (url: string, kind: UrlDialogKind) => void
  onCancel: () => void
}) {
  // Keep the last request through the close animation, so the card does not
  // blank out while it fades.
  const [shown, setShown] = React.useState(request)
  if (request && request !== shown) setShown(request)

  return (
    <CreateSurface
      open={request !== null}
      onOpenChange={(open) => {
        if (!open) onCancel()
      }}
      size="sm"
      // Focus goes back to whatever had it at opening: the caller focuses the
      // editor first, so the caret returns to the same selection.
      restoreOpenerFocus
    >
      {shown && <UrlForm key={shown.id} request={shown} onSubmit={onSubmit} onCancel={onCancel} />}
    </CreateSurface>
  )
}

function UrlForm({
  request,
  onSubmit,
  onCancel,
}: {
  request: UrlDialogRequest
  onSubmit: (url: string, kind: UrlDialogKind) => void
  onCancel: () => void
}) {
  const { kind, initialUrl } = request
  const [value, setValue] = React.useState(initialUrl)
  const [error, setError] = React.useState<string | null>(null)
  const id = React.useId()
  const editing = kind === "link" && initialUrl !== ""

  const submit = () => {
    const result = validateEditorUrl(value, kind)
    if (!result.ok) {
      setError(result.error)
      return
    }
    onSubmit(result.url, kind)
  }

  const title = kind === "image" ? "Insert image" : editing ? "Edit link" : "Insert link"

  return (
    <>
      <CreateSurfaceHeader icon={kind === "image" ? ImagePlus : Link2} title={title} onClose={onCancel} />
      <CreateSurfaceBody>
        <CreateSurfaceField
          label={kind === "image" ? "Image URL" : "URL"}
          htmlFor={id}
          error={error}
          hint={editing ? "Leave empty to remove the link." : undefined}
        >
          <Input
            id={id}
            type="url"
            inputMode="url"
            autoComplete="off"
            spellCheck={false}
            autoFocus
            placeholder="https://"
            value={value}
            aria-invalid={error ? true : undefined}
            aria-describedby={error ? `${id}-error` : undefined}
            onFocus={(e) => e.currentTarget.select()}
            onChange={(e) => {
              setValue(e.target.value)
              if (error) setError(null)
            }}
            onKeyDown={(e) => {
              if (e.key === "Enter" && !e.nativeEvent.isComposing) {
                e.preventDefault()
                submit()
              }
            }}
            className={CREATE_SURFACE_INPUT}
          />
        </CreateSurfaceField>
      </CreateSurfaceBody>
      <CreateSurfaceFooter
        hint={
          <>
            <kbd className="font-mono">↵</kbd> to confirm · <kbd className="font-mono">Esc</kbd> to cancel
          </>
        }
        onCancel={onCancel}
        primaryLabel={editing ? "Save" : "Insert"}
        onPrimary={submit}
      />
    </>
  )
}
