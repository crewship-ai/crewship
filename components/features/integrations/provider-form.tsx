"use client"

import { useCallback, useId, useState } from "react"
import { ExternalLink, Send } from "lucide-react"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { Spinner } from "@/components/ui/spinner"
import { CREATE_SURFACE_INPUT, CreateSurfaceField } from "@/components/layout/create-surface"
import { cn } from "@/lib/utils"
import type { NotificationProvider, ProviderField } from "@/hooks/use-notification-channels"

interface ProviderFormProps {
  provider: NotificationProvider
  values: Record<string, string>
  onChange: (key: string, value: string) => void
  /** Sends a test with the current values without saving anything. */
  onTest: () => Promise<void>
}

/**
 * The per-provider form: one input per field the destination actually needs,
 * each with a one-line "where do I find this" and, where the provider has a
 * stable page for it, a link.
 *
 * This replaces a single "Service URL" box whose help text asked the user to
 * type `discord://token@channel` — a syntax they have never seen, with no
 * indication of where the token comes from. Fields, labels and help text all
 * come from the server's provider registry, so this component renders whatever
 * the backend describes and never carries its own provider list.
 *
 * It renders inside `CreateSurface` (add-channel-dialog.tsx), so the field
 * shape is the shell's `CreateSurfaceField` rather than a local one — which is
 * also how these inputs finally got an accessible name. The label used to be a
 * `<label>` that neither wrapped its input nor carried `htmlFor`, so every
 * provider field was an anonymous text box: "Webhook URL" was on screen and
 * nowhere in the accessibility tree.
 *
 * `Send test` stays HERE rather than moving to the surface's footer. It tests
 * the fields directly above it, saves nothing, and the surface is allowed
 * exactly one primary action — which is `Connect`.
 */
export function ProviderForm({ provider, values, onChange, onTest }: ProviderFormProps) {
  const [testing, setTesting] = useState(false)
  const [result, setResult] = useState<{ ok: boolean; message: string } | null>(null)

  const missingRequired = provider.fields.some(
    (f) => f.required && !(values[f.key] ?? "").trim(),
  )

  const handleTest = useCallback(async () => {
    setTesting(true)
    setResult(null)
    try {
      await onTest()
      setResult({ ok: true, message: `Test notification sent — check ${provider.label}.` })
    } catch (e) {
      setResult({ ok: false, message: e instanceof Error ? e.message : "Test send failed" })
    } finally {
      setTesting(false)
    }
  }, [onTest, provider.label])

  return (
    <div className="flex flex-col gap-3">
      {provider.fields.map((field) => (
        <ProviderFieldRow
          key={field.key}
          field={field}
          value={values[field.key] ?? ""}
          onChange={(v) => onChange(field.key, v)}
        />
      ))}

      {/* Test before saving. The saved-channel test only works once a channel
          exists, so without this the first confirmation that a pasted URL is
          right comes after committing it. */}
      <div className="flex items-center gap-2 pt-1">
        <Button
          type="button"
          variant="soft"
          size="sm"
          className="h-8 px-2.5 text-xs max-sm:h-12 max-sm:text-sm"
          disabled={testing || missingRequired}
          onClick={handleTest}
          title={missingRequired ? "Fill in the required fields first" : undefined}
        >
          {testing ? <Spinner className="mr-1.5 size-3" /> : <Send className="mr-1.5 size-3" />}
          Send test
        </Button>
        {result && (
          <span
            className={cn(
              "text-[11px]",
              result.ok ? "text-success" : "text-destructive",
            )}
          >
            {result.message}
          </span>
        )}
      </div>
    </div>
  )
}

function ProviderFieldRow({
  field,
  value,
  onChange,
}: {
  field: ProviderField
  value: string
  onChange: (v: string) => void
}) {
  const id = `${useId()}-${field.key}`

  return (
    <CreateSurfaceField
      label={field.label}
      htmlFor={id}
      required={field.required}
      hint={
        field.help || field.help_url ? (
          <span className="block max-w-[440px]">
            {field.help}
            {field.help_url && (
              <>
                {" "}
                <a
                  href={field.help_url}
                  target="_blank"
                  rel="noreferrer noopener"
                  className="inline-flex items-center gap-0.5 underline hover:text-foreground"
                >
                  Where do I find this?
                  <ExternalLink className="size-2.5" />
                </a>
              </>
            )}
          </span>
        ) : undefined
      }
    >
      <Input
        id={id}
        value={value}
        onChange={(e) => onChange(e.target.value)}
        placeholder={field.placeholder}
        // Secret values render masked so a shoulder-surfer or a screen share
        // doesn't leak a token that grants posting rights.
        type={field.type === "password" || field.secret ? "password" : "text"}
        autoComplete="off"
        spellCheck={false}
        className={cn(CREATE_SURFACE_INPUT, "max-w-[420px]")}
      />
    </CreateSurfaceField>
  )
}
