"use client"

import type React from "react"

import { Checkbox } from "@/components/ui/checkbox"
import { Input } from "@/components/ui/input"
import { Label } from "@/components/ui/label"
import { Textarea } from "@/components/ui/textarea"
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select"
import { cn } from "@/lib/utils"

/** `select` / `multiselect` options. The wire (lib/ask-template.ts) carries
 *  bare strings; the object form is accepted so a value and its visible label
 *  can differ without another schema change. */
export type FormFieldOption = string | { value: string; label?: string }

/**
 * The one field renderer for schema-driven forms in chat.
 *
 * Lifted out of `composer/slash-action-modal.tsx`, which had the only such
 * renderer in the product. The ask sheet needs the same job done — a
 * server-supplied schema turned into inputs — and the honest way to get it is
 * to share this component, not to grow a second switch that drifts from this
 * one the first time either side adds a type.
 *
 * Two rules carried over verbatim from the modal:
 *
 *   1. **An unknown type renders a text input.** The server owns the field
 *      catalogue; an unrecognised type means the dashboard is older than the
 *      server, and a text input the server can validate beats rendering
 *      nothing. This is what lets a new field type ship without a coordinated
 *      frontend release (PRD §6.1), and it is asserted from both sides.
 *   2. **A field with no `label` is labelled by its underscored name**, with
 *      the capitalisation done in CSS. That is exactly what the slash modal
 *      emitted before the extraction, and its schema (`SlashFormField`) has no
 *      `label` at all, so every slash field takes this path unchanged.
 *
 * Values are strings, one per field name — the shape the modal already used.
 * A multi-valued field (multiselect) is joined here and split here; nothing
 * outside this file needs to know the encoding.
 */

export interface FormFieldSpec {
  name: string
  type: string
  required?: boolean
  /** Widened to `unknown` so an `AskFormField` (whose default is `unknown`)
   *  and a `SlashFormField` (whose default is a string) both fit. This
   *  component never reads it — seeding the values map is the caller's job. */
  default?: unknown
  /** Ask forms carry a human label; slash commands do not (see rule 2). */
  label?: string
  placeholder?: string
  help?: string
  options?: FormFieldOption[]
  /** Currencies offered beside a `money` amount. */
  currency?: string[]
  multiple?: boolean
}

export interface FormFieldProps {
  field: FormFieldSpec
  value: string
  /** Deliberately event-shaped rather than `(value: string) => void`: it is
   *  what the modal already passed, so `onChange={setField(name)}` keeps
   *  working for a plain input without a wrapper at every call site. */
  onChange: (e: { target: { value: string } }) => void
  /** Prefixed onto the DOM id, so two schema-driven forms on one page (the
   *  composer's sheet and anything else mounted beside it) cannot collide.
   *  Empty by default — the slash modal's ids stay `field.name`. */
  idPrefix?: string
  /** Prefix for `data-testid` on the sub-controls a field type grows
   *  (currency select, upload slot). */
  testIdPrefix?: string
  /** Rendered in place of the input for `file` / `photo`. Supplied by the ask
   *  sheet, which owns the upload; a caller without one (the slash modal) gets
   *  the text fallback, per rule 1. */
  attachmentSlot?: React.ReactNode
}

/** How a multiselect's chosen values are packed into the single string this
 *  component's value model carries. */
const MULTI_SEPARATOR = ", "

export function optionValue(option: FormFieldOption): string {
  return typeof option === "string" ? option : option.value
}

export function optionLabel(option: FormFieldOption): string {
  return typeof option === "string" ? option : option.label ?? option.value
}

/** The text of a field's label. Exported because the sheet's validation says
 *  which field is missing, and it must say it the same way the label does. */
export function fieldLabelText(field: FormFieldSpec): string {
  const explicit = field.label?.trim()
  return explicit && explicit !== "" ? explicit : field.name.replace(/_/g, " ")
}

function splitMulti(value: string): string[] {
  return value
    .split(",")
    .map((v) => v.trim())
    .filter((v) => v !== "")
}

/**
 * `"1249 CZK"` ⇄ amount + currency.
 *
 * A money field is two controls and ONE entry in the values map, so that this
 * component keeps the single-string interface the slash modal already used.
 * The sheet splits it again on the way to the renderer, where a money field
 * named `amount` answers to `{{amount}}` and `{{amount_currency}}` — deriving
 * the second name from the first is what stops two money fields fighting over
 * one `{{currency}}` (lib/ask-template.ts).
 */
const MONEY_RE = /^\s*(.*?)\s*([A-Za-z]{3})\s*$/
export const DEFAULT_CURRENCIES = ["CZK", "EUR", "USD", "GBP"]

export function splitMoney(
  value: string,
  currencies: string[] = DEFAULT_CURRENCIES,
): { amount: string; currency: string } {
  const fallback = currencies[0] ?? DEFAULT_CURRENCIES[0]
  const m = MONEY_RE.exec(value)
  if (m) return { amount: m[1], currency: m[2].toUpperCase() }
  return { amount: value.trim(), currency: fallback }
}

function joinMoney(amount: string, currency: string): string {
  const a = amount.trim()
  return a === "" ? "" : `${a} ${currency}`
}

export function FormField({
  field,
  value,
  onChange,
  idPrefix = "",
  testIdPrefix = "form-field",
  attachmentSlot,
}: FormFieldProps) {
  const id = `${idPrefix}${field.name}`
  const explicitLabel = !!field.label?.trim()
  const emit = (next: string) => onChange({ target: { value: next } })

  const label = (
    <Label htmlFor={id} className={cn(!explicitLabel && "capitalize")}>
      {fieldLabelText(field)}
      {field.required && <span className="ml-1 text-destructive">*</span>}
    </Label>
  )

  // Rendered under any field that carries one. Slash fields never do, so the
  // modal's markup is unchanged.
  const help = field.help ? (
    <p className="text-xs text-muted-foreground">{field.help}</p>
  ) : null

  const options = field.options ?? []

  switch (field.type) {
    case "textarea":
      return (
        <div className="space-y-1">
          {label}
          <Textarea
            id={id}
            value={value}
            onChange={onChange}
            rows={4}
            placeholder={field.placeholder}
          />
          {help}
        </div>
      )

    case "cron":
      return (
        <div className="space-y-1">
          {label}
          <Input
            id={id}
            value={value}
            onChange={onChange}
            className="font-mono text-sm"
            placeholder="0 7 * * MON"
          />
          <p className="text-xs text-muted-foreground">
            Standard cron expression (5 fields). Server validates parse + timezone.
          </p>
        </div>
      )

    case "timezone":
      return (
        <div className="space-y-1">
          {label}
          <Select value={value || "UTC"} onValueChange={emit}>
            <SelectTrigger id={id}>
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              {/* Minimal initial set; expand based on usage telemetry. */}
              <SelectItem value="UTC">UTC</SelectItem>
              <SelectItem value="Europe/Prague">Europe/Prague</SelectItem>
              <SelectItem value="Europe/London">Europe/London</SelectItem>
              <SelectItem value="America/New_York">America/New_York</SelectItem>
              <SelectItem value="America/Los_Angeles">America/Los_Angeles</SelectItem>
              <SelectItem value="Asia/Tokyo">Asia/Tokyo</SelectItem>
            </SelectContent>
          </Select>
          {help}
        </div>
      )

    case "priority":
      return (
        <div className="space-y-1">
          {label}
          <Select value={value || "none"} onValueChange={emit}>
            <SelectTrigger id={id}>
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value="none">None</SelectItem>
              <SelectItem value="low">Low</SelectItem>
              <SelectItem value="medium">Medium</SelectItem>
              <SelectItem value="high">High</SelectItem>
              <SelectItem value="urgent">Urgent</SelectItem>
            </SelectContent>
          </Select>
          {help}
        </div>
      )

    case "memory_scope":
      return (
        <div className="space-y-1">
          {label}
          <Select value={value || "agent"} onValueChange={emit}>
            <SelectTrigger id={id}>
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value="agent">Agent — only this agent remembers</SelectItem>
              <SelectItem value="crew">Crew — shared across crew agents</SelectItem>
              <SelectItem value="workspace">Workspace — visible to every crew</SelectItem>
            </SelectContent>
          </Select>
          {help}
        </div>
      )

    case "credential_type":
      return (
        <div className="space-y-1">
          {label}
          <Select value={value || "SECRET"} onValueChange={emit}>
            <SelectTrigger id={id}>
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value="SECRET">Secret</SelectItem>
              <SelectItem value="USERPASS">Username + password</SelectItem>
              <SelectItem value="OAUTH2">OAuth2 (pending grant)</SelectItem>
            </SelectContent>
          </Select>
          {help}
        </div>
      )

    case "secret":
      return (
        <div className="space-y-1">
          {label}
          <Input
            id={id}
            type="password"
            value={value}
            onChange={onChange}
            autoComplete="off"
          />
          {help}
        </div>
      )

    case "slug":
      return (
        <div className="space-y-1">
          {label}
          <Input
            id={id}
            value={value}
            onChange={onChange}
            placeholder="kebab-case-slug"
            className="font-mono text-sm"
          />
          {help}
        </div>
      )

    /* ---- ask-form types (PRD §6.1) ------------------------------------- */

    case "number":
      return (
        <div className="space-y-1">
          {label}
          <Input
            id={id}
            type="number"
            value={value}
            onChange={onChange}
            placeholder={field.placeholder}
          />
          {help}
        </div>
      )

    case "money": {
      const currencies = field.currency?.length ? field.currency : DEFAULT_CURRENCIES
      const { amount, currency } = splitMoney(value, currencies)
      return (
        <div className="space-y-1">
          {label}
          <div className="flex items-center gap-2">
            <Input
              id={id}
              type="number"
              inputMode="decimal"
              className="flex-1"
              value={amount}
              placeholder={field.placeholder}
              onChange={(e) => emit(joinMoney(e.target.value, currency))}
            />
            <Select value={currency} onValueChange={(c) => emit(joinMoney(amount, c))}>
              <SelectTrigger
                className="w-[92px]"
                data-testid={`${testIdPrefix}-${field.name}-currency`}
                aria-label={`${fieldLabelText(field)} currency`}
              >
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                {currencies.map((c) => (
                  <SelectItem key={c} value={c}>
                    {c}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
          </div>
          {help}
        </div>
      )
    }

    case "date":
      return (
        <div className="space-y-1">
          {label}
          <Input id={id} type="date" value={value} onChange={onChange} />
          {help}
        </div>
      )

    case "month":
      return (
        <div className="space-y-1">
          {label}
          <Input id={id} type="month" value={value} onChange={onChange} />
          {help}
        </div>
      )

    case "select":
      return (
        <div className="space-y-1">
          {label}
          <Select value={value} onValueChange={emit}>
            <SelectTrigger id={id}>
              <SelectValue placeholder={field.placeholder ?? "Choose…"} />
            </SelectTrigger>
            <SelectContent>
              {options.map((o) => (
                <SelectItem key={optionValue(o)} value={optionValue(o)}>
                  {optionLabel(o)}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
          {help}
        </div>
      )

    case "multiselect": {
      const selected = new Set(splitMulti(value))
      const toggle = (v: string, on: boolean) => {
        const next = new Set(selected)
        if (on) next.add(v)
        else next.delete(v)
        // Emit in schema order, not click order — the rendered message must
        // not depend on which box the user happened to tick first.
        emit(
          options
            .map(optionValue)
            .filter((o) => next.has(o))
            .join(MULTI_SEPARATOR),
        )
      }
      return (
        <div className="space-y-1">
          {/* Not htmlFor'd at anything: the group has several controls, and
              pointing the label at one of them would mislabel it. */}
          <span className={cn("text-sm leading-none font-medium", !explicitLabel && "capitalize")}>
            {fieldLabelText(field)}
            {field.required && <span className="ml-1 text-destructive">*</span>}
          </span>
          <div role="group" aria-label={fieldLabelText(field)} className="flex flex-wrap gap-x-4 gap-y-2 pt-1">
            {options.map((o) => {
              const v = optionValue(o)
              const optionId = `${id}-${v}`
              return (
                <div key={v} className="flex items-center gap-2">
                  <Checkbox
                    id={optionId}
                    checked={selected.has(v)}
                    onCheckedChange={(c) => toggle(v, c === true)}
                  />
                  <Label htmlFor={optionId} className="font-normal">
                    {optionLabel(o)}
                  </Label>
                </div>
              )
            })}
          </div>
          {help}
        </div>
      )
    }

    case "checkbox":
      return (
        <div className="space-y-1">
          <div className="flex items-center gap-2">
            <Checkbox
              id={id}
              checked={value === "true"}
              onCheckedChange={(c) => emit(c === true ? "true" : "")}
            />
            <Label htmlFor={id} className={cn("font-normal", !explicitLabel && "capitalize")}>
              {fieldLabelText(field)}
              {field.required && <span className="ml-1 text-destructive">*</span>}
            </Label>
          </div>
          {help}
        </div>
      )

    case "file":
    case "photo":
      // Without a slot this falls through to the text input below — the same
      // "render something the server can validate" rule as an unknown type.
      if (attachmentSlot) {
        return (
          <div className="space-y-1">
            {label}
            <div data-testid={`${testIdPrefix}-${field.name}-attachment`}>{attachmentSlot}</div>
            {help}
          </div>
        )
      }
    // falls through

    case "text":
    default:
      // Unknown types fall back to text — server controls the
      // catalog, so an unrecognised type means the dashboard is
      // older than the server. Showing a text input + letting the
      // server validate beats rendering nothing.
      return (
        <div className="space-y-1">
          {label}
          <Input id={id} value={value} onChange={onChange} placeholder={field.placeholder} />
          {help}
        </div>
      )
  }
}
