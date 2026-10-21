"use client"

import * as React from "react"

import { Input } from "@/components/ui/input"
import {
  CREATE_SURFACE_INPUT,
  CreateSurface,
  CreateSurfaceBody,
  CreateSurfaceField,
  CreateSurfaceFooter,
  CreateSurfaceHeader,
  CreateSurfaceRefusal,
} from "@/components/layout/create-surface"

// New folder (ODCH-RT-01): a name. A folder only groups routines in the
// panel; a refusal (a name already taken) is shown in the dialog in the
// server's words and keeps what was typed. The same dialog renames a folder
// and makes a subfolder from the folder's menu (#3768): it then opens with
// the current name, or says which folder the new one goes in.
export function RoutineFolderDialog({
  open,
  onOpenChange,
  onCreate,
  title = "New folder",
  description = "A folder groups routines in the panel. A routine is in at most one folder.",
  context,
  initialName = "",
  primaryLabel = "Create folder",
}: {
  open: boolean
  onOpenChange: (open: boolean) => void
  /** Resolves to a refusal message, or null once the folder is saved. */
  onCreate: (name: string) => Promise<string | null>
  title?: string
  description?: string
  /** The folder this is about, shown above the title. */
  context?: string
  /** What the field opens with: the folder's name when renaming. */
  initialName?: string
  primaryLabel?: string
}) {
  const [name, setName] = React.useState("")
  const [refusal, setRefusal] = React.useState<string | null>(null)
  const [busy, setBusy] = React.useState(false)
  const nameId = React.useId()
  React.useEffect(() => {
    if (open) {
      setName(initialName)
      setRefusal(null)
    }
  }, [open, initialName])
  const trimmed = name.trim()
  // Renaming to the same name is nothing to save.
  const unchanged = initialName !== "" && trimmed === initialName.trim()
  const submit = async () => {
    if (!trimmed || unchanged || busy) return
    setBusy(true)
    setRefusal(null)
    try {
      const message = await onCreate(trimmed)
      if (message) setRefusal(message)
      else onOpenChange(false)
    } finally {
      setBusy(false)
    }
  }
  return (
    <CreateSurface open={open} onOpenChange={onOpenChange} size="sm" dirty={trimmed !== initialName.trim()} onSubmit={() => void submit()}>
      <CreateSurfaceHeader
        concept="routines"
        context={context}
        title={title}
        description={description}
        onClose={() => onOpenChange(false)}
      />
      <CreateSurfaceBody>
        <CreateSurfaceField label="Name" htmlFor={nameId} required>
          <Input
            id={nameId}
            className={CREATE_SURFACE_INPUT}
            value={name}
            maxLength={80}
            autoFocus
            onChange={(e) => {
              setName(e.target.value)
              if (refusal) setRefusal(null)
            }}
          />
        </CreateSurfaceField>
      </CreateSurfaceBody>
      <CreateSurfaceRefusal message={refusal} onDismiss={() => setRefusal(null)} />
      <CreateSurfaceFooter
        onCancel={() => onOpenChange(false)}
        primaryLabel={primaryLabel}
        onPrimary={() => void submit()}
        primaryDisabled={!trimmed || unchanged || busy}
        busy={busy}
      />
    </CreateSurface>
  )
}
