"use client"

// SchemaForm — renders a form from a ConnectorField[] declaration.
// One component handles all five field types:
//
//   - text     → <input type="text">
//   - password → <input type="password">
//   - number   → <input type="number">
//   - select   → <select> with one <option> per choice
//   - bool     → <input type="checkbox">
//
// Required fields are validated on submit (empty → block, no
// onSubmit). Defaults pre-fill missing values before onSubmit fires.
// Help text is rendered below the input as plain string — the parent
// sheet can swap to a markdown renderer if it wants. Submit button
// label is "Connect" by default; ConnectorConnectSheet overrides to
// "Verify & save" for the verify-then-install flow.

import {
  useEffect,
  useMemo,
  useState,
  type ChangeEvent,
  type FormEvent,
  type ReactElement,
} from "react"

import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"

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

// initialState seeds the controlled-form map from `initialValues` and
// each field's own `default`. initialValues wins so a parent editing
// an existing row doesn't lose user input to a manifest default.
function initialState(
  fields: ConnectorField[],
  initialValues?: Record<string, string>,
): Record<string, string> {
  const out: Record<string, string> = {}
  for (const fld of fields) {
    if (initialValues && fld.key in initialValues) {
      out[fld.key] = initialValues[fld.key] ?? ""
    } else if (fld.default !== undefined) {
      out[fld.key] = fld.default
    } else {
      out[fld.key] = ""
    }
  }
  return out
}

export function SchemaForm(props: SchemaFormProps): ReactElement {
  const { fields, initialValues, onSubmit, submitting = false, submitLabel = "Connect" } = props
  const [values, setValues] = useState<Record<string, string>>(() => initialState(fields, initialValues))

  // Re-seed when the field set or initial values shape changes so a
  // parent that swaps manifests doesn't leave stale keys behind. We
  // key on field ids + initialValues identity so typing into a stable
  // form doesn't re-seed mid-edit.
  const seedKey = useMemo(
    () => fields.map((f) => f.key).join("|") + "::" + JSON.stringify(initialValues ?? {}),
    [fields, initialValues],
  )
  useEffect(() => {
    setValues(initialState(fields, initialValues))
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [seedKey])

  function setField(key: string, value: string) {
    setValues((prev) => ({ ...prev, [key]: value }))
  }

  function handleSubmit(e: FormEvent<HTMLFormElement>) {
    e.preventDefault()
    if (submitting) return

    // Apply defaults for any field the user left blank.
    const resolved: Record<string, string> = {}
    for (const fld of fields) {
      const v = values[fld.key] ?? ""
      if (v === "" && fld.default !== undefined) {
        resolved[fld.key] = fld.default
      } else {
        resolved[fld.key] = v
      }
    }

    // Required-field validation: any required field that's still
    // empty blocks the submit. The HTML `required` attribute also
    // triggers browser-native validation, but tests fire events
    // directly and bypass that — keep the JS check too.
    for (const fld of fields) {
      if (fld.required && (resolved[fld.key] ?? "").trim() === "") {
        return
      }
    }

    void onSubmit(resolved)
  }

  return (
    <form onSubmit={handleSubmit} className="flex flex-col gap-4" noValidate>
      {fields.map((fld) => {
        const id = `schema-form-${fld.key}`
        // The HTML `required` attribute alone is enough for testing-library
        // and screen readers to mark the field. Visual asterisks are kept
        // out of the label string so getByLabelText("Host") still matches
        // when fld.label is "Host" and required=true; renderers that want
        // an asterisk should add it via a separate aria-hidden span.
        const labelText = fld.label
        const common = {
          id,
          disabled: submitting,
          placeholder: fld.placeholder,
          required: fld.required,
        }

        if (fld.type === "bool") {
          return (
            <div key={fld.key} className="flex flex-col gap-1">
              <label htmlFor={id} className="inline-flex items-center gap-2 text-sm font-medium">
                <input
                  {...common}
                  type="checkbox"
                  checked={values[fld.key] === "true"}
                  onChange={(e: ChangeEvent<HTMLInputElement>) =>
                    setField(fld.key, e.currentTarget.checked ? "true" : "")
                  }
                  className="h-4 w-4 rounded border-input"
                />
                <span>{labelText}</span>
              </label>
              {fld.help && <p className="text-xs text-muted-foreground">{fld.help}</p>}
            </div>
          )
        }

        if (fld.type === "select") {
          return (
            <div key={fld.key} className="flex flex-col gap-1">
              <label htmlFor={id} className="text-sm font-medium">
                {labelText}
              </label>
              <select
                {...common}
                value={values[fld.key] ?? ""}
                onChange={(e: ChangeEvent<HTMLSelectElement>) => setField(fld.key, e.currentTarget.value)}
                className="h-9 rounded-md border bg-transparent px-3 text-sm shadow-xs"
              >
                {(fld.choices ?? []).map((c) => (
                  <option key={c} value={c}>
                    {c}
                  </option>
                ))}
              </select>
              {fld.help && <p className="text-xs text-muted-foreground">{fld.help}</p>}
            </div>
          )
        }

        const htmlType = fld.type === "password" ? "password" : fld.type === "number" ? "number" : "text"
        return (
          <div key={fld.key} className="flex flex-col gap-1">
            <label htmlFor={id} className="text-sm font-medium">
              {labelText}
            </label>
            <Input
              {...common}
              type={htmlType}
              value={values[fld.key] ?? ""}
              onChange={(e: ChangeEvent<HTMLInputElement>) => setField(fld.key, e.currentTarget.value)}
            />
            {fld.help && <p className="text-xs text-muted-foreground">{fld.help}</p>}
          </div>
        )
      })}

      <div className="flex justify-end">
        <Button type="submit" disabled={submitting}>
          {submitLabel}
        </Button>
      </div>
    </form>
  )
}
