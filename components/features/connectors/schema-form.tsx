"use client"

// SchemaForm — renders a form from a ConnectorField[] declaration.
// One component handles all four auth modes:
//
//   - text/number → <input>
//   - password    → <input type=password>
//   - select      → <select> with choices[]
//   - bool        → <input type=checkbox>
//
// Required fields are validated on submit. Defaults pre-fill missing
// values. Help is markdown — kept as plain string here so we don't
// pull a heavy markdown lib into the form layer; the parent sheet
// renders it (or strips it) as it sees fit.
//
// TDD STUB — body throws until implemented.

import type { ReactElement } from "react"
import type { ConnectorField } from "./types"

export interface SchemaFormProps {
  fields: ConnectorField[]
  /** Initial values, e.g. when editing an existing connection. */
  initialValues?: Record<string, string>
  /** Called on Submit with all field values resolved (defaults applied). */
  onSubmit: (values: Record<string, string>) => void | Promise<void>
  /** Disable inputs (during submit). */
  submitting?: boolean
  /** Override submit button label. Default "Connect". */
  submitLabel?: string
}

// Rendered as a non-crashing placeholder until SchemaForm is
// implemented. Keeps a parent sheet from blowing up if it mounts
// before the impl lands.
export function SchemaForm(_: SchemaFormProps): ReactElement {
  return (
    <div role="status" aria-live="polite" className="text-sm text-muted-foreground p-4">
      Schema form is not implemented yet.
    </div>
  )
}
