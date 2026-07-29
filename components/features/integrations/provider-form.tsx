"use client"

import { useCallback, useState } from "react"
import { ExternalLink, Send } from "lucide-react"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { Spinner } from "@/components/ui/spinner"
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
    <div className="space-y-3">
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
          className="h-7 px-2.5 text-xs"
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
  return (
    <div className="space-y-1">
      <label className="flex items-center gap-1 text-[11px] font-medium text-foreground/80">
        {field.label}
        {field.required && <span className="text-destructive">*</span>}
      </label>
      <Input
        value={value}
        onChange={(e) => onChange(e.target.value)}
        placeholder={field.placeholder}
        // Secret values render masked so a shoulder-surfer or a screen share
        // doesn't leak a token that grants posting rights.
        type={field.type === "password" || field.secret ? "password" : "text"}
        autoComplete="off"
        spellCheck={false}
        className="h-7 max-w-[420px] text-xs"
      />
      {(field.help || field.help_url) && (
        <p className="max-w-[440px] text-[11px] leading-snug text-muted-foreground">
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
        </p>
      )}
    </div>
  )
}
